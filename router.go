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
	caller     HostModelCaller
}

// NewRouter 创建 Router 实例
func NewRouter(cfg PluginConfig, caller HostModelCaller) *Router {
	r := &Router{
		cfg:        cfg,
		sessions:   NewSessionTracker(1 * time.Hour),
		classifier: NewClassifierClient(caller),
		caller:     caller,
	}
	return r
}

// UpdateConfig 热重载路由配置
func (r *Router) UpdateConfig(cfg PluginConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
	r.classifier = NewClassifierClient(r.caller)
}

// RouteModel 评估传入请求并返回路由决策
func (r *Router) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse {
	r.mu.RLock()
	cfg := r.cfg
	classifierClient := r.classifier
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
			return pluginapi.ModelRouteResponse{
				Handled:     true,
				TargetKind:  pluginapi.ModelRouteTargetProvider,
				Target:      dec.TargetProvider,
				TargetModel: dec.TargetModel,
				Reason:      "affinity:tool_turn_held",
			}
		}
	}

	// 3. 意图分类：当规则中有意图要求且提取到了用户输入时，通过宿主内部直接调用分类器
	var currentIntent string
	if group.Classifier != "" && feat.UserText != "" {
		candidateIntents := collectCandidateIntents(group.Rules, feat)
		if len(candidateIntents) > 0 {
			classifierCtx, cancel := context.WithTimeout(ctx, defaultClassifierTimeout)
			detected, errClassify := classifierClient.Classify(classifierCtx, group.Classifier, candidateIntents, feat.UserText)
			cancel()
			if errClassify == nil && detected != "" {
				currentIntent = detected
			}
		}
	}

	// 4. 顺序匹配分流规则列表（首条完全命中的规则生效）
	for i, rule := range group.Rules {
		if MatchRule(rule, feat, currentIntent) {
			provider, targetModel := ParseMember(rule.Use)
			if targetModel == "" {
				continue
			}
			if sessionKey != "" {
				r.sessions.SetDecision(sessionKey, TurnDecision{
					TargetProvider: provider,
					TargetModel:    targetModel,
					Intent:         currentIntent,
				}, now)
			}
			return pluginapi.ModelRouteResponse{
				Handled:     true,
				TargetKind:  pluginapi.ModelRouteTargetProvider,
				Target:      provider,
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
			if sessionKey != "" {
				r.sessions.SetDecision(sessionKey, TurnDecision{
					TargetProvider: provider,
					TargetModel:    targetModel,
					Intent:         currentIntent,
				}, now)
			}
			return pluginapi.ModelRouteResponse{
				Handled:     true,
				TargetKind:  pluginapi.ModelRouteTargetProvider,
				Target:      provider,
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
