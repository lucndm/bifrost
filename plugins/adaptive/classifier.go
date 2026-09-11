package adaptive

import (
	"strings"
)

// ErrorAction says what a matching rule does to the route that produced the error.
type ErrorAction string

const (
	// ErrorActionCooldown penalizes the route and puts it on an exponential
	// cooldown, during which it receives no traffic. Use for errors that are
	// the route's fault and are worth waiting out briefly (rate limits, 5xx).
	ErrorActionCooldown ErrorAction = "cooldown"
	// ErrorActionPenalize counts the error against the route's health score
	// but never cools it down. Default for unclassified errors.
	ErrorActionPenalize ErrorAction = "penalize"
	// ErrorActionFailFast leaves the route's health untouched: the request was
	// bad, not the route, so cooling the route down would be wrong. The error
	// still propagates to the client (or core's retry/fallback logic) as-is.
	ErrorActionFailFast ErrorAction = "fail_fast"
)

// ErrorScope says how far a cooldown reaches. The default is route-scoped:
// one model of one key cooling down. Key-scoped cooldowns block the key
// across every model — the right shape for auth and billing failures, where
// the key is the broken thing and no model of it will work either.
type ErrorScope string

const (
	// ScopeRoute (default when empty) cools the (provider, model, key) route.
	ScopeRoute ErrorScope = "route"
	// ScopeKey cools the (provider, key) pair across all models.
	ScopeKey ErrorScope = "key"
)

// ErrorRule is one entry in the ordered error-classification table. Policy is
// data, not code: adding or retuning a rule is a config edit, not a deploy.
//
// A rule matches an error when every listed dimension agrees:
//   - StatusCodes empty means "any status" (network errors carry none);
//   - TextPatterns empty means "any message"; otherwise any listed substring,
//     matched case-insensitively against the error message and type, agrees.
//
// The first matching rule wins, so order specific rules before generic ones.
type ErrorRule struct {
	Name         string      `json:"name"`
	StatusCodes  []int       `json:"status_codes,omitempty"`
	TextPatterns []string    `json:"text_patterns,omitempty"`
	Action       ErrorAction `json:"action"`
	// CooldownMs is the base cooldown for ErrorActionCooldown. The tracker
	// multiplies it by 2^(strikes-1) per consecutive failure and caps the
	// result, so a route that keeps failing waits longer each time.
	CooldownMs int64 `json:"cooldown_ms,omitempty"`
	// Scope widens the cooldown: ScopeRoute (default) cools only the route
	// that failed; ScopeKey cools the key across every model, for errors that
	// are about the key itself (deactivated, banned, out of credit).
	Scope ErrorScope `json:"scope,omitempty"`
}

// Classification is what the classifier decided about one error.
type Classification struct {
	Rule       string
	Action     ErrorAction
	CooldownMs int64
	Scope      ErrorScope
}

// Default cooldown bases, in milliseconds. Tuned so a transient provider
// hiccup recovers within one or two recompute cycles instead of being skipped
// forever, while a banned or exhausted key stays out long enough not to burn
// requests on it.
const (
	defaultRateLimitCooldownMs = int64(2000)
	defaultServerErrorCooldown = int64(1000)
	defaultAuthErrorCooldownMs = int64(30_000)
	defaultBannedCooldownMs    = int64(60_000)
	defaultNetworkCooldownMs   = int64(1000)
	maxCooldownMs              = int64(240_000)
)

// DefaultErrorRules is the shipped classification table, ordered first-match
// wins. It encodes the two distinctions that matter operationally: errors a
// different route would avoid (cooldown) versus errors no route would avoid
// (fail fast), and brief outages worth waiting out versus accounts that are
// gone for a while.
func DefaultErrorRules() []ErrorRule {
	return []ErrorRule{
		{
			Name:         "rate-limit",
			StatusCodes:  []int{429},
			TextPatterns: []string{"rate limit", "quota", "usage limit", "resource_exhausted", "too many requests"},
			Action:       ErrorActionCooldown,
			CooldownMs:   defaultRateLimitCooldownMs,
		},
		{
			// 5xx errors penalize through the error rate instead of arming a
			// cooldown: the enterprise design fails a direction on *sustained*
			// errors, and the exponential error-rate decay forgives a single
			// blip within a cycle or two. Rate limits and dead accounts, by
			// contrast, are worth waiting out explicitly.
			Name:        "server-error",
			StatusCodes: []int{500, 502, 503, 504, 529},
			Action:      ErrorActionPenalize,
		},
		{
			Name:         "account-banned",
			StatusCodes:  []int{401, 402, 403},
			TextPatterns: []string{"deactivated", "banned", "suspended", "blocked", "revoked", "billing", "credit"},
			Action:       ErrorActionCooldown,
			CooldownMs:   defaultBannedCooldownMs,
			Scope:        ScopeKey,
		},
		{
			Name:        "auth-error",
			StatusCodes: []int{401, 402, 403},
			Action:      ErrorActionCooldown,
			CooldownMs:  defaultAuthErrorCooldownMs,
			Scope:       ScopeKey,
		},
		{
			Name:         "network-error",
			TextPatterns: []string{"timeout", "connection reset", "connection refused", "dial tcp", "eof"},
			Action:       ErrorActionCooldown,
			CooldownMs:   defaultNetworkCooldownMs,
		},
		{
			Name:        "bad-request",
			StatusCodes: []int{400, 404, 413, 422},
			Action:      ErrorActionFailFast,
		},
	}
}

// Classifier evaluates errors against an ordered rule table.
type Classifier struct {
	rules []ErrorRule
}

// NewClassifier builds a classifier from the given rules. Nil or empty rules
// fall back to DefaultErrorRules so a misconfigured deployment never loses
// classification entirely.
func NewClassifier(rules []ErrorRule) *Classifier {
	if len(rules) == 0 {
		rules = DefaultErrorRules()
	}
	return &Classifier{rules: rules}
}

// Classify returns the first rule that matches the error. Unmatched errors
// classify as a mild penalize with no cooldown: unknown territory should
// influence scoring but never take a route out of rotation.
func (c *Classifier) Classify(statusCode *int, errType, errMsg string) Classification {
	haystack := strings.ToLower(errType + " " + errMsg)
	for _, rule := range c.rules {
		if !rule.matches(statusCode, haystack) {
			continue
		}
		scope := rule.Scope
		if scope == "" {
			scope = ScopeRoute
		}
		return Classification{Rule: rule.Name, Action: rule.Action, CooldownMs: rule.CooldownMs, Scope: scope}
	}
	return Classification{Rule: "default", Action: ErrorActionPenalize, Scope: ScopeRoute}
}

func (r *ErrorRule) matches(statusCode *int, lowerMsg string) bool {
	if len(r.StatusCodes) > 0 {
		if statusCode == nil {
			return false
		}
		found := false
		for _, code := range r.StatusCodes {
			if *statusCode == code {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(r.TextPatterns) > 0 {
		matched := false
		for _, pattern := range r.TextPatterns {
			if strings.Contains(lowerMsg, strings.ToLower(pattern)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
