package adaptive

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryAfterOverridesExponential(t *testing.T) {
	tr, _ := newTestTracker()
	// A 429 whose Retry-After says 30s: the precise value must win over the
	// exponential estimate (2000ms base).
	tr.Observe("openai", "gpt-4", "key-a", true, 0,
		Classification{Rule: "rate-limit", Action: ErrorActionCooldown, CooldownMs: 2000}, 30_000)
	snap, cooling, _ := tr.Snapshot()
	k := routeKey{Provider: "openai", Model: "gpt-4", KeyName: "key-a"}
	assert.True(t, cooling[k])
	assert.Equal(t, int64(30_000), snap[k].cooldownMs, "parsed Retry-After must replace the exponential estimate")

	// Without a parsed hint, the exponential estimate applies.
	tr.Observe("openai", "gpt-4", "key-b", true, 0,
		Classification{Rule: "rate-limit", Action: ErrorActionCooldown, CooldownMs: 2000}, 0)
	snap, _, _ = tr.Snapshot()
	kb := routeKey{Provider: "openai", Model: "gpt-4", KeyName: "key-b"}
	assert.Equal(t, int64(2000), snap[kb].cooldownMs)
}

func TestKeyWideLockBlocksOtherModels(t *testing.T) {
	tr, _ := newTestTracker()
	// An auth failure on model A locks the KEY across models.
	tr.Observe("openai", "model-a", "dead", true, 0,
		Classification{Rule: "auth-error", Action: ErrorActionCooldown, CooldownMs: 30_000, Scope: ScopeKey}, 0)

	_, keyCooling := tr.keyLockSnapshotForTest()
	assert.True(t, keyCooling[keyLockKey{Provider: "openai", KeyName: "dead"}],
		"key-scoped failure must arm a key-wide lock")

	// A success on that key clears the lock (breaker reset).
	tr.Observe("openai", "model-a", "dead", false, 50, Classification{}, 0)
	_, keyCooling = tr.keyLockSnapshotForTest()
	assert.False(t, keyCooling[keyLockKey{Provider: "openai", KeyName: "dead"}],
		"success must clear the key-wide lock")
}

func TestKeyLockExpiresNaturally(t *testing.T) {
	tr, clock := newTestTracker()
	tr.Observe("openai", "model-a", "dead", true, 0,
		Classification{Rule: "auth-error", Action: ErrorActionCooldown, CooldownMs: 30_000, Scope: ScopeKey}, 0)

	later := time.Now().Add(31 * time.Second)
	*clock = func() time.Time { return later }
	_, keyCooling := tr.keyLockSnapshotForTest()
	assert.False(t, keyCooling[keyLockKey{Provider: "openai", KeyName: "dead"}],
		"expired key lock must not report cooling")
}

// keyLockSnapshotForTest exposes the active key locks for assertions.
func (t *Tracker) keyLockSnapshotForTest() (map[keyLockKey]bool, map[keyLockKey]bool) {
	_, _, locks := t.Snapshot()
	active := make(map[keyLockKey]bool, len(locks))
	for kl := range locks {
		active[kl] = true
	}
	return active, active
}

func TestExhaustionRetryAfter(t *testing.T) {
	e := deterministicEngine(t, 1)
	now := time.Now()

	// No observations: not exhausted.
	_, ok := e.ExhaustionRetryAfterMs("openai", "gpt-4", now)
	assert.False(t, ok)

	// One cooling key: below the two-key confidence floor.
	e.Observe("openai", "gpt-4", "k1", true, 0, intPtr(429), "", "rate limited", 0)
	e.recompute()
	_, ok = e.ExhaustionRetryAfterMs("openai", "gpt-4", now)
	assert.False(t, ok)

	// Two cooling keys: exhausted, with the EARLIEST expiry.
	e.Observe("openai", "gpt-4", "k2", true, 0, intPtr(429), "", "rate limited", 45_000)
	e.recompute()
	ms, ok := e.ExhaustionRetryAfterMs("openai", "gpt-4", now)
	require.True(t, ok)
	assert.Greater(t, ms, int64(0))
	assert.LessOrEqual(t, ms, int64(2000), "earliest expiry is k1's base 2s, not k2's retry-after 45s")

	// A healthy key returns the direction to service.
	e.Observe("openai", "gpt-4", "k3", false, 100, nil, "", "", 0)
	e.recompute()
	_, ok = e.ExhaustionRetryAfterMs("openai", "gpt-4", now)
	assert.False(t, ok)
}

func TestPreLLMHookFailFastShortCircuit(t *testing.T) {
	on := true
	p := newTestPlugin(t, &Config{FailFastWhenExhausted: &on})

	// Both observed keys cooling.
	p.engine.Observe("openai", "gpt-4", "k1", true, 0, intPtr(429), "", "rate limited", 0)
	p.engine.Observe("openai", "gpt-4", "k2", true, 0, intPtr(429), "", "rate limited", 60_000)
	p.engine.recompute()

	ctx := newHookContext()
	req := newChatRequest("openai", "gpt-4", nil) // no fallbacks: this IS the last resort
	_, shortCircuit, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "exhausted last-resort attempt must short-circuit")
	require.NotNil(t, shortCircuit.Error)
	assert.NotNil(t, shortCircuit.Error.StatusCode)
	assert.Equal(t, 503, *shortCircuit.Error.StatusCode)
	assert.Contains(t, shortCircuit.Error.Error.Message, "cooling down")

	// With a fallback remaining, the request must NOT be short-circuited.
	ctx2 := newHookContext()
	req2 := newChatRequest("openai", "gpt-4", []schemas.Fallback{{Provider: "anthropic", Model: "claude-3"}})
	_, shortCircuit2, err := p.PreLLMHook(ctx2, req2)
	require.NoError(t, err)
	assert.Nil(t, shortCircuit2, "a remaining fallback must suppress the short-circuit")

	// Switch off: never short-circuits.
	off := false
	p2 := newTestPlugin(t, &Config{FailFastWhenExhausted: &off})
	p2.engine.Observe("openai", "gpt-4", "k1", true, 0, intPtr(429), "", "rate limited", 0)
	p2.engine.Observe("openai", "gpt-4", "k2", true, 0, intPtr(429), "", "rate limited", 0)
	p2.engine.recompute()
	ctx3 := newHookContext()
	req3 := newChatRequest("openai", "gpt-4", nil)
	_, shortCircuit3, err := p2.PreLLMHook(ctx3, req3)
	require.NoError(t, err)
	assert.Nil(t, shortCircuit3)
}

func TestPreLLMHookFailFastRespectsPins(t *testing.T) {
	on := true
	p := newTestPlugin(t, &Config{FailFastWhenExhausted: &on})
	p.engine.Observe("openai", "gpt-4", "k1", true, 0, intPtr(429), "", "rate limited", 0)
	p.engine.Observe("openai", "gpt-4", "k2", true, 0, intPtr(429), "", "rate limited", 0)
	p.engine.recompute()

	// A caller-pinned key suppresses the fail-fast: the caller asked for a
	// specific key and gets the upstream's own answer for it.
	ctx := newHookContext()
	ctx.SetValue(schemas.BifrostContextKeyAPIKeyName, "caller-key")
	req := newChatRequest("openai", "gpt-4", nil)
	_, shortCircuit, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)
	assert.Nil(t, shortCircuit)
}
