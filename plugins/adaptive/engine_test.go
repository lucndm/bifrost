package adaptive

import (
	"math/rand"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deterministicEngine returns an engine whose picks follow a fixed seed.
func deterministicEngine(t *testing.T, seed int64) *Engine {
	t.Helper()
	e := NewEngine(nil, DefaultRecomputeInterval, nil)
	e.rand = rand.New(rand.NewSource(seed))
	return e
}

// feedResults drives observations through the engine and recomputes.
func feedResults(t *testing.T, e *Engine, provider, model, key string, fails int) {
	t.Helper()
	for i := 0; i < fails; i++ {
		status := 503
		e.Observe(provider, model, key, true, 0, &status, "", "upstream unavailable", 0)
	}
	if fails == 0 {
		e.Observe(provider, model, key, false, 100, nil, "", "", 0)
	}
	e.recompute()
}

func TestRecomputePublishesDirectionScores(t *testing.T) {
	e := deterministicEngine(t, 1)
	feedResults(t, e, "openai", "gpt-4", "key-a", 0)
	feedResults(t, e, "anthropic", "claude-3", "key-b", 1)

	snap := e.snapshot.Load()
	require.NotNil(t, snap)

	openai := snap.directions[directionKey{Provider: "openai", Model: "gpt-4"}]
	assert.Equal(t, StateHealthy, openai.State)

	// A single penalized 5xx lands above the degraded threshold but below the
	// failed one: traffic share drops without removing the direction.
	anthropic := snap.directions[directionKey{Provider: "anthropic", Model: "claude-3"}]
	assert.Equal(t, StateDegraded, anthropic.State)
	assert.Less(t, anthropic.Weight, openai.Weight)
}

func TestRankDirectionsPromotesHealthyFallback(t *testing.T) {
	e := deterministicEngine(t, 1)
	feedResults(t, e, "openai", "gpt-4", "key-a", 0)
	feedResults(t, e, "anthropic", "claude-3", "key-b", 6)

	// anthropic primary (failed) with openai fallback (healthy): the healthy
	// fallback must win the probabilistic pick, and the pairs must not split.
	provider, model, fallbacks := e.RankDirections("anthropic", "claude-3", []schemas.Fallback{
		{Provider: "openai", Model: "gpt-4"},
	}, true, false, false)
	assert.Equal(t, schemas.ModelProvider("openai"), provider)
	assert.Equal(t, "gpt-4", model, "winner keeps its own provider-refined model")
	require.Len(t, fallbacks, 1)
	assert.Equal(t, schemas.ModelProvider("anthropic"), fallbacks[0].Provider)
	assert.Equal(t, "claude-3", fallbacks[0].Model, "displaced primary keeps its model attached")
}

func TestRankDirectionsPruneFailed(t *testing.T) {
	e := deterministicEngine(t, 1)
	feedResults(t, e, "openai", "gpt-4", "key-a", 0)
	feedResults(t, e, "anthropic", "claude-3", "key-b", 6)
	feedResults(t, e, "groq", "llama-3", "key-c", 0)

	// Prune drops the failed anthropic direction; the two healthy ones split
	// the pick, and the loser of the pick becomes the fallback.
	provider, _, fallbacks := e.RankDirections("anthropic", "claude-3", []schemas.Fallback{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "groq", Model: "llama-3"},
	}, true, true, false)
	chain := []string{string(provider)}
	for _, fb := range fallbacks {
		chain = append(chain, string(fb.Provider))
	}
	assert.Len(t, chain, 2, "healthy directions must survive pruning")
	assert.NotContains(t, chain, "anthropic", "failed direction must be pruned")
}

func TestRankDirectionsRerouteFailed(t *testing.T) {
	e := deterministicEngine(t, 1)
	feedResults(t, e, "openai", "gpt-4", "key-a", 0)
	feedResults(t, e, "anthropic", "claude-3", "key-b", 6)

	// rerouteFailed swaps a Failed primary for the first healthy fallback
	// without the probabilistic pick needing to act.
	provider, _, _ := e.RankDirections("anthropic", "claude-3", []schemas.Fallback{
		{Provider: "openai", Model: "gpt-4"},
	}, true, false, true)
	assert.Equal(t, schemas.ModelProvider("openai"), provider)
}

func TestRankDirectionsNeverFiltersLastCandidate(t *testing.T) {
	e := deterministicEngine(t, 1)
	feedResults(t, e, "anthropic", "claude-3", "key-b", 6)
	feedResults(t, e, "groq", "llama-3", "key-c", 6)

	// Everything failed: with prune on, the last candidate must survive.
	_, _, fallbacks := e.RankDirections("anthropic", "claude-3", []schemas.Fallback{
		{Provider: "groq", Model: "llama-3"},
	}, true, true, false)
	require.Len(t, fallbacks, 1, "prune must never drop the last remaining candidate")
	assert.Equal(t, schemas.ModelProvider("groq"), fallbacks[0].Provider)
}

func TestRankDirectionsUntouchedWithoutData(t *testing.T) {
	e := deterministicEngine(t, 1)
	// No observations at all: optimistic scores, so both orders are valid
	// outcomes of the probabilistic pick — assert the chain is preserved
	// either way, never filtered or duplicated.
	provider, _, fallbacks := e.RankDirections("openai", "gpt-4", []schemas.Fallback{
		{Provider: "anthropic", Model: "claude-3"},
	}, true, false, false)
	seen := map[string]bool{string(provider): true}
	require.Len(t, fallbacks, 1)
	seen[string(fallbacks[0].Provider)] = true
	assert.Len(t, seen, 2, "both directions must survive the rerank")
}

func TestRankDirectionsDeduplicatesDirections(t *testing.T) {
	e := deterministicEngine(t, 1)
	provider, _, fallbacks := e.RankDirections("openai", "gpt-4", []schemas.Fallback{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-3"},
	}, true, false, false)
	seen := map[string]bool{string(provider): true}
	for _, fb := range fallbacks {
		seen[string(fb.Provider)] = true
	}
	assert.Len(t, seen, 2, "duplicate primary direction must be dropped, both survivors distinct")
	require.Len(t, fallbacks, 1)
}

func TestPickKeyRequiresTwoObservedKeys(t *testing.T) {
	e := deterministicEngine(t, 1)
	e.Observe("openai", "gpt-4", "solo", false, 100, nil, "", "", 0)
	e.recompute()
	_, ok := e.PickKey("openai", "gpt-4", time.Now())
	assert.False(t, ok, "one observed key gives adaptive nothing to add")

	e.Observe("openai", "gpt-4", "duo", false, 100, nil, "", "", 0)
	e.recompute()
	name, ok := e.PickKey("openai", "gpt-4", time.Now())
	assert.True(t, ok)
	assert.Contains(t, []string{"solo", "duo"}, name)
}

func TestPickKeyExcludesCoolingKeys(t *testing.T) {
	e := deterministicEngine(t, 1)
	status := 429
	e.Observe("openai", "gpt-4", "bad", true, 0, &status, "", "rate limited", 0)
	e.Observe("openai", "gpt-4", "good", false, 100, nil, "", "", 0)
	e.recompute()

	// bad is cooling down; the only non-failed observed key is good, which is
	// below the pin threshold on its own — so adaptive stays out of the way.
	_, ok := e.PickKey("openai", "gpt-4", time.Now())
	assert.False(t, ok)

	// With a second healthy key, the pick happens and never lands on the
	// cooling key.
	e.Observe("openai", "gpt-4", "good2", false, 100, nil, "", "", 0)
	e.recompute()
	name, ok := e.PickKey("openai", "gpt-4", time.Now())
	assert.True(t, ok)
	assert.NotEqual(t, "bad", name)
}

func TestEngineStopIsIdempotent(t *testing.T) {
	e := NewEngine(nil, DefaultRecomputeInterval, nil)
	e.Start(t.Context())
	e.Stop()
	e.Stop() // second call must not panic or hang
}

func TestPickWeightedBasics(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	weights := []float64{10, 0.01, 0.01}
	counts := make([]int, 3)
	for i := 0; i < 500; i++ {
		counts[pickWeighted(r, weights)]++
	}
	assert.Greater(t, counts[0], 400, "dominant weight wins the vast majority of picks")
}
