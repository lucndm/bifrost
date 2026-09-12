/**
 * Adaptive Routing RTK Query API
 * Live configuration + metrics snapshot for the OSS adaptive load balancer.
 */

import { AdaptiveConfig, AdaptiveMetricsSnapshot } from "@/lib/types/adaptive";
import { baseApi } from "./baseApi";

export const adaptiveApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getAdaptiveConfig: builder.query<AdaptiveConfig, void>({
			query: () => ({ url: "/adaptive/config", method: "GET" }),
			providesTags: ["AdaptiveConfig"],
		}),
		updateAdaptiveConfig: builder.mutation<AdaptiveConfig, AdaptiveConfig>({
			query: (config) => ({
				url: "/adaptive/config",
				method: "PUT",
				body: config,
			}),
			invalidatesTags: ["AdaptiveConfig"],
		}),
		getAdaptiveMetrics: builder.query<AdaptiveMetricsSnapshot, void>({
			query: () => ({ url: "/adaptive/metrics", method: "GET" }),
			providesTags: ["AdaptiveMetrics"],
		}),
	}),
});

export const { useGetAdaptiveConfigQuery, useUpdateAdaptiveConfigMutation, useGetAdaptiveMetricsQuery } = adaptiveApi;