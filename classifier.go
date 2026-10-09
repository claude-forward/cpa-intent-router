package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

var firstNumberRegex = regexp.MustCompile(`\d+`)

const (
	defaultClassifierTimeout = 8 * time.Second
	defaultCacheTTL           = 10 * time.Minute
	defaultFailureCooldown    = 30 * time.Second
)

type cacheEntry struct {
	intent    string
	expiresAt time.Time
}

type inFlightCall struct {
	done chan struct{}
	res  string
	err  error
}

// HostModelCaller 定义宿主进程内直接执行模型的内部回调函数类型
type HostModelCaller func(ctx context.Context, model string, body []byte) ([]byte, error)

// ClassifierClient 执行结构化意图判别
type ClassifierClient struct {
	caller HostModelCaller

	mu       sync.Mutex
	cache    map[string]cacheEntry
	inflight map[string]*inFlightCall
	failures map[string]time.Time
}

// NewClassifierClient 实例化分类器，接收内部宿主执行回调
func NewClassifierClient(caller HostModelCaller) *ClassifierClient {
	return &ClassifierClient{
		caller:   caller,
		cache:    make(map[string]cacheEntry),
		inflight: make(map[string]*inFlightCall),
		failures: make(map[string]time.Time),
	}
}

// Classify 通过小模型对用户发言在候选意图列表中进行极简结构化分类
func (c *ClassifierClient) Classify(ctx context.Context, model string, intents []string, userText string) (string, error) {
	if len(intents) == 0 || strings.TrimSpace(userText) == "" {
		return "", nil
	}
	if c.caller == nil {
		return "", errors.New("host model caller is not available")
	}

	// 1. 生成唯一缓存与并发合并指纹
	h := sha256.New()
	h.Write([]byte(model + "\x00" + strings.Join(intents, "\x00") + "\x00" + strings.TrimSpace(userText)))
	key := hex.EncodeToString(h.Sum(nil))

	now := time.Now()

	// 2. 检查缓存、故障熔断与单飞合并
	c.mu.Lock()
	if entry, ok := c.cache[key]; ok && now.Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.intent, nil
	}
	if failTime, ok := c.failures[model]; ok && now.Sub(failTime) < defaultFailureCooldown {
		c.mu.Unlock()
		return "", fmt.Errorf("classifier model %q is resting after failure", model)
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.res, call.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	call := &inFlightCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.inflight, key)
		close(call.done)
		c.mu.Unlock()
	}()

	// 3. 内部原生执行极简分类请求
	resultIntent, err := c.executeClassification(ctx, model, intents, userText)
	call.res = resultIntent
	call.err = err

	c.mu.Lock()
	if err != nil {
		c.failures[model] = time.Now()
	} else {
		delete(c.failures, model)
		c.cache[key] = cacheEntry{
			intent:    resultIntent,
			expiresAt: time.Now().Add(defaultCacheTTL),
		}
		if len(c.cache) > 4096 {
			c.evictExpiredCacheLocked(time.Now())
		}
	}
	c.mu.Unlock()

	return resultIntent, err
}

func (c *ClassifierClient) evictExpiredCacheLocked(now time.Time) {
	for k, v := range c.cache {
		if now.After(v.expiresAt) {
			delete(c.cache, k)
		}
	}
}

func (c *ClassifierClient) executeClassification(ctx context.Context, model string, intents []string, userText string) (string, error) {
	prompt := buildClassificationPrompt(intents, userText)

	reqBody := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0,
		"max_tokens":  16,
		"stream":      false,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	// 宿主直接内部执行，避免跨进程/网络回环调用
	respBytes, err := c.caller(ctx, model, bodyBytes)
	if err != nil {
		return "", err
	}

	content := gjson.GetBytes(respBytes, "choices.0.message.content").String()
	return parseIntentAnswer(content, intents)
}

func buildClassificationPrompt(intents []string, userText string) string {
	var b strings.Builder
	b.WriteString("You route a user's message to a coding assistant.\n")
	b.WriteString("Given numbered kinds and the user's message, answer with the number of the kind that fits the message best.\n")
	b.WriteString("Answer 0 only when the message is plainly none of the kinds. Answer with the number only.\n\n")
	b.WriteString("Kinds:\n")
	for i, in := range intents {
		fmt.Fprintf(&b, "%d. %s\n", i+1, in)
	}
	b.WriteString("\nThe user's message:\n<message>\n")
	b.WriteString(userText)
	b.WriteString("\n</message>\n\nThe number of the kind that fits it best (0 only if none does):")
	return b.String()
}

func parseIntentAnswer(answer string, intents []string) (string, error) {
	numStr := firstNumberRegex.FindString(strings.TrimSpace(answer))
	if numStr == "" {
		return "", errors.New("no number found in classifier response")
	}

	n, err := strconv.Atoi(numStr)
	if err != nil || n < 0 || n > len(intents) {
		return "", fmt.Errorf("invalid intent index %d (expected 0 to %d)", n, len(intents))
	}

	if n == 0 {
		return "", nil
	}
	return intents[n-1], nil
}
