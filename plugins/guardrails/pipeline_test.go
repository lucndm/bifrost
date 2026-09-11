package guardrails

import (
	"strings"
	"testing"
)

func TestLuhnValid(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"4111111111111111", true},
		{"4111 1111 1111 1111", true},
		{"4111-1111-1111-1111", true},
		{"4111111111111112", false},
		{"1234567890123", false},
		{"not a card", false},
		{"411111111111", false},
	}
	for _, tc := range cases {
		if got := luhnValid(tc.in); got != tc.want {
			t.Errorf("luhnValid(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestValidIPv4(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"192.168.1.1", true},
		{"0.0.0.0", true},
		{"255.255.255.255", true},
		{"256.1.1.1", false},
		{"1.2.3", false},
		{"01.2.3.4", false},
		{"1.2.3.4.5", false},
	}
	for _, tc := range cases {
		if got := validIPv4(tc.in); got != tc.want {
			t.Errorf("validIPv4(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func piiConfig(entities []string) *Config {
	return &Config{
		Enabled: true,
		Rules: []Rule{{
			ID: "pii", Enabled: true, ApplyTo: ApplyToBoth, Action: ActionRedact,
			Detector: DetectorPII, Entities: entities,
		}},
	}
}

func TestPIIDetectorEntities(t *testing.T) {
	cases := []struct {
		entity string
		text   string
		want   bool
	}{
		{"email", "reach me at john.doe+tag@sub.example.co.uk", true},
		{"email", "no email here", false},
		{"phone", "call +84 901 234 567 today", true},
		{"phone", "order 123456 shipped", false},
		{"credit_card", "card 4111 1111 1111 1111 ending", true},
		{"credit_card", "card 4111 1111 1111 1112 ending", false},
		{"credit_card", "invoice 1234567890123 items", false},
		{"ip_address", "server 10.0.255.1 down", true},
		{"ip_address", "version 1.2.99 build", false},
		{"api_key", "key sk-abcdefghijklmnopqrstuvwxyz", true},
		{"api_key", "key sk-short", false},
	}
	for _, tc := range cases {
		cfg := piiConfig([]string{tc.entity})
		rules, err := compileConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		outcome := runRules(rules, phaseInput, []string{tc.text})
		matched := len(outcome.executions) > 0 && outcome.executions[0].Findings > 0
		if matched != tc.want {
			t.Errorf("%s %q: matched = %v, want %v (executions=%v)", tc.entity, tc.text, matched, tc.want, outcome.executions)
		}
	}
}

func TestPIIDefaultAllEntities(t *testing.T) {
	cfg := piiConfig(nil)
	rules, err := compileConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	outcome := runRules(rules, phaseInput, []string{"mail me@a.com from 8.8.8.8"})
	if outcome.blocked != nil && len(outcome.replacements) < 2 {
		t.Fatalf("expected multiple entity replacements, got %v", outcome.replacements)
	}
	if len(outcome.replacements) < 2 {
		t.Fatalf("expected >=2 replacements, got %v", outcome.replacements)
	}
}

func TestStableNumberedTokens(t *testing.T) {
	cfg := piiConfig([]string{"email"})
	rules, err := compileConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"a@b.com and c@d.com", "a@b.com again"}
	outcome := runRules(rules, phaseInput, texts)
	if texts[0] != "[EMAIL_1] and [EMAIL_2]" {
		t.Fatalf("texts[0] = %q", texts[0])
	}
	if texts[1] != "[EMAIL_1] again" {
		t.Fatalf("texts[1] = %q (token must be stable across texts)", texts[1])
	}
	if outcome.replacements["a@b.com"] != "[EMAIL_1]" || outcome.replacements["c@d.com"] != "[EMAIL_2]" {
		t.Fatalf("replacements = %v", outcome.replacements)
	}
}

func TestRegexCaseInsensitiveByDefault(t *testing.T) {
	cfg := &Config{Enabled: true, Rules: []Rule{{
		ID: "r", Enabled: true, ApplyTo: ApplyToInput, Action: ActionBlock,
		Detector: DetectorRegex, Patterns: []string{"forbidden"},
	}}}
	rules, err := compileConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if outcome := runRules(rules, phaseInput, []string{"totally FORBIDDEN word"}); outcome.blocked == nil {
		t.Fatal("case-insensitive match expected by default")
	}

	cfg.Rules[0].CaseSensitive = true
	rules, err = compileConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if outcome := runRules(rules, phaseInput, []string{"totally FORBIDDEN word"}); outcome.blocked != nil {
		t.Fatal("case-sensitive rule must not match uppercased text")
	}
}

func TestMaxFindingsBound(t *testing.T) {
	cfg := &Config{Enabled: true, Rules: []Rule{{
		ID: "r", Enabled: true, ApplyTo: ApplyToInput, Action: ActionRedact,
		Detector: DetectorRegex, Patterns: []string{"[a-z]+"},
	}}}
	rules, err := compileConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("word ", 500)
	outcome := runRules(rules, phaseInput, []string{long})
	if len(outcome.executions) != 1 || outcome.executions[0].Findings > defaultMaxFindingsPerRule {
		t.Fatalf("findings = %d, want <= %d", outcome.executions[0].Findings, defaultMaxFindingsPerRule)
	}
}

func TestLogActionRecordsWithoutMutating(t *testing.T) {
	cfg := &Config{Enabled: true, Rules: []Rule{{
		ID: "log-rule", Enabled: true, ApplyTo: ApplyToInput, Action: ActionLog,
		Detector: DetectorRegex, Patterns: []string{"sensitive"},
	}}}
	rules, err := compileConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"sensitive data"}
	outcome := runRules(rules, phaseInput, texts)
	if outcome.blocked != nil || len(outcome.replacements) != 0 {
		t.Fatalf("log action must not block or replace, got %v %v", outcome.blocked, outcome.replacements)
	}
	if len(outcome.executions) != 1 || outcome.executions[0].Status != statusIntervened {
		t.Fatalf("executions = %v", outcome.executions)
	}
	if texts[0] != "sensitive data" {
		t.Fatalf("text mutated: %q", texts[0])
	}
}

func TestLongestMatchReplacedFirst(t *testing.T) {
	cfg := &Config{Enabled: true, Rules: []Rule{{
		ID: "r", Enabled: true, ApplyTo: ApplyToInput, Action: ActionRedact,
		Detector:    DetectorRegex,
		Patterns:    []string{"secret", "supersecret"},
		Replacement: "[MASKED]",
	}}}
	rules, err := compileConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"top supersecret value"}
	runRules(rules, phaseInput, texts)
	if texts[0] != "top [MASKED] value" {
		t.Fatalf("nested literal not handled longest-first: %q", texts[0])
	}
}
