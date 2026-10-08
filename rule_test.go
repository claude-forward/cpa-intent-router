package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestExtractFeatures_ClaudeToolResult(t *testing.T) {
	body := []byte(`{
		"model": "group/dev",
		"messages": [
			{"role": "user", "content": "list directory"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "call_1", "name": "ls", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_1", "content": "file1.txt"}]}
		]
	}`)

	headers := http.Header{}
	headers.Set("User-Agent", "claude-code/1.0.0")

	req := pluginapi.ModelRouteRequest{
		SourceFormat:   "claude",
		RequestedModel: "group/dev",
		Headers:        headers,
		Body:           body,
	}

	feat := ExtractFeatures(req, time.Now())
	if !feat.Within {
		t.Errorf("expected feat.Within=true for tool_result")
	}
	if feat.Agent != "claude" {
		t.Errorf("expected feat.Agent=claude, got %s", feat.Agent)
	}
}

func TestExtractFeatures_ImagesAndEffort(t *testing.T) {
	body := []byte(`{
		"model": "group/dev",
		"reasoning_effort": "high",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "look at this image"},
					{"type": "image_url", "image_url": {"url": "http://example.com/img.png"}}
				]
			}
		]
	}`)

	req := pluginapi.ModelRouteRequest{
		SourceFormat:   "openai",
		RequestedModel: "group/dev",
		Body:           body,
	}

	feat := ExtractFeatures(req, time.Now())
	if !feat.Images {
		t.Errorf("expected feat.Images=true")
	}
	if feat.Effort != "high" {
		t.Errorf("expected feat.Effort=high, got %s", feat.Effort)
	}
	if feat.UserText != "look at this image" {
		t.Errorf("expected UserText='look at this image', got %q", feat.UserText)
	}
}

func TestMatchRule(t *testing.T) {
	feat := RequestFeatures{
		Tokens:   150000,
		Images:   true,
		Effort:   "high",
		Agent:    "claude",
		UserText: "refactor this function",
		At:       time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC),
	}

	r1 := RuleConfig{
		Use:    "claude-opus",
		Tokens: 100000,
		Images: true,
	}
	if !MatchRule(r1, feat, "") {
		t.Errorf("expected r1 to match")
	}

	r2 := RuleConfig{
		Use:    "fast-model",
		Tokens: 200000,
	}
	if MatchRule(r2, feat, "") {
		t.Errorf("expected r2 not to match (insufficient tokens)")
	}

	r3 := RuleConfig{
		Use:    "codex-model",
		Agents: []string{"codex"},
	}
	if MatchRule(r3, feat, "") {
		t.Errorf("expected r3 not to match (agent mismatch)")
	}

	rIntent := RuleConfig{
		Use:    "planner-model",
		Intent: "refactoring",
	}
	if !MatchRule(rIntent, feat, "refactoring") {
		t.Errorf("expected rIntent to match with intent refactoring")
	}
	if MatchRule(rIntent, feat, "testing") {
		t.Errorf("expected rIntent not to match with intent testing")
	}
}
