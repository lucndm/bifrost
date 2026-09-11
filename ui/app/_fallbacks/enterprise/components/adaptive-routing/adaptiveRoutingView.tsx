import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { AdaptiveRouteState } from "@/lib/types/adaptive";
import { useGetAdaptiveMetricsQuery } from "@/lib/store/apis/adaptiveApi";
import { ArrowUpRight, RefreshCw, Shuffle } from "lucide-react";
import { Link } from "@tanstack/react-router";
import { useMemo } from "react";
import { Button } from "@/components/ui/button";

const stateVariant: Record<AdaptiveRouteState, "default" | "secondary" | "destructive" | "outline"> = {
	healthy: "default",
	degraded: "secondary",
	recovering: "outline",
	failed: "destructive",
};

function StateBadge({ state }: { state: AdaptiveRouteState }) {
	return (
		<Badge variant={stateVariant[state]} className="capitalize" data-testid={`adaptive-state-${state}`}>
			{state}
		</Badge>
	);
}

function pct(n: number): string {
	return `${(n * 100).toFixed(1)}%`;
}

function ms(n: number): string {
	return n > 0 ? `${n.toFixed(0)} ms` : "—";
}

/**
 * OSS dashboard for the adaptive load balancer: the live weight snapshot over
 * directions (provider + model) and routes (provider + model + key). Enterprise
 * deployments override this view through the app/enterprise symlink.
 */
export default function AdaptiveRoutingView() {
	const { data, error, isLoading, refetch, isFetching } = useGetAdaptiveMetricsQuery(undefined, {
		pollingInterval: 5000,
	});

	const hasDirections = useMemo(() => (data?.directions?.length ?? 0) > 0, [data]);
	const hasRoutes = useMemo(() => (data?.routes?.length ?? 0) > 0, [data]);

	return (
		<div className="flex h-full w-full flex-col gap-4 p-4" data-testid="adaptive-routing-dashboard">
			<div className="flex flex-wrap items-center justify-between gap-2">
				<div className="flex items-center gap-2">
					<Shuffle className="h-5 w-5" />
					<h1 className="text-xl font-semibold">Adaptive Routing</h1>
				</div>
				<div className="flex items-center gap-3">
					<span className="text-muted-foreground text-xs" data-testid="adaptive-metrics-updated-at">
						{data?.updated_at ? `Snapshot ${new Date(data.updated_at).toLocaleTimeString()}` : ""}
					</span>
					<Button variant="outline" size="sm" onClick={() => refetch()} disabled={isFetching} data-testid="adaptive-metrics-refresh">
						<RefreshCw className={`mr-1 h-3.5 w-3.5 ${isFetching ? "animate-spin" : ""}`} />
						Refresh
					</Button>
					<Link
						to="/workspace/adaptive-routing/settings"
						className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-sm"
						data-testid="adaptive-routing-settings-link"
					>
						Settings
						<ArrowUpRight className="h-4 w-4" />
					</Link>
				</div>
			</div>
			<p className="text-muted-foreground text-sm">
				Traffic distribution across providers and keys is weighted by live error rates and latency. Weights recompute every few seconds;
				routes start at full weight and adapt from observed traffic.
			</p>

			{isLoading ? (
				<div className="space-y-2">
					<Skeleton className="h-8 w-full" />
					<Skeleton className="h-8 w-full" />
					<Skeleton className="h-8 w-2/3" />
				</div>
			) : error ? (
				<Card>
					<CardHeader>
						<CardTitle>Metrics unavailable</CardTitle>
						<CardDescription>The adaptive routing plugin is not loaded or the metrics endpoint could not be reached.</CardDescription>
					</CardHeader>
				</Card>
			) : (
				<>
					<Card>
						<CardHeader>
							<CardTitle>Providers (directions)</CardTitle>
							<CardDescription>Health of each provider for a model, aggregated over its keys.</CardDescription>
						</CardHeader>
						<CardContent>
							{hasDirections ? (
								<Table data-testid="adaptive-directions-table">
									<TableHeader>
										<TableRow>
											<TableHead>Provider</TableHead>
											<TableHead>Model</TableHead>
											<TableHead>State</TableHead>
											<TableHead>Weight</TableHead>
											<TableHead>Error rate</TableHead>
											<TableHead>Latency</TableHead>
										</TableRow>
									</TableHeader>
									<TableBody>
										{data!.directions.map((d) => (
											<TableRow key={`${d.provider}:${d.model}`}>
												<TableCell className="font-medium">{d.provider}</TableCell>
												<TableCell>{d.model}</TableCell>
												<TableCell>
													<StateBadge state={d.state} />
												</TableCell>
												<TableCell>{pct(d.weight)}</TableCell>
												<TableCell>{pct(d.err_rate)}</TableCell>
												<TableCell>{ms(d.latency_ms)}</TableCell>
											</TableRow>
										))}
									</TableBody>
								</Table>
							) : (
								<p className="text-muted-foreground text-sm" data-testid="adaptive-directions-empty">
									No traffic observed yet. Route a few requests and this table fills in within a recompute cycle.
								</p>
							)}
						</CardContent>
					</Card>

					<Card>
						<CardHeader>
							<CardTitle>Keys (routes)</CardTitle>
							<CardDescription>
								Per-key health under the selected provider. Routes on an armed cooldown receive no traffic until it expires.
							</CardDescription>
						</CardHeader>
						<CardContent>
							{hasRoutes ? (
								<Table data-testid="adaptive-routes-table">
									<TableHeader>
										<TableRow>
											<TableHead>Provider</TableHead>
											<TableHead>Model</TableHead>
											<TableHead>Key</TableHead>
											<TableHead>State</TableHead>
											<TableHead>Weight</TableHead>
											<TableHead>Error rate</TableHead>
											<TableHead>Latency</TableHead>
										</TableRow>
									</TableHeader>
									<TableBody>
										{data!.routes.map((r) => (
											<TableRow key={`${r.provider}:${r.model}:${r.key_name}`}>
												<TableCell className="font-medium">{r.provider}</TableCell>
												<TableCell>{r.model}</TableCell>
												<TableCell className="font-mono text-xs">{r.key_name}</TableCell>
												<TableCell>
													<StateBadge state={r.state} />
													{r.in_cooldown && (
														<Badge variant="destructive" className="ml-1" data-testid="adaptive-route-cooldown">
															cooldown
															{r.cooldown_remaining_ms > 0 && ` ${Math.ceil(r.cooldown_remaining_ms / 1000)}s`}
														</Badge>
													)}
													{r.key_locked && (
														<Badge variant="destructive" className="ml-1" data-testid="adaptive-route-key-locked">
															key locked
														</Badge>
													)}
												</TableCell>
												<TableCell>{r.weight === 0 ? "0%" : pct(r.weight)}</TableCell>
												<TableCell>{pct(r.err_rate)}</TableCell>
												<TableCell>{ms(r.latency_ms)}</TableCell>
											</TableRow>
										))}
									</TableBody>
								</Table>
							) : (
								<p className="text-muted-foreground text-sm" data-testid="adaptive-routes-empty">
									No keys observed yet. Keys appear here after they serve traffic.
								</p>
							)}
						</CardContent>
					</Card>
				</>
			)}
		</div>
	);
}