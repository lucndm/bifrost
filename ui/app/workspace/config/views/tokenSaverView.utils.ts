import { TOKEN_SAVER_FILTERS, TokenSaverConfig, TokenSaverFilter, TokenSaverSettings } from "@/lib/types/plugins";

export interface TokenSaverOverride {
	key: string;
	rtk: boolean;
}

export interface TokenSaverFormValues {
	rtk: boolean;
	minBytes: number;
	maxBytes: number;
	filters: TokenSaverFilter[];
	logStats: boolean;
	models: TokenSaverOverride[];
	virtualKeys: TokenSaverOverride[];
}

export const defaultTokenSaverFormValues: TokenSaverFormValues = {
	rtk: true,
	minBytes: 500,
	maxBytes: 10 * 1024 * 1024,
	filters: [...TOKEN_SAVER_FILTERS],
	logStats: true,
	models: [],
	virtualKeys: [],
};

const settingsToBool = (settings?: TokenSaverSettings): boolean => settings?.rtk ?? true;

export function toTokenSaverFormValues(config?: TokenSaverConfig): TokenSaverFormValues {
	const enabled = config?.rtk_filters?.enabled;
	const toOverrides = (map?: Record<string, TokenSaverSettings>): TokenSaverOverride[] =>
		Object.entries(map ?? {})
			.map(([key, settings]) => ({ key, rtk: settingsToBool(settings) }))
			.sort((a, b) => a.key.localeCompare(b.key));
	return {
		rtk: settingsToBool(config?.default),
		minBytes: config?.rtk_filters?.min_bytes ?? defaultTokenSaverFormValues.minBytes,
		maxBytes: config?.rtk_filters?.max_bytes ?? defaultTokenSaverFormValues.maxBytes,
		filters: enabled?.length ? enabled.filter((filter) => TOKEN_SAVER_FILTERS.includes(filter)) : [...TOKEN_SAVER_FILTERS],
		logStats: config?.log_stats ?? defaultTokenSaverFormValues.logStats,
		models: toOverrides(config?.models),
		virtualKeys: toOverrides(config?.virtual_keys),
	};
}

export function validateTokenSaverForm(values: TokenSaverFormValues): string | null {
	if (!Number.isInteger(values.minBytes) || values.minBytes < 0) return "Minimum payload size must be a non-negative integer.";
	if (!Number.isInteger(values.maxBytes) || values.maxBytes <= 0) return "Maximum payload size must be a positive integer.";
	if (values.maxBytes < values.minBytes) return "Maximum payload size must be greater than or equal to the minimum.";
	if (values.rtk && values.filters.length === 0) return "Select at least one RTK filter.";
	for (const [label, overrides] of [
		["Model", values.models],
		["Virtual key", values.virtualKeys],
	] as const) {
		const seen = new Set<string>();
		for (const override of overrides) {
			if (!override.key.trim()) return `${label} override pattern must not be empty.`;
			if (seen.has(override.key)) return `${label} override "${override.key}" is duplicated.`;
			seen.add(override.key);
		}
	}
	return null;
}

const overridesToMap = (overrides: TokenSaverOverride[]): Record<string, TokenSaverSettings> => {
	const map: Record<string, TokenSaverSettings> = {};
	for (const override of overrides) map[override.key.trim()] = { rtk: override.rtk };
	return map;
};

export function buildTokenSaverConfig(values: TokenSaverFormValues): TokenSaverConfig {
	return {
		default: { rtk: values.rtk },
		virtual_keys: overridesToMap(values.virtualKeys),
		models: overridesToMap(values.models),
		rtk_filters: {
			min_bytes: values.minBytes,
			max_bytes: values.maxBytes,
			enabled: values.filters,
		},
		log_stats: values.logStats,
	};
}