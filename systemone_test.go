package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewSystemOneClient_Disabled(t *testing.T) {
	if got := NewSystemOneClient(nil); got != nil {
		t.Errorf("expected nil client for nil config, got %v", got)
	}
	if got := NewSystemOneClient(&ClassifierConfig{}); got != nil {
		t.Errorf("expected nil client for empty endpoint, got %v", got)
	}
	client := NewSystemOneClient(&ClassifierConfig{Endpoint: "https://example.com/v1/systemone"})
	if client == nil || !client.Enabled() {
		t.Errorf("expected enabled client")
	}
	if client.Timeout() != defaultSystemOneTimeout {
		t.Errorf("expected default timeout %v, got %v", defaultSystemOneTimeout, client.Timeout())
	}
}

func TestSystemOneClient_ModelFallback(t *testing.T) {
	c1 := NewSystemOneClient(&ClassifierConfig{
		Endpoint: "https://example.com/v1/systemone",
		Model:    "jev-latest",
	})
	if c1.Model("gemini-flash") != "jev-latest" {
		t.Errorf("expected global model jev-latest, got %s", c1.Model("gemini-flash"))
	}

	c2 := NewSystemOneClient(&ClassifierConfig{
		Endpoint: "https://example.com/v1/systemone",
	})
	if c2.Model("gemini-flash") != "gemini-flash" {
		t.Errorf("expected fallback gemini-flash, got %s", c2.Model("gemini-flash"))
	}
}

func TestSystemOneClient_Classify_Success_TypeSafeOfficial(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotContentType string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		if errDecode := json.NewDecoder(r.Body).Decode(&gotBody); errDecode != nil {
			t.Errorf("decode request body: %v", errDecode)
		}
		w.Header().Set("Content-Type", "application/json")
		// TypeSafe Official responses format
		_, _ = w.Write([]byte(`{
			"id": "sys1_01923",
			"model": "jev-latest",
			"answers": {
				"intent": {
					"choice": "complex_architecture",
					"confidence": 0.96
				}
			},
			"usage": {"prompt_tokens": 65}
		}`))
	}))
	defer server.Close()

	client := NewSystemOneClient(&ClassifierConfig{
		Endpoint: server.URL + "/v1/systemone",
		APIKey:   "ts-secret",
		Model:    "jev-latest",
	})
	intents := []string{"quick_lookup", "complex_architecture", "creative_writing"}

	got, err := client.Classify(context.Background(), client.Model(""), intents, "How do I implement the saga pattern in Go?")
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}
	if got != "complex_architecture" {
		t.Errorf("expected complex_architecture, got %q", got)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/v1/systemone" {
		t.Errorf("expected path /v1/systemone, got %s", gotPath)
	}
	if gotAuth != "Bearer ts-secret" {
		t.Errorf("expected bearer auth, got %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("expected json content type, got %q", gotContentType)
	}
	if gotBody["model"] != "jev-latest" {
		t.Errorf("expected model jev-latest in request body, got %v", gotBody["model"])
	}
	if gotBody["state"] != "How do I implement the saga pattern in Go?" {
		t.Errorf("unexpected state field: %v", gotBody["state"])
	}

	questions, ok := gotBody["questions"].(map[string]any)
	if !ok || questions["intent"] == nil {
		t.Fatalf("expected questions.intent map, got %v", gotBody["questions"])
	}
}

func TestSystemOneClient_Classify_NoneOfTheAbove(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "sys1_01924",
			"model": "jev-latest",
			"answers": {
				"intent": {
					"choice": "none_of_the_above",
					"confidence": 0.99
				}
			}
		}`))
	}))
	defer server.Close()

	client := NewSystemOneClient(&ClassifierConfig{
		Endpoint: server.URL + "/v1/systemone",
	})
	intents := []string{"writing_tests"}

	// When user prompt is completely unrelated, return empty intent so router continues to fallback
	got, err := client.Classify(context.Background(), "jev-latest", intents, "Translate this poem to French")
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string when none_of_the_above chosen, got %q", got)
	}
}

func TestSystemOneClient_Classify_EnvExpansion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer env-secret" {
			t.Errorf("expected expanded bearer token, got %q", auth)
		}
		_, _ = w.Write([]byte(`{"answers":{"intent":{"choice":"quick question"}}}`))
	}))
	defer server.Close()

	t.Setenv("TEST_SYSTEMONE_ENDPOINT", server.URL+"/v1/systemone")
	t.Setenv("TEST_SYSTEMONE_KEY", "env-secret")

	client := NewSystemOneClient(&ClassifierConfig{
		Endpoint: "$TEST_SYSTEMONE_ENDPOINT",
		APIKey:   "${TEST_SYSTEMONE_KEY}",
	})
	if client == nil || !client.Enabled() {
		t.Fatalf("expected client built from env-expanded endpoint")
	}

	got, err := client.Classify(context.Background(), "", []string{"quick question", "deep work"}, "what is 1+1?")
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}
	if got != "quick question" {
		t.Errorf("expected quick question, got %q", got)
	}
}

func TestSystemOneClient_Classify_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewSystemOneClient(&ClassifierConfig{Endpoint: server.URL + "/v1/systemone"})
	if _, err := client.Classify(context.Background(), "", []string{"a", "b"}, "hello"); err == nil {
		t.Fatalf("expected error for non-2xx status")
	} else if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected status in error, got %v", err)
	}
}

func TestSystemOneClient_Classify_UnknownSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"intent":{"choice":"not_a_candidate"}}}`))
	}))
	defer server.Close()

	client := NewSystemOneClient(&ClassifierConfig{Endpoint: server.URL + "/v1/systemone"})
	if _, err := client.Classify(context.Background(), "", []string{"a", "b"}, "hello"); err == nil {
		t.Fatalf("expected error for selected value outside candidate intents")
	}
}

func TestMatchSelectedIntent(t *testing.T) {
	intents := []string{"fixing tests", "quick question"}

	got, err := matchSelectedIntent("Quick Question", intents)
	if err != nil {
		t.Fatalf("matchSelectedIntent failed: %v", err)
	}
	if got != "quick question" {
		t.Errorf("expected quick question, got %q", got)
	}

	gotNone, errNone := matchSelectedIntent(noneIntentOption, intents)
	if errNone != nil || gotNone != "" {
		t.Errorf("expected empty string and nil error for noneIntentOption, got (%q, %v)", gotNone, errNone)
	}

	if _, err := matchSelectedIntent("  ", intents); err == nil {
		t.Errorf("expected error for empty selection")
	}
	if _, err := matchSelectedIntent("other", intents); err == nil {
		t.Errorf("expected error for unknown selection")
	}
}
