package tokensaver

import (
	"sort"

	"github.com/maximhq/bifrost/core/schemas"
)

// SpanNameTokenSaver is the emitted span name. The homelab lake pipeline
// (ADR-0082) pivots spans by name prefix `rtk.` — the prefix is a contract.
const SpanNameTokenSaver = "rtk.token_saver"

// Span attribute keys follow the token-saver OTel mapping contract (C1);
// the RisingWave MV pivots on these exact keys.
const (
	spanAttrShape             = "rtk.shape"
	spanAttrFeatures          = "rtk.features.applied"
	spanAttrBytesIn           = "rtk.request.bytes.in"
	spanAttrBytesOut          = "rtk.request.bytes.out"
	spanAttrBytesSaved        = "rtk.request.bytes.saved"
	spanAttrFiltersApplied    = "rtk.filters.applied"
	spanAttrFilterHits        = "rtk.filter.hits"
	spanAttrOptOut            = "rtk.optout"
	spanAttrOptOutSource      = "rtk.optout.source"
	spanAttrCavemanLevel      = "caveman.level"
	spanAttrPonytailLevel     = "ponytail.level"
	spanAttrFailOpen          = "rtk.failopen"
	spanAttrFailOpenFeature   = "rtk.failopen.feature"
	spanAttrErrorCount        = "rtk.error.count"
	spanFailOpenFeatureCompre = "compress"
)

// shapeForRequest maps the request variant onto the bounded shape values from
// the mapping contract (openai_chat | openai_responses | unknown).
func shapeForRequest(req *schemas.BifrostRequest) string {
	switch {
	case req == nil:
		return "unknown"
	case req.ChatRequest != nil:
		return "openai_chat"
	case req.ResponsesRequest != nil:
		return "openai_responses"
	default:
		return "unknown"
	}
}

// setTokenSaverSpanAttributes writes the per-request RTK outcome onto the span
// in one shot. Byte counts cover only blobs that actually passed the pipeline.
func setTokenSaverSpanAttributes(span *schemas.Span, req *schemas.BifrostRequest, stats compressStats, resolved resolvedSettings, failOpen bool) {
	features := []string{}
	if resolved.RTK {
		features = append(features, "rtk")
	}
	filters := make([]string, 0, len(stats.filters))
	for name := range stats.filters {
		filters = append(filters, name)
	}
	sort.Strings(filters)
	span.SetAttributes(map[string]any{
		spanAttrShape:           shapeForRequest(req),
		spanAttrFeatures:        features,
		spanAttrBytesIn:         stats.totalBytes,
		spanAttrBytesOut:        stats.totalBytes - stats.savedBytes,
		spanAttrBytesSaved:      stats.savedBytes,
		spanAttrFiltersApplied:  filters,
		spanAttrFilterHits:      int64(stats.hits),
		spanAttrOptOut:          false,
		spanAttrOptOutSource:    "",
		spanAttrCavemanLevel:    resolved.Caveman,
		spanAttrPonytailLevel:   resolved.Ponytail,
		spanAttrFailOpen:        failOpen,
		spanAttrErrorCount:      int64(stats.errors),
		spanAttrFailOpenFeature: "",
	})
	if failOpen {
		span.SetAttribute(spanAttrFailOpenFeature, spanFailOpenFeatureCompre)
	}
}
