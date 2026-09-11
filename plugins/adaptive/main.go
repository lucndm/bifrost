// Package adaptive implements the OSS adaptive load balancer as a Bifrost
// plugin. It distributes traffic across the routes a request already carries
// — the selected provider plus its fallback chain at Level 1, and the keys of
// the resolved (provider, model) at Level 2 — based on real-time per-route
// metrics: a time-decayed error rate (primary), peer-relative latency
// (secondary), and per-route exponential cooldowns armed by classified errors.
//
// Design in one paragraph: the PostLLMHook classifies each attempt outcome
// against a config-driven rule table (errors a different route would avoid
// cool the route down; errors no route would avoid do not touch health) and
// blends it into per-route metrics. Every recompute interval (default 5s) a
// background loop scores all routes into a four-state health machine
// (Healthy/Degraded/Failed/Recovering) and atomically publishes a weight
// snapshot. Request routing reads only that snapshot, so the hot path pays a
// couple of map lookups — weight computation never sits between a request and
// its provider.
//
// Placement: the plugin registers as a post_builtin plugin, so its
// PreRequestHook runs after governance has load balanced the request and the
// routing rules engine has materialized its decision, and before the model
// catalog resolver fills an empty provider. It never sees the governance
// allowlist and never widens a request's choices — it only reorders (and,
// opt-in, prunes) what the request already carries.
package adaptive

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// PluginName is the name of the adaptive routing plugin.
const PluginName = "adaptive"

// Defaults for the operator switches. Selection switches default on; the
// chain-surgery switches are opt-in, matching the enterprise load balancer.
const (
	defaultDirectionSelectionEnabled = true
	defaultRouteSelectionEnabled     = true
	defaultRecomputeIntervalMs       = int(5000)
)

// Config is the plugin configuration, persisted through the standard plugin
// config machinery. Pointer fields distinguish "not set" (keep the default)
// from an explicit value, so config.json blocks are presence-aware.
type Config struct {
	// DirectionSelectionEnabled enables Level 1: reranking the request's
	// primary direction against its fallback chain by adaptive weight.
	DirectionSelectionEnabled *bool `json:"direction_selection_enabled,omitempty"`
	// RouteSelectionEnabled enables Level 2: pinning the best observed key of
	// the resolved (provider, model) for this attempt.
	RouteSelectionEnabled *bool `json:"route_selection_enabled,omitempty"`
	// AppendFallbacksToPinned appends the healthy governance-eligible
	// providers for the request's model behind the fallbacks it already
	// carries, so a pinned primary fails over instead of failing fast.
	// Requires the governance plugin. Opt-in.
	AppendFallbacksToPinned *bool `json:"append_fallbacks_to_pinned,omitempty"`
	// RerouteFailedDirections promotes the first healthy fallback ahead of a
	// Failed primary. Opt-in.
	RerouteFailedDirections *bool `json:"reroute_failed_directions,omitempty"`
	// PruneFailedFallbacks drops Failed directions from the fallback chain
	// (never the last remaining candidate). Opt-in.
	PruneFailedFallbacks *bool `json:"prune_failed_fallbacks,omitempty"`
	// FailFastWhenExhausted short-circuits the attempt with 503 + Retry-After
	// when every observed key of the resolved direction is cooling down AND
	// this attempt is the request's last resort (no remaining fallbacks).
	// Requests that still have fallbacks keep them: the short-circuit error
	// is only raised when nothing viable remains. Opt-in, because adaptive
	// only knows the keys it has observed — an unobserved key may still be
	// healthy.
	FailFastWhenExhausted *bool `json:"fail_fast_when_exhausted,omitempty"`
	// RecomputeIntervalMs is how often weights are recomputed. 0 keeps the
	// default (5000). Read at plugin start; changing it takes effect on the
	// next plugin reload.
	RecomputeIntervalMs *int `json:"recompute_interval_ms,omitempty"`
	// ErrorRules replaces the default error-classification table. Ordered,
	// first match wins. Empty keeps the defaults.
	ErrorRules []ErrorRule `json:"error_rules,omitempty"`
}

// Governance is what adaptive needs from the governance plugin. Optional:
// without it the plugin still reranks and pins keys, but
// AppendFallbacksToPinned has no allowlist to draw candidates from. It is
// satisfied by the registered governance plugin rather than its store, so a
// deployment that swaps in its own governance implementation supplies the
// same capability through this narrow surface.
type Governance interface {
	// ResolveAccess answers what the request may reach, resolving it once and
	// recording it on the request's grant.
	ResolveAccess(ctx *schemas.BifrostContext) (schemas.Access, error)
}

// modelRefiner narrows the model catalog to the one call append-fallbacks
// needs: rewriting the incoming model into the provider's own name.
type modelRefiner interface {
	RefineModelForProvider(provider schemas.ModelProvider, model string) (string, error)
}

// switches is the resolved, immutable view of Config used on the hot path.
type switches struct {
	directionSelection bool
	routeSelection     bool
	appendFallbacks    bool
	rerouteFailed      bool
	pruneFailed        bool
	failFastExhausted  bool
}

func resolveSwitches(cfg *Config) switches {
	s := switches{
		directionSelection: defaultDirectionSelectionEnabled,
		routeSelection:     defaultRouteSelectionEnabled,
	}
	if cfg != nil {
		if cfg.DirectionSelectionEnabled != nil {
			s.directionSelection = *cfg.DirectionSelectionEnabled
		}
		if cfg.RouteSelectionEnabled != nil {
			s.routeSelection = *cfg.RouteSelectionEnabled
		}
		if cfg.AppendFallbacksToPinned != nil {
			s.appendFallbacks = *cfg.AppendFallbacksToPinned
		}
		if cfg.RerouteFailedDirections != nil {
			s.rerouteFailed = *cfg.RerouteFailedDirections
		}
		if cfg.PruneFailedFallbacks != nil {
			s.pruneFailed = *cfg.PruneFailedFallbacks
		}
		if cfg.FailFastWhenExhausted != nil {
			s.failFastExhausted = *cfg.FailFastWhenExhausted
		}
	}
	return s
}

// AdaptivePlugin is the plugin instance. Implements schemas.LLMPlugin.
type AdaptivePlugin struct {
	switches atomic.Pointer[switches]
	config   atomic.Pointer[configView]
	engine   *Engine
	logger   schemas.Logger

	// governance and modelCatalog are optional collaborators wired by the
	// transport after Init; both are set once at startup and only read from
	// the request path afterwards.
	governance   Governance
	modelCatalog modelRefiner

	cleanupOnce sync.Once
}

// configView is the resolved configuration as an operator would read it back:
// every pointer switch filled in with its effective value.
type configView struct {
	DirectionSelectionEnabled bool        `json:"direction_selection_enabled"`
	RouteSelectionEnabled     bool        `json:"route_selection_enabled"`
	AppendFallbacksToPinned   bool        `json:"append_fallbacks_to_pinned"`
	RerouteFailedDirections   bool        `json:"reroute_failed_directions"`
	PruneFailedFallbacks      bool        `json:"prune_failed_fallbacks"`
	FailFastWhenExhausted     bool        `json:"fail_fast_when_exhausted"`
	RecomputeIntervalMs       int         `json:"recompute_interval_ms"`
	ErrorRules                []ErrorRule `json:"error_rules"`
}

func newConfigView(cfg *Config, interval time.Duration, rules []ErrorRule) *configView {
	s := resolveSwitches(cfg)
	return &configView{
		DirectionSelectionEnabled: s.directionSelection,
		RouteSelectionEnabled:     s.routeSelection,
		AppendFallbacksToPinned:   s.appendFallbacks,
		RerouteFailedDirections:   s.rerouteFailed,
		PruneFailedFallbacks:      s.pruneFailed,
		FailFastWhenExhausted:     s.failFastExhausted,
		RecomputeIntervalMs:       int(interval.Milliseconds()),
		ErrorRules:                rules,
	}
}

// SetGovernance wires the governance plugin for AppendFallbacksToPinned.
// Called by the transport after Init; passing nil disables the behavior.
func (p *AdaptivePlugin) SetGovernance(g Governance) { p.governance = g }

// SetModelCatalog wires the model catalog so appended fallbacks carry each
// provider's own model name. Without it, appended fallbacks reuse the
// incoming model string.
func (p *AdaptivePlugin) SetModelCatalog(mc modelRefiner) { p.modelCatalog = mc }

// GetConfig returns the effective configuration.
func (p *AdaptivePlugin) GetConfig() *configView { return p.config.Load() }

// Metrics returns the current weight snapshot for the operator API.
func (p *AdaptivePlugin) Metrics() MetricsSnapshot { return p.engine.Metrics() }

// UpdateConfig applies a new configuration live: switches swap atomically and
// the error-classification table is rebuilt. The recompute interval is read
// once at startup and is deliberately not hot-reloadable — resizing the
// background loop mid-flight buys nothing over a plugin reload.
func (p *AdaptivePlugin) UpdateConfig(cfg *Config) error {
	if cfg == nil {
		cfg = &Config{}
	}
	rules := cfg.ErrorRules
	// Validate the rule table eagerly so a bad rule never reaches the hot path.
	NewClassifier(rules)
	s := resolveSwitches(cfg)
	p.switches.Store(&s)
	p.engine.SetRules(rules)
	p.config.Store(newConfigView(cfg, p.engine.interval(), rules))
	p.logger.Debug("[Adaptive] configuration updated live")
	return nil
}

// Init builds and starts the adaptive plugin.
func Init(_ context.Context, config *Config, logger schemas.Logger) (*AdaptivePlugin, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}
	interval := DefaultRecomputeInterval
	var rules []ErrorRule
	if config != nil {
		if config.RecomputeIntervalMs != nil && *config.RecomputeIntervalMs > 0 {
			interval = time.Duration(*config.RecomputeIntervalMs) * time.Millisecond
		}
		rules = config.ErrorRules
	}
	p := &AdaptivePlugin{
		logger: logger,
	}
	s := resolveSwitches(config)
	p.switches.Store(&s)
	p.config.Store(newConfigView(config, interval, rules))
	p.engine = NewEngine(logger, interval, rules)
	p.engine.Start(context.Background())
	logger.Debug("[Adaptive] plugin initialized: direction_selection=%v route_selection=%v append_fallbacks=%v recompute=%s",
		s.directionSelection, s.routeSelection, s.appendFallbacks, interval)
	return p, nil
}

// GetName implements schemas.BasePlugin.
func (p *AdaptivePlugin) GetName() string { return PluginName }

// Cleanup implements schemas.BasePlugin.
func (p *AdaptivePlugin) Cleanup() error {
	p.cleanupOnce.Do(func() {
		p.engine.Stop()
		p.logger.Debug("[Adaptive] plugin cleaned up")
	})
	return nil
}

// PreRequestHook implements schemas.LLMPlugin — Level 1.
//
// Runs after governance's weighted load balancing and the routing rules
// engine (the plugin is post_builtin), so the request already carries a
// resolved primary direction and, usually, a fallback chain. The hook
// reranks that chain by adaptive weight. Mutations here are committed to the
// request and observed by every fallback attempt.
func (p *AdaptivePlugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	s := p.switches.Load()
	if s == nil || (!s.directionSelection && !s.rerouteFailed && !s.pruneFailed && !s.appendFallbacks) {
		return nil
	}
	provider, model, fallbacks := req.GetRequestFields()
	if model == "" || provider == "" {
		// Nothing decided upstream to rerank: either the request pins nothing
		// and no plugin resolved a provider, or it is not routable yet. The
		// model catalog resolver runs after this plugin and may still act.
		return nil
	}
	newProvider, newModel, newFallbacks := p.engine.RankDirections(
		provider, model, fallbacks, s.directionSelection, s.pruneFailed, s.rerouteFailed)
	if s.appendFallbacks && p.governance != nil {
		// Append after reranking, so governance-eligible providers land
		// strictly behind whatever the request already configured — appended
		// candidates extend the chain, they never jump it.
		newFallbacks = p.appendEligibleFallbacks(ctx, newProvider, newModel, newFallbacks)
	}
	if newProvider != provider {
		req.SetProvider(newProvider)
		req.SetModel(newModel)
		req.SetFallbacks(newFallbacks)
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineLoadbalancing)
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineLoadbalancing, schemas.LogLevelInfo,
			"Adaptive rerank moved primary from "+string(provider)+" to "+string(newProvider)+" for model "+model)
		return nil
	}
	if len(newFallbacks) != len(fallbacks) {
		// Same primary, changed chain (prune/reroute/append/dedup).
		req.SetFallbacks(newFallbacks)
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineLoadbalancing)
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineLoadbalancing, schemas.LogLevelDebug,
			"Adaptive rerank adjusted fallback chain for model "+model)
	}
	return nil
}

// appendEligibleFallbacks returns the request's fallbacks extended with every
// governance-eligible provider for the model that is not already in the
// chain and is not Failed. Appended entries are ordered heaviest-weight first,
// with each provider's own refined model when a catalog is wired. A nil
// access (key-less or miswired request) appends nothing — the ordinary
// pass-through, not a misconfiguration.
func (p *AdaptivePlugin) appendEligibleFallbacks(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, existing []schemas.Fallback) []schemas.Fallback {
	access, err := p.governance.ResolveAccess(ctx)
	if err != nil {
		p.logger.Warn("[Adaptive] append-fallbacks: failed to resolve access: %v", err)
		return existing
	}
	if access == nil {
		return existing
	}
	candidates := access.ProvidersForModel(model)
	if len(candidates) == 0 {
		return existing
	}

	present := map[schemas.ModelProvider]struct{}{provider: {}}
	for _, fb := range existing {
		present[fb.Provider] = struct{}{}
	}

	appended := make([]directionCandidate, 0, len(candidates))
	snap := p.engine.snapshot.Load()
	for _, c := range candidates {
		cp := schemas.ModelProvider(c.Provider)
		if _, dup := present[cp]; dup {
			continue
		}
		score := DirectionScore{State: StateHealthy, Weight: unseenScore}
		if snap != nil {
			score = directionScoreFor(snap, c.Provider, model)
		}
		if score.State == StateFailed {
			continue
		}
		present[cp] = struct{}{}
		appended = append(appended, directionCandidate{provider: cp, model: model, score: score})
	}
	if len(appended) == 0 {
		return existing
	}
	// Heaviest first, mirroring governance's own fallback ordering.
	sort.SliceStable(appended, func(i, j int) bool {
		return appended[i].score.Weight > appended[j].score.Weight
	})

	out := existing
	for _, a := range appended {
		fbModel := a.model
		if p.modelCatalog != nil {
			if refined, rerr := p.modelCatalog.RefineModelForProvider(a.provider, model); rerr == nil {
				fbModel = refined
			} else {
				p.logger.Debug("[Adaptive] append-fallbacks: failed to refine model %s for %s: %v", model, a.provider, rerr)
			}
		}
		out = append(out, schemas.Fallback{Provider: a.provider, Model: fbModel})
	}
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineLoadbalancing, schemas.LogLevelInfo,
		"Adaptive appended "+fmt.Sprint(len(appended))+" governance-eligible fallbacks for model "+model)
	return out
}

// PreLLMHook implements schemas.LLMPlugin — Level 2.
//
// Runs once per attempt, including every fallback retry, after the provider
// and model for THIS attempt are final. Writes the chosen key onto the
// non-reserved BifrostContextKeyAdaptivePinnedAPIKeyName (restricted writes
// are blocked during plugin hooks); core normalizes it into the reserved
// key-selection context after the pre-request phase, losing to any explicit
// caller or routing-rule pin.
func (p *AdaptivePlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	s := p.switches.Load()
	if s == nil || !s.routeSelection || ctx == nil {
		return req, nil, nil
	}
	provider, model, fallbacks := req.GetRequestFields()
	if provider == "" || model == "" {
		return req, nil, nil
	}
	// Defer to anything more specific than an adaptive preference:
	//   - a caller-pinned key (id or name, e.g. the x-bf-api-key header),
	//   - a routing-rule pin, which core normalizes into the ID key,
	//   - session stickiness, which promises the same key for a session.
	//
	// Known limitation (flagged by the quota-tracker research): a sticky
	// session whose key starts failing gets no adaptive protection here,
	// because the plugin cannot see which key the session is pinned to.
	// Fixing that needs core-side awareness and belongs to the quota-tracker
	// work, not this plugin.
	if anyPinOrStickySession(ctx) {
		return req, nil, nil
	}

	// Fail fast when this direction has nothing left AND the request has
	// nowhere else to go. The short-circuit error is terminal in core — it
	// does not enter the fallback chain — so it is only raised when this
	// attempt is the request's last resort; with fallbacks remaining, the
	// doomed attempt is skipped by construction (core moves on) or fails
	// naturally. The opt-in switch covers the residual risk that an
	// unobserved key of this direction is actually healthy.
	if s.failFastExhausted {
		fallbackIndex, _ := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int)
		if fallbackIndex >= len(fallbacks) {
			if retryAfterMs, exhausted := p.engine.ExhaustionRetryAfterMs(string(provider), model, time.Now()); exhausted {
				seconds := (retryAfterMs + 999) / 1000
				if seconds < 1 {
					seconds = 1
				}
				short := &schemas.LLMPluginShortCircuit{
					Error: &schemas.BifrostError{
						StatusCode: schemas.Ptr(503),
						Error: &schemas.ErrorField{
							Message: fmt.Sprintf("all observed keys for %s/%s are cooling down; retry in %ds", provider, model, seconds),
						},
					},
				}
				ctx.AppendRoutingEngineLog(schemas.RoutingEngineLoadbalancing, schemas.LogLevelWarn,
					fmt.Sprintf("Adaptive fail-fast: %s/%s exhausted, retry in %ds", provider, model, seconds))
				return req, short, nil
			}
		}
	}

	keyName, ok := p.engine.PickKey(provider, model, time.Now())
	if !ok || keyName == "" {
		return req, nil, nil
	}
	ctx.SetValue(schemas.BifrostContextKeyAdaptivePinnedAPIKeyName, keyName)
	return req, nil, nil
}

// anyPinOrStickySession reports whether the context already carries an
// explicit key decision or a session-stickiness promise.
func anyPinOrStickySession(ctx *schemas.BifrostContext) bool {
	if v, ok := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); ok && v != "" {
		return true
	}
	if v, ok := ctx.Value(schemas.BifrostContextKeyAPIKeyName).(string); ok && v != "" {
		return true
	}
	if v, ok := ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID).(string); ok && v != "" {
		return true
	}
	if v, ok := ctx.Value(schemas.BifrostContextKeySessionID).(string); ok && v != "" {
		return true
	}
	return false
}

// PostLLMHook implements schemas.LLMPlugin — the metrics feed.
//
// Records one observation per attempt: success on the final chunk of a stream
// (or the single response of a unary call), failure on the attempt's error.
// RoutingInfo names the exact provider/model/key that served the attempt, so
// the tracker's routes are always real, observed routes.
//
// Post hooks run in reverse registration order, so this hook runs before
// governance's and logging's — nothing downstream is affected; the hook never
// modifies the response or error.
func (p *AdaptivePlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		return resp, bifrostErr, nil
	}
	if bifrostErr != nil {
		p.observeError(ctx, bifrostErr)
		return resp, bifrostErr, nil
	}
	if resp == nil {
		return resp, bifrostErr, nil
	}
	// Streams call PostLLMHook per chunk; observe once, on the final chunk,
	// where ExtraFields.Latency is the total. Non-streaming requests call it
	// exactly once and carry no stream-start marker, so they observe here.
	if isStreamingContext(ctx) && !bifrost.IsFinalChunk(ctx) {
		return resp, bifrostErr, nil
	}
	extra := resp.GetExtraFields()
	if extra == nil {
		return resp, bifrostErr, nil
	}
	info := extra.RoutingInfo
	if info.Provider == "" || info.Model == "" {
		return resp, bifrostErr, nil
	}
	latencyMs := float64(extra.Latency)
	p.engine.Observe(string(info.Provider), info.Model, info.Key, false, latencyMs, nil, "", "", 0)
	return resp, bifrostErr, nil
}

// isStreamingContext reports whether the request is a streamed response,
// mirroring core's own streaming detection in RunPostLLMHooks.
func isStreamingContext(ctx *schemas.BifrostContext) bool {
	return ctx.Value(schemas.BifrostContextKeyStreamStartTime) != nil
}

// observeError extracts the attempt identity from an error and records it.
// When the provider forwarded its response headers onto the context, a
// Retry-After value is parsed and takes precedence over the exponential
// backoff estimate — a precise reset time beats a guessed one.
func (p *AdaptivePlugin) observeError(ctx *schemas.BifrostContext, bifrostErr *schemas.BifrostError) {
	info := bifrostErr.ExtraFields.RoutingInfo
	provider := string(info.Provider)
	if provider == "" {
		provider = string(bifrostErr.ExtraFields.Provider)
	}
	model := info.Model
	if model == "" {
		model = bifrostErr.ExtraFields.ResolvedModelUsed
	}
	if provider == "" || model == "" {
		// The attempt never reached a provider (auth, budget, queue
		// rejection). Nothing happened to a route, so nothing is recorded.
		return
	}
	errType, errMsg := "", ""
	if bifrostErr.Error != nil {
		if bifrostErr.Error.Type != nil {
			errType = *bifrostErr.Error.Type
		}
		errMsg = bifrostErr.Error.Message
	}
	latencyMs := float64(bifrostErr.ExtraFields.Latency)
	p.engine.Observe(provider, model, info.Key, true, latencyMs, bifrostErr.StatusCode, errType, errMsg, retryAfterFromContext(ctx))
}

// retryAfterFromContext reads the provider's Retry-After from the response
// headers the transport forwarded onto the context. Accepts delay-seconds and
// HTTP-date forms; anything unparsable means "no hint", not zero.
func retryAfterFromContext(ctx *schemas.BifrostContext) int64 {
	if ctx == nil {
		return 0
	}
	headers, ok := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
	if !ok {
		return 0
	}
	var raw string
	for k, v := range headers {
		if strings.EqualFold(k, "Retry-After") {
			raw = strings.TrimSpace(v)
			break
		}
	}
	if raw == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil && secs > 0 {
		return secs * 1000
	}
	if date, err := http.ParseTime(raw); err == nil {
		if d := time.Until(date).Milliseconds(); d > 0 {
			return d
		}
	}
	return 0
}
