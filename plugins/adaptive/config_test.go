package adaptive

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAccess embeds the Access interface so any un-overridden method panics
// loudly — these tests only exercise ProvidersForModel.
type mockAccess struct {
	schemas.Access
	providers []schemas.ProviderCandidate
}

func (m *mockAccess) ProvidersForModel(string) []schemas.ProviderCandidate { return m.providers }

type mockGovernance struct{ access schemas.Access }

func (m *mockGovernance) ResolveAccess(*schemas.BifrostContext) (schemas.Access, error) {
	return m.access, nil
}

type mockRefiner struct{}

func (mockRefiner) RefineModelForProvider(provider schemas.ModelProvider, model string) (string, error) {
	return string(provider) + "/" + model, nil
}

func newAppendTestPlugin(t *testing.T, access schemas.Access) *AdaptivePlugin {
	t.Helper()
	p := newTestPlugin(t, &Config{AppendFallbacksToPinned: schemas.Ptr(true)})
	if access != nil {
		p.SetGovernance(&mockGovernance{access: access})
	}
	p.SetModelCatalog(mockRefiner{})
	return p
}

func TestUpdateConfigSwapsSwitchesAndRulesLive(t *testing.T) {
	p := newTestPlugin(t, nil)

	// Defaults first.
	view := p.GetConfig()
	require.NotNil(t, view)
	assert.True(t, view.DirectionSelectionEnabled)
	assert.False(t, view.AppendFallbacksToPinned)

	// A custom rule table: make 400s cooldown-classified, so the swap is
	// observable through classification behavior, not just through the view.
	on := true
	status := 400
	err := p.UpdateConfig(&Config{
		DirectionSelectionEnabled: &on,
		ErrorRules: []ErrorRule{
			{Name: "custom-400", StatusCodes: []int{400}, Action: ErrorActionCooldown, CooldownMs: 5000},
		},
	})
	require.NoError(t, err)

	p.engine.Observe("openai", "gpt-4", "key-a", true, 0, &status, "", "bad request", 0)
	_, cooling, _ := p.engine.tracker.Snapshot()
	k := routeKey{Provider: "openai", Model: "gpt-4", KeyName: "key-a"}
	assert.True(t, cooling[k], "swapped rule table must govern classification immediately")

	view = p.GetConfig()
	assert.True(t, view.DirectionSelectionEnabled)
	require.Len(t, view.ErrorRules, 1)
	assert.Equal(t, "custom-400", view.ErrorRules[0].Name)

	// Empty rules restore the defaults. A fresh key, because the custom rule
	// already armed a 5s cooldown on key-a — restoring defaults does not
	// retroactively disarm an armed backoff.
	require.NoError(t, p.UpdateConfig(&Config{}))
	view = p.GetConfig()
	assert.Empty(t, view.ErrorRules, "nil rules view as defaults")
	p.engine.Observe("openai", "gpt-4", "key-b", true, 0, &status, "", "bad request", 0)
	_, cooling, _ = p.engine.tracker.Snapshot()
	assert.False(t, cooling[routeKey{Provider: "openai", Model: "gpt-4", KeyName: "key-b"}],
		"default table classifies 400 as fail-fast: no cooldown")
}

func TestMetricsSortedAndPopulated(t *testing.T) {
	e := deterministicEngine(t, 1)
	e.Observe("openai", "gpt-4", "key-b", false, 100, nil, "", "", 0)
	e.Observe("openai", "gpt-4", "key-a", false, 90, nil, "", "", 0)
	e.Observe("anthropic", "claude-3", "key-c", false, 80, nil, "", "", 0)
	e.recompute()

	snap := e.Metrics()
	assert.False(t, snap.UpdatedAt.IsZero())
	require.Len(t, snap.Directions, 2)
	assert.Equal(t, "anthropic", snap.Directions[0].Provider, "directions sorted by provider")
	assert.Equal(t, "openai", snap.Directions[1].Provider)
	require.Len(t, snap.Routes, 3)
	assert.Equal(t, "key-a", snap.Routes[1].KeyName, "routes sorted by provider, model, key")
}

func TestPreRequestHookAppendsEligibleFallbacks(t *testing.T) {
	access := &mockAccess{providers: []schemas.ProviderCandidate{
		{Provider: "openai"},    // already the primary: skipped
		{Provider: "groq"},      // eligible and unseen: appended
		{Provider: "anthropic"}, // eligible but Failed: skipped
	}}
	p := newAppendTestPlugin(t, access)

	// Make anthropic Failed on the incoming model (direction health is per
	// provider+model pair) so the snapshot excludes it from the append.
	p.engine.Observe("anthropic", "gpt-4", "dead-key", true, 0, intPtr(429), "", "rate limited", 0)
	p.engine.recompute()

	ctx := newHookContext()
	req := newChatRequest("openai", "gpt-4", nil)
	require.NoError(t, p.PreRequestHook(ctx, req))

	_, _, fallbacks := req.GetRequestFields()
	require.Len(t, fallbacks, 1)
	assert.Equal(t, schemas.ModelProvider("groq"), fallbacks[0].Provider)
	assert.Equal(t, "groq/gpt-4", fallbacks[0].Model, "appended fallbacks carry the provider-refined model")
}

func TestPreRequestHookAppendKeepsExistingChainFirst(t *testing.T) {
	access := &mockAccess{providers: []schemas.ProviderCandidate{
		{Provider: "openai"},
		{Provider: "groq"},
	}}
	// Direction selection off so the existing chain's internal order is not
	// reranked — this test isolates the append placement, not the pick.
	off := false
	p := newTestPlugin(t, &Config{AppendFallbacksToPinned: schemas.Ptr(true), DirectionSelectionEnabled: &off})
	p.SetGovernance(&mockGovernance{access: access})

	ctx := newHookContext()
	req := newChatRequest("openai", "gpt-4", []schemas.Fallback{{Provider: "xai", Model: "gpt-4"}})
	require.NoError(t, p.PreRequestHook(ctx, req))

	_, _, fallbacks := req.GetRequestFields()
	require.Len(t, fallbacks, 2)
	assert.Equal(t, schemas.ModelProvider("xai"), fallbacks[0].Provider, "caller-configured fallbacks stay ahead of appended ones")
	assert.Equal(t, schemas.ModelProvider("groq"), fallbacks[1].Provider)
}

func TestPreRequestHookAppendNoopWithoutGovernance(t *testing.T) {
	p := newAppendTestPlugin(t, nil) // no governance wired

	ctx := newHookContext()
	req := newChatRequest("openai", "gpt-4", nil)
	require.NoError(t, p.PreRequestHook(ctx, req))
	_, _, fallbacks := req.GetRequestFields()
	assert.Empty(t, fallbacks, "append-fallbacks without governance must not guess candidates")
}

func TestPreRequestHookAppendNoopWhenSwitchOff(t *testing.T) {
	access := &mockAccess{providers: []schemas.ProviderCandidate{{Provider: "groq"}}}
	off := false
	p := newTestPlugin(t, &Config{AppendFallbacksToPinned: &off})
	p.SetGovernance(&mockGovernance{access: access})

	ctx := newHookContext()
	req := newChatRequest("openai", "gpt-4", nil)
	require.NoError(t, p.PreRequestHook(ctx, req))
	_, _, fallbacks := req.GetRequestFields()
	assert.Empty(t, fallbacks)
}

func TestPreRequestHookAppendNilAccess(t *testing.T) {
	p := newAppendTestPlugin(t, &mockAccess{providers: nil}) // governance resolves to no access

	ctx := newHookContext()
	req := newChatRequest("openai", "gpt-4", nil)
	require.NoError(t, p.PreRequestHook(ctx, req))
	_, _, fallbacks := req.GetRequestFields()
	assert.Empty(t, fallbacks)
}

func TestMetricsEmptyBeforeTraffic(t *testing.T) {
	p := newTestPlugin(t, nil)
	snap := p.Metrics()
	assert.False(t, snap.UpdatedAt.IsZero())
	assert.Empty(t, snap.Directions)
	assert.Empty(t, snap.Routes)
	_ = time.Now // keep time import if assertions change
}
