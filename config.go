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

// PluginConfig represents the overall plugin configuration.
type PluginConfig struct {
	Enabled    bool          `json:"enabled" yaml:"enabled"`
	ConfigFile string        `json:"config_file,omitempty" yaml:"config_file,omitempty"`
	GatewayURL string        `json:"gateway_url,omitempty" yaml:"gateway_url,omitempty"`
	GatewayKey string        `json:"gateway_key,omitempty" yaml:"gateway_key,omitempty"`
	Groups     []GroupConfig `json:"groups,omitempty" yaml:"groups,omitempty"`
}

// GroupConfig defines a virtual routing group.
type GroupConfig struct {
	ID         string       `json:"id" yaml:"id"`
	Name       string       `json:"name" yaml:"name"`
	Members    []string     `json:"members" yaml:"members"`
	Classifier string       `json:"classifier,omitempty" yaml:"classifier,omitempty"`
	Effort     string       `json:"effort,omitempty" yaml:"effort,omitempty"`
	Context    int          `json:"context,omitempty" yaml:"context,omitempty"`
	Rules      []RuleConfig `json:"rules,omitempty" yaml:"rules,omitempty"`
	Fallback   string       `json:"fallback,omitempty" yaml:"fallback,omitempty"`
}

// RuleConfig defines a conditional routing rule within a group.
type RuleConfig struct {
	Use     string      `json:"use" yaml:"use"`
	Tokens  int         `json:"tokens,omitempty" yaml:"tokens,omitempty"`
	Images  bool        `json:"images,omitempty" yaml:"images,omitempty"`
	Effort  string      `json:"effort,omitempty" yaml:"effort,omitempty"`
	Agents  []string    `json:"agents,omitempty" yaml:"agents,omitempty"`
	Intent  string      `json:"intent,omitempty" yaml:"intent,omitempty"`
	Compact bool        `json:"compact,omitempty" yaml:"compact,omitempty"`
	Time    *TimeWindow `json:"time,omitempty" yaml:"time,omitempty"`
}

// TimeWindow defines time-based routing criteria.
type TimeWindow struct {
	From string   `json:"from" yaml:"from"`
	To   string   `json:"to" yaml:"to"`
	Days []string `json:"days,omitempty" yaml:"days,omitempty"`
}

func defaultPluginConfig() PluginConfig {
	return PluginConfig{
		Enabled:    true,
		GatewayURL: "http://127.0.0.1:8000",
		Groups:     nil,
	}
}

// decodeConfig parses YAML or JSON configuration bytes.
func decodeConfig(raw []byte) (PluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(raw) == 0 {
		return cfg, nil
	}

	// Try YAML first (YAML is a superset of JSON)
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		if jsonErr := json.Unmarshal(raw, &cfg); jsonErr != nil {
			return PluginConfig{}, fmt.Errorf("failed to parse config (yaml: %v, json: %v)", err, jsonErr)
		}
	}

	// If external config file is specified, load groups from it
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

	// Support file with top-level "groups" or an array of groups directly
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
	cfg.GatewayURL = strings.TrimRight(strings.TrimSpace(cfg.GatewayURL), "/")
	if cfg.GatewayURL == "" {
		cfg.GatewayURL = "http://127.0.0.1:8000"
	}
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

// ParseMember parses "provider/model[:effort]" or "model" into provider and canonical target model.
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

	// Extract reasoning effort if specified like model:high
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

func isEffortLevel(val string) bool {
	switch val {
	case "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// MatchTimeWindow evaluates if a given timestamp falls within the time window.
func MatchTimeWindow(w *TimeWindow, now time.Time) bool {
	if w == nil {
		return true
	}
	if len(w.Days) > 0 {
		weekdayStr := strings.ToLower(now.Weekday().String()[:3]) // "mon", "tue", ...
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
	// Cross-midnight window (e.g. 22:00 -> 08:00)
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
