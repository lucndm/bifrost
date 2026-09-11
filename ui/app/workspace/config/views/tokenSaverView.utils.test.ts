import { describe, expect, it } from "vitest";
import { buildTokenSaverConfig, defaultTokenSaverFormValues, toTokenSaverFormValues, validateTokenSaverForm } from "./tokenSaverView.utils";

describe("token saver config helpers", () => {
	it("hydrates effective defaults when no plugin row exists", () => {
		expect(toTokenSaverFormValues()).toEqual(defaultTokenSaverFormValues);
	});

	it("preserves explicit false values and enabled filters", () => {
		expect(
			toTokenSaverFormValues({
				default: { rtk: false },
				rtk_filters: { min_bytes: 100, max_bytes: 200, enabled: ["git-diff"] },
				log_stats: false,
			}),
		).toEqual({ rtk: false, minBytes: 100, maxBytes: 200, filters: ["git-diff"], logStats: false });
	});

	it("rejects invalid payload bounds", () => {
		expect(validateTokenSaverForm({ ...defaultTokenSaverFormValues, minBytes: 1000, maxBytes: 500 })).toContain("greater than");
	});

	it("builds the backend config shape", () => {
		expect(buildTokenSaverConfig(defaultTokenSaverFormValues)).toEqual({
			default: { rtk: true },
			rtk_filters: { min_bytes: 500, max_bytes: 10485760, enabled: ["git-diff", "dedup-log", "smart-truncate"] },
			log_stats: true,
		});
	});
});