package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

// TurnDecision records the model and provider selected for a given conversation turn.
type TurnDecision struct {
	TargetProvider string
	TargetModel    string
	Intent         string
	UpdatedAt      time.Time
}

// SessionTracker tracks conversation session state to preserve model affinity within a turn.
type SessionTracker struct {
	mu       sync.RWMutex
	sessions map[string]TurnDecision
	ttl      time.Duration
}

// NewSessionTracker creates a session tracker with a specified retention TTL.
func NewSessionTracker(ttl time.Duration) *SessionTracker {
	if ttl <= 0 {
		ttl = 1 * time.Hour
	}
	return &SessionTracker{
		sessions: make(map[string]TurnDecision),
		ttl:      ttl,
	}
}

// GenerateSessionKey produces a unique conversation key from headers or request payload.
func (st *SessionTracker) GenerateSessionKey(groupID string, req pluginapi.ModelRouteRequest) string {
	// 1. Try explicit session headers
	for _, h := range []string{
		"X-Session-Id",
		"X-Claude-Session-Id",
		"Session-Id",
		"Prompt-Cache-Key",
		"Conversation-Id",
	} {
		if val := req.Headers.Get(h); strings.TrimSpace(val) != "" {
			return groupID + "|" + strings.TrimSpace(val)
		}
	}

	// 2. Try body conversation/session fields
	if len(req.Body) > 0 {
		parsed := gjson.ParseBytes(req.Body)
		if convID := parsed.Get("conversation_id").String(); convID != "" {
			return groupID + "|" + convID
		}
		if pck := parsed.Get("prompt_cache_key").String(); pck != "" {
			return groupID + "|" + pck
		}
	}

	// 3. Fallback to hash of the conversation's first user message
	firstWordsHash := extractFirstUserWordsHash(req.Body)
	if firstWordsHash != "" {
		return groupID + "|firstwords:" + firstWordsHash
	}

	return ""
}

func extractFirstUserWordsHash(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	parsed := gjson.ParseBytes(body)
	messages := parsed.Get("messages")
	if !messages.IsArray() {
		messages = parsed.Get("contents")
	}
	if !messages.IsArray() {
		return ""
	}

	var firstText string
	messages.ForEach(func(_, msg gjson.Result) bool {
		role := strings.ToLower(msg.Get("role").String())
		if role == "user" {
			content := msg.Get("content")
			if content.Type == gjson.String {
				firstText = content.String()
				return false
			}
			if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					if part.Get("type").String() == "text" || part.Get("text").Exists() {
						firstText = part.Get("text").String()
						return false
					}
					return true
				})
				return false
			}
		}
		return true
	})

	if firstText == "" {
		return ""
	}

	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(firstText)))
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// GetDecision retrieves the active decision for the session key if within TTL.
func (st *SessionTracker) GetDecision(key string, now time.Time) (TurnDecision, bool) {
	if key == "" {
		return TurnDecision{}, false
	}
	st.mu.RLock()
	d, ok := st.sessions[key]
	st.mu.RUnlock()
	if !ok {
		return TurnDecision{}, false
	}
	if now.Sub(d.UpdatedAt) > st.ttl {
		return TurnDecision{}, false
	}
	return d, true
}

// SetDecision records the chosen model decision for the session key.
func (st *SessionTracker) SetDecision(key string, d TurnDecision, now time.Time) {
	if key == "" {
		return
	}
	d.UpdatedAt = now
	st.mu.Lock()
	st.sessions[key] = d
	if len(st.sessions) > 4096 {
		st.evictExpiredLocked(now)
	}
	st.mu.Unlock()
}

func (st *SessionTracker) evictExpiredLocked(now time.Time) {
	for k, v := range st.sessions {
		if now.Sub(v.UpdatedAt) > st.ttl {
			delete(st.sessions, k)
		}
	}
}
