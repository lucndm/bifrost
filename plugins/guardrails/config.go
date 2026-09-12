package guardrails

import (
	"fmt"
	"regexp"
	"strings"
)

const PluginName = "guardrails"

type ApplyTo string

const (
	ApplyToInput  ApplyTo = "input"
	ApplyToOutput ApplyTo = "output"
	ApplyToBoth   ApplyTo = "both"
)

type Action string

const (
	ActionBlock  Action = "block"
	ActionRedact Action = "redact"
	ActionLog    Action = "log"
)

type DetectorType string

const (
	DetectorRegex DetectorType = "regex"
	DetectorPII   DetectorType = "pii"
)

const (
	defaultMaxFindingsPerRule = 100
)

type Rule struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Enabled  bool         `json:"enabled"`
	ApplyTo  ApplyTo      `json:"apply_to"`
	Action   Action       `json:"action"`
	Detector DetectorType `json:"detector"`

	Patterns      []string `json:"patterns,omitempty"`
	CaseSensitive bool     `json:"case_sensitive,omitempty"`

	Entities []string `json:"entities,omitempty"`

	Replacement string `json:"replacement,omitempty"`
}

type Config struct {
	Enabled bool   `json:"enabled"`
	Rules   []Rule `json:"rules"`
}

func (r Rule) appliesTo(phase phase) bool {
	switch r.ApplyTo {
	case ApplyToBoth:
		return true
	case ApplyToInput:
		return phase == phaseInput
	case ApplyToOutput:
		return phase == phaseOutput
	default:
		return false
	}
}

type compiledRule struct {
	rule    Rule
	regexes []*regexp.Regexp
	pii     *piiDetector
	maxHits int
}

type validator struct {
	knownEntities map[string]struct{}
}

func compileConfig(cfg *Config) ([]compiledRule, error) {
	seen := make(map[string]struct{}, len(cfg.Rules))
	compiled := make([]compiledRule, 0, len(cfg.Rules))

	for i, rule := range cfg.Rules {
		if rule.ID == "" {
			return nil, fmt.Errorf("rules[%d]: id is required", i)
		}
		if _, dup := seen[rule.ID]; dup {
			return nil, fmt.Errorf("rules[%d]: duplicate id %q", i, rule.ID)
		}
		seen[rule.ID] = struct{}{}

		if rule.Name == "" {
			rule.Name = rule.ID
		}
		if rule.ApplyTo != ApplyToInput && rule.ApplyTo != ApplyToOutput && rule.ApplyTo != ApplyToBoth {
			return nil, fmt.Errorf("rule %q: apply_to must be one of input, output, both", rule.ID)
		}
		if rule.Action != ActionBlock && rule.Action != ActionRedact && rule.Action != ActionLog {
			return nil, fmt.Errorf("rule %q: action must be one of block, redact, log", rule.ID)
		}

		c := compiledRule{rule: rule, maxHits: defaultMaxFindingsPerRule}

		switch rule.Detector {
		case DetectorRegex:
			if len(rule.Patterns) == 0 {
				return nil, fmt.Errorf("rule %q: regex detector requires at least one pattern", rule.ID)
			}
			for j, pattern := range rule.Patterns {
				if pattern == "" {
					return nil, fmt.Errorf("rule %q: patterns[%d] is empty", rule.ID, j)
				}
				if !rule.CaseSensitive {
					pattern = "(?i)" + pattern
				}
				re, err := regexp.Compile(pattern)
				if err != nil {
					return nil, fmt.Errorf("rule %q: patterns[%d]: %w", rule.ID, j, err)
				}
				c.regexes = append(c.regexes, re)
			}
		case DetectorPII:
			det, err := newPIIDetector(rule.Entities)
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", rule.ID, err)
			}
			c.pii = det
		default:
			return nil, fmt.Errorf("rule %q: detector must be one of regex, pii", rule.ID)
		}

		compiled = append(compiled, c)
	}
	return compiled, nil
}

var knownPIIEntities = func() map[string]struct{} {
	m := make(map[string]struct{}, len(piiEntityPatterns))
	for name := range piiEntityPatterns {
		m[name] = struct{}{}
	}
	return m
}()

func normalizeEntities(entities []string) ([]string, error) {
	if len(entities) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(entities))
	for _, e := range entities {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if _, ok := knownPIIEntities[e]; !ok {
			return nil, fmt.Errorf("unknown pii entity %q", e)
		}
		out = append(out, e)
	}
	return out, nil
}
