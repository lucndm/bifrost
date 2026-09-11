import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, useCreatePluginMutation, useGetPluginsQuery, useUpdatePluginMutation } from "@/lib/store";
import { TOKEN_SAVER_FILTERS, TOKEN_SAVER_PLUGIN, TokenSaverConfig, TokenSaverFilter } from "@/lib/types/plugins";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Loader2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import {
	buildTokenSaverConfig,
	defaultTokenSaverFormValues,
	toTokenSaverFormValues,
	TokenSaverFormValues,
	TokenSaverOverride,
	validateTokenSaverForm,
} from "./tokenSaverView.utils";

const filterLabels: Record<TokenSaverFilter, { title: string; description: string }> = {
	"git-diff": { title: "Git diff", description: "Caps oversized diff hunks while preserving file and hunk headers." },
	"dedup-log": { title: "Deduplicate logs", description: "Collapses consecutive duplicate log lines and caps very long output." },
	"smart-truncate": { title: "Smart truncate", description: "Keeps the beginning and end of oversized text while removing the middle." },
};

export default function TokenSaverView() {
	const hasUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: plugins, isLoading } = useGetPluginsQuery();
	const plugin = useMemo(() => plugins?.find((item) => item.name === TOKEN_SAVER_PLUGIN), [plugins]);
	const [values, setValues] = useState<TokenSaverFormValues>(defaultTokenSaverFormValues);
	const [savedValues, setSavedValues] = useState<TokenSaverFormValues>(defaultTokenSaverFormValues);
	const [updatePlugin, { isLoading: isUpdating }] = useUpdatePluginMutation();
	const [createPlugin, { isLoading: isCreating }] = useCreatePluginMutation();
	const isSaving = isUpdating || isCreating;

	useEffect(() => {
		if (plugins === undefined) return;
		const next = toTokenSaverFormValues(plugin?.config as TokenSaverConfig | undefined);
		setValues(next);
		setSavedValues(next);
	}, [plugins, plugin]);

	const validationError = useMemo(() => validateTokenSaverForm(values), [values]);
	const hasChanges = useMemo(() => JSON.stringify(values) !== JSON.stringify(savedValues), [values, savedValues]);
	const updateValues = (updates: Partial<TokenSaverFormValues>) => setValues((current) => ({ ...current, ...updates }));

	const handleEnabledChange = async (enabled: boolean) => {
		if (!hasUpdateAccess) return;
		try {
			const config = plugin?.config ?? buildTokenSaverConfig(values);
			if (plugin) {
				await updatePlugin({ name: TOKEN_SAVER_PLUGIN, data: { enabled, config } }).unwrap();
			} else if (enabled) {
				await createPlugin({ name: TOKEN_SAVER_PLUGIN, enabled, config, path: "" }).unwrap();
			}
			toast.success(enabled ? "Token Saver enabled" : "Token Saver disabled");
		} catch (error) {
			toast.error(`Failed to ${enabled ? "enable" : "disable"} Token Saver: ${getErrorMessage(error)}`);
		}
	};

	const handleFilterChange = (filter: TokenSaverFilter, enabled: boolean) => {
		updateValues({ filters: enabled ? [...values.filters, filter] : values.filters.filter((item) => item !== filter) });
	};

	const handleSave = async () => {
		if (!hasUpdateAccess || validationError) return;
		const config = buildTokenSaverConfig(values);
		try {
			const updated = plugin
				? await updatePlugin({ name: TOKEN_SAVER_PLUGIN, data: { enabled: plugin.enabled, config } }).unwrap()
				: await createPlugin({ name: TOKEN_SAVER_PLUGIN, enabled: false, config, path: "" }).unwrap();
			const next = toTokenSaverFormValues(updated.config as TokenSaverConfig);
			setValues(next);
			setSavedValues(next);
			toast.success("Token Saver configuration updated");
		} catch (error) {
			toast.error(`Failed to update Token Saver configuration: ${getErrorMessage(error)}`);
		}
	};

	return (
		<div className="mx-auto w-full max-w-4xl space-y-6" data-testid="token-saver-view">
			<PageTitle title="Token Saver">
				Reduce input token usage by compressing oversized tool results before requests are dispatched to providers and fallbacks.
			</PageTitle>

			{isLoading ? (
				<div className="flex items-center justify-center py-8">
					<Loader2 className="text-muted-foreground h-4 w-4 animate-spin" />
				</div>
			) : (
				<div className="space-y-6">
					<div className="flex items-center justify-between gap-4">
						<div className="space-y-1">
							<Label htmlFor="token-saver-enabled">Enable Token Saver</Label>
							<p className="text-muted-foreground text-sm">Loads or unloads the plugin immediately without restarting Bifrost.</p>
						</div>
						<Switch
							id="token-saver-enabled"
							data-testid="token-saver-enable-switch"
							size="md"
							checked={Boolean(plugin?.enabled)}
							disabled={!hasUpdateAccess || isSaving}
							onCheckedChange={handleEnabledChange}
						/>
					</div>

					<div className={cn("space-y-6 rounded-md border p-5", !hasUpdateAccess && "opacity-60")}>
						<div className="flex items-center justify-between gap-4">
							<div className="space-y-1">
								<Label htmlFor="token-saver-rtk">RTK compression</Label>
								<p className="text-muted-foreground text-sm">Compress supported Chat tool messages and Responses function call output.</p>
							</div>
							<Switch
								id="token-saver-rtk"
								data-testid="token-saver-rtk-switch"
								checked={values.rtk}
								disabled={!hasUpdateAccess || isSaving}
								onCheckedChange={(rtk) => updateValues({ rtk })}
							/>
						</div>

						<div className="grid gap-4 sm:grid-cols-2">
							<div className="space-y-2">
								<Label htmlFor="token-saver-min-bytes">Minimum payload size (bytes)</Label>
								<Input
									id="token-saver-min-bytes"
									data-testid="token-saver-min-bytes-input"
									type="number"
									min={0}
									step={1}
									value={values.minBytes}
									disabled={!hasUpdateAccess || isSaving}
									onChange={(event) => updateValues({ minBytes: Number(event.target.value) })}
								/>
							</div>
							<div className="space-y-2">
								<Label htmlFor="token-saver-max-bytes">Maximum payload size (bytes)</Label>
								<Input
									id="token-saver-max-bytes"
									data-testid="token-saver-max-bytes-input"
									type="number"
									min={1}
									step={1}
									value={values.maxBytes}
									disabled={!hasUpdateAccess || isSaving}
									onChange={(event) => updateValues({ maxBytes: Number(event.target.value) })}
								/>
							</div>
						</div>

						<div className="space-y-3">
							<div>
								<Label>RTK filters</Label>
								<p className="text-muted-foreground text-sm">Specialized filters run before the generic truncation fallback.</p>
							</div>
							{TOKEN_SAVER_FILTERS.map((filter) => (
								<div key={filter} className="flex items-center justify-between gap-4 rounded-md border p-3">
									<div>
										<p className="text-sm font-medium">{filterLabels[filter].title}</p>
										<p className="text-muted-foreground text-xs">{filterLabels[filter].description}</p>
									</div>
									<Switch
										data-testid={`token-saver-filter-${filter}`}
										checked={values.filters.includes(filter)}
										disabled={!hasUpdateAccess || isSaving || !values.rtk}
										onCheckedChange={(enabled) => handleFilterChange(filter, enabled)}
									/>
								</div>
							))}
						</div>

						<div className="flex items-center justify-between gap-4">
							<div className="space-y-1">
								<Label htmlFor="token-saver-log-stats">Log savings statistics</Label>
								<p className="text-muted-foreground text-sm">
									Logs saved bytes, percentage, filters, and hit count when compression succeeds.
								</p>
							</div>
							<Switch
								id="token-saver-log-stats"
								data-testid="token-saver-log-stats-switch"
								checked={values.logStats}
								disabled={!hasUpdateAccess || isSaving}
								onCheckedChange={(logStats) => updateValues({ logStats })}
							/>
						</div>

						<OverrideSection
							title="Per-model overrides"
							description="Glob patterns matched against the routed model (longest pattern wins). Model overrides take precedence over virtual key overrides."
							inputPlaceholder="claude-sonnet-*"
							overrides={values.models}
							disabled={!hasUpdateAccess || isSaving}
							onChange={(models) => updateValues({ models })}
						/>

						<OverrideSection
							title="Per-virtual-key overrides"
							description="Keyed by the governance-resolved virtual key name. Requests without a matching key fall back to the defaults above."
							inputPlaceholder="vk-opencode"
							overrides={values.virtualKeys}
							disabled={!hasUpdateAccess || isSaving}
							onChange={(virtualKeys) => updateValues({ virtualKeys })}
						/>

						{validationError && (
							<div className="border-destructive/40 bg-destructive/10 text-destructive rounded-sm border p-3 text-sm">
								{validationError}
							</div>
						)}
						<div className="flex justify-end">
							<Button
								data-testid="token-saver-save-button"
								disabled={!hasUpdateAccess || isSaving || !hasChanges || Boolean(validationError)}
								onClick={handleSave}
							>
								{isSaving && <Loader2 className="h-4 w-4 animate-spin" />}Save configuration
							</Button>
						</div>
					</div>
				</div>
			)}
		</div>
	);
}

function OverrideSection({
	title,
	description,
	inputPlaceholder,
	overrides,
	disabled,
	onChange,
}: {
	title: string;
	description: string;
	inputPlaceholder: string;
	overrides: TokenSaverOverride[];
	disabled: boolean;
	onChange: (overrides: TokenSaverOverride[]) => void;
}) {
	const updateOverride = (index: number, updates: Partial<TokenSaverOverride>) => {
		onChange(overrides.map((override, i) => (i === index ? { ...override, ...updates } : override)));
	};
	return (
		<div className="space-y-3">
			<div>
				<Label>{title}</Label>
				<p className="text-muted-foreground text-sm">{description}</p>
			</div>
			{overrides.map((override, index) => (
				<div key={index} className="flex items-center gap-3 rounded-md border p-3">
					<Input
						data-testid={`token-saver-override-key-${index}`}
						placeholder={inputPlaceholder}
						value={override.key}
						disabled={disabled}
						className="flex-1 font-mono"
						onChange={(event) => updateOverride(index, { key: event.target.value })}
					/>
					<div className="flex shrink-0 items-center gap-2">
						<Switch
							data-testid={`token-saver-override-rtk-${index}`}
							checked={override.rtk}
							disabled={disabled}
							onCheckedChange={(rtk) => updateOverride(index, { rtk })}
						/>
						<Button
							variant="ghost"
							size="sm"
							data-testid={`token-saver-override-delete-${index}`}
							disabled={disabled}
							onClick={() => onChange(overrides.filter((_, i) => i !== index))}
						>
							Remove
						</Button>
					</div>
				</div>
			))}
			<Button
				variant="outline"
				size="sm"
				data-testid={`token-saver-override-add-${title.includes("model") ? "model" : "virtual-key"}`}
				disabled={disabled}
				onClick={() => onChange([...overrides, { key: "", rtk: true }])}
			>
				Add override
			</Button>
		</div>
	);
}