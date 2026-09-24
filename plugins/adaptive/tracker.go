package adaptive

import (
	"math"
	"sync"
	"time"
)

// Tracker knobs. The half-life is short on purpose: penalties must decay fast
// enough that a healed route is back at full traffic within a couple of
// recompute cycles, matching the enterprise behavior of rapid forgiveness.
const (
	// errHalfLife is how long an error rate takes to halve absent new samples.
	errHalfLife = 15 * time.Second
	// errBlendAlpha weights a fresh sample when blending into the EWMA.
	errBlendAlpha = 0.35
	// latencyBlendAlpha weights a fresh latency when blending into the EWMA.
	latencyBlendAlpha = 0.25
	// defaultIdleTTL is how long an unobserved route stays in the tracker
	// before the sweep drops it. Keeps the map bounded without losing routes
	// that simply have not been picked recently.
	defaultIdleTTL = 10 * time.Minute
)

// routeKey identifies one route: a specific key of a specific provider
// serving a specific model. Every metric the tracker keeps is per route.
type routeKey struct {
	Provider string
	Model    string
	KeyName  string
}

// directionKey identifies the Level 1 balancing unit: which providers can
// serve a model, across all of their keys.
type directionKey struct {
	Provider string
	Model    string
}

// routeMetrics is the per-route observation state. Guarded by Tracker.mu.
type routeMetrics struct {
	errRate       float64   // time-decayed EWMA in [0,1]
	lastOutcome   time.Time // when errRate was last blended/decayed
	latencyEWMA   float64   // milliseconds; 0 until the first sample
	latencyCount  int64
	strikes       int       // consecutive failures, reset on success
	cooldownUntil time.Time // zero when not cooling down
	cooldownMs    int64     // current backoff level in ms
	consecSuccess int       // recovery evidence after a cooldown
	lastSeen      time.Time // last observation of any kind
	totalRequests uint64
	totalErrors   uint64
}

// keyLockKey identifies a key-wide lock: a specific key of a provider, across
// every model it serves. Key-scoped failures (auth, billing) land here.
type keyLockKey struct {
	Provider string
	KeyName  string
}

// keyLockState is the per-key lock state. Guarded by Tracker.mu.
type keyLockState struct {
	strikes       int
	cooldownUntil time.Time
	cooldownMs    int64
	lastSeen      time.Time
}

// Tracker owns the in-memory metrics for every route this node has observed,
// plus key-wide locks for failures that are about the key itself. Per-node by
// design: latency and error profiles differ per region, and importing another
// node's view would pollute local route health.
type Tracker struct {
	mu       sync.Mutex
	routes   map[routeKey]*routeMetrics
	keyLocks map[keyLockKey]*keyLockState
	now      func() time.Time
	// maxCooldownMs bounds the exponential backoff. Configurable so a
	// deployment can cover long quota windows (weekly/monthly limits) without
	// probing the exhausted route every defaultMaxCooldownMs.
	maxCooldownMs int64
}

// NewTracker returns an empty tracker using the real clock. maxCooldownMs <= 0
// falls back to defaultMaxCooldownMs.
func NewTracker(maxCooldownMs int64) *Tracker {
	if maxCooldownMs <= 0 {
		maxCooldownMs = defaultMaxCooldownMs
	}
	return &Tracker{
		routes:        make(map[routeKey]*routeMetrics),
		keyLocks:      make(map[keyLockKey]*keyLockState),
		now:           time.Now,
		maxCooldownMs: maxCooldownMs,
	}
}

// Observe records one request outcome for a route.
//
//   - A classified failure blends into the error rate; cooldown-classified
//     failures also arm (or escalate) the backoff — route-scoped by default,
//     key-wide when the rule says the key itself is the problem. A precise
//     retryAfterMs (parsed from the provider's Retry-After) beats the
//     exponential estimate, mirroring the priority order of the reference
//     implementations.
//   - A fail-fast-classified failure leaves route health untouched — the
//     request was bad, not the route.
//   - A success blends toward healthy, resets strikes (breaker reset) and
//     accumulates recovery evidence. It also clears any key-wide lock on the
//     key that just served successfully.
func (t *Tracker) Observe(provider, model, keyName string, failed bool, latencyMs float64, cls Classification, retryAfterMs int64) {
	if provider == "" || model == "" {
		return
	}
	k := routeKey{Provider: provider, Model: model, KeyName: keyName}
	kl := keyLockKey{Provider: provider, KeyName: keyName}
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.routes[k]
	if !ok {
		m = &routeMetrics{}
		t.routes[k] = m
	}
	now := t.now()
	t.decayLocked(m, now)

	m.totalRequests++
	m.lastSeen = now

	if failed {
		switch cls.Action {
		case ErrorActionFailFast:
			// The route is not to blame; count the request but not the error.
			m.lastOutcome = now
			return
		case ErrorActionCooldown:
			// Precise retry-after wins over the exponential estimate.
			if retryAfterMs > 0 {
				m.strikes++
				m.consecSuccess = 0
				m.cooldownMs = retryAfterMs
				m.cooldownUntil = now.Add(time.Duration(retryAfterMs) * time.Millisecond)
			} else {
				m.strikes++
				m.consecSuccess = 0
				m.cooldownMs = backoffMs(cls.CooldownMs, t.maxCooldownMs, m.strikes)
				m.cooldownUntil = now.Add(time.Duration(m.cooldownMs) * time.Millisecond)
			}
			if cls.Scope == ScopeKey {
				t.lockKeyLocked(kl, now, m.cooldownMs, m.strikes)
			}
		default: // ErrorActionPenalize and anything unrecognized
			m.strikes++
			m.consecSuccess = 0
		}
		m.totalErrors++
		m.errRate = lerp(m.errRate, 1, errBlendAlpha)
		m.lastOutcome = now
		return
	}

	// Success: breaker reset, recovery evidence, blend toward healthy.
	m.strikes = 0
	m.consecSuccess++
	m.errRate = lerp(m.errRate, 0, errBlendAlpha)
	m.lastOutcome = now
	// A key that just served successfully is not broken: clear its key-wide
	// lock outright so one stale auth failure cannot outlive a real success.
	delete(t.keyLocks, kl)
	if latencyMs > 0 {
		if m.latencyCount == 0 {
			// First sample seeds the EWMA directly; lerping from zero would
			// understate every route's first latency.
			m.latencyEWMA = latencyMs
		} else {
			m.latencyEWMA = lerp(m.latencyEWMA, latencyMs, latencyBlendAlpha)
		}
		m.latencyCount++
	}
}

// lockKeyLocked arms or escalates the key-wide lock. Guarded by Tracker.mu.
func (t *Tracker) lockKeyLocked(kl keyLockKey, now time.Time, cooldownMs int64, strikes int) {
	st, ok := t.keyLocks[kl]
	if !ok {
		st = &keyLockState{}
		t.keyLocks[kl] = st
	}
	st.strikes = strikes
	st.cooldownMs = cooldownMs
	st.cooldownUntil = now.Add(time.Duration(cooldownMs) * time.Millisecond)
	st.lastSeen = now
}

// keyLockedLocked reports whether the key-wide lock is active. Guarded by Tracker.mu.
func (t *Tracker) keyLockedLocked(kl keyLockKey, now time.Time) (bool, time.Duration) {
	st, ok := t.keyLocks[kl]
	if !ok {
		return false, 0
	}
	if now.Before(st.cooldownUntil) {
		return true, st.cooldownUntil.Sub(now)
	}
	return false, 0
}

// inCooldownLocked reports whether the route is currently serving no traffic
// because of an armed backoff.
func (t *Tracker) inCooldownLocked(m *routeMetrics, now time.Time) bool {
	return !m.cooldownUntil.IsZero() && now.Before(m.cooldownUntil)
}

// decayLocked ages the error rate by elapsed time before a new sample lands,
// so a route that stops being picked heals even while receiving no traffic.
func (t *Tracker) decayLocked(m *routeMetrics, now time.Time) {
	if m.lastOutcome.IsZero() || m.errRate == 0 {
		return
	}
	elapsed := now.Sub(m.lastOutcome)
	if elapsed <= 0 {
		return
	}
	halvings := elapsed.Seconds() / errHalfLife.Seconds()
	if halvings > 0 {
		m.errRate *= math.Pow(0.5, halvings)
		if m.errRate < 1e-6 {
			m.errRate = 0
		}
	}
}

// Snapshot returns a copy of every tracked route plus whether it was cooling
// down at read time, and the active key-wide locks. The recompute loop calls
// this; nothing on the hot path does.
func (t *Tracker) Snapshot() (map[routeKey]routeMetrics, map[routeKey]bool, map[keyLockKey]time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	out := make(map[routeKey]routeMetrics, len(t.routes))
	cooling := make(map[routeKey]bool, len(t.routes))
	for k, m := range t.routes {
		t.decayLocked(m, now)
		out[k] = *m
		cooling[k] = t.inCooldownLocked(m, now)
	}
	locks := make(map[keyLockKey]time.Time, len(t.keyLocks))
	for kl, st := range t.keyLocks {
		if now.Before(st.cooldownUntil) {
			locks[kl] = st.cooldownUntil
		}
	}
	return out, cooling, locks
}

// Sweep drops routes and key locks idle longer than ttl so neither map can
// grow without bound. Called from the recompute loop.
func (t *Tracker) Sweep(ttl time.Duration) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	removed := 0
	for k, m := range t.routes {
		if now.Sub(m.lastSeen) > ttl {
			delete(t.routes, k)
			removed++
		}
	}
	for kl, st := range t.keyLocks {
		if now.Sub(st.lastSeen) > ttl {
			delete(t.keyLocks, kl)
			removed++
		}
	}
	return removed
}

// Len returns the number of tracked routes. Test convenience.
func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.routes)
}

// lerp moves current toward sample by alpha. Unlike a first-sample shortcut,
// this keeps the first observation proportional: a single failure takes the
// error rate to alpha, not to 1.0, so one blip degrades but never fails a
// route on its own.
func lerp(current, sample, alpha float64) float64 {
	return current + alpha*(sample-current)
}

// backoffMs computes the exponential backoff for a strike count:
// base * 2^(strikes-1), capped at capMs. Strike 1 waits the base, so a single
// transient failure costs one quiet cycle, not an exile.
func backoffMs(baseMs, capMs int64, strikes int) int64 {
	if baseMs <= 0 {
		baseMs = defaultServerErrorCooldown
	}
	if strikes < 1 {
		strikes = 1
	}
	if capMs <= 0 {
		capMs = defaultMaxCooldownMs
	}
	ms := baseMs
	for i := 1; i < strikes && ms < capMs; i++ {
		ms *= 2
	}
	if ms > capMs {
		ms = capMs
	}
	return ms
}
