package main

import (
	"context"
	"testing"
)

func TestParseIntentAnswer(t *testing.T) {
	intents := []string{"fixing tests", "quick question", "architecture planning"}

	tests := []struct {
		answer     string
		wantIntent string
		wantErr    bool
	}{
		{"1", "fixing tests", false},
		{"2", "quick question", false},
		{"3", "architecture planning", false},
		{"The answer is 2.", "quick question", false},
		{"0", "", false},
		{"99", "", true},
		{"none", "", true},
	}

	for _, tc := range tests {
		got, err := parseIntentAnswer(tc.answer, intents)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseIntentAnswer(%q) err = %v, wantErr = %v", tc.answer, err, tc.wantErr)
			continue
		}
		if got != tc.wantIntent {
			t.Errorf("parseIntentAnswer(%q) = %q, want %q", tc.answer, got, tc.wantIntent)
		}
	}
}

func TestClassifierClient_DirectInternalCaller(t *testing.T) {
	callCount := 0
	mockCaller := func(ctx context.Context, model string, body []byte) ([]byte, error) {
		callCount++
		return []byte(`{
			"choices": [
				{
					"message": {
						"content": "2"
					}
				}
			]
		}`), nil
	}

	client := NewClassifierClient(mockCaller)
	intents := []string{"fixing tests", "quick question", "planning"}

	// First call should invoke mock caller
	ctx := context.Background()
	res, err := client.Classify(ctx, "mock-model", intents, "what time is it?")
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}
	if res != "quick question" {
		t.Fatalf("expected 'quick question', got %q", res)
	}
	if callCount != 1 {
		t.Fatalf("expected callCount=1, got %d", callCount)
	}

	// Second identical call should hit cache without calling caller again
	res2, err2 := client.Classify(ctx, "mock-model", intents, "what time is it?")
	if err2 != nil {
		t.Fatalf("Classify second call failed: %v", err2)
	}
	if res2 != "quick question" {
		t.Fatalf("expected 'quick question', got %q", res2)
	}
	if callCount != 1 {
		t.Fatalf("expected callCount to remain 1, got %d", callCount)
	}
}
