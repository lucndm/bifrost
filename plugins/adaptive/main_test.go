package adaptive

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestPlugin(t *testing.T, cfg *Config) *AdaptivePlugin {
	t.Helper()
	if cfg == nil {
		cfg = &Config{}
	}
	p, err := Init(context.Background(), cfg, &nopLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Cleanup() })
	return p
}

// nopLogger satisfies schemas.Logger without output.
type nopLogger struct{}

func (l *nopLogger) Debug(string, ...interface{})           {}
func (l *nopLogger) Info(string, ...interface{})            {}
func (l *nopLogger) Warn(string, ...interface{})            {}
func (l *nopLogger) Error(string, ...interface{})           {}
func (l *nopLogger) Fatal(string, ...interface{})           {}
func (l *nopLogger) SetLevel(schemas.LogLevel)              {}
func (l *nopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l *nopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func newHookContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
}

func newChatRequest(provider schemas.ModelProvider, model string, fallbacks []schemas.Fallback) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider:  provider,
			Model:     model,
			Fallbacks: fallbacks,
		},
	}
}

func newChatResponse(info schemas.RoutingInfo, latency int64) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RoutingInfo: info,
				Latency:     latency,
			},
		},
	}
}

func TestPluginName(t *testing.T) {
	p := newTestPlugin(t, nil)
	assert.Equal(t, PluginName, p.GetName())
}

func TestInitDefaultsResolveSwitches(t *testing.T) {
	p := newTestPlugin(t, nil)
	s := p.switches.Load()
	require.NotNil(t, s)
	assert.True(t, s.directionSelection, "direction selection defaults on")
	assert.True(t, s.routeSelection, "route selection defaults on")
	assert.False(t, s.rerouteFailed, "chain surgery is opt-in")
	assert.False(t, s.pruneFailed, "chain surgery is opt-in")
}

func TestInitExplicitSwitches(t *testing.T) {
	off, on := false, true
	p := newTestPlugin(t, &Config{
		DirectionSelectionEnabled: &off,
		RouteSelectionEnabled:     &on,
		RerouteFailedDirections:   &on,
		PruneFailedFallbacks:      &on,
	})
	s := p.switches.Load()
	assert.False(t, s.directionSelection)
	assert.True(t, s.routeSelection)
	assert.True(t, s.rerouteFailed)
	assert.True(t, s.pruneFailed)
}

func TestPreRequestHookReranksFailedPrimary(t *testing.T) {
	p := newTestPlugin(t, nil)
	// Drive anthropic into failure through real classified observations.
	status := 503
	for i := 0; i < 6; i++ {
		p.engine.Observe("anthropic", "claude-3", "key-b", true, 0, &status, "", "upstream unavailable", 0)
	}
	p.engine.recompute()

	ctx := newHookContext()
	req := newChatRequest("anthropic", "claude-3", []schemas.Fallback{{Provider: "openai", Model: "gpt-4"}})
	require.NoError(t, p.PreRequestHook(ctx, req))

	provider, model, fallbacks := req.GetRequestFields()
	assert.Equal(t, schemas.ModelProvider("openai"), provider, "healthy fallback must be promoted")
	assert.Equal(t, "gpt-4", model)
	require.Len(t, fallbacks, 1)
	assert.Equal(t, schemas.ModelProvider("anthropic"), fallbacks[0].Provider)
	assert.Equal(t, "claude-3", fallbacks[0].Model, "displaced primary keeps its model")
}

func TestPreRequestHookNoopWithoutProviderOrSwitches(t *testing.T) {
	p := newTestPlugin(t, nil)

	// No provider resolved upstream: nothing to rerank.
	ctx := newHookContext()
	req := newChatRequest("", "gpt-4", nil)
	require.NoError(t, p.PreRequestHook(ctx, req))
	provider, _, _ := req.GetRequestFields()
	assert.Equal(t, schemas.ModelProvider(""), provider)

	// Direction selection off: hook must not touch the request.
	off := false
	p2 := newTestPlugin(t, &Config{DirectionSelectionEnabled: &off})
	req2 := newChatRequest("anthropic", "claude-3", []schemas.Fallback{{Provider: "openai", Model: "gpt-4"}})
	require.NoError(t, p2.PreRequestHook(ctx, req2))
	gotProvider, _, gotFallbacks := req2.GetRequestFields()
	assert.Equal(t, schemas.ModelProvider("anthropic"), gotProvider)
	assert.Len(t, gotFallbacks, 1)
}

func TestPreLLMHookPinsAdaptiveKeyAndDefersToPins(t *testing.T) {
	p := newTestPlugin(t, nil)
	p.engine.Observe("openai", "gpt-4", "key-a", false, 100, nil, "", "", 0)
	p.engine.Observe("openai", "gpt-4", "key-b", false, 100, nil, "", "", 0)
	p.engine.recompute()

	// With two healthy keys the hook pins one of them.
	ctx := newHookContext()
	req := newChatRequest("openai", "gpt-4", nil)
	_, shortCircuit, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)
	assert.Nil(t, shortCircuit)
	pinned, ok := ctx.Value(schemas.BifrostContextKeyAdaptivePinnedAPIKeyName).(string)
	require.True(t, ok, "adaptive pin must be written to the non-reserved key")
	assert.Contains(t, []string{"key-a", "key-b"}, pinned)

	// A caller-pinned key name wins: the hook must not overwrite it.
	ctx2 := newHookContext()
	ctx2.SetValue(schemas.BifrostContextKeyAPIKeyName, "caller-key")
	req2 := newChatRequest("openai", "gpt-4", nil)
	_, _, err = p.PreLLMHook(ctx2, req2)
	require.NoError(t, err)
	_, exists := ctx2.Value(schemas.BifrostContextKeyAdaptivePinnedAPIKeyName).(string)
	assert.False(t, exists, "caller pin must suppress the adaptive pin")

	// A routing-rule pin also suppresses it.
	ctx3 := newHookContext()
	ctx3.SetValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID, "rule-pin")
	req3 := newChatRequest("openai", "gpt-4", nil)
	_, _, err = p.PreLLMHook(ctx3, req3)
	require.NoError(t, err)
	_, exists = ctx3.Value(schemas.BifrostContextKeyAdaptivePinnedAPIKeyName).(string)
	assert.False(t, exists, "routing-rule pin must suppress the adaptive pin")

	// Session stickiness also suppresses it.
	ctx4 := newHookContext()
	ctx4.SetValue(schemas.BifrostContextKeySessionID, "session-1")
	req4 := newChatRequest("openai", "gpt-4", nil)
	_, _, err = p.PreLLMHook(ctx4, req4)
	require.NoError(t, err)
	_, exists = ctx4.Value(schemas.BifrostContextKeyAdaptivePinnedAPIKeyName).(string)
	assert.False(t, exists, "session stickiness must suppress the adaptive pin")
}

func TestPostLLMHookObservesSuccessAndError(t *testing.T) {
	p := newTestPlugin(t, nil)

	// Non-streaming success: RoutingInfo names the route; Latency feeds the EWMA.
	ctx := newHookContext()
	resp := newChatResponse(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4", Key: "key-a"}, 150)
	_, _, err := p.PostLLMHook(ctx, resp, nil)
	require.NoError(t, err)

	// Error: classified from status code; cooldown arms the route.
	errResp := &schemas.BifrostError{StatusCode: intPtr(429), Error: &schemas.ErrorField{Message: "rate limited"}}
	errResp.ExtraFields.RoutingInfo = schemas.RoutingInfo{Provider: "openai", Model: "gpt-4", Key: "key-b"}
	_, _, err = p.PostLLMHook(ctx, nil, errResp)
	require.NoError(t, err)

	snap, cooling, _ := p.engine.tracker.Snapshot()
	a := snap[routeKey{Provider: "openai", Model: "gpt-4", KeyName: "key-a"}]
	assert.InDelta(t, 150.0, a.latencyEWMA, 0.001)
	bKey := routeKey{Provider: "openai", Model: "gpt-4", KeyName: "key-b"}
	b := snap[bKey]
	assert.True(t, cooling[bKey], "rate-limited key must be cooling down")
	assert.Greater(t, b.errRate, a.errRate)
}

func TestPostLLMHookSkipsMidStreamChunks(t *testing.T) {
	p := newTestPlugin(t, nil)

	// Streaming context (StreamStartTime set) mid-stream: not final, skipped.
	ctx := newHookContext()
	ctx.SetValue(schemas.BifrostContextKeyStreamStartTime, time.Now())
	mid := newChatResponse(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4", Key: "key-a"}, 10)
	_, _, err := p.PostLLMHook(ctx, mid, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, p.engine.tracker.Len(), "mid-stream chunk must not be observed")

	// Final chunk: observed once.
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	final := newChatResponse(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4", Key: "key-a"}, 200)
	_, _, err = p.PostLLMHook(ctx, final, nil)
	require.NoError(t, err)
	snap, _, _ := p.engine.tracker.Snapshot()
	a := snap[routeKey{Provider: "openai", Model: "gpt-4", KeyName: "key-a"}]
	assert.InDelta(t, 200.0, a.latencyEWMA, 0.001)
}

func TestPostLLMHookIgnoresUnroutedErrors(t *testing.T) {
	p := newTestPlugin(t, nil)
	// Auth/budget rejections never reached a route: no observation.
	errResp := &schemas.BifrostError{StatusCode: intPtr(401)}
	_, _, err := p.PostLLMHook(newHookContext(), nil, errResp)
	require.NoError(t, err)
	assert.Equal(t, 0, p.engine.tracker.Len())
}
