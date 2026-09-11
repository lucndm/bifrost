// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains the adaptive load balancer operator API: live configuration
// reads and updates, and the metrics snapshot that backs the routing dashboard.
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/adaptive"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// AdaptiveHandler serves the adaptive load balancer's operator API.
//
// The plugin instance is resolved per request, mirroring the cache-clear
// endpoints: plugin reloads through /api/plugins swap instances, and a
// boot-time capture would go stale on the first reload.
type AdaptiveHandler struct {
	resolve     func() *adaptive.AdaptivePlugin
	configStore configstore.ConfigStore
}

// NewAdaptiveHandler builds the handler. resolve must return the live plugin
// or nil when it is not loaded; configStore may be nil, in which case config
// updates apply in memory only and the caller is told persistence is off.
func NewAdaptiveHandler(resolve func() *adaptive.AdaptivePlugin, configStore configstore.ConfigStore) (*AdaptiveHandler, error) {
	if resolve == nil {
		return nil, fmt.Errorf("adaptive plugin resolver is required")
	}
	return &AdaptiveHandler{resolve: resolve, configStore: configStore}, nil
}

// RegisterRoutes registers the adaptive load balancer routes.
func (h *AdaptiveHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.Handle(fasthttp.MethodGet, "/api/adaptive/config", lib.ChainMiddlewares(h.getConfig, middlewares...))
	r.Handle(fasthttp.MethodPut, "/api/adaptive/config", lib.ChainMiddlewares(h.updateConfig, middlewares...))
	r.Handle(fasthttp.MethodGet, "/api/adaptive/metrics", lib.ChainMiddlewares(h.getMetrics, middlewares...))
}

// resolvePlugin returns the live plugin or reports 503 on the context.
func (h *AdaptiveHandler) resolvePlugin(ctx *fasthttp.RequestCtx) (*adaptive.AdaptivePlugin, bool) {
	plugin := h.resolve()
	if plugin == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "adaptive plugin is not loaded")
		return nil, false
	}
	return plugin, true
}

// getConfig serves the effective adaptive configuration.
func (h *AdaptiveHandler) getConfig(ctx *fasthttp.RequestCtx) {
	plugin, ok := h.resolvePlugin(ctx)
	if !ok {
		return
	}
	SendJSON(ctx, plugin.GetConfig())
}

// updateConfig applies a new adaptive configuration: live on the running
// plugin, and persisted through the plugin store when one is wired so the
// settings survive restarts and propagate through /api/plugins like any
// other plugin configuration.
func (h *AdaptiveHandler) updateConfig(ctx *fasthttp.RequestCtx) {
	plugin, ok := h.resolvePlugin(ctx)
	if !ok {
		return
	}

	// Decode twice from the same body: the typed view validates and applies;
	// the raw view persists verbatim so a future field the running binary
	// does not know can still round-trip through the store.
	body := ctx.PostBody()
	var payload adaptive.Config
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid request payload: %v", err))
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request format: multiple JSON values")
		return
	}

	if err := plugin.UpdateConfig(&payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if err := h.persistConfig(ctx, body); err != nil {
		// The running plugin already picked the change up; only persistence
		// failed, so the settings live until the next restart.
		SendError(ctx, fasthttp.StatusInternalServerError,
			fmt.Sprintf("config applied in memory but not persisted, it will be lost on restart: %v", err))
		return
	}
	SendJSON(ctx, plugin.GetConfig())
}

// persistConfig writes the submitted config JSON into the plugin store as the
// "adaptive" plugin's configuration, creating the row when absent. It mirrors
// the plugins admin handler's persistence without going through it, because a
// full plugin reload here would discard the metrics the operator is tuning
// against.
func (h *AdaptiveHandler) persistConfig(ctx *fasthttp.RequestCtx, body []byte) error {
	if h.configStore == nil {
		return nil // nothing to persist into; in-memory update already applied
	}
	var raw map[string]any
	if err := sonic.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("config is not a JSON object: %v", err)
	}

	existing, err := h.configStore.GetPlugin(ctx, adaptive.PluginName)
	switch {
	case err == nil:
		existing.Config = raw
		if err := h.configStore.UpdatePlugin(ctx, existing); err != nil {
			return fmt.Errorf("failed to update stored plugin config: %v", err)
		}
	case errors.Is(err, context.Canceled):
		return err
	default:
		plugin := &configstoreTables.TablePlugin{
			Name:     adaptive.PluginName,
			Enabled:  true,
			Config:   raw,
			IsCustom: false,
		}
		if createErr := h.configStore.CreatePlugin(ctx, plugin); createErr != nil {
			return fmt.Errorf("failed to store plugin config: %v", createErr)
		}
	}
	return nil
}

// getMetrics serves the current weight snapshot: every observed direction and
// route with its health state and relative weight. This is the dashboard feed.
func (h *AdaptiveHandler) getMetrics(ctx *fasthttp.RequestCtx) {
	plugin, ok := h.resolvePlugin(ctx)
	if !ok {
		return
	}
	SendJSON(ctx, plugin.Metrics())
}
