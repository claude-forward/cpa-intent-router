package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Router 负责执行两阶段动态路由：静态规则初筛与内部小模型极简意图分类
type Router struct {
	mu         sync.RWMutex
	cfg        PluginConfig
	sessions   *SessionTracker
	classifier *ClassifierClient
	systemOne  *SystemOneClient
	caller     HostModelCaller
	hostModels map[string]string
}

// NewRouter 创建 Router 实例
func NewRouter(cfg PluginConfig, caller HostModelCaller) *Router {
	r := &Router{
		cfg:        cfg,
		sessions:   NewSessionTracker(1 * time.Hour),
		classifier: NewClassifierClient(caller),
		systemOne:  NewSystemOneClient(cfg.SystemOne),
		caller:     caller,
		hostModels: LoadHostModelMappings(""),
	}
	return r
}

// UpdateConfig 热重载路由配置
func (r *Router) UpdateConfig(cfg PluginConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
	r.classifier = NewClassifierClient(r.caller)
	r.systemOne = NewSystemOneClient(cfg.SystemOne)
	r.hostModels = LoadHostModelMappings("")
}

// SetHostModels 设置宿主模型映射索引（主要供单元测试使用）
func (r *Router) SetHostModels(mappings map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hostModels = mappings
}

// RouteModel 评估传入请求并返回路由决策
func (r *Router) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse {
	r.mu.RLock()
	cfg := r.cfg
	classifierClient := r.classifier
	systemOneClient := r.systemOne
	hostModels := r.hostModels
	r.mu.RUnlock()

	if !cfg.Enabled {
		return pluginapi.ModelRouteResponse{Handled: false}
	}

	// 1. 匹配目标虚拟路由组
	group, ok := findMatchingGroup(cfg.Groups, req.RequestedModel)
	if !ok {
		return pluginapi.ModelRouteResponse{Handled: false}
	}

	now := time.Now()
	feat := ExtractFeatures(req, now)
	sessionKey := r.sessions.GenerateSessionKey(group.ID, req)

	// 2. 轮次亲和性锁定：同一个 Turn 内部的多步工具交互（Tool Result）严格锁定在初始选择的模型
	if feat.Within && sessionKey != "" {
		if dec, found := r.sessions.GetDecision(sessionKey, now); found && dec.TargetModel != "" {
			targetProvider := ResolveProvider(dec.TargetProvider, dec.TargetModel, hostModels, req.AvailableProviders)
			return pluginapi.ModelRouteResponse{
				Handled:     true,
				TargetKind:  pluginapi.ModelRouteTargetProvider,
				Target:      targetProvider,
				TargetModel: dec.TargetModel,
				Reason:      "affinity:tool_turn_held",
			}
		}
	}

	// 3. 惰性意图分类器闭包：仅当前序静态规则未命中且遇到 intent 规则时才按需触发小模型分类
	var currentIntent string
	var classifiedDone bool

	getIntent := func() string {
		if classifiedDone {
			return currentIntent
		}
		classifiedDone = true
		if group.Classifier != "" && feat.UserText != "" {
			candidateIntents := collectCandidateIntents(group.Rules, feat)
			if len(candidateIntents) > 0 {
				// 配置了外部 System One 分类器时优先直连 POST /v1/systemone；
				// 未配置或调用失败（返回空意图）时回退到宿主内部 host.model.execute 调用
				if systemOneClient != nil && systemOneClient.Enabled() {
					model := systemOneClient.Model(group.Classifier)
					classifierCtx, cancel := context.WithTimeout(ctx, systemOneClient.Timeout())
					detected, errClassify := systemOneClient.Classify(classifierCtx, model, candidateIntents, feat.UserText)
					cancel()
					if errClassify == nil && detected != "" {
						currentIntent = detected
					}
				}
				if currentIntent == "" {
					classifierCtx, cancel := context.WithTimeout(ctx, defaultClassifierTimeout)
					detected, errClassify := classifierClient.Classify(classifierCtx, group.Classifier, candidateIntents, feat.UserText)
					cancel()
					if errClassify == nil && detected != "" {
						currentIntent = detected
					}
				}
			}
		}
		return currentIntent
	}

	// 4. 顺序匹配分流规则列表（首条完全命中的规则生效）
	for i, rule := range group.Rules {
		var ruleIntent string
		if rule.Intent != "" {
			ruleIntent = getIntent()
		}
		if MatchRule(rule, feat, ruleIntent) {
			provider, targetModel := ParseMember(rule.Use)
			if targetModel == "" {
				continue
			}
			targetProvider := ResolveProvider(provider, targetModel, hostModels, req.AvailableProviders)
			if sessionKey != "" {
				r.sessions.SetDecision(sessionKey, TurnDecision{
					TargetProvider: targetProvider,
					TargetModel:    targetModel,
					Intent:         ruleIntent,
				}, now)
			}
			return pluginapi.ModelRouteResponse{
				Handled:     true,
				TargetKind:  pluginapi.ModelRouteTargetProvider,
				Target:      targetProvider,
				TargetModel: targetModel,
				Reason:      fmt.Sprintf("rule:hit_index_%d", i+1),
			}
		}
	}

	// 5. 规则均未命中时回退到组兜底模型
	fallbackChoice := group.Fallback
	if fallbackChoice == "" && len(group.Members) > 0 {
		fallbackChoice = group.Members[0]
	}
	if fallbackChoice != "" {
		provider, targetModel := ParseMember(fallbackChoice)
		if targetModel != "" {
			targetProvider := ResolveProvider(provider, targetModel, hostModels, req.AvailableProviders)
			if sessionKey != "" {
				r.sessions.SetDecision(sessionKey, TurnDecision{
					TargetProvider: targetProvider,
					TargetModel:    targetModel,
					Intent:         currentIntent,
				}, now)
			}
			return pluginapi.ModelRouteResponse{
				Handled:     true,
				TargetKind:  pluginapi.ModelRouteTargetProvider,
				Target:      targetProvider,
				TargetModel: targetModel,
				Reason:      "fallback:group_default",
			}
		}
	}

	return pluginapi.ModelRouteResponse{Handled: false}
}

func findMatchingGroup(groups []GroupConfig, requestedModel string) (GroupConfig, bool) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return GroupConfig{}, false
	}

	cleanModel := strings.TrimPrefix(requestedModel, "group/")

	for _, g := range groups {
		if strings.EqualFold(g.ID, cleanModel) || strings.EqualFold(g.ID, requestedModel) {
			return g, true
		}
	}
	return GroupConfig{}, false
}

func collectCandidateIntents(rules []RuleConfig, feat RequestFeatures) []string {
	var intents []string
	for _, r := range rules {
		if r.Intent == "" {
			continue
		}
		if r.Tokens > 0 && feat.Tokens < r.Tokens {
			continue
		}
		if r.Images && !feat.Images {
			continue
		}
		if r.Time != nil && !MatchTimeWindow(r.Time, feat.At) {
			continue
		}
		intents = append(intents, r.Intent)
	}
	return intents
}
