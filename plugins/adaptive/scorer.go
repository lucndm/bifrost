package adaptive

import (
	"math"
	"time"
)

// Route health states, mirroring the enterprise load balancer's four-state
// machine. Healthy routes compete on merit, Degraded routes lose traffic
// share, Failed routes (including anything on an armed cooldown) receive none,
// and Recovering routes get a boosted floor so they can prove themselves on
// live traffic instead of staying starved.
type RouteState string

const (
	StateHealthy    RouteState = "healthy"
	StateDegraded   RouteState = "degraded"
	StateFailed     RouteState = "failed"
	StateRecovering RouteState = "recovering"
)

// Scoring constants. Error rate is the primary signal, latency the secondary
// one, exactly as in the enterprise design. The weight floor is what keeps a
// recovering route in rotation: no route that is allowed to receive traffic
// ever drops below minRouteShare of an equally-healthy peer.
const (
	errRateDegradedThreshold = 0.15
	errRateFailedThreshold   = 0.50
	errorPenaltyWeight       = 0.60
	latencyPenaltyWeight     = 0.25
	// minRouteShare is the weight floor for routes allowed to receive traffic.
	minRouteShare = 0.05
	// recoveringFloor is the score a Recovering route starts from, so it climbs
	// quickly instead of being held down by stale error history.
	recoveringFloor = 0.35
	// recoverProveCount is how many consecutive successes after a cooldown
	// upgrade a route from Recovering back to Healthy.
	recoverProveCount = 3
	// unseenScore is the optimistic score of a route the node has never
	// observed: it competes at roughly fair share and self-corrects within a
	// cycle or two. Deliberately optimistic, per the enterprise cold-start rule.
	unseenScore = 1.0
)

// RouteScore is the computed, published view of one route. Immutable once in
// a snapshot; readers on the hot path get a consistent copy via the engine.
type RouteScore struct {
	Provider   string     `json:"provider"`
	Model      string     `json:"model"`
	KeyName    string     `json:"key_name"`
	State      RouteState `json:"state"`
	Weight     float64    `json:"weight"` // relative in (0,1]; 0 = excluded (cooling down)
	ErrRate    float64    `json:"err_rate"`
	LatencyMs  float64    `json:"latency_ms"`
	InCooldown bool       `json:"in_cooldown"`
	Score      float64    `json:"score"`
	// CooldownRemainingMs is how long the armed backoff has left, 0 when not
	// cooling. This is what powers the aggregate Retry-After when every key
	// of a direction is cooling down.
	CooldownRemainingMs int64 `json:"cooldown_remaining_ms"`
	// KeyLocked marks routes whose KEY is locked across all models (auth or
	// billing failure). Such routes are excluded from selection regardless of
	// their own model's health.
	KeyLocked bool `json:"key_locked"`
}

// DirectionScore is the computed view of one (provider, model) pair,
// aggregated over its routes.
type DirectionScore struct {
	Provider  string     `json:"provider"`
	Model     string     `json:"model"`
	State     RouteState `json:"state"`
	Weight    float64    `json:"weight"` // relative in (0,1]
	ErrRate   float64    `json:"err_rate"`
	LatencyMs float64    `json:"latency_ms"`
}

// ScoreRoute computes one route's state and weight from its metrics.
//
// peerLatency is the fair-share latency of the direction's healthy routes;
// a route is scored against its peers, not against any absolute target.
func ScoreRoute(m routeMetrics, inCooldown bool, peerLatency float64, now time.Time) RouteScore {
	rs := RouteScore{
		Provider:   "",
		Model:      "",
		KeyName:    "",
		ErrRate:    m.errRate,
		LatencyMs:  m.latencyEWMA,
		InCooldown: inCooldown,
	}
	if inCooldown && now.Before(m.cooldownUntil) {
		rs.CooldownRemainingMs = m.cooldownUntil.Sub(now).Milliseconds()
	}

	// State machine.
	switch {
	case inCooldown || m.errRate >= errRateFailedThreshold:
		rs.State = StateFailed
	case !m.cooldownUntil.IsZero() && m.consecSuccess < recoverProveCount:
		// Cooldown expired but the route has not proven itself yet. Checked
		// before the degraded threshold: a route fresh off a backoff carries
		// stale error history, and it must climb through Recovering rather
		// than sit in Degraded while proving itself.
		rs.State = StateRecovering
	case m.errRate >= errRateDegradedThreshold:
		rs.State = StateDegraded
	default:
		rs.State = StateHealthy
	}

	// Primary signal: time-decayed error rate.
	score := 1 - errorPenaltyWeight*m.errRate

	// Secondary signal: latency relative to peers, capped so one slow route
	// cannot zero itself out. Only meaningful once there is a peer baseline.
	if peerLatency > 0 && m.latencyEWMA > 0 {
		ratio := m.latencyEWMA / peerLatency
		penalty := (ratio - 1) / 2 // 2x peer latency saturates the penalty
		if penalty < 0 {
			penalty = 0
		}
		if penalty > 1 {
			penalty = 1
		}
		score -= latencyPenaltyWeight * penalty
	}
	if score < 0 {
		score = 0
	}
	rs.Score = score

	// Map score to weight with health-aware floors and caps.
	switch rs.State {
	case StateFailed:
		if inCooldown {
			rs.Weight = 0 // hard exclusion while the backoff is armed
		} else {
			rs.Weight = minRouteShare // failing on sustained errors: minimum probe share
		}
	case StateRecovering:
		// Favor recovery: never below the recovering floor, still capped by a
		// healthy ceiling so a recovering route cannot leapfrog proven peers.
		rs.Weight = math.Max(math.Min(score, 0.9), recoveringFloor)
	default:
		rs.Weight = math.Max(score, minRouteShare)
	}
	return rs
}

// UnseenRouteScore is the score assigned to a route with no observations.
func UnseenRouteScore(provider, model, keyName string) RouteScore {
	return RouteScore{
		Provider: provider,
		Model:    model,
		KeyName:  keyName,
		State:    StateHealthy,
		Weight:   unseenScore,
		Score:    unseenScore,
	}
}

// ScoreDirection aggregates the routes of one (provider, model) into the
// Level 1 unit. Unobserved routes contribute optimistic full weight, so a
// provider with no history competes at fair share. The direction state is the
// worst state among its *viable* (non-Failed) routes; it is Failed only when
// every observed route is Failed — one dead key must not take a provider with
// healthy keys out of Level 1 rotation.
func ScoreDirection(provider, model string, routes []RouteScore) DirectionScore {
	if len(routes) == 0 {
		return DirectionScore{
			Provider: provider,
			Model:    model,
			State:    StateHealthy,
			Weight:   unseenScore,
		}
	}
	state := StateHealthy
	worst := -1
	var weightSum, errSum, latencySum, latencyN float64
	for _, r := range routes {
		if r.State != StateFailed {
			if rank := stateRank(r.State); rank > worst {
				worst = rank
				state = r.State
			}
		}
		weightSum += r.Weight
		errSum += r.ErrRate
		if r.LatencyMs > 0 {
			latencySum += r.LatencyMs
			latencyN++
		}
	}
	if worst == -1 {
		// Every observed route is Failed.
		state = StateFailed
	}
	weight := weightSum / float64(len(routes))
	if weight < minRouteShare {
		weight = minRouteShare
	}
	dir := DirectionScore{
		Provider: provider,
		Model:    model,
		State:    state,
		Weight:   weight,
		ErrRate:  errSum / float64(len(routes)),
	}
	if latencyN > 0 {
		dir.LatencyMs = latencySum / latencyN
	}
	return dir
}

// stateRank orders states from best to worst for the direction aggregate.
func stateRank(s RouteState) int {
	switch s {
	case StateHealthy:
		return 0
	case StateRecovering:
		return 1
	case StateDegraded:
		return 2
	case StateFailed:
		return 3
	default:
		return 0
	}
}
