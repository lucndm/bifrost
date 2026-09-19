package quotatracker

import (
	"context"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Defaults for the deployment-facing configuration.
const (
	DefaultPollInterval = 60 * time.Second
	DefaultHTTPTimeout  = 10 * time.Second
	// MinPollInterval keeps a mistyped interval from turning into a hot loop
	// against the quota API.
	MinPollInterval = 10 * time.Second
)

// Environment variables read by ConfigFromEnv.
const (
	EnvEnabled      = "BIFROST_QUOTA_TRACKER_ENABLED"
	EnvInterval     = "BIFROST_QUOTA_TRACKER_INTERVAL"
	EnvThreshold    = "BIFROST_QUOTA_TRACKER_THRESHOLD"
	EnvTrackedHosts = "BIFROST_QUOTA_TRACKER_TRACKED_HOSTS"
	EnvStaleAfter   = "BIFROST_QUOTA_TRACKER_STALE_AFTER"
)

// keyState is what the tracker retains per key between polls.
type keyState struct {
	Name     string
	Snapshot KeySnapshot
}

// Tracker polls provider quota endpoints and exposes two surfaces: a
// schemas.KeyPoolFilter for routing (veto exhausted keys) and VisitSnapshots
// for observability exporters. All methods are safe for concurrent use.
type Tracker struct {
	cfg    Config
	source KeySource
	logger schemas.Logger

	// store holds the latest poll's complete snapshot set. Each poll builds a
	// fresh map and swaps the pointer, so readers always see a consistent set
	// and vanished keys drop out without explicit cleanup.
	store atomic.Pointer[map[KeyRef]*keyState]

	// vetoedKeys tracks the previous tick's veto set so state transitions are
	// logged once, not every poll.
	vetoedKeys sync.Map // KeyRef -> struct{}

	started atomic.Bool
	stopped chan struct{}
}

// New builds a Tracker. The returned tracker is inert until Start is called;
// its filter is safe to install even when disabled (it is a no-op then).
func New(cfg Config, source KeySource, logger schemas.Logger) *Tracker {
	applyDefaults(&cfg)
	return &Tracker{
		cfg:     cfg,
		source:  source,
		logger:  logger,
		stopped: make(chan struct{}),
	}
}

func applyDefaults(cfg *Config) {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.PollInterval < MinPollInterval {
		cfg.PollInterval = MinPollInterval
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = DefaultHTTPTimeout
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = 3 * cfg.PollInterval
	}
	if cfg.QuotaURL == nil {
		cfg.QuotaURL = builtinQuotaURL
	}
	if len(cfg.TrackedHosts) == 0 {
		cfg.TrackedHosts = builtinTrackedHosts()
	}
	if cfg.Fetch == nil {
		cfg.Fetch = defaultHTTPFetch(cfg.HTTPTimeout)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
}

// now is the tracker's clock accessor.
func (t *Tracker) now() time.Time {
	return t.cfg.Now()
}

// ConfigFromEnv builds a Config from BIFROST_QUOTA_TRACKER_* environment
// variables, with defaults applied — the returned value is the effective
// configuration, safe to log as-is. getenv is injectable for tests.
func ConfigFromEnv(getenv func(string) string) Config {
	cfg := Config{Enabled: false}
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg.Enabled = isTruthy(getenv(EnvEnabled))
	if raw := strings.TrimSpace(getenv(EnvInterval)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			cfg.PollInterval = d
		} else if secs, err := strconv.Atoi(raw); err == nil {
			cfg.PollInterval = time.Duration(secs) * time.Second
		}
	}
	if raw := strings.TrimSpace(getenv(EnvThreshold)); raw != "" {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			cfg.VetoThreshold = math.Max(0, math.Min(100, f))
		}
	}
	if raw := strings.TrimSpace(getenv(EnvStaleAfter)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			cfg.StaleAfter = d
		}
	}
	if raw := strings.TrimSpace(getenv(EnvTrackedHosts)); raw != "" {
		hosts := make([]string, 0, 4)
		for _, h := range strings.Split(raw, ",") {
			if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
				hosts = append(hosts, h)
			}
		}
		if len(hosts) > 0 {
			cfg.TrackedHosts = hosts
		}
	}
	applyDefaults(&cfg)
	return cfg
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Start launches the poll loop. The first poll runs immediately; subsequent
// polls every PollInterval. Start is idempotent; Stop terminates the loop.
func (t *Tracker) Start(ctx context.Context) {
	if t == nil || !t.cfg.Enabled {
		return
	}
	if !t.started.CompareAndSwap(false, true) {
		return
	}
	go t.run(ctx)
}

// Stop terminates the poll loop and waits for it to exit.
func (t *Tracker) Stop() {
	if t == nil {
		return
	}
	select {
	case <-t.stopped:
		return
	default:
	}
	close(t.stopped)
}

func (t *Tracker) run(ctx context.Context) {
	ticker := time.NewTicker(t.cfg.PollInterval)
	defer ticker.Stop()
	t.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopped:
			return
		case <-ticker.C:
			t.poll(ctx)
		}
	}
}

// poll refreshes the whole snapshot set: enumerate keys, fetch each tracked
// key concurrently, swap the store. Keys that left the source drop out of the
// fresh map, which releases their veto automatically.
func (t *Tracker) poll(ctx context.Context) {
	providers := t.source()
	type job struct {
		ref      KeyRef
		name     string
		quotaURL string
		apiKey   string
	}
	tracked := make(map[string]struct{}, len(t.cfg.TrackedHosts))
	for _, h := range t.cfg.TrackedHosts {
		tracked[strings.ToLower(h)] = struct{}{}
	}

	jobs := make([]job, 0, 8)
	for _, p := range providers {
		host := hostOf(p.BaseURL)
		if _, ok := tracked[host]; !ok {
			continue
		}
		quotaURL, known := t.cfg.QuotaURL(host)
		if !known {
			t.logger.Warn("quota tracker: provider %s has tracked host %q but no known quota endpoint, skipping", p.Provider, host)
			continue
		}
		for _, k := range p.Keys {
			if strings.TrimSpace(k.Value) == "" {
				continue
			}
			jobs = append(jobs, job{
				ref:      KeyRef{Provider: p.Provider, KeyID: k.ID},
				name:     k.Name,
				quotaURL: quotaURL,
				apiKey:   k.Value,
			})
		}
	}

	fresh := make(map[KeyRef]*keyState, len(jobs))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			snap := t.fetchKeySnapshot(ctx, j.ref, j.quotaURL, j.apiKey)
			mu.Lock()
			defer mu.Unlock()
			fresh[j.ref] = &keyState{Name: j.name, Snapshot: snap}
		}(j)
	}
	wg.Wait()
	t.store.Store(&fresh)

	t.logVetoTransitions(fresh)
}

// logVetoTransitions emits one Info line when a key enters or leaves the veto
// set, so operators see routing-relevant quota events without per-poll noise.
func (t *Tracker) logVetoTransitions(fresh map[KeyRef]*keyState) {
	now := t.now()
	current := make(map[KeyRef]struct{})
	for ref, state := range fresh {
		if t.vetoed(ref, state, now) {
			current[ref] = struct{}{}
			if _, was := t.vetoedKeys.Load(ref); !was {
				t.vetoedKeys.Store(ref, struct{}{})
				t.logger.Info("quota tracker: key %s (%s/%s) vetoed — quota window exhausted, routing will avoid it until reset", state.Name, ref.Provider, ref.KeyID)
			}
			continue
		}
		if _, was := t.vetoedKeys.Load(ref); was {
			t.vetoedKeys.Delete(ref)
			t.logger.Info("quota tracker: key %s (%s/%s) released — quota window has capacity again", state.Name, ref.Provider, ref.KeyID)
		}
	}
	t.vetoedKeys.Range(func(k, _ any) bool {
		ref := k.(KeyRef)
		if _, still := current[ref]; !still {
			t.vetoedKeys.Delete(ref)
		}
		return true
	})
}

// Filter returns the schemas.KeyPoolFilter to install on BifrostConfig. The
// filter drops keys whose latest snapshot shows an exhausted window and
// otherwise passes the pool through untouched. Filter errors fail open in
// core, and this implementation never returns an error at all.
func (t *Tracker) Filter() schemas.KeyPoolFilter {
	return func(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, keys []schemas.Key) ([]schemas.Key, error) {
		if t == nil || !t.cfg.Enabled {
			return keys, nil
		}
		store := t.store.Load()
		if store == nil {
			return keys, nil
		}
		now := t.now()
		kept := make([]schemas.Key, 0, len(keys))
		for _, k := range keys {
			ref := KeyRef{Provider: string(provider), KeyID: k.ID}
			state, ok := (*store)[ref]
			if !ok || !t.vetoed(ref, state, now) {
				kept = append(kept, k)
				continue
			}
			t.logger.Debug("quota tracker: skipping key %s (%s) for provider %s — quota window exhausted", k.Name, k.ID, provider)
		}
		return kept, nil
	}
}

// vetoed decides whether a key state excludes the key right now. Any window
// at or below the veto threshold vetoes, until its reset time passes (if
// known) or a fresh poll reports capacity again. Failures (Status != OK),
// missing snapshots and stale snapshots all return false — fail open.
func (t *Tracker) vetoed(ref KeyRef, state *keyState, now time.Time) bool {
	snap := state.Snapshot
	if snap.Status != StatusOK {
		return false
	}
	if now.Sub(snap.FetchedAt) > t.cfg.StaleAfter {
		return false
	}
	for _, w := range snap.Windows {
		if w.RemainingPercent > t.cfg.VetoThreshold {
			continue
		}
		if w.ResetAt.IsZero() || now.Before(w.ResetAt) {
			return true
		}
		// Reset time passed but no fresh poll yet — optimistically allow; a
		// still-drained window re-vetoes within one poll interval.
	}
	return false
}

// VisitSnapshots projects the current store into SnapshotViews, one per
// window, for observability exporters. Views include keys whose last fetch
// failed (Status != OK, no windows) as a single status-only view so dashboards
// can alert on tracker health, and never mutate the store.
func (t *Tracker) VisitSnapshots(fn func(SnapshotView)) {
	if t == nil || fn == nil {
		return
	}
	store := t.store.Load()
	if store == nil {
		return
	}
	now := t.now()
	for ref, state := range *store {
		snap := state.Snapshot
		stale := snap.Status == StatusOK && now.Sub(snap.FetchedAt) > t.cfg.StaleAfter
		if len(snap.Windows) == 0 {
			fn(SnapshotView{
				Provider: ref.Provider,
				KeyID:    ref.KeyID,
				KeyName:  state.Name,
				Plan:     snap.Plan,
				Window:   "none",
				Vetoed:   false,
				Status:   snapshotStatus(snap.Status, stale),
			})
			continue
		}
		vetoed := t.vetoed(ref, state, now)
		for _, w := range snap.Windows {
			fn(SnapshotView{
				Provider:         ref.Provider,
				KeyID:            ref.KeyID,
				KeyName:          state.Name,
				Plan:             snap.Plan,
				Window:           w.WindowKey,
				RemainingPercent: w.RemainingPercent,
				ResetAtUnix:      resetUnix(w.ResetAt),
				Vetoed:           vetoed,
				Status:           snapshotStatus(snap.Status, stale),
			})
		}
	}
}

func snapshotStatus(status schemas.QuotaFetchStatus, stale bool) schemas.QuotaFetchStatus {
	if stale {
		return StatusStale
	}
	return status
}

func resetUnix(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.Unix())
}
