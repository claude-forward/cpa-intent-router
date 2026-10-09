package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// PluginConfig 插件全局配置
type PluginConfig struct {
	// Enabled 是否启用意图路由插件（默认 true）
	Enabled bool `json:"enabled" yaml:"enabled"`

	// ConfigFile 可选：从独立外部文件加载组与规则配置（支持 YAML/JSON）
	ConfigFile string `json:"config_file,omitempty" yaml:"config_file,omitempty"`

	// Groups 路由组定义列表
	Groups []GroupConfig `json:"groups,omitempty" yaml:"groups,omitempty"`
}

// GroupConfig 虚拟路由组配置
type GroupConfig struct {
	// ID 组唯一标识，客户端通过 "group/<id>" 或直接 "<id>" 请求该虚拟模型
	ID string `json:"id" yaml:"id"`

	// Name 组显示名称，用于 /v1/models 列表展示与日志记录
	Name string `json:"name" yaml:"name"`

	// Members 组内包含的模型列表，格式支持 "provider/model[:effort]" 或纯模型名
	Members []string `json:"members" yaml:"members"`

	// Classifier 意图判别器小模型（如 "gemini-2.5-flash"、"deepseek-chat"）
	// 当 rules 包含自然语言意图（intent）时，宿主内部将直接调用该小模型极简判别
	Classifier string `json:"classifier,omitempty" yaml:"classifier,omitempty"`

	// Effort 预留组推理深度设置（如 "auto"）
	Effort string `json:"effort,omitempty" yaml:"effort,omitempty"`

	// Context 预留组最大上下文限制（tokens）
	Context int `json:"context,omitempty" yaml:"context,omitempty"`

	// Rules 分流规则列表：按从上到下顺序逐条匹配，首条命中即终止并分流
	Rules []RuleConfig `json:"rules,omitempty" yaml:"rules,omitempty"`

	// Fallback 当所有规则均未命中时的兜底目标模型（默认使用 members[0]）
	Fallback string `json:"fallback,omitempty" yaml:"fallback,omitempty"`
}

// RuleConfig 单条分流匹配规则（一条规则内的所有已配置条件必须同时满足才算命中，即 AND 逻辑）
type RuleConfig struct {
	// Use 规则命中时分流的目标模型（必填，如 "claude/claude-3-7-sonnet:high"）
	Use string `json:"use" yaml:"use"`

	// Tokens 长度分流条件：请求上下文估算 Token 数量达到此阈值时匹配（如 200000）
	Tokens int `json:"tokens,omitempty" yaml:"tokens,omitempty"`

	// Images 多模态分流条件：请求消息体中包含图片或文件附件时匹配
	Images bool `json:"images,omitempty" yaml:"images,omitempty"`

	// Effort 思考深度分流条件：请求要求 reasoning 思考（"on", "low", "medium", "high", "xhigh", "max"）
	Effort string `json:"effort,omitempty" yaml:"effort,omitempty"`

	// Agents 客户端标识分流条件：来源 Agent 匹配指定列表（如 ["claude", "codex", "opencode"]）
	Agents []string `json:"agents,omitempty" yaml:"agents,omitempty"`

	// Intent 意图分类分流条件：自然语言意图描述（如 "writing or fixing tests", "a quick question"）
	// 触发前置小模型分类器判定
	Intent string `json:"intent,omitempty" yaml:"intent,omitempty"`

	// Compact 会话压缩分流条件：请求为客户端对话自动压缩/总结操作（如 /compact）时匹配
	Compact bool `json:"compact,omitempty" yaml:"compact,omitempty"`

	// Time 时间窗口分流条件：当前请求本地时间满足指定时段和星期几时匹配
	Time *TimeWindow `json:"time,omitempty" yaml:"time,omitempty"`
}

// TimeWindow 时间窗口定义
type TimeWindow struct {
	// From 开始时间，格式 "HH:MM"（24小时制，如 "14:00"）
	From string `json:"from" yaml:"from"`

	// To 结束时间，格式 "HH:MM"（支持跨午夜，如 22:00 -> 08:00）
	To string `json:"to" yaml:"to"`

	// Days 适用的星期几列表（如 ["mon", "tue", "wed", "thu", "fri"]，空表示每天生效）
	Days []string `json:"days,omitempty" yaml:"days,omitempty"`
}

func defaultPluginConfig() PluginConfig {
	return PluginConfig{
		Enabled: true,
		Groups:  nil,
	}
}

// decodeConfig 解析 YAML 或 JSON 配置字节
func decodeConfig(raw []byte) (PluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(raw) == 0 {
		return cfg, nil
	}

	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		if jsonErr := json.Unmarshal(raw, &cfg); jsonErr != nil {
			return PluginConfig{}, fmt.Errorf("failed to parse config (yaml: %v, json: %v)", err, jsonErr)
		}
	}

	if cfg.ConfigFile != "" {
		if err := loadExternalConfigFile(&cfg); err != nil {
			return PluginConfig{}, fmt.Errorf("failed to load external config file %q: %w", cfg.ConfigFile, err)
		}
	}

	normalizeConfig(&cfg)
	return cfg, nil
}

func loadExternalConfigFile(cfg *PluginConfig) error {
	path := strings.TrimSpace(cfg.ConfigFile)
	if path == "" {
		return nil
	}
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return err
	}

	var wrapper struct {
		Groups []GroupConfig `json:"groups" yaml:"groups"`
	}
	if errYaml := yaml.Unmarshal(data, &wrapper); errYaml == nil && len(wrapper.Groups) > 0 {
		cfg.Groups = wrapper.Groups
		return nil
	}

	var directGroups []GroupConfig
	if errYaml := yaml.Unmarshal(data, &directGroups); errYaml == nil && len(directGroups) > 0 {
		cfg.Groups = directGroups
		return nil
	}

	return fmt.Errorf("no valid group configuration found in %s", cleanPath)
}

func normalizeConfig(cfg *PluginConfig) {
	for i := range cfg.Groups {
		g := &cfg.Groups[i]
		g.ID = strings.TrimSpace(g.ID)
		g.Name = strings.TrimSpace(g.Name)
		g.Classifier = strings.TrimSpace(g.Classifier)
		g.Fallback = strings.TrimSpace(g.Fallback)
		if g.Fallback == "" && len(g.Members) > 0 {
			g.Fallback = g.Members[0]
		}
		for j := range g.Rules {
			r := &g.Rules[j]
			r.Use = strings.TrimSpace(r.Use)
			r.Intent = strings.TrimSpace(r.Intent)
			r.Effort = strings.ToLower(strings.TrimSpace(r.Effort))
			if r.Time != nil {
				r.Time.From = strings.TrimSpace(r.Time.From)
				r.Time.To = strings.TrimSpace(r.Time.To)
				for k := range r.Time.Days {
					r.Time.Days[k] = strings.ToLower(strings.TrimSpace(r.Time.Days[k]))
				}
			}
		}
	}
}

// ParseMember 解析 "provider/model[:effort]" 或 "model" 为 provider 和规范目标模型名
func ParseMember(member string) (provider, targetModel string) {
	member = strings.TrimSpace(member)
	if member == "" {
		return "", ""
	}

	var effort string
	var modelPart string

	if idx := strings.Index(member, "/"); idx != -1 {
		provider = strings.ToLower(strings.TrimSpace(member[:idx]))
		modelPart = strings.TrimSpace(member[idx+1:])
	} else {
		modelPart = member
	}

	if colonIdx := strings.LastIndex(modelPart, ":"); colonIdx != -1 {
		possibleEffort := strings.ToLower(strings.TrimSpace(modelPart[colonIdx+1:]))
		if isEffortLevel(possibleEffort) {
			effort = possibleEffort
			modelPart = strings.TrimSpace(modelPart[:colonIdx])
		}
	}

	if effort != "" && !strings.Contains(modelPart, "(") {
		targetModel = fmt.Sprintf("%s(%s)", modelPart, effort)
	} else {
		targetModel = modelPart
	}

	return provider, targetModel
}

// ResolveProvider 解析目标模型对应的有效 Provider。
// 优先级：
// 1. 若配置中已显式指定（如 "antigravity/claude-sonnet-4-6"、"codex/gpt-6.1-sol"、"kenari/deepseek-flash"），优先采用并映射到真实 Provider 标识；
// 2. 若未显式指定（如 "deepseek-flash"），优先在宿主全局配置动态映射表中查找匹配的 Provider；
// 3. 若映射表未收录（如 OAuth 动态挂载凭据），结合宿主传入的 availableProviders 智能匹配；
// 确保 Target 永不为空且存在于 availableProviders 中（避免触发宿主 unavailable provider / invalid target 拒绝）。
func ResolveProvider(specifiedProvider, model string, modelMappings map[string]string, availableProviders []string) string {
	specifiedProvider = strings.ToLower(strings.TrimSpace(specifiedProvider))
	modelLower := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.Index(modelLower, "("); idx != -1 {
		modelLower = strings.TrimSpace(modelLower[:idx])
	}

	has := func(p string) bool {
		for _, ap := range availableProviders {
			if strings.EqualFold(ap, p) {
				return true
			}
		}
		return false
	}

	// 1. 若用户在规则中显式指定了 provider
	if specifiedProvider != "" {
		if has(specifiedProvider) {
			return specifiedProvider
		}
		compatKey := "openai-compatible-" + specifiedProvider
		if has(compatKey) {
			return compatKey
		}
		if specifiedProvider == "openai-compatibility" {
			if modelMappings != nil {
				if p, ok := modelMappings[modelLower]; ok && strings.HasPrefix(p, "openai-compatible-") && has(p) {
					return p
				}
			}
			for _, ap := range availableProviders {
				if strings.HasPrefix(strings.ToLower(ap), "openai-compatible-") {
					return ap
				}
			}
		}
		return specifiedProvider
	}

	// 2. 未显式指定 provider（如 use: "deepseek-flash" 或 use: "claude-sonnet-4-6"）
	// 2a. 优先从宿主配置模型映射表中查出该模型所属的实际 provider
	if modelMappings != nil {
		if p, ok := modelMappings[modelLower]; ok && p != "" {
			if has(p) {
				return p
			}
			if p == "openai-compatibility" {
				for _, ap := range availableProviders {
					if strings.HasPrefix(strings.ToLower(ap), "openai-compatible-") {
						return ap
					}
				}
			}
		}
	}

	// 2b. 常见模型前缀与凭据类型智能匹配（如 OAuth 动态凭据模型）
	if strings.HasPrefix(modelLower, "claude-") || strings.HasPrefix(modelLower, "gemini-") {
		if has("antigravity") {
			return "antigravity"
		}
		if has("claude") {
			return "claude"
		}
		if has("gemini") {
			return "gemini"
		}
	}
	if strings.HasPrefix(modelLower, "gpt-") {
		if has("codex") {
			return "codex"
		}
		if has("openai") {
			return "openai"
		}
	}
	if strings.HasPrefix(modelLower, "grok-") || strings.HasPrefix(modelLower, "xai-") {
		if has("xai") {
			return "xai"
		}
	}

	// 2c. 兜底策略：若为 openai-compatible-* 模型，优先选取
	for _, ap := range availableProviders {
		if strings.HasPrefix(strings.ToLower(ap), "openai-compatible-") {
			return ap
		}
	}
	if has("openai-compatibility") {
		return "openai-compatibility"
	}
	if has("openai") {
		return "openai"
	}
	if len(availableProviders) > 0 {
		return availableProviders[0]
	}
	return "openai"
}

// FindHostConfigPath 探测宿主主配置文件路径
func FindHostConfigPath() string {
	for i := 0; i < len(os.Args); i++ {
		arg := os.Args[i]
		if (arg == "-config" || arg == "--config") && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
		if strings.HasPrefix(arg, "-config=") {
			return strings.TrimPrefix(arg, "-config=")
		}
		if strings.HasPrefix(arg, "--config=") {
			return strings.TrimPrefix(arg, "--config=")
		}
	}
	candidates := []string{
		"config.yaml",
		"/data/cli/config.yaml",
		"../config.yaml",
		"../../config.yaml",
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

// LoadHostModelMappings 从宿主整体配置中解析各个 Provider 及其注册的模型列表
func LoadHostModelMappings(configPath string) map[string]string {
	mappings := make(map[string]string)
	if configPath == "" {
		configPath = FindHostConfigPath()
	}
	if configPath == "" {
		return mappings
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return mappings
	}

	return ParseHostModelMappingsFromYAML(data)
}

// ParseHostModelMappingsFromYAML 解析宿主配置 YAML 字节构建 model -> provider 索引
func ParseHostModelMappingsFromYAML(data []byte) map[string]string {
	mappings := make(map[string]string)
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil || len(root.Content) == 0 {
		return mappings
	}

	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return mappings
	}

	for i := 0; i < len(doc.Content)-1; i += 2 {
		keyNode := doc.Content[i]
		valNode := doc.Content[i+1]
		key := strings.ToLower(strings.TrimSpace(keyNode.Value))

		var provider string
		switch key {
		case "openai-compatibility":
			if valNode.Kind == yaml.SequenceNode {
				for _, item := range valNode.Content {
					compatName := extractMappingField(item, "name")
					itemProvider := "openai-compatibility"
					if compatName != "" {
						itemProvider = "openai-compatible-" + strings.ToLower(compatName)
					}
					extractModelsFromYAMLNode(item, itemProvider, mappings)
					if compatName != "" {
						mappings["__compat__"+strings.ToLower(compatName)] = itemProvider
					}
				}
			}
			continue
		case "codex-api-key":
			provider = "codex"
		case "claude-api-key":
			provider = "claude"
		case "gemini-api-key":
			provider = "gemini"
		case "xai-api-key":
			provider = "xai"
		case "vertex-api-key":
			provider = "vertex"
		case "meta-api-key":
			provider = "meta"
		}

		if provider != "" && valNode.Kind == yaml.SequenceNode {
			for _, item := range valNode.Content {
				extractModelsFromYAMLNode(item, provider, mappings)
			}
		}
	}

	return mappings
}

func extractMappingField(node *yaml.Node, fieldName string) string {
	if node.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i < len(node.Content)-1; i += 2 {
		if strings.EqualFold(strings.TrimSpace(node.Content[i].Value), fieldName) {
			return strings.TrimSpace(node.Content[i+1].Value)
		}
	}
	return ""
}

func extractModelsFromYAMLNode(node *yaml.Node, provider string, mappings map[string]string) {
	if node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i < len(node.Content)-1; i += 2 {
		k := strings.ToLower(strings.TrimSpace(node.Content[i].Value))
		v := node.Content[i+1]
		if k == "models" && v.Kind == yaml.SequenceNode {
			for _, modelNode := range v.Content {
				if modelNode.Kind == yaml.MappingNode {
					for mIdx := 0; mIdx < len(modelNode.Content)-1; mIdx += 2 {
						mk := strings.ToLower(strings.TrimSpace(modelNode.Content[mIdx].Value))
						mv := strings.ToLower(strings.TrimSpace(modelNode.Content[mIdx+1].Value))
						if (mk == "name" || mk == "alias") && mv != "" {
							mappings[mv] = provider
						}
					}
				}
			}
		}
	}
}

func isEffortLevel(val string) bool {
	switch val {
	case "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// MatchTimeWindow 判断时间是否落在时间窗口内
func MatchTimeWindow(w *TimeWindow, now time.Time) bool {
	if w == nil {
		return true
	}
	if len(w.Days) > 0 {
		weekdayStr := strings.ToLower(now.Weekday().String()[:3])
		matchedDay := false
		for _, d := range w.Days {
			if strings.EqualFold(d, weekdayStr) {
				matchedDay = true
				break
			}
		}
		if !matchedDay {
			return false
		}
	}

	if w.From == "" && w.To == "" {
		return true
	}

	fromMin, okFrom := parseClockMinutes(w.From)
	toMin, okTo := parseClockMinutes(w.To)
	if !okFrom || !okTo {
		return true
	}

	nowMin := now.Hour()*60 + now.Minute()
	if fromMin <= toMin {
		return nowMin >= fromMin && nowMin < toMin
	}
	return nowMin >= fromMin || nowMin < toMin
}

func parseClockMinutes(s string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
