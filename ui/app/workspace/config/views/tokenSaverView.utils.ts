import { TOKEN_SAVER_FILTERS, TokenSaverConfig, TokenSaverFilter } from "@/lib/types/plugins";

export interface TokenSaverFormValues {
	rtk: boolean;
	minBytes: number;
	maxBytes: number;
	filters: TokenSaverFilter[];
	logStats: boolean;
}

export const defaultTokenSaverFormValues: TokenSaverFormValues = {
	rtk: true,
	minBytes: 500,
	maxBytes: 10 * 1024 * 1024,
	filters: [...TOKEN_SAVER_FILTERS],
	logStats: true,
};

export function toTokenSaverFormValues(config?: TokenSaverConfig): TokenSaverFormValues {
	const enabled = config?.rtk_filters?.enabled;
	return {
		rtk: config?.default?.rtk ?? defaultTokenSaverFormValues.rtk,
		minBytes: config?.rtk_filters?.min_bytes ?? defaultTokenSaverFormValues.minBytes,
		maxBytes: config?.rtk_filters?.max_bytes ?? defaultTokenSaverFormValues.maxBytes,
		filters: enabled?.length ? enabled.filter((filter) => TOKEN_SAVER_FILTERS.includes(filter)) : [...TOKEN_SAVER_FILTERS],
		logStats: config?.log_stats ?? defaultTokenSaverFormValues.logStats,
	};
}

export function validateTokenSaverForm(values: TokenSaverFormValues): string | null {
	if (!Number.isInteger(values.minBytes) || values.minBytes < 0) return "Minimum payload size must be a non-negative integer.";
	if (!Number.isInteger(values.maxBytes) || values.maxBytes <= 0) return "Maximum payload size must be a positive integer.";
	if (values.maxBytes < values.minBytes) return "Maximum payload size must be greater than or equal to the minimum.";
	if (values.rtk && values.filters.length === 0) return "Select at least one RTK filter.";
	return null;
}

export function buildTokenSaverConfig(values: TokenSaverFormValues): TokenSaverConfig {
	return {
		default: { rtk: values.rtk },
		rtk_filters: {
			min_bytes: values.minBytes,
			max_bytes: values.maxBytes,
			enabled: values.filters,
		},
		log_stats: values.logStats,
	};
}