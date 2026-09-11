package tokensaver

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// compressStats aggregates per-request RTK results for the stats log line.
type compressStats struct {
	savedBytes int64
	totalBytes int64
	hits       int
	filters    map[string]int
}

func (s compressStats) filterNames() string {
	if len(s.filters) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(s.filters))
	for name := range s.filters {
		parts = append(parts, name)
	}
	// deterministic order
	for i := 0; i < len(parts); i++ {
		for j := i + 1; j < len(parts); j++ {
			if parts[j] < parts[i] {
				parts[i], parts[j] = parts[j], parts[i]
			}
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// compressRequest walks every request shape that carries tool output and
// compresses eligible text in place. It never touches:
//   - tool results flagged as errors (is_error / error) — debugging signal,
//   - provider-native raw payloads (verbatim passthrough by contract).
func (p *Plugin) compressRequest(req *schemas.BifrostRequest) compressStats {
	stats := compressStats{filters: map[string]int{}}
	if req == nil {
		return stats
	}
	switch {
	case req.ChatRequest != nil:
		p.compressChat(req.ChatRequest, &stats)
	case req.ResponsesRequest != nil:
		p.compressResponses(req.ResponsesRequest, &stats)
	}
	return stats
}

// compressChat handles BifrostChatRequest: messages with role "tool".
func (p *Plugin) compressChat(req *schemas.BifrostChatRequest, stats *compressStats) {
	if req == nil || len(req.Input) == 0 {
		return
	}
	for i := range req.Input {
		msg := &req.Input[i]
		// Fail-open per message: a malformed entry never blocks the request.
		func() {
			defer func() {
				if r := recover(); r != nil {
					p.logger.Warn("token-saver: recovered while compressing chat message %d: %v", i, r)
				}
			}()
			if msg.Role != schemas.ChatMessageRoleTool || msg.ChatToolMessage == nil {
				return
			}
			if msg.ChatToolMessage.IsError != nil && *msg.ChatToolMessage.IsError {
				return
			}
			if msg.Content == nil {
				return
			}
			if msg.Content.ContentStr != nil {
				p.compressStringPtr(msg.Content.ContentStr, stats)
				return
			}
			for j := range msg.Content.ContentBlocks {
				block := &msg.Content.ContentBlocks[j]
				if block.Text == nil {
					continue
				}
				p.compressStringPtr(block.Text, stats)
			}
		}()
	}
}

// compressResponses handles BifrostResponsesRequest: function_call_output items.
func (p *Plugin) compressResponses(req *schemas.BifrostResponsesRequest, stats *compressStats) {
	if req == nil || len(req.Input) == 0 {
		return
	}
	for i := range req.Input {
		item := &req.Input[i]
		func() {
			defer func() {
				if r := recover(); r != nil {
					p.logger.Warn("token-saver: recovered while compressing responses input %d: %v", i, r)
				}
			}()
			if item.Type == nil || *item.Type != schemas.ResponsesMessageTypeFunctionCallOutput {
				return
			}
			tool := item.ResponsesToolMessage
			if tool == nil || tool.Output == nil || tool.Output.ResponsesToolCallOutputStr == nil {
				return
			}
			// Error outputs are debugging signal — never compress.
			if tool.Error != nil {
				return
			}
			p.compressStringPtr(tool.Output.ResponsesToolCallOutputStr, stats)
		}()
	}
}

// compressStringPtr applies the detect→apply→guard pipeline to one payload.
func (p *Plugin) compressStringPtr(ptr *string, stats *compressStats) {
	if ptr == nil {
		return
	}
	original := *ptr
	size := int64(len(original))
	if size < p.filters.minBytes || size > p.filters.maxBytes {
		return
	}
	name, ok := p.filters.detect(original)
	if !ok {
		return
	}
	compressed, ok := p.filters.apply(name, original)
	if !ok {
		return
	}
	*ptr = compressed
	stats.savedBytes += size - int64(len(compressed))
	stats.totalBytes += size
	stats.hits++
	stats.filters[name]++
}
