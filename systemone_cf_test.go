package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSystemOneClient_Classify_WorkersAIWrappedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Cloudflare Workers AI wrapped format: result.answers.intent.choice
		_, _ = w.Write([]byte(`{
			"result": {
				"model": "clef-flash",
				"answers": {
					"intent": {
						"type": "choice",
						"choice": "complex_architecture",
						"probabilities": {
							"quick_lookup": 0.002,
							"complex_architecture": 0.995,
							"none_of_the_above": 0.003
						},
						"confidence": 0.985
					}
				},
				"usage": {"input_tokens": 150, "output_tokens": 0}
			},
			"success": true,
			"errors": [],
			"messages": []
		}`))
	}))
	defer server.Close()

	client := NewSystemOneClient(&ClassifierConfig{
		Endpoint: server.URL + "/client/v4/accounts/test/ai/run/@cf/cloudflare/clef-flash",
		APIKey:   "cf-token",
	})

	intents := []string{"quick_lookup", "complex_architecture"}
	got, err := client.Classify(context.Background(), "", intents, "How to design distributed saga?")
	if err != nil {
		t.Fatalf("expected successful classification, got err: %v", err)
	}
	if got != "complex_architecture" {
		t.Errorf("expected complex_architecture, got %q", got)
	}
}
