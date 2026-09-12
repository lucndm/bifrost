package guardrails

import (
	"fmt"
	"regexp"
	"strings"
)

type piiEntity struct {
	name    string
	pattern *regexp.Regexp
	confirm func(match string) bool
}

type piiDetector struct {
	entities []piiEntity
}

var piiEntityPatterns = map[string]string{
	"email":       `[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`,
	"phone":       `\+\d{1,3}[\s.\-]?\(?\d{2,4}\)?[\s.\-]?\d{3,4}[\s.\-]?\d{3,4}`,
	"credit_card": `\b(?:\d[ \-]?){13,19}\b`,
	"ip_address":  `\b(?:\d{1,3}\.){3}\d{1,3}\b`,
	"api_key":     `\b(?:sk|pk|rk)\-[A-Za-z0-9]{16,}\b`,
}

func newPIIDetector(entities []string) (*piiDetector, error) {
	normalized, err := normalizeEntities(entities)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		normalized = make([]string, 0, len(piiEntityPatterns))
		for name := range piiEntityPatterns {
			normalized = append(normalized, name)
		}
	}

	det := &piiDetector{entities: make([]piiEntity, 0, len(normalized))}
	for _, name := range normalized {
		re, err := regexp.Compile(piiEntityPatterns[name])
		if err != nil {
			return nil, fmt.Errorf("entity %q: %w", name, err)
		}
		det.entities = append(det.entities, piiEntity{
			name:    name,
			pattern: re,
			confirm: piiEntityConfirm[name],
		})
	}
	return det, nil
}

var piiEntityConfirm = map[string]func(string) bool{
	"credit_card": luhnValid,
	"ip_address":  validIPv4,
}

func (d *piiDetector) inspect(text string, maxHits int) []Finding {
	var findings []Finding
	for _, entity := range d.entities {
		matches := entity.pattern.FindAllString(text, -1)
		seen := make(map[string]struct{}, len(matches))
		for _, match := range matches {
			match = strings.TrimSpace(match)
			if match == "" {
				continue
			}
			if entity.confirm != nil && !entity.confirm(match) {
				continue
			}
			if _, dup := seen[match]; dup {
				continue
			}
			seen[match] = struct{}{}
			findings = append(findings, Finding{Type: entity.name, Value: match})
			if len(findings) >= maxHits {
				return findings
			}
		}
	}
	return findings
}

func luhnValid(s string) bool {
	digits := make([]int, 0, len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		} else if r != ' ' && r != '-' {
			return false
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

func validIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > 3 {
			return false
		}
		if len(part) > 1 && part[0] == '0' {
			return false
		}
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
			n = n*10 + int(r-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}
