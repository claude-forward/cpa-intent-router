package main

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

var systemReminderRegex = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// RequestFeatures captures observed attributes from a routing request.
type RequestFeatures struct {
	Tokens   int
	Images   bool
	Effort   string
	Agent    string
	Compact  bool
	Within   bool
	UserText string
	At       time.Time
}

// ExtractFeatures extracts routing criteria from the incoming request.
func ExtractFeatures(req pluginapi.ModelRouteRequest, now time.Time) RequestFeatures {
	feat := RequestFeatures{
		At: now,
	}

	// 1. Detect Agent from User-Agent or custom headers
	feat.Agent = detectAgent(req)

	// 2. Inspect Body
	if len(req.Body) > 0 {
		feat.Tokens = estimateTokens(req.Body)

		parsed := gjson.ParseBytes(req.Body)

		// Check reasoning effort
		feat.Effort = extractEffort(parsed)

		// Check compact
		feat.Compact = isCompactingRequest(parsed)

		// Parse messages to check images, userText, and within-turn status
		inspectMessages(parsed, &feat)
	}

	return feat
}

func detectAgent(req pluginapi.ModelRouteRequest) string {
	ua := strings.ToLower(req.Headers.Get("User-Agent"))
	if strings.Contains(ua, "claude-code") || strings.Contains(ua, "claude_code") {
		return "claude"
	}
	if strings.Contains(ua, "codex") {
		return "codex"
	}
	if strings.Contains(ua, "opencode") {
		return "opencode"
	}
	if strings.Contains(ua, "cursor") {
		return "cursor"
	}
	if strings.Contains(ua, "gemini") {
		return "gemini"
	}
	if strings.Contains(ua, "pi") {
		return "pi"
	}
	return ""
}

func estimateTokens(body []byte) int {
	// A conservative character-based estimation: ~4 chars per token
	if len(body) == 0 {
		return 0
	}
	return len(body) / 4
}

func extractEffort(root gjson.Result) string {
	if eff := root.Get("reasoning_effort").String(); eff != "" {
		return strings.ToLower(eff)
	}
	if eff := root.Get("reasoning.effort").String(); eff != "" {
		return strings.ToLower(eff)
	}
	if root.Get("thinking.budget_tokens").Exists() {
		budget := root.Get("thinking.budget_tokens").Int()
		if budget > 0 {
			return "on"
		}
	}
	if root.Get("generationConfig.thinkingConfig.thinkingBudget").Exists() {
		budget := root.Get("generationConfig.thinkingConfig.thinkingBudget").Int()
		if budget > 0 {
			return "on"
		}
	}
	return ""
}

func isCompactingRequest(root gjson.Result) bool {
	// Claude Code / Compact prompts usually ask to summarize conversation history
	userText := strings.ToLower(root.Get("messages.#(role==\"user\").content").String())
	if strings.Contains(userText, "summary of the conversation so far") ||
		strings.Contains(userText, "compact the conversation") ||
		strings.Contains(userText, "compress this conversation") {
		return true
	}
	return false
}

func inspectMessages(root gjson.Result, feat *RequestFeatures) {
	messages := root.Get("messages")
	if !messages.IsArray() {
		// Gemini uses contents array
		messages = root.Get("contents")
	}
	if !messages.IsArray() {
		return
	}

	var lastUserText string

	messages.ForEach(func(_, msg gjson.Result) bool {
		role := strings.ToLower(msg.Get("role").String())

		// OpenAI tool messages
		if role == "tool" {
			feat.Within = true
		}

		content := msg.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, part gjson.Result) bool {
				partType := strings.ToLower(part.Get("type").String())
				if partType == "image" || partType == "image_url" {
					feat.Images = true
				}
				if partType == "tool_result" {
					// Claude tool_result is sent within role=user
					feat.Within = true
				}
				if role == "user" && (partType == "text" || partType == "") {
					txt := part.Get("text").String()
					if txt != "" {
						lastUserText = txt
					}
				}
				return true
			})
		} else if content.Type == gjson.String {
			if role == "user" {
				lastUserText = content.String()
			}
		}

		// Gemini parts inspection
		parts := msg.Get("parts")
		if parts.IsArray() {
			parts.ForEach(func(_, part gjson.Result) bool {
				if part.Get("inlineData").Exists() || part.Get("fileData").Exists() {
					feat.Images = true
				}
				if part.Get("functionResponse").Exists() {
					feat.Within = true
				}
				if (role == "user" || role == "") && part.Get("text").Exists() {
					lastUserText = part.Get("text").String()
				}
				return true
			})
		}

		return true
	})

	// Clean user text (strip system reminders and truncate if oversized)
	if lastUserText != "" {
		cleanText := strings.TrimSpace(systemReminderRegex.ReplaceAllString(lastUserText, ""))
		runes := []rune(cleanText)
		const maxChars = 4000
		if len(runes) > maxChars {
			cleanText = string(runes[:maxChars])
		}
		feat.UserText = cleanText
	}
}

// MatchRule checks if all conditions of the rule are satisfied.
func MatchRule(rule RuleConfig, feat RequestFeatures, currentIntent string) bool {
	// 1. Tokens threshold
	if rule.Tokens > 0 && feat.Tokens < rule.Tokens {
		return false
	}

	// 2. Images requirement
	if rule.Images && !feat.Images {
		return false
	}

	// 3. Reasoning effort
	if rule.Effort != "" {
		if rule.Effort == "on" {
			if feat.Effort == "" || feat.Effort == "none" {
				return false
			}
		} else if !strings.EqualFold(rule.Effort, feat.Effort) {
			return false
		}
	}

	// 4. Agents restriction
	if len(rule.Agents) > 0 {
		if !slices.Contains(rule.Agents, feat.Agent) {
			return false
		}
	}

	// 5. Compact check
	if rule.Compact && !feat.Compact {
		return false
	}

	// 6. Time window check
	if rule.Time != nil && !MatchTimeWindow(rule.Time, feat.At) {
		return false
	}

	// 7. Intent check
	if rule.Intent != "" {
		if currentIntent == "" || !strings.EqualFold(rule.Intent, currentIntent) {
			return false
		}
	}

	return true
}
