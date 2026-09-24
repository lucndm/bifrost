package adaptive

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func intPtr(i int) *int { return &i }

func TestClassifyRateLimit(t *testing.T) {
	c := NewClassifier(nil)
	cls := c.Classify(intPtr(429), "", "Too many requests, please slow down")
	assert.Equal(t, "rate-limit", cls.Rule)
	assert.Equal(t, ErrorActionCooldown, cls.Action)
	assert.Equal(t, defaultRateLimitCooldownMs, cls.CooldownMs)
}

func TestClassifyServerErrorPenalizes(t *testing.T) {
	c := NewClassifier(nil)
	cls := c.Classify(intPtr(503), "", "upstream unavailable")
	assert.Equal(t, "server-error", cls.Rule)
	// 5xx feeds the error rate (sustained errors fail the route) but does not
	// arm a cooldown — a single blip is forgiven by the decay.
	assert.Equal(t, ErrorActionPenalize, cls.Action)
	assert.Equal(t, int64(0), cls.CooldownMs)
}

func TestClassifyBannedBeatsGenericAuth(t *testing.T) {
	c := NewClassifier(nil)
	banned := c.Classify(intPtr(401), "", "account deactivated by provider")
	assert.Equal(t, "account-banned", banned.Rule)
	assert.Equal(t, defaultBannedCooldownMs, banned.CooldownMs)

	generic := c.Classify(intPtr(403), "", "permission denied for this model")
	assert.Equal(t, "auth-error", generic.Rule)
	assert.Equal(t, defaultAuthErrorCooldownMs, generic.CooldownMs)
}

func TestClassifyNetworkErrorWithoutStatus(t *testing.T) {
	c := NewClassifier(nil)
	cls := c.Classify(nil, "", "dial tcp 1.2.3.4:443: connection refused")
	assert.Equal(t, "network-error", cls.Rule)
	assert.Equal(t, ErrorActionCooldown, cls.Action)
}

func TestClassifyFailFastLeavesRouteAlone(t *testing.T) {
	c := NewClassifier(nil)
	cls := c.Classify(intPtr(400), "invalid_request_error", "messages: field required")
	assert.Equal(t, "bad-request", cls.Rule)
	assert.Equal(t, ErrorActionFailFast, cls.Action)
	assert.Equal(t, int64(0), cls.CooldownMs)
}

func TestClassifyUnknownDefaultsToPenalize(t *testing.T) {
	c := NewClassifier(nil)
	cls := c.Classify(intPtr(418), "teapot", "short and stout")
	assert.Equal(t, "default", cls.Rule)
	assert.Equal(t, ErrorActionPenalize, cls.Action)
	assert.Equal(t, int64(0), cls.CooldownMs)
}

func TestClassifyTextOnlyRuleMatchesAnyStatus(t *testing.T) {
	c := NewClassifier(nil)
	// "timeout" text rule has no status codes, so a 408 carrying timeout text matches it.
	cls := c.Classify(intPtr(408), "", "request timeout while waiting for provider")
	assert.Equal(t, "network-error", cls.Rule)
}

func TestClassifyStatusCodeRuleRequiresStatusMatch(t *testing.T) {
	c := NewClassifier(nil)
	// "rate-limit" lists 429; a 500 with rate-limit text is caught by
	// server-error first (ordered), but a 400 with quota text must not match
	// the rate-limit rule — first-match-wins walks the table in order and
	// bad-request (400) comes after network-error. Assert the ordering holds.
	cls := c.Classify(intPtr(400), "", "quota exceeded for this model")
	assert.Equal(t, "bad-request", cls.Rule, "status-scoped rule must not match a 400 even with quota text")
}

func TestClassifyCustomRulesReplaceDefaults(t *testing.T) {
	custom := []ErrorRule{
		{Name: "always-cool", Action: ErrorActionCooldown, CooldownMs: 5},
	}
	c := NewClassifier(custom)
	cls := c.Classify(intPtr(400), "", "anything")
	assert.Equal(t, "always-cool", cls.Rule)
	assert.Equal(t, int64(5), cls.CooldownMs)
}

func TestBackoffEscalatesAndCaps(t *testing.T) {
	base := int64(1000)
	assert.Equal(t, int64(1000), backoffMs(base, 0, 1))
	assert.Equal(t, int64(2000), backoffMs(base, 0, 2))
	assert.Equal(t, int64(4000), backoffMs(base, 0, 3))
	assert.Equal(t, defaultMaxCooldownMs, backoffMs(base, 0, 20), "must cap at maxCooldownMs")
	assert.Equal(t, int64(60000), backoffMs(base, 60000, 7), "custom cap bounds the backoff")
	assert.Equal(t, defaultServerErrorCooldown, backoffMs(0, 0, 1), "non-positive base falls back")
}
