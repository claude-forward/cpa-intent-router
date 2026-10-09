package main

import (
	"testing"
	"time"
)

func TestDecodeConfig_YAML(t *testing.T) {
	raw := []byte(`
enabled: true
groups:
  - id: "dev-team"
    name: "Dev Team Routing"
    members:
      - "claude/claude-3-7-sonnet:high"
      - "gemini/gemini-2.5-pro"
      - "deepseek-chat"
    classifier: "gemini/gemini-2.5-flash"
    rules:
      - use: "claude/claude-3-7-sonnet:high"
        effort: "high"
      - use: "gemini/gemini-2.5-pro"
        tokens: 100000
      - use: "deepseek-chat"
        intent: "quick question"
`)

	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig failed: %v", err)
	}
	if !cfg.Enabled {
		t.Errorf("expected Enabled=true")
	}
	if len(cfg.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(cfg.Groups))
	}
	g := cfg.Groups[0]
	if g.ID != "dev-team" {
		t.Errorf("expected group ID dev-team, got %s", g.ID)
	}
	if len(g.Rules) != 3 {
		t.Errorf("expected 3 rules, got %d", len(g.Rules))
	}
	if g.Fallback != "claude/claude-3-7-sonnet:high" {
		t.Errorf("expected fallback member %s, got %s", "claude/claude-3-7-sonnet:high", g.Fallback)
	}
}

func TestDecodeConfig_SystemOne(t *testing.T) {
	raw := []byte(`
enabled: true
systemone:
  type: "systemone"
  url: "https://api.typesafe.ai/v1/systemone"
  api_key: "$TYPESAFE_API_KEY"
  model: "jev"
  timeout: "2s"
groups:
  - id: "dev"
    members: ["deepseek-chat"]
    classifier: "group-classifier"
    rules:
      - use: "deepseek-chat"
        intent: "quick question"
`)

	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig failed: %v", err)
	}
	if cfg.SystemOne == nil {
		t.Fatalf("expected SystemOne config to be parsed")
	}
	if cfg.SystemOne.Type != ClassifierTypeSystemOne {
		t.Errorf("expected type %s, got %s", ClassifierTypeSystemOne, cfg.SystemOne.Type)
	}
	if cfg.SystemOne.Endpoint != "https://api.typesafe.ai/v1/systemone" {
		t.Errorf("expected url alias to populate endpoint, got %q", cfg.SystemOne.Endpoint)
	}
	if cfg.SystemOne.APIKey != "$TYPESAFE_API_KEY" {
		t.Errorf("expected api_key preserved for later expansion, got %q", cfg.SystemOne.APIKey)
	}
	if cfg.SystemOne.Model != "jev" {
		t.Errorf("expected model jev, got %q", cfg.SystemOne.Model)
	}
	if cfg.SystemOne.Timeout != 2*time.Second {
		t.Errorf("expected timeout 2s, got %s", cfg.SystemOne.Timeout)
	}
}

func TestDecodeConfig_SystemOneDefaultsTypeFromEndpoint(t *testing.T) {
	raw := []byte(`
systemone:
  endpoint: "https://api.typesafe.ai/v1/systemone"
`)

	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig failed: %v", err)
	}
	if cfg.SystemOne == nil || cfg.SystemOne.Type != ClassifierTypeSystemOne {
		t.Fatalf("expected endpoint to imply systemone type, got %+v", cfg.SystemOne)
	}
}

func TestDecodeConfig_SystemOneInvalidTimeout(t *testing.T) {
	raw := []byte(`
systemone:
  endpoint: "https://api.typesafe.ai/v1/systemone"
  timeout: "not-a-duration"
`)

	if _, err := decodeConfig(raw); err == nil {
		t.Fatalf("expected error for invalid timeout")
	}
}

func TestParseMember(t *testing.T) {
	tests := []struct {
		input        string
		wantProvider string
		wantModel    string
	}{
		{"claude/claude-3-7-sonnet", "claude", "claude-3-7-sonnet"},
		{"claude/claude-3-7-sonnet:high", "claude", "claude-3-7-sonnet(high)"},
		{"claude/claude-3-7-sonnet:low", "claude", "claude-3-7-sonnet(low)"},
		{"gemini/gemini-2.5-pro", "gemini", "gemini-2.5-pro"},
		{"gpt-5.4-fast", "", "gpt-5.4-fast"},
		{"gpt-5.4:max", "", "gpt-5.4(max)"},
	}

	for _, tc := range tests {
		gotP, gotM := ParseMember(tc.input)
		if gotP != tc.wantProvider || gotM != tc.wantModel {
			t.Errorf("ParseMember(%q) = (%q, %q), want (%q, %q)", tc.input, gotP, gotM, tc.wantProvider, tc.wantModel)
		}
	}
}

func TestMatchTimeWindow(t *testing.T) {
	now := time.Date(2026, 10, 8, 14, 30, 0, 0, time.UTC) // Thursday 14:30

	w1 := &TimeWindow{
		From: "14:00",
		To:   "18:00",
		Days: []string{"mon", "thu", "fri"},
	}
	if !MatchTimeWindow(w1, now) {
		t.Errorf("expected w1 to match Thursday 14:30")
	}

	w2 := &TimeWindow{
		From: "15:00",
		To:   "18:00",
		Days: []string{"thu"},
	}
	if MatchTimeWindow(w2, now) {
		t.Errorf("expected w2 not to match 14:30")
	}

	// Cross-midnight window
	wCross := &TimeWindow{
		From: "22:00",
		To:   "08:00",
	}
	midnightNow := time.Date(2026, 10, 8, 23, 15, 0, 0, time.UTC)
	if !MatchTimeWindow(wCross, midnightNow) {
		t.Errorf("expected wCross to match 23:15")
	}
	morningNow := time.Date(2026, 10, 8, 7, 30, 0, 0, time.UTC)
	if !MatchTimeWindow(wCross, morningNow) {
		t.Errorf("expected wCross to match 07:30")
	}
	noonNow := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if MatchTimeWindow(wCross, noonNow) {
		t.Errorf("expected wCross not to match 12:00")
	}
}

func TestParseHostModelMappingsFromYAML(t *testing.T) {
	mockHostYAML := []byte(`
openai-compatibility:
  - name: kenari
    models:
      - name: deepseek-v4-1-flash
        alias: ""
      - name: deepseek-v4-flash
        alias: "deepseek-flash"
codex-api-key:
  - api-key: sk-xxx
    models:
      - name: gpt-6.1-sol
      - name: gpt-6-luna
claude-api-key:
  - api-key: sk-ant-xxx
    models:
      - name: claude-sonnet-4-6
`)

	mappings := ParseHostModelMappingsFromYAML(mockHostYAML)
	if len(mappings) == 0 {
		t.Fatalf("expected mappings to be parsed")
	}

	if mappings["deepseek-flash"] != "openai-compatible-kenari" {
		t.Errorf("expected deepseek-flash -> openai-compatible-kenari, got %s", mappings["deepseek-flash"])
	}
	if mappings["deepseek-v4-flash"] != "openai-compatible-kenari" {
		t.Errorf("expected deepseek-v4-flash -> openai-compatible-kenari, got %s", mappings["deepseek-v4-flash"])
	}
	if mappings["gpt-6.1-sol"] != "codex" {
		t.Errorf("expected gpt-6.1-sol -> codex, got %s", mappings["gpt-6.1-sol"])
	}
	if mappings["claude-sonnet-4-6"] != "claude" {
		t.Errorf("expected claude-sonnet-4-6 -> claude, got %s", mappings["claude-sonnet-4-6"])
	}
}

func TestResolveProvider_TwoStyles(t *testing.T) {
	mockMappings := map[string]string{
		"deepseek-flash": "openai-compatible-kenari",
	}
	available := []string{"antigravity", "codex", "openai-compatible-kenari"}

	// 风格一：纯模型名（无 Provider 前缀），通过宿主配置动态查表解析至对应真实 Provider
	p1 := ResolveProvider("", "deepseek-flash", mockMappings, available)
	if p1 != "openai-compatible-kenari" {
		t.Errorf("expected p1=openai-compatible-kenari, got %s", p1)
	}

	// 风格二：显式指定 Provider 前缀（如 antigravity/claude-sonnet-4-6）
	p2 := ResolveProvider("antigravity", "claude-sonnet-4-6", mockMappings, available)
	if p2 != "antigravity" {
		t.Errorf("expected p2=antigravity, got %s", p2)
	}

	// 风格三：显式指定 compat 名称别名（如 kenari/deepseek-flash）
	p3 := ResolveProvider("kenari", "deepseek-flash", mockMappings, available)
	if p3 != "openai-compatible-kenari" {
		t.Errorf("expected p3=openai-compatible-kenari, got %s", p3)
	}

	// 风格四：显式指定通用名称（如 openai-compatibility/deepseek-flash）
	p4 := ResolveProvider("openai-compatibility", "deepseek-flash", mockMappings, available)
	if p4 != "openai-compatible-kenari" {
		t.Errorf("expected p4=openai-compatible-kenari, got %s", p4)
	}

	// 风格五：OAuth 动态凭据模型（未在 API key 段中出现，但存在 antigravity 可用提供商）
	p5 := ResolveProvider("", "claude-sonnet-4-6", mockMappings, available)
	if p5 != "antigravity" {
		t.Errorf("expected p5=antigravity, got %s", p5)
	}
}
