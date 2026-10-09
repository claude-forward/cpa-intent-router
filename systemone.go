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
	// systemOneQuestionName 为 System One choice 问题名称，响应结果按该名称取回
	systemOneQuestionName = "intent"

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

// NewSystemOneClient 依据配置实例化 System One 分类器；配置为空或缺少 endpoint 时返回 nil
func NewSystemOneClient(cfg *ClassifierConfig) *SystemOneClient {
	if cfg == nil {
		return nil
	}
	if cfg.Type != "" && cfg.Type != ClassifierTypeSystemOne {
		return nil
	}
	endpoint := strings.TrimSpace(os.ExpandEnv(cfg.Endpoint))
	if endpoint == "" {
		return nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultSystemOneTimeout
	}
	return &SystemOneClient{
		endpoint: endpoint,
		apiKey:   strings.TrimSpace(os.ExpandEnv(cfg.APIKey)),
		model:    strings.TrimSpace(os.ExpandEnv(cfg.Model)),
		timeout:  timeout,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// Enabled 报告分类器是否已就绪可用
func (c *SystemOneClient) Enabled() bool {
	return c != nil && c.endpoint != ""
}

// Timeout 返回单次分类请求超时
func (c *SystemOneClient) Timeout() time.Duration {
	if c == nil || c.timeout <= 0 {
		return defaultSystemOneTimeout
	}
	return c.timeout
}

// Model 返回使用的决策模型名：优先采用全局配置，其次回退到组的 classifier 字段
func (c *SystemOneClient) Model(fallback string) string {
	if c != nil && c.model != "" {
		return c.model
	}
	return strings.TrimSpace(fallback)
}

// Classify 向 System One 服务发起 choice 判定并解析 results.selected 得到命中的意图
func (c *SystemOneClient) Classify(ctx context.Context, model string, intents []string, userText string) (string, error) {
	if !c.Enabled() {
		return "", errors.New("systemone classifier is not configured")
	}
	if len(intents) == 0 || strings.TrimSpace(userText) == "" {
		return "", nil
	}

	reqBody := map[string]any{
		"input": userText,
		"questions": []map[string]any{
			{
				"type":    "choice",
				"name":    systemOneQuestionName,
				"options": intents,
			},
		},
	}
	if model != "" {
		reqBody["model"] = model
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

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxSystemOneResponseBytes))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("systemone request failed with status %d", resp.StatusCode)
	}

	selected := gjson.GetBytes(respBytes, "results."+systemOneQuestionName+".selected").String()
	return matchSelectedIntent(selected, intents)
}

// matchSelectedIntent 将 System One 返回的 selected 值映射回候选意图列表
func matchSelectedIntent(selected string, intents []string) (string, error) {
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return "", errors.New("systemone response has no selected value")
	}
	for _, in := range intents {
		if strings.EqualFold(strings.TrimSpace(in), selected) {
			return in, nil
		}
	}
	return "", fmt.Errorf("systemone selected %q is not a candidate intent", selected)
}
