package guardrails

import (
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

type pipelineOutcome struct {
	blocked      *compiledRule
	blockPhase   phase
	executions   []execution
	replacements map[string]string
}

func (o *pipelineOutcome) record(rule compiledRule, phase phase, status executionStatus, findings int) {
	o.executions = append(o.executions, execution{
		RuleID:   rule.rule.ID,
		RuleName: rule.rule.Name,
		Phase:    phase,
		Action:   rule.rule.Action,
		Status:   status,
		Findings: findings,
	})
}

func runRules(rules []compiledRule, phase phase, texts []string) pipelineOutcome {
	outcome := pipelineOutcome{}
	for _, rule := range rules {
		if !rule.rule.Enabled || !rule.rule.appliesTo(phase) {
			continue
		}

		var findings []Finding
		for _, text := range texts {
			if text == "" {
				continue
			}
			var det detector
			if rule.regexes != nil {
				det = newRegexDetector(rule.regexes)
			} else if rule.pii != nil {
				det = rule.pii
			}
			if det == nil {
				continue
			}
			findings = append(findings, det.inspect(text, rule.maxHits)...)
		}

		if len(findings) == 0 {
			outcome.record(rule, phase, statusSuccess, 0)
			continue
		}

		switch rule.rule.Action {
		case ActionBlock:
			outcome.record(rule, phase, statusIntervened, len(findings))
			outcome.blocked = &rule
			outcome.blockPhase = phase
			return outcome
		case ActionRedact:
			ruleReplacements := mergeReplacements(nil, rule, findings)
			applyToValues(texts, ruleReplacements)
			outcome.replacements = mergeReplacements(outcome.replacements, rule, findings)
			outcome.record(rule, phase, statusIntervened, len(findings))
		case ActionLog:
			outcome.record(rule, phase, statusIntervened, len(findings))
		}
	}
	return outcome
}

func mergeReplacements(dest map[string]string, rule compiledRule, findings []Finding) map[string]string {
	if dest == nil {
		dest = make(map[string]string, len(findings))
	}
	replacement := rule.rule.Replacement
	numbered := replacement == ""
	counts := make(map[string]int)
	for _, finding := range findings {
		if finding.Value == "" {
			continue
		}
		if _, exists := dest[finding.Value]; exists {
			continue
		}
		if numbered {
			counts[finding.Type]++
			dest[finding.Value] = "[" + strings.ToUpper(finding.Type) + "_" + strconv.Itoa(counts[finding.Type]) + "]"
		} else {
			dest[finding.Value] = replacement
		}
	}
	return dest
}

func applyToValues(texts []string, replacements map[string]string) {
	if len(replacements) == 0 {
		return
	}
	for i, text := range texts {
		texts[i] = schemas.ApplyLiteralReplacements(text, replacements)
	}
}

func applyReplacements(texts []*string, replacements map[string]string) {
	if len(replacements) == 0 {
		return
	}
	for _, text := range texts {
		if text == nil {
			continue
		}
		*text = schemas.ApplyLiteralReplacements(*text, replacements)
	}
}
