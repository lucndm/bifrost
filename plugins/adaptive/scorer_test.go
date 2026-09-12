package adaptive

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestScoreRouteHealthy(t *testing.T) {
	m := routeMetrics{errRate: 0, latencyEWMA: 100, consecSuccess: 5}
	rs := ScoreRoute(m, false, 100, time.Now())
	assert.Equal(t, StateHealthy, rs.State)
	assert.InDelta(t, 1.0, rs.Weight, 0.0001, "perfect route scores full weight")
}

func TestScoreRouteDegradedAtThreshold(t *testing.T) {
	m := routeMetrics{errRate: errRateDegradedThreshold}
	rs := ScoreRoute(m, false, 0, time.Now())
	assert.Equal(t, StateDegraded, rs.State)
	assert.Greater(t, rs.Weight, minRouteShare)
	assert.Less(t, rs.Weight, 1.0)
}

func TestScoreRouteFailedOnSustainedErrors(t *testing.T) {
	m := routeMetrics{errRate: errRateFailedThreshold}
	rs := ScoreRoute(m, false, 0, time.Now())
	assert.Equal(t, StateFailed, rs.State)
	assert.InDelta(t, minRouteShare, rs.Weight, 0.0001, "sustained-failure route keeps only the probe floor")
}

func TestScoreRouteCooldownExcludesCompletely(t *testing.T) {
	m := routeMetrics{errRate: 0.9}
	rs := ScoreRoute(m, true, 0, time.Now())
	assert.Equal(t, StateFailed, rs.State)
	assert.True(t, rs.InCooldown)
	assert.InDelta(t, 0.0, rs.Weight, 0.0001, "armed cooldown must receive zero traffic")
}

func TestScoreRouteRecoveringFavored(t *testing.T) {
	// Cooldown expired, few consecutive successes: Recovering with a floor.
	m := routeMetrics{errRate: 0.2, consecSuccess: 1, cooldownUntil: time.Now().Add(-time.Minute)}
	rs := ScoreRoute(m, false, 0, time.Now())
	assert.Equal(t, StateRecovering, rs.State)
	assert.GreaterOrEqual(t, rs.Weight, recoveringFloor, "recovering route gets the boosted floor")

	// After enough consecutive successes the route is proven — but its stale
	// error history still governs: until the decay pulls it under the
	// degraded threshold it stays Degraded.
	m.consecSuccess = recoverProveCount
	rs = ScoreRoute(m, false, 0, time.Now())
	assert.Equal(t, StateDegraded, rs.State)

	// Once the decayed error rate clears the threshold, proven routes are
	// Healthy again.
	m.errRate = 0.05
	rs = ScoreRoute(m, false, 0, time.Now())
	assert.Equal(t, StateHealthy, rs.State)
}

func TestScoreRouteLatencyRelativePenalty(t *testing.T) {
	// Same error rate, but 3x peer latency: penalized versus the peer.
	slow := ScoreRoute(routeMetrics{errRate: 0, latencyEWMA: 300}, false, 100, time.Now())
	fast := ScoreRoute(routeMetrics{errRate: 0, latencyEWMA: 100}, false, 100, time.Now())
	assert.Less(t, slow.Weight, fast.Weight, "peer-relative latency must penalize the slow route")
	assert.Greater(t, fast.Weight, 0.9)
}

func TestScoreDirectionAggregate(t *testing.T) {
	// No routes: optimistic fair share.
	unseen := ScoreDirection("openai", "gpt-4", nil)
	assert.Equal(t, StateHealthy, unseen.State)
	assert.InDelta(t, unseenScore, unseen.Weight, 0.0001)

	// Worst state wins for the aggregate label.
	mixed := ScoreDirection("openai", "gpt-4", []RouteScore{
		{State: StateHealthy, Weight: 1},
		{State: StateDegraded, Weight: 0.4},
	})
	assert.Equal(t, StateDegraded, mixed.State)

	// One Failed route among healthy ones must not fail the direction —
	// a single dead key cannot take the provider out of Level 1 rotation.
	oneDead := ScoreDirection("openai", "gpt-4", []RouteScore{
		{State: StateHealthy, Weight: 1},
		{State: StateHealthy, Weight: 1},
		{State: StateFailed, Weight: 0},
	})
	assert.NotEqual(t, StateFailed, oneDead.State)

	// Every route Failed: the direction is Failed.
	allFailed := ScoreDirection("openai", "gpt-4", []RouteScore{
		{State: StateFailed, Weight: 0, InCooldown: true},
		{State: StateFailed, Weight: minRouteShare},
	})
	assert.Equal(t, StateFailed, allFailed.State)
}

func TestUnseenRouteScore(t *testing.T) {
	rs := UnseenRouteScore("openai", "gpt-4", "key-a")
	assert.Equal(t, StateHealthy, rs.State)
	assert.InDelta(t, unseenScore, rs.Weight, 0.0001)
	assert.Equal(t, "key-a", rs.KeyName)
}
