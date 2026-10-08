package main

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestClassifierClient_MockServer(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"choices": [
				{
					"message": {
						"content": "2"
					}
				}
			]
		}`))
	}))
	defer server.Close()

	client := NewClassifierClient(server.URL, "test-key")
	intents := []string{"fixing tests", "quick question", "planning"}

	// First call should reach server
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

	// Second identical call should hit cache without calling server again
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
