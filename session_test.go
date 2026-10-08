package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestSessionTracker_HeaderKey(t *testing.T) {
	st := NewSessionTracker(30 * time.Minute)
	headers := http.Header{}
	headers.Set("X-Session-Id", "sess-12345")

	req := pluginapi.ModelRouteRequest{
		Headers: headers,
	}

	key := st.GenerateSessionKey("dev-group", req)
	if key != "dev-group|sess-12345" {
		t.Errorf("expected session key 'dev-group|sess-12345', got %q", key)
	}

	now := time.Now()
	st.SetDecision(key, TurnDecision{
		TargetProvider: "claude",
		TargetModel:    "claude-3-7-sonnet",
	}, now)

	dec, ok := st.GetDecision(key, now)
	if !ok || dec.TargetModel != "claude-3-7-sonnet" {
		t.Fatalf("expected decision claude-3-7-sonnet, got %+v", dec)
	}

	// Test TTL expiration
	_, okExpired := st.GetDecision(key, now.Add(31*time.Minute))
	if okExpired {
		t.Errorf("expected decision to expire after TTL")
	}
}

func TestSessionTracker_FirstWordsFallback(t *testing.T) {
	st := NewSessionTracker(30 * time.Minute)
	body := []byte(`{
		"messages": [
			{"role": "user", "content": "How do I write a web server in Go?"}
		]
	}`)

	req := pluginapi.ModelRouteRequest{
		Body: body,
	}

	key := st.GenerateSessionKey("my-group", req)
	if len(key) <= len("my-group|firstwords:") {
		t.Errorf("expected non-empty firstwords session key, got %q", key)
	}

	// Repeated call with same first user message should yield identical session key
	body2 := []byte(`{
		"messages": [
			{"role": "user", "content": "How do I write a web server in Go?"},
			{"role": "assistant", "content": "You can use net/http."},
			{"role": "user", "content": "Can you show an example?"}
		]
	}`)
	req2 := pluginapi.ModelRouteRequest{Body: body2}
	key2 := st.GenerateSessionKey("my-group", req2)
	if key != key2 {
		t.Errorf("expected stable session key across turns, got %q vs %q", key, key2)
	}
}
