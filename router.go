package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Router orchestrates two-stage dynamic routing: static rule filtering and LLM intent classification.
type Router struct {
	mu         sync.RWMutex
	cfg        PluginConfig
	sessions   *SessionTracker
	classifier *ClassifierClient
}

// NewRouter creates an initialized Router instance.
func NewRouter(cfg PluginConfig) *Router {
	r := &Router{
		cfg:        cfg,
		sessions:   NewSessionTracker(1 * time.Hour),
		classifier: NewClassifierClient(cfg.GatewayURL, cfg.GatewayKey),
	}
	return r
}

// UpdateConfig hot-reloads the router configuration.
func (r *Router) UpdateConfig(cfg PluginConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
	r.classifier = NewClassifierClient(cfg.GatewayURL, cfg.GatewayKey)
}

// RouteModel evaluates the incoming request against routing groups and rules.
func (r *Router) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse {
	r.mu.RLock()
	cfg := r.cfg
	classifierClient := r.classifier
	r.mu.RUnlock()

	if !cfg.Enabled {
		return pluginapi.ModelRouteResponse{Handled: false}
	}

	// 1. Identify matching routing group
	group, ok := findMatchingGroup(cfg.Groups, req.RequestedModel)
	if !ok {
		return pluginapi.ModelRouteResponse{Handled: false}
	}

	now := time.Now()
	feat := ExtractFeatures(req, now)
	sessionKey := r.sessions.GenerateSessionKey(group.ID, req)

	// 2. Check session / turn locking: within-turn tool call responses stay with current model
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

	// 3. Classify intent if rules require intent matching and user text is present
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

	// 4. Evaluate rules sequentially (first match wins)
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

	// 5. Fallback if no rules matched
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

	// Match "group/<group_id>" or bare "<group_id>"
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
		// Quick pre-filtering on static criteria (tokens, images, effort, agents, time)
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
