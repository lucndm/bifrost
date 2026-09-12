package guardrails

import "github.com/maximhq/bifrost/core/schemas"

func supportedRequestType(req *schemas.BifrostRequest) bool {
	if req == nil {
		return false
	}
	return req.RequestType == schemas.ChatCompletionRequest ||
		req.RequestType == schemas.ChatCompletionStreamRequest
}

func collectMessageTexts(messages []schemas.ChatMessage) []*string {
	var texts []*string
	for i := range messages {
		msg := &messages[i]
		if msg.Content == nil {
			continue
		}
		if msg.Content.ContentStr != nil {
			texts = append(texts, msg.Content.ContentStr)
		}
		for j := range msg.Content.ContentBlocks {
			block := &msg.Content.ContentBlocks[j]
			if block.Text != nil {
				texts = append(texts, block.Text)
			}
		}
	}
	return texts
}

func extractRequestTexts(req *schemas.BifrostRequest) []*string {
	if req.ChatRequest == nil {
		return nil
	}
	return collectMessageTexts(req.ChatRequest.Input)
}

func extractResponseTexts(resp *schemas.BifrostResponse) []*string {
	if resp == nil || resp.ChatResponse == nil {
		return nil
	}
	var texts []*string
	for i := range resp.ChatResponse.Choices {
		choice := &resp.ChatResponse.Choices[i]
		if choice.ChatNonStreamResponseChoice == nil || choice.Message == nil {
			continue
		}
		texts = append(texts, collectMessageTexts([]schemas.ChatMessage{*choice.Message})...)
	}
	return texts
}

func valuesOf(texts []*string) []string {
	out := make([]string, 0, len(texts))
	for _, p := range texts {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}
