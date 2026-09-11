/**
 * Adaptive Routing (OSS load balancer) types
 * Backs /api/adaptive/config and /api/adaptive/metrics
 */

export type AdaptiveRouteState = "healthy" | "degraded" | "failed" | "recovering";

/** One rule in the ordered error-classification table. */
export interface AdaptiveErrorRule {
	name: string;
	status_codes?: number[];
	text_patterns?: string[];
	action: "cooldown" | "penalize" | "fail_fast";
	cooldown_ms?: number;
	/** "route" (default) cools the failing model+key; "key" cools the key across all models. */
	scope?: "route" | "key";
}

/** Effective adaptive configuration as served by GET /api/adaptive/config. */
export interface AdaptiveConfig {
	direction_selection_enabled: boolean;
	route_selection_enabled: boolean;
	append_fallbacks_to_pinned: boolean;
	reroute_failed_directions: boolean;
	prune_failed_fallbacks: boolean;
	/** 503 + Retry-After when every observed key is cooling AND no fallback remains. */
	fail_fast_when_exhausted: boolean;
	recompute_interval_ms: number;
	error_rules: AdaptiveErrorRule[];
}

/** Aggregated health of one (provider, model) pair — Level 1. */
export interface AdaptiveDirectionScore {
	provider: string;
	model: string;
	state: AdaptiveRouteState;
	weight: number;
	err_rate: number;
	latency_ms: number;
}

/** Computed health of one (provider, model, key) route — Level 2. */
export interface AdaptiveRouteScore {
	provider: string;
	model: string;
	key_name: string;
	state: AdaptiveRouteState;
	weight: number;
	err_rate: number;
	latency_ms: number;
	in_cooldown: boolean;
	/** How long the armed backoff has left, 0 when not cooling. */
	cooldown_remaining_ms: number;
	/** True when the KEY is locked across all models (auth/billing failure). */
	key_locked: boolean;
	score: number;
}

/** GET /api/adaptive/metrics payload. */
export interface AdaptiveMetricsSnapshot {
	updated_at: string;
	directions: AdaptiveDirectionScore[];
	routes: AdaptiveRouteScore[];
}