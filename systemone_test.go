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
	if c := NewSystemOneClient(nil); c != nil {
		t.Errorf("expected nil client for nil config, got %+v", c)
	}
	if c := NewSystemOneClient(&ClassifierConfig{Endpoint: "   "}); c != nil {
		t.Errorf("expected nil client for empty endpoint, got %+v", c)
	}
	if c := NewSystemOneClient(&ClassifierConfig{Type: "host", Endpoint: "https://example.test/v1/systemone"}); c != nil {
		t.Errorf("expected nil client for unsupported type, got %+v", c)
	}

	c := NewSystemOneClient(&ClassifierConfig{Endpoint: "https://example.test/v1/systemone"})
	if !c.Enabled() {
		t.Fatalf("expected client to be enabled")
	}
	if c.Timeout() != defaultSystemOneTimeout {
		t.Errorf("expected default timeout %s, got %s", defaultSystemOneTimeout, c.Timeout())
	}
}

func TestSystemOneClient_ModelFallback(t *testing.T) {
	c := NewSystemOneClient(&ClassifierConfig{Endpoint: "https://example.test/v1/systemone"})
	if got := c.Model("group-classifier"); got != "group-classifier" {
		t.Errorf("expected fallback model group-classifier, got %q", got)
	}

	c2 := NewSystemOneClient(&ClassifierConfig{Endpoint: "https://example.test/v1/systemone", Model: "jev"})
	if got := c2.Model("group-classifier"); got != "jev" {
		t.Errorf("expected configured model jev, got %q", got)
	}
}

func TestSystemOneClient_Classify_Success(t *testing.T) {
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
		_, _ = w.Write([]byte(`{
			"id": "sys1_01923",
			"model": "jev",
			"results": {
				"intent": {
					"selected": "complex_architecture",
					"probabilities": {
						"quick_lookup": 0.02,
						"complex_architecture": 0.96
					}
				}
			},
			"usage": {"prompt_tokens": 65}
		}`))
	}))
	defer server.Close()

	client := NewSystemOneClient(&ClassifierConfig{
		Endpoint: server.URL + "/v1/systemone",
		APIKey:   "ts-secret",
		Model:    "jev",
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
	if gotBody["model"] != "jev" {
		t.Errorf("expected model jev in request body, got %v", gotBody["model"])
	}
	if gotBody["input"] != "How do I implement the saga pattern in Go?" {
		t.Errorf("unexpected input field: %v", gotBody["input"])
	}

	questions, ok := gotBody["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("expected single questions entry, got %v", gotBody["questions"])
	}
	q, ok := questions[0].(map[string]any)
	if !ok {
		t.Fatalf("expected question object, got %T", questions[0])
	}
	if q["type"] != "choice" {
		t.Errorf("expected question type choice, got %v", q["type"])
	}
	if q["name"] != systemOneQuestionName {
		t.Errorf("expected question name %s, got %v", systemOneQuestionName, q["name"])
	}
	options, ok := q["options"].([]any)
	if !ok || len(options) != len(intents) {
		t.Fatalf("expected %d options, got %v", len(intents), q["options"])
	}
	for i, opt := range options {
		if opt != intents[i] {
			t.Errorf("expected option[%d]=%s, got %v", i, intents[i], opt)
		}
	}
}

func TestSystemOneClient_Classify_EnvExpansion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer env-secret" {
			t.Errorf("expected expanded bearer token, got %q", auth)
		}
		_, _ = w.Write([]byte(`{"results":{"intent":{"selected":"quick question"}}}`))
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
		_, _ = w.Write([]byte(`{"results":{"intent":{"selected":"not_a_candidate"}}}`))
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

	if _, err := matchSelectedIntent("  ", intents); err == nil {
		t.Errorf("expected error for empty selection")
	}
	if _, err := matchSelectedIntent("other", intents); err == nil {
		t.Errorf("expected error for unknown selection")
	}
}
