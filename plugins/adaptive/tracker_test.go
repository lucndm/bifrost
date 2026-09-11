package adaptive

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// newTestTracker returns a tracker with a controllable clock.
func newTestTracker() (*Tracker, *func() time.Time) {
	t := NewTracker()
	base := time.Now()
	clock := func() time.Time { return base }
	t.now = func() time.Time { return clock() }
	return t, &clock
}

func TestTrackerObserveSuccessCreatesRoute(t *testing.T) {
	tr, _ := newTestTracker()
	tr.Observe("openai", "gpt-4", "key-a", false, 120, Classification{}, 0)
	snap, cooling, _ := tr.Snapshot()
	assert.Len(t, snap, 1)
	k := routeKey{Provider: "openai", Model: "gpt-4", KeyName: "key-a"}
	m, ok := snap[k]
	assert.True(t, ok)
	assert.False(t, cooling[k])
	assert.InDelta(t, 0.0, m.errRate, 0.35) // blended toward 0 from a cold start
	assert.InDelta(t, 120.0, m.latencyEWMA, 0.001)
	assert.Equal(t, uint64(1), m.totalRequests)
	assert.Equal(t, uint64(0), m.totalErrors)
}

func TestTrackerCooldownArmsAndEscalates(t *testing.T) {
	tr, _ := newTestTracker()
	tr.Observe("openai", "gpt-4", "key-a", true, 0, Classification{Rule: "rate-limit", Action: ErrorActionCooldown, CooldownMs: 2000}, 0)
	snap, cooling, _ := tr.Snapshot()
	k := routeKey{Provider: "openai", Model: "gpt-4", KeyName: "key-a"}
	assert.True(t, cooling[k], "first cooldown-classified failure must arm the backoff")
	assert.Equal(t, int64(2000), snap[k].cooldownMs)
	assert.Equal(t, 1, snap[k].strikes)

	// Second consecutive failure doubles the level.
	tr.Observe("openai", "gpt-4", "key-a", true, 0, Classification{Rule: "rate-limit", Action: ErrorActionCooldown, CooldownMs: 2000}, 0)
	snap, _, _ = tr.Snapshot()
	assert.Equal(t, int64(4000), snap[k].cooldownMs)
	assert.Equal(t, 2, snap[k].strikes)
}

func TestTrackerSuccessResetsStrikes(t *testing.T) {
	tr, _ := newTestTracker()
	cls := Classification{Rule: "server-error", Action: ErrorActionCooldown, CooldownMs: 1000}
	tr.Observe("openai", "gpt-4", "key-a", true, 0, cls, 0)
	tr.Observe("openai", "gpt-4", "key-a", true, 0, cls, 0)
	// Breaker reset on success: a recovering route must not carry stale strikes.
	tr.Observe("openai", "gpt-4", "key-a", false, 100, Classification{}, 0)
	snap, _, _ := tr.Snapshot()
	k := routeKey{Provider: "openai", Model: "gpt-4", KeyName: "key-a"}
	assert.Equal(t, 0, snap[k].strikes)
	assert.Equal(t, 1, snap[k].consecSuccess)
	assert.Equal(t, uint64(2), snap[k].totalErrors)
}

func TestTrackerFailFastLeavesHealthUntouched(t *testing.T) {
	tr, _ := newTestTracker()
	// Establish a healthy route.
	tr.Observe("openai", "gpt-4", "key-a", false, 100, Classification{}, 0)
	snapBefore, coolingBefore, _ := tr.Snapshot()
	k := routeKey{Provider: "openai", Model: "gpt-4", KeyName: "key-a"}

	// A bad request is not the route's fault.
	tr.Observe("openai", "gpt-4", "key-a", true, 0, Classification{Rule: "bad-request", Action: ErrorActionFailFast}, 0)
	snapAfter, coolingAfter, _ := tr.Snapshot()
	assert.InDelta(t, snapBefore[k].errRate, snapAfter[k].errRate, 0.0001)
	assert.Equal(t, 0, snapAfter[k].strikes)
	assert.False(t, coolingAfter[k])
	assert.False(t, coolingBefore[k])
	assert.Equal(t, uint64(2), snapAfter[k].totalRequests, "request counted even when health untouched")
	assert.Equal(t, uint64(0), snapAfter[k].totalErrors, "fail-fast must not count as a route error")
}

func TestTrackerDecayHealsIdleRoutes(t *testing.T) {
	tr, clock := newTestTracker()
	tr.Observe("openai", "gpt-4", "key-a", true, 0, Classification{Rule: "server-error", Action: ErrorActionCooldown, CooldownMs: 1000}, 0)
	k := routeKey{Provider: "openai", Model: "gpt-4", KeyName: "key-a"}
	snap, _, _ := tr.Snapshot()
	errored := snap[k].errRate

	// Advance the clock well past the half-life and re-snapshot: the decay
	// applies at read time, so a route that stopped being picked still heals.
	later := time.Now().Add(4 * errHalfLife)
	*clock = func() time.Time { return later }
	snapLater, _, _ := tr.Snapshot()
	assert.Less(t, snapLater[k].errRate, errored/2, "error rate must halve per half-life of idle time")
}

func TestTrackerSweepDropsIdle(t *testing.T) {
	tr, clock := newTestTracker()
	tr.Observe("openai", "gpt-4", "key-a", false, 100, Classification{}, 0)
	tr.Observe("openai", "gpt-4", "key-b", false, 100, Classification{}, 0)
	assert.Equal(t, 2, tr.Len())

	swept := time.Now().Add(2 * defaultIdleTTL)
	*clock = func() time.Time { return swept }
	tr.Observe("openai", "gpt-4", "key-a", false, 100, Classification{}, 0) // key-a stays fresh
	tr.Sweep(defaultIdleTTL)
	assert.Equal(t, 1, tr.Len())
	remaining := map[string]bool{}
	snap, _, _ := tr.Snapshot()
	for k := range snap {
		remaining[k.KeyName] = true
	}
	assert.True(t, remaining["key-a"])
	assert.False(t, remaining["key-b"], "idle route must be swept")
}

func TestTrackerIgnoresEmptyIdentity(t *testing.T) {
	tr, _ := newTestTracker()
	tr.Observe("", "gpt-4", "key-a", false, 100, Classification{}, 0)
	tr.Observe("openai", "", "key-a", false, 100, Classification{}, 0)
	assert.Equal(t, 0, tr.Len())
}
