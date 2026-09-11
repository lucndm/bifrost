import { describe, expect, it } from "vitest";
import {
	buildTokenSaverConfig,
	defaultTokenSaverFormValues,
	toTokenSaverFormValues,
	TokenSaverFormValues,
	validateTokenSaverForm,
} from "./tokenSaverView.utils";

describe("token saver config helpers", () => {
	it("hydrates effective defaults when no plugin row exists", () => {
		expect(toTokenSaverFormValues()).toEqual(defaultTokenSaverFormValues);
	});

	it("preserves explicit false values, enabled filters, and overrides", () => {
		expect(
			toTokenSaverFormValues({
				default: { rtk: false },
				rtk_filters: { min_bytes: 100, max_bytes: 200, enabled: ["git-diff"] },
				log_stats: false,
				models: { "claude-*": { rtk: true }, "gpt-5": { rtk: false } },
				virtual_keys: { "vk-opencode": { rtk: false } },
			}),
		).toEqual({
			rtk: false,
			minBytes: 100,
			maxBytes: 200,
			filters: ["git-diff"],
			logStats: false,
			models: [
				{ key: "claude-*", rtk: true },
				{ key: "gpt-5", rtk: false },
			],
			virtualKeys: [{ key: "vk-opencode", rtk: false }],
		});
	});

	it("round-trips overrides through the backend config shape", () => {
		const values: TokenSaverFormValues = {
			...defaultTokenSaverFormValues,
			models: [{ key: "claude-sonnet-*", rtk: false }],
			virtualKeys: [{ key: "vk-opencode", rtk: true }],
		};
		const config = buildTokenSaverConfig(values);
		expect(config.models).toEqual({ "claude-sonnet-*": { rtk: false } });
		expect(config.virtual_keys).toEqual({ "vk-opencode": { rtk: true } });
		expect(toTokenSaverFormValues(config)).toEqual(values);
	});

	it("rejects invalid payload bounds and duplicate overrides", () => {
		expect(validateTokenSaverForm({ ...defaultTokenSaverFormValues, minBytes: 1000, maxBytes: 500 })).toContain("greater than");
		expect(
			validateTokenSaverForm({
				...defaultTokenSaverFormValues,
				models: [
					{ key: "a-*", rtk: true },
					{ key: "a-*", rtk: false },
				],
			}),
		).toContain("duplicated");
		expect(validateTokenSaverForm({ ...defaultTokenSaverFormValues, models: [{ key: "  ", rtk: true }] })).toContain("must not be empty");
	});
});