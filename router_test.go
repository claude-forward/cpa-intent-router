package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRouter_StaticRuleMatch(t *testing.T) {
	cfg := PluginConfig{
		Enabled: true,
		Groups: []GroupConfig{
			{
				ID:      "dev-group",
				Members: []string{"claude/claude-3-7-sonnet", "deepseek-chat"},
				Rules: []RuleConfig{
					{
						Use:    "claude/claude-3-7-sonnet:high",
						Tokens: 50000,
					},
					{
						Use: "deepseek-chat",
					},
				},
				Fallback: "deepseek-chat",
			},
		},
	}

	router := NewRouter(cfg, nil)
	ctx := context.Background()

	// 1. Request with small body -> matches rule 2 (deepseek-chat)
	reqSmall := pluginapi.ModelRouteRequest{
		RequestedModel: "group/dev-group",
		Body:           []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
	respSmall := router.RouteModel(ctx, reqSmall)
	if !respSmall.Handled {
		t.Fatalf("expected respSmall.Handled=true")
	}
	if respSmall.TargetModel != "deepseek-chat" {
		t.Errorf("expected targetModel deepseek-chat, got %s", respSmall.TargetModel)
	}

	// 2. Request with huge body (over 50000 tokens ~ 200000 bytes) -> matches rule 1 (claude-3-7-sonnet:high)
	hugeBody := append([]byte(`{"messages":[{"role":"user","content":"`), make([]byte, 210000)...)
	hugeBody = append(hugeBody, []byte(`"}]}`)...)
	reqBig := pluginapi.ModelRouteRequest{
		RequestedModel: "dev-group",
		Body:           hugeBody,
	}
	respBig := router.RouteModel(ctx, reqBig)
	if !respBig.Handled {
		t.Fatalf("expected respBig.Handled=true")
	}
	if respBig.Target != "claude" || respBig.TargetModel != "claude-3-7-sonnet(high)" {
		t.Errorf("expected target claude/claude-3-7-sonnet(high), got %s/%s", respBig.Target, respBig.TargetModel)
	}
}

func TestRouter_ToolTurnAffinity(t *testing.T) {
	cfg := PluginConfig{
		Enabled: true,
		Groups: []GroupConfig{
			{
				ID:      "qa-group",
				Members: []string{"claude/claude-3-7-sonnet", "gemini/gemini-2.5-pro"},
				Rules: []RuleConfig{
					{
						Use: "claude/claude-3-7-sonnet",
					},
				},
			},
		},
	}

	router := NewRouter(cfg, nil)
	ctx := context.Background()

	headers := http.Header{}
	headers.Set("X-Session-Id", "session-qa-1")

	// Initial user turn -> routes to claude-3-7-sonnet
	turn1 := pluginapi.ModelRouteRequest{
		RequestedModel: "group/qa-group",
		Headers:        headers,
		Body:           []byte(`{"messages":[{"role":"user","content":"check status"}]}`),
	}
	resp1 := router.RouteModel(ctx, turn1)
	if !resp1.Handled || resp1.TargetModel != "claude-3-7-sonnet" {
		t.Fatalf("turn1 failed: %+v", resp1)
	}

	// Mid-turn tool result -> must retain the same model even if payload conditions change
	toolTurn := pluginapi.ModelRouteRequest{
		RequestedModel: "group/qa-group",
		Headers:        headers,
		Body: []byte(`{"messages":[
			{"role":"user","content":"check status"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"status"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}
		]}`),
	}
	respTool := router.RouteModel(ctx, toolTurn)
	if !respTool.Handled || respTool.TargetModel != "claude-3-7-sonnet" || respTool.Reason != "affinity:tool_turn_held" {
		t.Fatalf("toolTurn affinity check failed: %+v", respTool)
	}
}

func TestRouter_IntentClassification(t *testing.T) {
	callCount := 0
	mockCaller := func(ctx context.Context, model string, body []byte) ([]byte, error) {
		callCount++
		return []byte(`{"choices":[{"message":{"content":"1"}}]}`), nil
	}

	cfg := PluginConfig{
		Enabled: true,
		Groups: []GroupConfig{
			{
				ID:         "intent-group",
				Classifier: "test-classifier-model",
				Members:    []string{"deepseek-chat", "claude/claude-3-7-sonnet"},
				Rules: []RuleConfig{
					// Static rule first
					{
						Use:    "claude/claude-3-7-sonnet:high",
						Tokens: 50000,
					},
					// Intent rule before fallback
					{
						Use:    "deepseek-chat",
						Intent: "simple question",
					},
				},
				Fallback: "claude/claude-3-7-sonnet",
			},
		},
	}

	router := NewRouter(cfg, mockCaller)
	ctx := context.Background()

	// 1. Static rule hits -> mockCaller should NOT be called (Lazy evaluation)
	hugeBody := append([]byte(`{"messages":[{"role":"user","content":"`), make([]byte, 210000)...)
	hugeBody = append(hugeBody, []byte(`"}]}`)...)
	reqBig := pluginapi.ModelRouteRequest{
		RequestedModel: "group/intent-group",
		Body:           hugeBody,
	}
	respBig := router.RouteModel(ctx, reqBig)
	if !respBig.Handled || respBig.TargetModel != "claude-3-7-sonnet(high)" {
		t.Fatalf("respBig failed: %+v", respBig)
	}
	if callCount != 0 {
		t.Errorf("expected 0 classifier calls when static rule matches, got %d", callCount)
	}

	// 2. Static rule misses -> evaluates intent rule right above fallback -> invokes classifier
	reqIntent := pluginapi.ModelRouteRequest{
		RequestedModel: "group/intent-group",
		Body:           []byte(`{"messages":[{"role":"user","content":"what is 1+1?"}]}`),
	}
	respIntent := router.RouteModel(ctx, reqIntent)
	if !respIntent.Handled || respIntent.TargetModel != "deepseek-chat" {
		t.Fatalf("respIntent failed: %+v", respIntent)
	}
	if callCount != 1 {
		t.Errorf("expected 1 classifier call, got %d", callCount)
	}
}
