import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useGetAdaptiveConfigQuery, useUpdateAdaptiveConfigMutation } from "@/lib/store/apis/adaptiveApi";
import { AdaptiveConfig } from "@/lib/types/adaptive";
import { toast } from "sonner";
import { useEffect, useMemo, useState } from "react";

interface SwitchRow {
	key: keyof Pick<
		AdaptiveConfig,
		| "direction_selection_enabled"
		| "route_selection_enabled"
		| "append_fallbacks_to_pinned"
		| "reroute_failed_directions"
		| "prune_failed_fallbacks"
		| "fail_fast_when_exhausted"
	>;
	label: string;
	description: string;
	defaultOn: boolean;
}

const SWITCH_ROWS: SwitchRow[] = [
	{
		key: "direction_selection_enabled",
		label: "Provider selection (direction)",
		description: "Pick the healthiest provider for a model from the request's provider and fallback chain.",
		defaultOn: true,
	},
	{
		key: "route_selection_enabled",
		label: "Key selection (route)",
		description: "Pin the best observed key of the selected provider. Explicit caller or rule pins always win.",
		defaultOn: true,
	},
	{
		key: "append_fallbacks_to_pinned",
		label: "Append fallbacks to pinned requests",
		description:
			"Add the healthy governance-eligible providers for the model behind the fallbacks a request already configured. Requires the governance plugin.",
		defaultOn: false,
	},
	{
		key: "reroute_failed_directions",
		label: "Re-route failed providers",
		description: "When the primary provider's direction has failed, promote the first healthy fallback ahead of it.",
		defaultOn: false,
	},
	{
		key: "prune_failed_fallbacks",
		label: "Prune failed fallbacks",
		description: "Drop failed directions from the request's fallback chain (never the last one).",
		defaultOn: false,
	},
	{
		key: "fail_fast_when_exhausted",
		label: "Fail fast when keys exhausted",
		description:
			"Answer 503 with a Retry-After instead of sending a request into keys that are all cooling down. Only when the attempt is the request's last resort and the keys were observed cooling — an unobserved key may still be healthy.",
		defaultOn: false,
	},
];

/**
 * OSS settings for the adaptive load balancer. The five switches apply live
 * (no restart) once saved; the recompute interval is read at plugin start.
 * Enterprise deployments override this view through the app/enterprise symlink.
 */
export default function LoadBalancerSettingsView() {
	const { data, isLoading, error, refetch, isFetching } = useGetAdaptiveConfigQuery();
	const [updateConfig, { isLoading: isSaving }] = useUpdateAdaptiveConfigMutation();

	const [draft, setDraft] = useState<AdaptiveConfig | null>(null);

	// Seed the draft from the server config, re-seeding only when the identity
	// of the fetched config changes (poll-free query).
	useEffect(() => {
		if (data) setDraft(data);
	}, [data]);

	const dirty = useMemo(() => {
		if (!data || !draft) return false;
		return SWITCH_ROWS.some((row) => draft[row.key] !== data[row.key]);
	}, [data, draft]);

	if (error) {
		return (
			<div className="w-full py-4" data-testid="adaptive-settings-error">
				<Card>
					<CardHeader>
						<CardTitle>Settings unavailable</CardTitle>
						<CardDescription>
							The adaptive routing plugin is not loaded or the config endpoint could not be reached. Enable the "adaptive" plugin and try
							again.
						</CardDescription>
					</CardHeader>
					<CardContent>
						<Button variant="outline" size="sm" onClick={() => refetch()} disabled={isFetching}>
							Retry
						</Button>
					</CardContent>
				</Card>
			</div>
		);
	}

	if (isLoading || !draft) {
		return (
			<div className="w-full space-y-2 py-4" data-testid="adaptive-settings-loading">
				<Skeleton className="h-10 w-full" />
				<Skeleton className="h-10 w-full" />
				<Skeleton className="h-10 w-3/4" />
			</div>
		);
	}

	const save = async () => {
		try {
			await updateConfig(draft).unwrap();
			toast.success("Adaptive routing settings applied");
		} catch {
			toast.error("Failed to apply adaptive routing settings");
		}
	};

	return (
		<div className="flex w-full flex-col gap-4 py-4" data-testid="adaptive-settings">
			<Card>
				<CardHeader>
					<CardTitle>Adaptive load balancing</CardTitle>
					<CardDescription>
						Weights recompute every {draft.recompute_interval_ms / 1000}s from live error rates and latency. Selection switches apply
						immediately; the interval is read at startup.
					</CardDescription>
				</CardHeader>
				<CardContent className="flex flex-col gap-1">
					{SWITCH_ROWS.map((row) => (
						<div
							key={row.key}
							className="flex items-start justify-between gap-4 border-b py-3 last:border-b-0"
							data-testid={`adaptive-settings-${row.key.replace(/_/g, "-")}-row`}
						>
							<div className="space-y-1">
								<div className="flex items-center gap-2">
									<span className="text-sm font-medium">{row.label}</span>
									{row.defaultOn ? <Badge variant="secondary">default on</Badge> : <Badge variant="outline">opt-in</Badge>}
								</div>
								<p className="text-muted-foreground text-sm">{row.description}</p>
							</div>
							<Switch
								checked={draft[row.key]}
								onCheckedChange={(checked) => setDraft({ ...draft, [row.key]: checked })}
								data-testid={`adaptive-settings-${row.key.replace(/_/g, "-")}-switch`}
							/>
						</div>
					))}
				</CardContent>
			</Card>
			<Card>
				<CardHeader>
					<CardTitle>Error classification rules</CardTitle>
					<CardDescription>
						The ordered table that turns upstream errors into cooldowns, penalties or fail-fasts — first match wins. Key-scoped rules cool
						the key across every model. Edit the table through <code className="font-mono text-xs">PUT /api/adaptive/config</code> (the{" "}
						<code className="font-mono text-xs">error_rules</code> array); empty restores the defaults shown here.
					</CardDescription>
				</CardHeader>
				<CardContent data-testid="adaptive-settings-error-rules">
					{(draft.error_rules?.length ?? 0) === 0 ? (
						<p className="text-muted-foreground text-sm">
							Using the built-in default rule table (rate limits, server errors, banned accounts, auth failures, network errors, bad
							requests).
						</p>
					) : (
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Name</TableHead>
									<TableHead>Status codes</TableHead>
									<TableHead>Text patterns</TableHead>
									<TableHead>Action</TableHead>
									<TableHead>Base cooldown</TableHead>
									<TableHead>Scope</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{draft.error_rules.map((rule, i) => (
									<TableRow key={`${rule.name}-${i}`}>
										<TableCell className="font-medium">{rule.name}</TableCell>
										<TableCell className="font-mono text-xs">{rule.status_codes?.length ? rule.status_codes.join(", ") : "any"}</TableCell>
										<TableCell className="max-w-64 truncate font-mono text-xs" title={rule.text_patterns?.join(", ")}>
											{rule.text_patterns?.length ? rule.text_patterns.join(", ") : "—"}
										</TableCell>
										<TableCell>
											<Badge variant={rule.action === "cooldown" ? "destructive" : "secondary"}>{rule.action}</Badge>
										</TableCell>
										<TableCell>{rule.cooldown_ms ? `${rule.cooldown_ms / 1000}s` : "—"}</TableCell>
										<TableCell>{rule.scope === "key" ? "key-wide" : "route"}</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					)}
				</CardContent>
			</Card>
			<div className="flex justify-end">
				<Button onClick={save} disabled={!dirty || isSaving} data-testid="adaptive-settings-save">
					{isSaving ? "Applying…" : "Save changes"}
				</Button>
			</div>
		</div>
	);
}