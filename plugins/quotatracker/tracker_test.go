package quotatracker

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock steps a controllable clock through the tracker's decisions.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func (c *fakeClock) Add(d time.Duration) { c.t = c.t.Add(d) }

// testTracker wires a tracker over an injected fetch and clock; poll runs
// synchronously so tests are deterministic.
type testTracker struct {
	*Tracker
	clock *fakeClock
}

func newTestTracker(t *testing.T, cfg Config, source KeySource) *testTracker {
	t.Helper()
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0).UTC()}
	applyDefaults(&cfg)
	cfg.Now = clock.Now
	tracker := New(cfg, source, bifrost.NewNoOpLogger())
	return &testTracker{Tracker: tracker, clock: clock}
}

// staticSource returns a fixed inventory of one GLM-shaped provider.
func staticSource() KeySource {
	return func() []ProviderKeys {
		return []ProviderKeys{{
			Provider: "zai",
			BaseURL:  "https://api.z.ai/api/coding/paas/v4",
			Keys: []TrackedKey{
				{ID: "key-1", Name: "glm-main", Value: "sk-glm-1"},
				{ID: "key-2", Name: "glm-spare", Value: "sk-glm-2"},
			},
		}}
	}
}

func okBody(remainingSession float64, remainingWeekly float64) []byte {
	sessionUsed := 100 - remainingSession
	weeklyUsed := 100 - remainingWeekly
	return []byte(`{"data": {"level": "pro", "limits": [
		{"type": "CREDIT_LIMIT", "unit": 3, "number": 5, "percentage": ` + num(sessionUsed) + `, "nextResetTime": 0},
		{"type": "CREDIT_LIMIT", "unit": 6, "number": 1, "percentage": ` + num(weeklyUsed) + `, "nextResetTime": 0}
	]}}`)
}

func num(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// TestFilterVetoesExhaustedKeyUntilReset covers the core routing behavior:
// a key with a drained window is excluded from the pool while the window is
// out, comes back after the reset time passes, and a fresh poll reporting
// capacity releases it early.
func TestFilterVetoesExhaustedKeyUntilReset(t *testing.T) {
	resetAt := time.UnixMilli(1_787_905_548_392)
	drained := []byte(`{"data": {"level": "pro", "limits": [
		{"type": "CREDIT_LIMIT", "unit": 3, "number": 5, "percentage": 100, "nextResetTime": 1787905548392}
	]}}`)
	tt := newTestTracker(t, Config{Enabled: true, Fetch: func(ctx context.Context, url, key string) ([]byte, int, error) {
		if key == "sk-glm-1" {
			return drained, 200, nil
		}
		return okBody(80, 90), 200, nil
	}}, staticSource())

	tt.poll(context.Background())

	filter := tt.Filter()
	ctx := schemas.NewBifrostContext(context.Background(), time.Now())
	pool := []schemas.Key{
		{ID: "key-1", Name: "glm-main"},
		{ID: "key-2", Name: "glm-spare"},
	}

	// Before the reset time the drained key is vetoed; the healthy key stays.
	tt.clock.t = resetAt.Add(-1 * time.Hour)
	kept, err := filter(ctx, schemas.ModelProvider("zai"), "glm-5", pool)
	require.NoError(t, err)
	require.Len(t, kept, 1)
	assert.Equal(t, "key-2", kept[0].ID)

	// After the reset time the snapshot is optimistic: the key is allowed back
	// (fail-open) even before the next poll proves capacity.
	tt.clock.t = resetAt.Add(time.Minute)
	kept, err = filter(ctx, schemas.ModelProvider("zai"), "glm-5", pool)
	require.NoError(t, err)
	require.Len(t, kept, 2)

	// Other providers are untouched by GLM quota state.
	tt.clock.t = resetAt.Add(-1 * time.Hour)
	kept, err = filter(ctx, schemas.ModelProvider("anthropic"), "claude-3", pool)
	require.NoError(t, err)
	require.Len(t, kept, 2)
}

// TestPollStoresStateAndReleasesOnCapacity pins the happy path end to end:
// poll → drained key vetoed → poll showing capacity → key released.
func TestPollStoresStateAndReleasesOnCapacity(t *testing.T) {
	var sessionRemaining float64 = 0
	tt := newTestTracker(t, Config{Enabled: true, Fetch: func(ctx context.Context, url, key string) ([]byte, int, error) {
		if key == "sk-glm-1" {
			return okBody(sessionRemaining, 80), 200, nil
		}
		return okBody(80, 90), 200, nil
	}}, staticSource())

	tt.poll(context.Background())

	ctx := schemas.NewBifrostContext(context.Background(), time.Now())
	pool := []schemas.Key{{ID: "key-1", Name: "glm-main"}, {ID: "key-2", Name: "glm-spare"}}
	filter := tt.Filter()

	kept, err := filter(ctx, schemas.ModelProvider("zai"), "glm-5", pool)
	require.NoError(t, err)
	require.Len(t, kept, 1, "drained session window must veto key-1")

	sessionRemaining = 50
	tt.poll(context.Background())

	kept, err = filter(ctx, schemas.ModelProvider("zai"), "glm-5", pool)
	require.NoError(t, err)
	require.Len(t, kept, 2, "fresh poll with capacity must release the veto")
}

// TestFetchFailuresFailOpen is the safety contract: network errors, 401s, HTTP
// 500s and unparseable bodies must never remove a key from rotation.
func TestFetchFailuresFailOpen(t *testing.T) {
	cases := []struct {
		name   string
		fetch  func(ctx context.Context, url, key string) ([]byte, int, error)
		expect schemas.QuotaFetchStatus
	}{
		{"network error", func(ctx context.Context, url, key string) ([]byte, int, error) {
			return nil, 0, errors.New("connection refused")
		}, StatusNetwork},
		{"401", func(ctx context.Context, url, key string) ([]byte, int, error) {
			return []byte(`{"error":"unauthorized"}`), 401, nil
		}, StatusAuth},
		{"500", func(ctx context.Context, url, key string) ([]byte, int, error) {
			return []byte(`{"error":"boom"}`), 500, nil
		}, StatusHTTP},
		{"garbage body", func(ctx context.Context, url, key string) ([]byte, int, error) {
			return []byte(`<html>gateway timeout</html>`), 200, nil
		}, StatusParse},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tt := newTestTracker(t, Config{Enabled: true, Fetch: tc.fetch}, staticSource())
			tt.poll(context.Background())

			state := (*tt.store.Load())[KeyRef{Provider: "zai", KeyID: "key-1"}]
			require.NotNil(t, state)
			assert.Equal(t, tc.expect, state.Snapshot.Status)

			filter := tt.Filter()
			ctx := schemas.NewBifrostContext(context.Background(), time.Now())
			kept, err := filter(ctx, schemas.ModelProvider("zai"), "glm-5", []schemas.Key{{ID: "key-1", Name: "glm-main"}})
			require.NoError(t, err)
			assert.Len(t, kept, 1, "fail-open: fetch failure must not veto")
		})
	}
}

// TestStaleSnapshotFailsOpen: a key whose snapshot aged past StaleAfter (e.g.
// the quota endpoint died silently) must return to rotation rather than stay
// vetoed on stale data.
func TestStaleSnapshotFailsOpen(t *testing.T) {
	tt := newTestTracker(t, Config{
		Enabled:    true,
		StaleAfter: 5 * time.Minute,
		Fetch: func(ctx context.Context, url, key string) ([]byte, int, error) {
			return []byte(`{"data": {"level": "pro", "limits": [
				{"type": "CREDIT_LIMIT", "unit": 3, "number": 5, "percentage": 100, "nextResetTime": 0}
			]}}`), 200, nil
		},
	}, staticSource())
	tt.poll(context.Background())

	ctx := schemas.NewBifrostContext(context.Background(), time.Now())
	filter := tt.Filter()
	pool := []schemas.Key{{ID: "key-1", Name: "glm-main"}}

	kept, err := filter(ctx, schemas.ModelProvider("zai"), "glm-5", pool)
	require.NoError(t, err)
	require.Len(t, kept, 0, "fresh drained snapshot vetoes")

	tt.clock.Add(6 * time.Minute)
	kept, err = filter(ctx, schemas.ModelProvider("zai"), "glm-5", pool)
	require.NoError(t, err)
	assert.Len(t, kept, 1, "stale snapshot must fail open")
}

// TestDisabledTrackerIsANoOp: without the enable flag the filter passes
// everything through and Start does not spin a loop.
func TestDisabledTrackerIsANoOp(t *testing.T) {
	tt := newTestTracker(t, Config{Enabled: false, Fetch: func(ctx context.Context, url, key string) ([]byte, int, error) {
		return okBody(0, 0), 200, nil
	}}, staticSource())
	tt.Start(context.Background())
	tt.poll(context.Background()) // even a manual poll with data...

	filter := tt.Filter()
	ctx := schemas.NewBifrostContext(context.Background(), time.Now())
	kept, err := filter(ctx, schemas.ModelProvider("zai"), "glm-5", []schemas.Key{{ID: "key-1", Name: "glm-main"}})
	require.NoError(t, err)
	assert.Len(t, kept, 1, "...must not veto while disabled")
}

// TestVetoThresholdPreempts: a nonzero threshold vetoes near-drained windows,
// not just fully exhausted ones.
func TestVetoThresholdPreempts(t *testing.T) {
	tt := newTestTracker(t, Config{
		Enabled:       true,
		VetoThreshold: 5,
		Fetch: func(ctx context.Context, url, key string) ([]byte, int, error) {
			return okBody(3, 80), 200, nil // 3% remaining on session
		},
	}, staticSource())
	tt.poll(context.Background())

	filter := tt.Filter()
	ctx := schemas.NewBifrostContext(context.Background(), time.Now())
	kept, err := filter(ctx, schemas.ModelProvider("zai"), "glm-5", []schemas.Key{{ID: "key-1", Name: "glm-main"}})
	require.NoError(t, err)
	assert.Len(t, kept, 0, "remaining 3% must veto under a 5% threshold")
}

// TestUnknownHostsAndEmptyKeysAreSkipped pins the inventory filtering: only
// tracked hosts are polled and keyless entries never reach the fetch.
func TestUnknownHostsAndEmptyKeysAreSkipped(t *testing.T) {
	var fetched []string
	source := func() []ProviderKeys {
		return []ProviderKeys{
			{Provider: "openai", BaseURL: "https://api.openai.com/v1", Keys: []TrackedKey{{ID: "k-oai", Name: "oai", Value: "sk-oai"}}},
			{Provider: "zai", BaseURL: "https://api.z.ai/v1", Keys: []TrackedKey{
				{ID: "k-empty", Name: "no-value", Value: ""},
				{ID: "k-zai", Name: "glm", Value: "sk-zai"},
			}},
		}
	}
	tt := newTestTracker(t, Config{Enabled: true, Fetch: func(ctx context.Context, url, key string) ([]byte, int, error) {
		fetched = append(fetched, key)
		return []byte(`{"data": null}`), 200, nil
	}}, source)
	tt.poll(context.Background())

	assert.Equal(t, []string{"sk-zai"}, fetched, "only tracked-host keys with a value may be fetched")
	assert.Len(t, *tt.store.Load(), 1)
}

// TestVisitSnapshotsShape pins the observability projection: one view per
// window with the veto flag, plus a status-only view when a fetch failed.
func TestVisitSnapshotsShape(t *testing.T) {
	tt := newTestTracker(t, Config{Enabled: true, Fetch: func(ctx context.Context, url, key string) ([]byte, int, error) {
		if key == "sk-glm-1" {
			return []byte(`{"data": {"level": "pro", "limits": [
				{"type": "CREDIT_LIMIT", "unit": 3, "number": 5, "percentage": 100, "nextResetTime": 1787905548392}
			]}}`), 200, nil
		}
		return nil, 401, nil
	}}, staticSource())
	tt.clock.t = time.UnixMilli(1_787_000_000_000) // before the window's reset stamp
	tt.poll(context.Background())

	views := make([]SnapshotView, 0, 4)
	tt.VisitSnapshots(func(v SnapshotView) { views = append(views, v) })
	require.Len(t, views, 2)

	byKeyWindow := map[string]SnapshotView{}
	for _, v := range views {
		byKeyWindow[v.KeyID+"/"+v.Window] = v
	}

	main := byKeyWindow["key-1/session"]
	require.NotNil(t, main)
	assert.Equal(t, "Pro", main.Plan)
	assert.Equal(t, 0.0, main.RemainingPercent)
	assert.Equal(t, float64(time.UnixMilli(1_787_905_548_392).Unix()), main.ResetAtUnix)
	assert.True(t, main.Vetoed)
	assert.Equal(t, StatusOK, main.Status)

	spare := byKeyWindow["key-2/none"]
	require.NotNil(t, spare)
	assert.Equal(t, StatusAuth, spare.Status)
	assert.False(t, spare.Vetoed, "auth failure must not veto, only surface")
}

// TestConfigFromEnv covers the deployment configuration surface.
func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{
		EnvEnabled:      "true",
		EnvInterval:     "90s",
		EnvThreshold:    "10",
		EnvTrackedHosts: "api.z.ai, custom.example.com",
	}
	cfg := ConfigFromEnv(func(key string) string { return env[key] })
	applyDefaults(&cfg)

	assert.True(t, cfg.Enabled)
	assert.Equal(t, 90*time.Second, cfg.PollInterval)
	assert.Equal(t, 10.0, cfg.VetoThreshold)
	assert.Equal(t, []string{"api.z.ai", "custom.example.com"}, cfg.TrackedHosts)
	assert.Equal(t, 270*time.Second, cfg.StaleAfter, "stale derives from interval when unset")

	// Defaults with nothing set.
	cfg = applyDefaultsCopy(ConfigFromEnv(func(string) string { return "" }))
	assert.False(t, cfg.Enabled)
	assert.Equal(t, DefaultPollInterval, cfg.PollInterval)
	assert.Equal(t, 0.0, cfg.VetoThreshold)
	assert.Equal(t, builtinTrackedHosts(), cfg.TrackedHosts)
}

func applyDefaultsCopy(cfg Config) Config {
	applyDefaults(&cfg)
	return cfg
}
