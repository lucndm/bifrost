/**
 * Guardrails Configuration View (OSS)
 * Manage deterministic guardrail rules (regex/keyword + PII) for the
 * built-in `guardrails` plugin. The plugin is created on first enable and
 * persisted through the plugins API with hot reload.
 */

import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage } from "@/lib/store";
import { useCreatePluginMutation, useGetPluginQuery, useUpdatePluginMutation } from "@/lib/store/apis/pluginsApi";
import { GUARDRAILS_PLUGIN_NAME, GuardrailRule, GuardrailsPluginConfig, parseGuardrailsConfig } from "@/lib/types/guardrails";
import { ShieldCheck, Plus } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { GuardrailRuleSheet } from "./guardrailRuleSheet";
import { GuardrailsEmptyState } from "./guardrailsEmptyState";
import { GuardrailsRulesTable } from "./guardrailsRulesTable";

export function GuardrailsConfigView() {
	const { data: plugin, isLoading, error } = useGetPluginQuery(GUARDRAILS_PLUGIN_NAME);
	const [createPlugin, { isLoading: isCreating }] = useCreatePluginMutation();
	const [updatePlugin, { isLoading: isUpdating }] = useUpdatePluginMutation();

	const [sheetOpen, setSheetOpen] = useState(false);
	const [editingRule, setEditingRule] = useState<GuardrailRule | null>(null);

	const pluginMissing = !!error;
	const config = useMemo(() => parseGuardrailsConfig(plugin?.config), [plugin?.config]);
	const pluginEnabled = pluginMissing ? false : (plugin?.enabled ?? false);

	const persist = async (next: GuardrailsPluginConfig) => {
		try {
			if (pluginMissing) {
				await createPlugin({
					name: GUARDRAILS_PLUGIN_NAME,
					path: undefined,
					enabled: true,
					config: next,
				}).unwrap();
			} else {
				await updatePlugin({
					name: GUARDRAILS_PLUGIN_NAME,
					data: { enabled: pluginEnabled, config: next },
				}).unwrap();
			}
			toast.success("Guardrails configuration saved");
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	};

	const handleTogglePlugin = async (enabled: boolean) => {
		try {
			if (pluginMissing) {
				await createPlugin({
					name: GUARDRAILS_PLUGIN_NAME,
					path: undefined,
					enabled,
					config: { ...config, enabled },
				}).unwrap();
			} else {
				await updatePlugin({
					name: GUARDRAILS_PLUGIN_NAME,
					data: { enabled, config },
				}).unwrap();
			}
			toast.success(enabled ? "Guardrails enabled" : "Guardrails disabled");
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	};

	const handleToggleRule = async (rule: GuardrailRule, enabled: boolean) => {
		const next = {
			...config,
			rules: config.rules.map((r) => (r.id === rule.id ? { ...r, enabled } : r)),
		};
		await persist(next);
	};

	const handleDeleteRule = async (rule: GuardrailRule) => {
		const next = { ...config, rules: config.rules.filter((r) => r.id !== rule.id) };
		await persist(next);
		toast.success(`Rule "${rule.name}" deleted`);
	};

	const handleSaveRule = async (rule: GuardrailRule) => {
		const exists = config.rules.some((r) => r.id === rule.id);
		const next = {
			...config,
			rules: exists ? config.rules.map((r) => (r.id === rule.id ? rule : r)) : [...config.rules, rule],
		};
		await persist(next);
		setSheetOpen(false);
		setEditingRule(null);
	};

	const handleCreateNew = () => {
		setEditingRule(null);
		setSheetOpen(true);
	};

	const handleEdit = (rule: GuardrailRule) => {
		setEditingRule(rule);
		setSheetOpen(true);
	};

	if (!isLoading && !pluginMissing && config.rules.length === 0) {
		return (
			<div className="flex flex-col overflow-y-auto">
				<PageTitle>Deterministic guardrails for LLM traffic — regex, keyword and PII rules evaluated at the gateway</PageTitle>
				<GuardrailsEmptyState pluginEnabled={pluginEnabled} onAddClick={handleCreateNew} onTogglePlugin={handleTogglePlugin} />
			</div>
		);
	}

	if (pluginMissing && !isLoading) {
		return (
			<div className="flex flex-col overflow-y-auto">
				<PageTitle>Deterministic guardrails for LLM traffic — regex, keyword and PII rules evaluated at the gateway</PageTitle>
				<Card className="mx-auto w-full max-w-2xl">
					<CardHeader className="items-center text-center">
						<ShieldCheck className="text-primary mb-2 h-12 w-12" />
						<CardTitle>Enable Guardrails</CardTitle>
						<CardDescription>
							Add regex, keyword and PII rules that run at the gateway before requests reach providers and before responses reach your
							clients. Streaming responses are buffered and evaluated before delivery.
						</CardDescription>
					</CardHeader>
					<CardContent className="flex justify-center">
						<Button data-testid="guardrails-enable-btn" onClick={() => handleTogglePlugin(true)} disabled={isCreating}>
							<Plus className="mr-2 h-4 w-4" />
							Enable Guardrails
						</Button>
					</CardContent>
				</Card>
			</div>
		);
	}

	return (
		<div className="flex flex-col overflow-y-auto">
			<PageTitle>Deterministic guardrails for LLM traffic — regex, keyword and PII rules evaluated at the gateway</PageTitle>

			<div className="mb-4 flex items-center justify-between gap-4">
				<div className="flex items-center gap-3">
					<span className="text-muted-foreground text-sm">Plugin</span>
					<Badge variant={pluginEnabled ? "default" : "secondary"} data-testid="guardrails-plugin-status">
						{pluginEnabled ? "Active" : "Disabled"}
					</Badge>
					<Switch
						data-testid="guardrails-plugin-enabled"
						checked={pluginEnabled}
						onCheckedChange={handleTogglePlugin}
						disabled={isLoading || isUpdating || isCreating}
						aria-label="Toggle guardrails plugin"
					/>
				</div>
				<Button data-testid="guardrails-rule-add" onClick={handleCreateNew} disabled={isLoading} className="gap-2">
					<Plus className="h-4 w-4" />
					<span className="hidden sm:inline">New Rule</span>
				</Button>
			</div>

			<GuardrailsRulesTable
				rules={config.rules}
				isLoading={isLoading}
				pluginEnabled={pluginEnabled}
				onEdit={handleEdit}
				onToggle={handleToggleRule}
				onDelete={handleDeleteRule}
			/>

			<GuardrailRuleSheet
				open={sheetOpen}
				onOpenChange={(open) => {
					setSheetOpen(open);
					if (!open) setEditingRule(null);
				}}
				editingRule={editingRule}
				existingIds={config.rules.map((r) => r.id)}
				onSave={handleSaveRule}
			/>
		</div>
	);
}