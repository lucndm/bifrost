// Package tokensaver implements the token-saver pattern (ADR-0081) as a
// Bifrost plugin: passive compression of tool_result payloads (RTK) before
// dispatch. Caveman/Ponytail prompt injection is planned for phase 2.
//
// Contract (fail-open, idempotent, in-place):
//   - The plugin must never break a request: every hook swallows internal
//     errors and leaves the request untouched.
//   - Compression runs once per request in PreRequestHook — the phase whose
//     mutations are committed before any fan-out, so every provider attempt
//     (primary + fallbacks) sees the compressed body.
package tokensaver

import (
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// PluginName is the identifier used in config.json plugins[].name.
	PluginName = "token-saver"
)

// Settings controls which token-saver mechanisms apply to a request.
type Settings struct {
	// RTK enables passive compression of tool_result content.
	RTK bool `json:"rtk"`
	// Caveman level (off|lite|full|ultra) — phase 2.
	Caveman string `json:"caveman,omitempty"`
	// Ponytail level (off|lite|full|ultra) — phase 2.
	Ponytail string `json:"ponytail,omitempty"`
}

// RTKFilterSettings bounds which compression filters run.
type RTKFilterSettings struct {
	// MinBytes is the smallest payload worth compressing (default 500).
	MinBytes int64 `json:"min_bytes,omitempty"`
	// MaxBytes is the largest payload to touch (default 10 MiB) — bigger
	// payloads are left alone to bound CPU spent per request.
	MaxBytes int64 `json:"max_bytes,omitempty"`
	// Enabled lists active filters by name (default: all built-ins).
	Enabled []string `json:"enabled,omitempty"`
}

// Config is the plugin's config.json plugins[].config shape.
type Config struct {
	Default    *Settings          `json:"default,omitempty"`
	RTKFilters *RTKFilterSettings `json:"rtk_filters,omitempty"`
	LogStats   *bool              `json:"log_stats,omitempty"`
}

// Plugin implements schemas.LLMPlugin.
type Plugin struct {
	settings Settings
	filters  filterSet
	logStats bool
	logger   schemas.Logger
}

// Init creates a token-saver plugin instance.
func Init(config *Config, logger schemas.Logger) (*Plugin, error) {
	if logger == nil {
		return nil, ErrNilLogger
	}
	settings := Settings{RTK: true}
	if config != nil {
		if config.Default != nil {
			settings = *config.Default
		}
	}
	var fs filterSet
	if config != nil && config.RTKFilters != nil {
		fs = newFilterSet(config.RTKFilters)
	} else {
		fs = newFilterSet(nil)
	}
	logStats := true
	if config != nil && config.LogStats != nil {
		logStats = *config.LogStats
	}
	return &Plugin{
		settings: settings,
		filters:  fs,
		logStats: logStats,
		logger:   logger,
	}, nil
}

// GetName returns the plugin name.
func (p *Plugin) GetName() string {
	return PluginName
}

// Cleanup releases plugin resources (none held).
func (p *Plugin) Cleanup() error {
	return nil
}

// PreRequestHook runs the token-saver once per top-level request. Mutations
// here are committed before fan-out, so every attempt sees the same compressed
// body. Fail-open: any internal error is logged and the request passes through
// untouched.
func (p *Plugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Warn("token-saver: recovered from panic in PreRequestHook: %v", r)
		}
	}()
	if req == nil || !p.settings.RTK {
		return nil
	}
	stats := p.compressRequest(req)
	if p.logStats && stats.savedBytes > 0 {
		p.logger.Info("[token-saver] rtk saved %s / %s bytes (%.1f%%) via %s hits=%d",
			formatBytes(stats.savedBytes), formatBytes(stats.totalBytes),
			percent(stats.savedBytes, stats.totalBytes), stats.filterNames(), stats.hits)
	}
	return nil
}

// PreLLMHook is a no-op: compression already happened in PreRequestHook.
func (p *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook is a no-op for phase 1 (observability lands with metrics).
func (p *Plugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}
