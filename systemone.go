package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	// systemOneQuestionName 为 System One choice 问题名称
	systemOneQuestionName = "intent"

	// noneIntentOption 表示不匹配任何候选意图时的保留选项
	noneIntentOption = "none_of_the_above"

	// defaultSystemOneTimeout 为未显式配置 timeout 时的单次请求超时
	defaultSystemOneTimeout = 3 * time.Second

	// maxSystemOneResponseBytes 限制单次响应体读取上限，避免异常服务返回超大载荷
	maxSystemOneResponseBytes = 1 << 20
)

// SystemOneClient 通过 HTTP 直连 Jev / Clef 等 System One 决策模型服务（POST /v1/systemone）
type SystemOneClient struct {
	endpoint   string
	apiKey     string
	model      string
	timeout    time.Duration
	httpClient *http.Client
}

// NewSystemOneClient 根据配置构造 SystemOneClient。如果 cfg 为空或 endpoint 为空，返回 nil
func NewSystemOneClient(cfg *ClassifierConfig) *SystemOneClient {
	if cfg == nil {
		return nil
	}
	endpoint := strings.TrimSpace(os.ExpandEnv(cfg.Endpoint))
	if endpoint == "" {
		return nil
	}

	apiKey := strings.TrimSpace(os.ExpandEnv(cfg.APIKey))
	model := strings.TrimSpace(os.ExpandEnv(cfg.Model))

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultSystemOneTimeout
	}

	return &SystemOneClient{
		endpoint: endpoint,
		apiKey:   apiKey,
		model:    model,
		timeout:  timeout,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// Enabled 返回客户端是否已配置可用 endpoint
func (c *SystemOneClient) Enabled() bool {
	return c != nil && c.endpoint != ""
}

// Timeout 返回配置的单次请求超时
func (c *SystemOneClient) Timeout() time.Duration {
	if c == nil || c.timeout <= 0 {
		return defaultSystemOneTimeout
	}
	return c.timeout
}

// Model 返回该客户端绑定的全局模型；若未配置全局模型，则使用组级传入的 fallback
func (c *SystemOneClient) Model(fallback string) string {
	if c != nil && c.model != "" {
		return c.model
	}
	return strings.TrimSpace(fallback)
}

// Classify 向 System One 服务发起 choice 判定并解析 selected 得到命中的意图。
// 同时兼容两种主流协议格式：
// 1. TypeSafe 官方规范：{state: ..., questions: {intent: {type: "choice", criteria: [...]}}} -> answers.intent.choice
// 2. 数组格式规范：{input: ..., questions: [{type: "choice", name: "intent", options: [...]}]} -> results.intent.selected
func (c *SystemOneClient) Classify(ctx context.Context, model string, intents []string, userText string) (string, error) {
	if !c.Enabled() {
		return "", errors.New("systemone classifier is not configured")
	}
	if len(intents) == 0 || strings.TrimSpace(userText) == "" {
		return "", nil
	}

	optionsWithNone := append(append([]string(nil), intents...), noneIntentOption)

	criteriaMap := make(map[string]any, len(optionsWithNone))
	for _, opt := range optionsWithNone {
		criteriaMap[opt] = "Category for " + opt
	}

	reqBody := map[string]any{
		"state": userText,
		"input": userText,
		"questions": map[string]any{
			systemOneQuestionName: map[string]any{
				"type":         "choice",
				"instructions": "Classify the user input into the single best matching intent, or choose none_of_the_above if no intent is relevant.",
				"criteria":     criteriaMap,
				"options":      optionsWithNone,
			},
		},
	}
	if model != "" {
		reqBody["model"] = model
	} else if strings.Contains(c.endpoint, "@cf/") || strings.Contains(c.endpoint, "cloudflare") {
		reqBody["model"] = "clef-flash"
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxSystemOneResponseBytes+1))
	if err != nil {
		return "", err
	}
	if len(respBytes) > maxSystemOneResponseBytes {
		return "", errors.New("systemone response exceeds maximum allowed size")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("systemone request failed with status %d", resp.StatusCode)
	}

	var jsonCheck json.RawMessage
	if errJSON := json.Unmarshal(respBytes, &jsonCheck); errJSON != nil {
		return "", fmt.Errorf("systemone response is not valid JSON: %w", errJSON)
	}

	selected := gjson.GetBytes(respBytes, "answers."+systemOneQuestionName+".choice").String()
	if selected == "" {
		selected = gjson.GetBytes(respBytes, "result.answers."+systemOneQuestionName+".choice").String()
	}
	if selected == "" {
		selected = gjson.GetBytes(respBytes, "results."+systemOneQuestionName+".selected").String()
	}
	if selected == "" {
		selected = gjson.GetBytes(respBytes, "result.results."+systemOneQuestionName+".selected").String()
	}
	if selected == "" {
		selected = gjson.GetBytes(respBytes, "answers.0.choice").String()
	}

	return matchSelectedIntent(selected, intents)
}

// matchSelectedIntent 将 System One 返回的 selected 值映射回候选意图列表
func matchSelectedIntent(selected string, intents []string) (string, error) {
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return "", errors.New("systemone response has no selected value")
	}
	if strings.EqualFold(selected, noneIntentOption) {
		return "", nil
	}
	for _, in := range intents {
		if strings.EqualFold(strings.TrimSpace(in), selected) {
			return in, nil
		}
	}
	return "", fmt.Errorf("systemone selected %q is not a candidate intent", selected)
}
