package guardrails

import (
	"errors"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

type fakeGatedTracer struct {
	schemas.NoOpTracer
	paused       bool
	ended        bool
	cleared      bool
	resumed      bool
	buffer       []*schemas.BifrostStreamChunk
	accumulated  *schemas.BifrostResponse
	deliveredErr *schemas.BifrostError
	transform    schemas.PausedStreamBufferTransform
}

func (f *fakeGatedTracer) PauseStream(string)         { f.paused = true }
func (f *fakeGatedTracer) ResumeStream(string)        { f.resumed = true; f.paused = false }
func (f *fakeGatedTracer) IsStreamPaused(string) bool { return f.paused }
func (f *fakeGatedTracer) IsStreamEnded(string) bool  { return f.ended }
func (f *fakeGatedTracer) GetAccumulatedResponse(string) *schemas.BifrostResponse {
	return f.accumulated
}
func (f *fakeGatedTracer) EndStream(_ string, err *schemas.BifrostError) {
	f.ended = true
	f.deliveredErr = err
}
func (f *fakeGatedTracer) ClearPausedStreamBuffer(string) error {
	f.cleared = true
	f.buffer = nil
	return nil
}
func (f *fakeGatedTracer) TransformPausedStreamBuffer(_ string, transform schemas.PausedStreamBufferTransform) error {
	if transform == nil {
		return errors.New("nil transform")
	}
	result, err := transform(f.buffer)
	if err != nil {
		return err
	}
	f.buffer = result.Chunks
	f.transform = transform
	return nil
}

var _ schemas.Tracer = (*fakeGatedTracer)(nil)

func streamingCtx(t *testing.T, tracer *fakeGatedTracer) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-1")
	return ctx
}

func streamResp(contents ...string) *schemas.BifrostResponse {
	choices := make([]schemas.BifrostResponseChoice, 0, len(contents))
	for _, c := range contents {
		text := c
		choices = append(choices, schemas.BifrostResponseChoice{
			ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
				Delta: &schemas.ChatStreamResponseChoiceDelta{Content: &text},
			},
		})
	}
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{Choices: choices},
	}
	resp.PopulateExtraFields(schemas.ChatCompletionStreamRequest, "", "", "")
	return resp
}

func outputBlockConfig() *Config {
	return &Config{
		Enabled: true,
		Rules: []Rule{{
			ID: "out-block", Enabled: true, ApplyTo: ApplyToOutput, Action: ActionBlock,
			Detector: DetectorRegex, Patterns: []string{"forbidden"},
		}},
	}
}

func outputRedactConfig() *Config {
	return &Config{
		Enabled: true,
		Rules: []Rule{{
			ID: "out-redact", Enabled: true, ApplyTo: ApplyToOutput, Action: ActionRedact,
			Detector: DetectorPII, Entities: []string{"email"},
		}},
	}
}

func TestPauseEngagesGateWhenOutputRulesExist(t *testing.T) {
	p, err := Init(outputBlockConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := streamingCtx(t, &fakeGatedTracer{})
	if sc := p.pauseForOutputGuarding(ctx); sc != nil {
		t.Fatalf("unexpected short-circuit: %v", sc.Error)
	}
	if !ctx.IsStreamPaused() {
		t.Fatal("gate must be paused for stream requests with output rules")
	}
}

func TestPauseNoopWithoutOutputRules(t *testing.T) {
	cfg := validConfig()
	cfg.Rules = cfg.Rules[:1]
	cfg.Rules[0].ApplyTo = ApplyToInput
	p, err := Init(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := streamingCtx(t, &fakeGatedTracer{})
	if sc := p.pauseForOutputGuarding(ctx); sc != nil {
		t.Fatal("no output rules must not short-circuit")
	}
	if ctx.IsStreamPaused() {
		t.Fatal("input-only rules must not pause the stream")
	}
}

func TestGateUnavailableFailsClosedForEnforcingRules(t *testing.T) {
	p, err := Init(outputBlockConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	if sc := p.pauseForOutputGuarding(ctx); sc == nil {
		t.Fatal("missing gate with block rule must fail closed")
	}
}

func TestStreamBlockDropsBufferAndEndsWithError(t *testing.T) {
	p, err := Init(outputBlockConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tracer := &fakeGatedTracer{
		buffer:      []*schemas.BifrostStreamChunk{chunkOf("forbo"), chunkOf("rden content")},
		accumulated: accumulatedChat("forbidden content"),
	}
	ctx := streamingCtx(t, tracer)
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	resp := streamResp("")
	out, outErr, err := p.PostLLMHook(ctx, resp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outErr != nil {
		t.Fatalf("block is delivered via gate, not hook error: %v", outErr)
	}
	if !tracer.cleared {
		t.Fatal("paused buffer must be cleared on block")
	}
	if !tracer.ended || tracer.deliveredErr == nil {
		t.Fatal("stream must end with intervention error")
	}
	if tracer.resumed {
		t.Fatal("blocked stream must not resume")
	}
	_ = out
}

func TestStreamAllowResumesOnFinalChunk(t *testing.T) {
	p, err := Init(outputBlockConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tracer := &fakeGatedTracer{
		buffer:      []*schemas.BifrostStreamChunk{chunkOf("hello "), chunkOf("world")},
		accumulated: accumulatedChat("hello world"),
	}
	ctx := streamingCtx(t, tracer)
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	if _, _, err := p.PostLLMHook(ctx, streamResp(""), nil); err != nil {
		t.Fatal(err)
	}
	if !tracer.resumed {
		t.Fatal("clean stream must resume")
	}
	if tracer.ended {
		t.Fatal("clean stream must not end")
	}
}

func TestStreamRedactSynthesizesAcrossChunks(t *testing.T) {
	p, err := Init(outputRedactConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tracer := &fakeGatedTracer{
		buffer: []*schemas.BifrostStreamChunk{
			chunkOf("contact john@exam"),
			chunkOf("ple.com or jane@x.yz"),
		},
		accumulated: accumulatedChat("contact john@example.com or jane@x.yz thanks"),
	}
	ctx := streamingCtx(t, tracer)
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	final := streamResp(" thanks")
	out, outErr, err := p.PostLLMHook(ctx, final, nil)
	if err != nil || outErr != nil {
		t.Fatalf("err=%v outErr=%v", err, outErr)
	}
	if !tracer.resumed {
		t.Fatal("redacted stream must resume")
	}
	if len(tracer.buffer) != 2 {
		t.Fatalf("transform must preserve chunk count, got %d", len(tracer.buffer))
	}
	first := tracer.buffer[0].BifrostChatResponse.Choices[0].Delta.Content
	if first == nil || *first != "contact [EMAIL_1] or [EMAIL_2] thanks" {
		t.Fatalf("synthesized delta = %v", first)
	}
	second := tracer.buffer[1].BifrostChatResponse.Choices[0].Delta.Content
	if second == nil || *second != "" {
		t.Fatalf("trailing buffered delta must be emptied, got %v", second)
	}
	gotFinal := out.ChatResponse.Choices[0].Delta.Content
	if gotFinal == nil || *gotFinal != "" {
		t.Fatalf("final chunk delta must be emptied after synthesis, got %v", gotFinal)
	}
}

func TestStreamNonFinalChunksPassthrough(t *testing.T) {
	p, err := Init(outputBlockConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tracer := &fakeGatedTracer{}
	ctx := streamingCtx(t, tracer)

	resp := streamResp("partial forbidden")
	out, outErr, err := p.PostLLMHook(ctx, resp, nil)
	if err != nil || outErr != nil {
		t.Fatalf("err=%v outErr=%v", err, outErr)
	}
	if out != resp {
		t.Fatal("non-final chunk must pass through unchanged")
	}
	if tracer.ended || tracer.resumed {
		t.Fatal("non-final chunk must not touch the gate")
	}
}

func TestStreamRedactFailsClosedOnNonTextDeltas(t *testing.T) {
	cfg := outputRedactConfig()
	cfg.Rules[0].Patterns = nil
	p, err := Init(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	tracer := &fakeGatedTracer{
		buffer: []*schemas.BifrostStreamChunk{chunkOf("a@b.com")},
		accumulated: func() *schemas.BifrostResponse {
			acc := accumulatedChat("a@b.com")
			toolCallID := "call_1"
			acc.ChatResponse.Choices[0].Message.ChatAssistantMessage = &schemas.ChatAssistantMessage{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{{ID: &toolCallID}},
			}
			return acc
		}(),
	}
	ctx := streamingCtx(t, tracer)
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	if _, outErr, _ := p.PostLLMHook(ctx, streamResp(""), nil); outErr != nil {
		t.Fatalf("block must go through gate, got hook error %v", outErr)
	}
	if !tracer.ended || tracer.deliveredErr == nil {
		t.Fatal("non-text stream with redact rule must fail closed via EndStream")
	}
	if !tracer.cleared {
		t.Fatal("buffer must be dropped on fail-closed")
	}
	if !strings.Contains(tracer.deliveredErr.Error.Message, "redaction") {
		t.Fatalf("unexpected error message: %s", tracer.deliveredErr.Error.Message)
	}
}

func chunkOf(text string) *schemas.BifrostStreamChunk {
	content := text
	return &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Content: &content},
				},
			}},
		},
	}
}

func accumulatedChat(text string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role:    schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)},
					},
				},
			}},
		},
	}
}
