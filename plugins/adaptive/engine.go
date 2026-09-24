package adaptive

import (
	"context"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Engine tuning knobs.
const (
	// DefaultRecomputeInterval is how often weights are recomputed. Requests
	// route on the pre-computed snapshot, so this is also the maximum age of a
	// routing decision — the "~5-second adaptation" of the enterprise design.
	DefaultRecomputeInterval = 5 * time.Second
	// directionIdleTTL / routeIdleTTL bound tracker memory.
	trackerIdleTTL = 10 * time.Minute
	// routePinFreshness is how recently a key must have served this exact
	// (provider, model) before the engine will pin it. A pin of a deleted or
	// remapped key would fail key selection outright, so stale observations
	// are never pinned; core's static weighted rotation takes over instead.
	routePinFreshness = 5 * time.Minute
	// minObservedKeysToPin is how many live keys a direction needs before the
	// engine overrides core's rotation. With fewer, adaptive pinning adds no
	// information.
	minObservedKeysToPin = 2
)

// snapshot is the immutable published view the hot path reads. Swapped
// atomically by the recompute loop; never mutated in place.
type snapshot struct {
	routes     map[routeKey]RouteScore
	directions map[directionKey]DirectionScore
	updatedAt  time.Time
}

// Engine ties the tracker, classifier and scorer together: it classifies and
// records outcomes, recomputes the weight snapshot on a fixed interval, and
// answers the two selection questions (which direction, which key) from the
// latest snapshot.
type Engine struct {
	tracker *Tracker
	// classifier is swapped atomically by SetRules so a live config update
	// never races the observation path.
	classifier atomic.Pointer[Classifier]
	logger     schemas.Logger

	snapshot atomic.Pointer[snapshot]

	recomputeInterval time.Duration
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	stopOnce          sync.Once

	// randMu guards rand for the probabilistic picks (Go 1.27 auto-seeds the
	// top-level rand, but concurrent Float64 is fine; the mutex only keeps
	// determinism testable via SetRand hooking).
	randMu sync.Mutex
	rand   *rand.Rand
}

// NewEngine builds an engine. interval <= 0 means DefaultRecomputeInterval.
// rules nil/empty means the default classification table. maxCooldownMs <= 0
// means defaultMaxCooldownMs.
func NewEngine(logger schemas.Logger, interval time.Duration, rules []ErrorRule, maxCooldownMs int64) *Engine {
	if interval <= 0 {
		interval = DefaultRecomputeInterval
	}
	e := &Engine{
		tracker:           NewTracker(maxCooldownMs),
		logger:            logger,
		recomputeInterval: interval,
		rand:              rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	e.classifier.Store(NewClassifier(rules))
	e.snapshot.Store(&snapshot{
		routes:     make(map[routeKey]RouteScore),
		directions: make(map[directionKey]DirectionScore),
		updatedAt:  time.Now(),
	})
	return e
}

// SetRules swaps the error-classification table atomically. Nil or empty
// rules restore the defaults.
func (e *Engine) SetRules(rules []ErrorRule) {
	e.classifier.Store(NewClassifier(rules))
}

// interval returns the configured recompute interval.
func (e *Engine) interval() time.Duration { return e.recomputeInterval }

// MetricsSnapshot is the published view served over the metrics API.
type MetricsSnapshot struct {
	UpdatedAt  time.Time        `json:"updated_at"`
	Directions []DirectionScore `json:"directions"`
	Routes     []RouteScore     `json:"routes"`
}

// Metrics returns the current snapshot as sortable slices. Routes are sorted
// by provider, then model, then key; directions by provider, then model — so
// API consumers see a stable order between polls.
func (e *Engine) Metrics() MetricsSnapshot {
	snap := e.snapshot.Load()
	if snap == nil {
		return MetricsSnapshot{UpdatedAt: time.Now()}
	}
	out := MetricsSnapshot{UpdatedAt: snap.updatedAt}
	out.Directions = make([]DirectionScore, 0, len(snap.directions))
	for _, ds := range snap.directions {
		out.Directions = append(out.Directions, ds)
	}
	sort.Slice(out.Directions, func(i, j int) bool {
		if out.Directions[i].Provider != out.Directions[j].Provider {
			return out.Directions[i].Provider < out.Directions[j].Provider
		}
		return out.Directions[i].Model < out.Directions[j].Model
	})
	out.Routes = make([]RouteScore, 0, len(snap.routes))
	for _, rs := range snap.routes {
		out.Routes = append(out.Routes, rs)
	}
	sort.Slice(out.Routes, func(i, j int) bool {
		if out.Routes[i].Provider != out.Routes[j].Provider {
			return out.Routes[i].Provider < out.Routes[j].Provider
		}
		if out.Routes[i].Model != out.Routes[j].Model {
			return out.Routes[i].Model < out.Routes[j].Model
		}
		return out.Routes[i].KeyName < out.Routes[j].KeyName
	})
	return out
}

// Start launches the background recompute loop. Call once at plugin Init.
func (e *Engine) Start(ctx context.Context) {
	ctx, e.cancel = context.WithCancel(ctx)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(e.recomputeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.recompute()
			}
		}
	}()
}

// Stop terminates the loop and waits for it to exit. Safe to call multiple times.
func (e *Engine) Stop() {
	e.stopOnce.Do(func() {
		if e.cancel != nil {
			e.cancel()
		}
	})
	e.wg.Wait()
}

// recompute rebuilds the route and direction scores from the tracker and
// swaps the snapshot atomically.
func (e *Engine) recompute() {
	now := time.Now()
	metrics, routeCooling, keyLocks := e.tracker.Snapshot()
	// Key-wide locks (auth/billing failures) cool every route of that key,
	// whatever its own model's health says.
	cooling := make(map[routeKey]bool, len(routeCooling))
	for k := range metrics {
		cooling[k] = routeCooling[k]
		if !cooling[k] {
			if until, locked := keyLocks[keyLockKey{Provider: k.Provider, KeyName: k.KeyName}]; locked {
				_ = until
				cooling[k] = true
			}
		}
	}

	// Group routes by direction and establish the peer latency baseline from
	// healthy routes, so latency is relative, never absolute.
	type dir struct {
		routes     map[routeKey]routeMetrics
		cooling    map[routeKey]bool
		latencySum float64
		latencyN   int
	}
	grouped := make(map[directionKey]*dir)
	for k, m := range metrics {
		dk := directionKey{Provider: k.Provider, Model: k.Model}
		d, ok := grouped[dk]
		if !ok {
			d = &dir{routes: make(map[routeKey]routeMetrics), cooling: make(map[routeKey]bool)}
			grouped[dk] = d
		}
		d.routes[k] = m
		if cooling[k] {
			d.cooling[k] = true
		}
		if !cooling[k] && m.latencyEWMA > 0 {
			d.latencySum += m.latencyEWMA
			d.latencyN++
		}
	}

	routeScores := make(map[routeKey]RouteScore, len(metrics))
	directionScores := make(map[directionKey]DirectionScore, len(grouped))
	for dk, d := range grouped {
		peer := 0.0
		if d.latencyN > 0 {
			peer = d.latencySum / float64(d.latencyN)
		}
		scored := make([]RouteScore, 0, len(d.routes))
		for k, m := range d.routes {
			rs := ScoreRoute(m, d.cooling[k], peer, now)
			rs.Provider = k.Provider
			rs.Model = k.Model
			rs.KeyName = k.KeyName
			if _, locked := keyLocks[keyLockKey{Provider: k.Provider, KeyName: k.KeyName}]; locked {
				rs.KeyLocked = true
			}
			routeScores[k] = rs
			scored = append(scored, rs)
		}
		directionScores[dk] = ScoreDirection(dk.Provider, dk.Model, scored)
	}

	// Bound tracker memory while we are here.
	e.tracker.Sweep(trackerIdleTTL)

	e.snapshot.Store(&snapshot{
		routes:     routeScores,
		directions: directionScores,
		updatedAt:  now,
	})
}

// Observe classifies and records one attempt outcome. Called from PostLLMHook.
// status may be nil for transport-level failures.
func (e *Engine) Observe(provider, model, keyName string, failed bool, latencyMs float64, statusCode *int, errType, errMsg string, retryAfterMs int64) {
	cls := Classification{Rule: "success"}
	if failed {
		cls = e.classifier.Load().Classify(statusCode, errType, errMsg)
	}
	e.tracker.Observe(provider, model, keyName, failed, latencyMs, cls, retryAfterMs)
}

// ExhaustionRetryAfterMs reports the earliest cooldown expiry across a
// direction's observed live routes when EVERY one of them is cooling down,
// and whether the direction is thus exhausted for adaptive purposes. Routes
// of keys that were never observed do not count — adaptive only judges what
// it has seen, and an unobserved key may still be perfectly healthy.
func (e *Engine) ExhaustionRetryAfterMs(provider, model string, now time.Time) (int64, bool) {
	if provider == "" || model == "" {
		return 0, false
	}
	snap := e.snapshot.Load()
	if snap == nil {
		return 0, false
	}
	live := 0
	earliest := int64(-1)
	for k, rs := range snap.routes {
		if k.Provider != string(provider) || k.Model != model {
			continue
		}
		if now.Sub(updatedAtOf(snap, k)) > routePinFreshness {
			continue
		}
		live++
		if !rs.InCooldown || rs.CooldownRemainingMs <= 0 {
			// At least one live route is serving: not exhausted.
			return 0, false
		}
		if earliest < 0 || rs.CooldownRemainingMs < earliest {
			earliest = rs.CooldownRemainingMs
		}
	}
	if live < minObservedKeysToPin || earliest < 0 {
		return 0, false
	}
	return earliest, true
}

// directionCandidate is one (provider, model) pair a request may be routed
// to, with its current direction score. Candidates never split: a rerank
// moves the pair, keeping its provider-refined model attached.
type directionCandidate struct {
	provider schemas.ModelProvider
	model    string
	score    DirectionScore
}

// RankDirections implements Level 1 over the candidates a request already
// carries: the primary direction plus its fallback chain.
//
// Candidates are (provider, model) pairs — each fallback keeps its own model,
// refined for that provider. The winner moves to the front and the remaining
// candidates keep their original relative order (reorder, never filter), so
// the operator's fallback chain survives intact behind the reranked primary.
// A pair never splits: the displaced primary takes the winner's old slot with
// its own model.
//
// selectEnabled turns on the weighted pick among non-Failed directions (the
// direction_selection switch). When it is off, order only changes through the
// two opt-in surgeries: pruneFailed drops Failed directions from the chain
// (never starving it entirely), and rerouteFailed promotes the first
// non-Failed fallback ahead of a Failed primary.
func (e *Engine) RankDirections(primaryProvider schemas.ModelProvider, primaryModel string, fallbacks []schemas.Fallback, selectEnabled, pruneFailed, rerouteFailed bool) (schemas.ModelProvider, string, []schemas.Fallback) {
	if len(fallbacks) == 0 && primaryProvider == "" {
		return primaryProvider, primaryModel, fallbacks
	}

	snap := e.snapshot.Load()
	if snap == nil {
		return primaryProvider, primaryModel, fallbacks
	}

	candidates := make([]directionCandidate, 0, len(fallbacks)+1)
	candidates = append(candidates, directionCandidate{
		provider: primaryProvider,
		model:    primaryModel,
		score:    directionScoreFor(snap, string(primaryProvider), primaryModel),
	})
	seen := map[directionKey]struct{}{{Provider: string(primaryProvider), Model: primaryModel}: {}}
	for _, fb := range fallbacks {
		dk := directionKey{Provider: string(fb.Provider), Model: fb.Model}
		if _, dup := seen[dk]; dup {
			continue
		}
		seen[dk] = struct{}{}
		candidates = append(candidates, directionCandidate{
			provider: fb.Provider,
			model:    fb.Model,
			score:    directionScoreFor(snap, string(fb.Provider), fb.Model),
		})
	}
	if len(candidates) < 2 {
		return primaryProvider, primaryModel, fallbacks
	}

	// Opt-in chain surgery before selection, so both switches operate on
	// states rather than on the pick's outcome.
	if pruneFailed {
		kept := make([]directionCandidate, 0, len(candidates))
		for _, c := range candidates {
			if c.score.State != StateFailed {
				kept = append(kept, c)
			}
		}
		// Pruning must never starve the chain: if every candidate is Failed,
		// keep the original chain — core's retry and fallback machinery still
		// applies to it, and an empty chain would fail the request outright.
		if len(kept) > 0 {
			candidates = kept
		}
	}
	if rerouteFailed && candidates[0].score.State == StateFailed {
		for i := 1; i < len(candidates); i++ {
			if candidates[i].score.State != StateFailed {
				candidates[0], candidates[i] = candidates[i], candidates[0]
				break
			}
		}
	}

	// Viable candidates are the non-Failed ones. When everything is Failed
	// (or cooling), leave the chain untouched: core's retry and fallback
	// machinery still applies, and blocking would only add latency.
	pickable := make([]int, 0, len(candidates))
	weights := make([]float64, 0, len(candidates))
	for i, c := range candidates {
		if c.score.State == StateFailed {
			continue
		}
		pickable = append(pickable, i)
		weights = append(weights, c.score.Weight)
	}
	if len(pickable) == 0 {
		return candidates[0].provider, candidates[0].model, rebuildFallbacks(candidates)
	}
	if !selectEnabled {
		// Selection off: order stands as surgery left it.
		return candidates[0].provider, candidates[0].model, rebuildFallbacks(candidates)
	}

	var ranked []directionCandidate
	if len(pickable) == 1 {
		// Exactly one viable direction: it serves the request, deterministically.
		// The Failed ones follow in their original order as the chain of last
		// resort — core may still need them if the viable one fails on the wire.
		sole := pickable[0]
		ranked = append(ranked, candidates[sole])
		for i, c := range candidates {
			if i != sole {
				ranked = append(ranked, c)
			}
		}
		return ranked[0].provider, ranked[0].model, rebuildFallbacks(ranked)
	}

	e.randMu.Lock()
	winner := pickable[pickWeighted(e.rand, weights)]
	e.randMu.Unlock()

	ranked = append(ranked, candidates[winner])
	for i, c := range candidates {
		if i != winner {
			ranked = append(ranked, c)
		}
	}
	return ranked[0].provider, ranked[0].model, rebuildFallbacks(ranked)
}

// PickKey implements Level 2: choose which observed key of the resolved
// (provider, model) serves this attempt. Returns "" when adaptive pinning
// should stay out of the way — fewer than two live observed keys, no fresh
// observations, or every live key cooling down.
func (e *Engine) PickKey(provider schemas.ModelProvider, model string, now time.Time) (string, bool) {
	if provider == "" || model == "" {
		return "", false
	}
	snap := e.snapshot.Load()
	if snap == nil {
		return "", false
	}
	type liveKey struct {
		name   string
		weight float64
	}
	live := make([]liveKey, 0, 4)
	for k, rs := range snap.routes {
		if k.Provider != string(provider) || k.Model != model {
			continue
		}
		// Freshness guard: never pin an observation older than
		// routePinFreshness — the key may have been deleted or remapped, and
		// a stale pin fails the request instead of degrading it.
		if now.Sub(updatedAtOf(snap, k)) > routePinFreshness {
			continue
		}
		if rs.State == StateFailed || rs.Weight <= 0 {
			continue
		}
		live = append(live, liveKey{name: k.KeyName, weight: rs.Weight})
	}
	if len(live) < minObservedKeysToPin {
		return "", false
	}
	names := make([]string, len(live))
	weights := make([]float64, len(live))
	for i, lk := range live {
		names[i] = lk.name
		weights[i] = lk.weight
	}
	e.randMu.Lock()
	idx := pickWeighted(e.rand, weights)
	e.randMu.Unlock()
	return names[idx], true
}

// updatedAtOf reports when the route's observation was last refreshed. The
// snapshot itself does not carry per-route timestamps, so freshness is judged
// against the snapshot build time: anything in the snapshot is at most one
// recompute interval stale, which is well within routePinFreshness. The
// snapshot's updatedAt is the honest bound, so that is what is used.
func updatedAtOf(snap *snapshot, _ routeKey) time.Time {
	return snap.updatedAt
}

// directionScoreFor reads a direction's score, defaulting to the optimistic
// unseen score when the node has never observed it.
func directionScoreFor(snap *snapshot, provider, model string) DirectionScore {
	if ds, ok := snap.directions[directionKey{Provider: provider, Model: model}]; ok {
		return ds
	}
	return DirectionScore{
		Provider: provider,
		Model:    model,
		State:    StateHealthy,
		Weight:   unseenScore,
	}
}

// rebuildFallbacks rebuilds the fallback slice from ranked candidates,
// preserving each candidate's (provider, model) pair.
func rebuildFallbacks(candidates []directionCandidate) []schemas.Fallback {
	if len(candidates) <= 1 {
		return nil
	}
	out := make([]schemas.Fallback, 0, len(candidates)-1)
	for _, c := range candidates[1:] {
		out = append(out, schemas.Fallback{Provider: c.provider, Model: c.model})
	}
	return out
}

// pickWeighted returns the index chosen by weighted random. Weights are
// positive; the total is never zero because callers filter zero-weight
// entries.
func pickWeighted(r *rand.Rand, weights []float64) int {
	total := 0.0
	for _, w := range weights {
		total += w
	}
	if total <= 0 {
		return 0
	}
	value := r.Float64() * total
	acc := 0.0
	for i, w := range weights {
		acc += w
		if value <= acc {
			return i
		}
	}
	return len(weights) - 1
}
