package guardrails

import "regexp"

type regexDetector struct {
	regexes []*regexp.Regexp
}

func (d regexDetector) inspect(text string, maxHits int) []Finding {
	var findings []Finding
	seen := make(map[string]struct{})
	for _, re := range d.regexes {
		matches := re.FindAllString(text, -1)
		for _, match := range matches {
			if match == "" {
				continue
			}
			if _, dup := seen[match]; dup {
				continue
			}
			seen[match] = struct{}{}
			findings = append(findings, Finding{Type: "pattern", Value: match})
			if len(findings) >= maxHits {
				return findings
			}
		}
	}
	return findings
}

func newRegexDetector(regexes []*regexp.Regexp) detector {
	return regexDetector{regexes: regexes}
}
