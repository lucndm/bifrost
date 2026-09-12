package guardrails

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

func hasOutputRules(rules []compiledRule) bool {
	for _, rule := range rules {
		if rule.rule.Enabled && rule.rule.appliesTo(phaseOutput) {
			return true
		}
	}
	return false
}

func hasEnforcingOutputRules(rules []compiledRule) bool {
	for _, rule := range rules {
		if !rule.rule.Enabled || !rule.rule.appliesTo(phaseOutput) {
			continue
		}
		if rule.rule.Action == ActionBlock || rule.rule.Action == ActionRedact {
			return true
		}
	}
	return false
}

func isFinalStreamChunk(ctx *schemas.BifrostContext) bool {
	if ctx == nil {
		return false
	}
	v := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator)
	final, _ := v.(bool)
	return final
}

func isChatStreamResponse(resp *schemas.BifrostResponse) bool {
	if resp == nil {
		return false
	}
	ef := resp.GetExtraFields()
	return ef != nil && ef.RequestType == schemas.ChatCompletionStreamRequest
}

// pauseForOutputGuarding engages the stream gate before the provider call when
// output rules apply to a chat stream. When the gate is unavailable (SDK use
// with the NoOp tracer) the request fails closed for enforcing rules instead of
// streaming unguarded content.
func (p *Plugin) pauseForOutputGuarding(ctx *schemas.BifrostContext) *schemas.LLMPluginShortCircuit {
	if !hasOutputRules(p.rules) {
		return nil
	}
	ctx.PauseStream()
	if ctx.IsStreamPaused() {
		return nil
	}
	if hasEnforcingOutputRules(p.rules) {
		p.logger.Warn("guardrails: stream gate unavailable (no tracing tracer); failing closed for enforcing output rules")
		statusCode := 400
		return &schemas.LLMPluginShortCircuit{
			Error: &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &statusCode,
				Type:           schemas.Ptr("guardrail_blocked"),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("guardrail_blocked"),
					Message: "request blocked: guardrail output rules require stream gating which is unavailable for this request",
				},
				AllowFallbacks: schemas.Ptr(false),
			},
		}
	}
	return nil
}

// evaluateStreamChunk runs output rules once per stream, on the final chunk,
// against the accumulated response. The stream stays paused during the whole
// response so the client never receives content before the verdict.
func (p *Plugin) evaluateStreamChunk(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if !isFinalStreamChunk(ctx) {
		return resp, nil
	}

	resume := func() {
		ctx.ResumeStream()
	}
	failClosed := func(reason string) *schemas.BifrostError {
		_ = ctx.ClearPausedStreamBuffer()
		blockErr := p.blockError(&compiledRule{rule: Rule{ID: "stream", Name: "guardrails"}}, phaseOutput)
		blockErr.Error.Message = reason
		ctx.EndStream(blockErr)
		return nil
	}

	accumulated := ctx.GetAccumulatedResponse()
	if accumulated == nil || accumulated.ChatResponse == nil {
		resume()
		return resp, nil
	}

	texts := extractResponseTexts(accumulated)
	if len(texts) == 0 {
		resume()
		return resp, nil
	}

	outcome := runRules(p.rules, phaseOutput, valuesOf(texts))
	recordExecutions(ctx, outcome.executions)

	if outcome.blocked != nil {
		_ = ctx.ClearPausedStreamBuffer()
		ctx.EndStream(p.blockError(outcome.blocked, outcome.blockPhase))
		return resp, nil
	}

	if len(outcome.replacements) > 0 {
		if !accumulatedIsPureText(accumulated.ChatResponse) {
			return resp, failClosed("content blocked: guardrail redaction cannot be applied to streams carrying non-text deltas")
		}
		if err := p.redactStreamBuffer(ctx, resp, outcome.replacements); err != nil {
			p.logger.Warn("guardrails: stream redaction failed (%s); failing closed", err.Error())
			return resp, failClosed(fmt.Sprintf("content blocked: guardrail redaction could not be applied safely (%s)", err.Error()))
		}
		recordRedactions(ctx, schemas.RedactionPhaseOutput, outcome.replacements)
	}

	resume()
	return resp, nil
}

// redactStreamBuffer rewrites the paused chunk buffer into a single
// redacted content delta and empties the current final chunk's text, so the
// client receives exactly the redacted content regardless of how findings
// span chunk boundaries.
func (p *Plugin) redactStreamBuffer(ctx *schemas.BifrostContext, finalChunk *schemas.BifrostResponse, replacements map[string]string) error {
	if finalChunk == nil || finalChunk.ChatResponse == nil {
		return fmt.Errorf("final chunk has no chat response")
	}
	finalTexts := streamDeltaTexts(finalChunk.ChatResponse)
	if !streamIsPureText(finalChunk.ChatResponse) {
		return fmt.Errorf("stream carries non-text deltas (tool calls, reasoning or audio)")
	}

	type choiceTexts map[int]string
	buffered := make(choiceTexts)
	transform := func(chunks []*schemas.BifrostStreamChunk) (schemas.PausedStreamBufferTransformResult, error) {
		for _, chunk := range chunks {
			if chunk == nil || chunk.BifrostChatResponse == nil {
				continue
			}
			if !streamIsPureText(chunk.BifrostChatResponse) {
				return schemas.PausedStreamBufferTransformResult{}, fmt.Errorf("stream carries non-text deltas (tool calls, reasoning or audio)")
			}
			for choiceIndex, text := range streamDeltaTexts(chunk.BifrostChatResponse) {
				buffered[choiceIndex] += text
			}
		}

		if len(chunks) == 0 {
			return schemas.PausedStreamBufferTransformResult{Chunks: chunks, ReleaseCount: 0}, nil
		}

		first := chunks[0]
		if first == nil || first.BifrostChatResponse == nil {
			return schemas.PausedStreamBufferTransformResult{}, fmt.Errorf("first buffered chunk has no chat response")
		}

		cloned := make([]*schemas.BifrostStreamChunk, len(chunks))
		copy(cloned, chunks)

		firstChat := first.BifrostChatResponse
		for i := range firstChat.Choices {
			choice := &firstChat.Choices[i]
			if choice.ChatStreamResponseChoice == nil || choice.Delta == nil {
				continue
			}
			full := buffered[i] + finalTexts[i]
			redacted := schemas.ApplyLiteralReplacements(full, replacements)
			choice.Delta.Content = schemas.Ptr(redacted)
		}

		for _, chunk := range cloned[1:] {
			if chunk == nil || chunk.BifrostChatResponse == nil {
				continue
			}
			for i := range chunk.BifrostChatResponse.Choices {
				choice := &chunk.BifrostChatResponse.Choices[i]
				if choice.ChatStreamResponseChoice == nil || choice.Delta == nil {
					continue
				}
				choice.Delta.Content = schemas.Ptr("")
			}
		}

		return schemas.PausedStreamBufferTransformResult{Chunks: cloned, ReleaseCount: len(cloned)}, nil
	}

	if err := ctx.TransformPausedStreamBuffer(transform); err != nil {
		return err
	}

	for i := range finalChunk.ChatResponse.Choices {
		choice := &finalChunk.ChatResponse.Choices[i]
		if choice.ChatStreamResponseChoice == nil || choice.Delta == nil {
			continue
		}
		choice.Delta.Content = schemas.Ptr("")
	}
	return nil
}

// accumulatedIsPureText reports whether the accumulated stream carries only
// plain assistant content. Tool calls, reasoning, audio or refusals make
// chunk-synthesis redaction unsafe, so such streams fail closed instead.
func accumulatedIsPureText(chat *schemas.BifrostChatResponse) bool {
	if chat == nil {
		return true
	}
	for i := range chat.Choices {
		choice := &chat.Choices[i]
		if choice.ChatNonStreamResponseChoice == nil || choice.Message == nil {
			continue
		}
		assistant := choice.Message.ChatAssistantMessage
		if assistant == nil {
			continue
		}
		if len(assistant.ToolCalls) > 0 || assistant.Audio != nil || assistant.Reasoning != nil || assistant.Refusal != nil {
			return false
		}
	}
	return true
}

func streamDeltaTexts(chat *schemas.BifrostChatResponse) map[int]string {
	texts := make(map[int]string)
	if chat == nil {
		return texts
	}
	for i := range chat.Choices {
		choice := &chat.Choices[i]
		if choice.ChatStreamResponseChoice == nil || choice.Delta == nil || choice.Delta.Content == nil {
			continue
		}
		texts[i] += *choice.Delta.Content
	}
	return texts
}

func streamIsPureText(chat *schemas.BifrostChatResponse) bool {
	if chat == nil {
		return true
	}
	for i := range chat.Choices {
		choice := &chat.Choices[i]
		if choice.ChatStreamResponseChoice == nil || choice.Delta == nil {
			continue
		}
		delta := choice.Delta
		if len(delta.ToolCalls) > 0 || delta.Reasoning != nil || delta.Audio != nil || delta.Refusal != nil {
			return false
		}
	}
	return true
}
