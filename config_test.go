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
