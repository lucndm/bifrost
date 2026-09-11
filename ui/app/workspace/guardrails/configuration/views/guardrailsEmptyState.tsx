/**
 * Guardrails Empty State
 * Shown when the plugin exists but no rules are configured yet.
 */

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { GUARDRAIL_ACTION_OPTIONS, GUARDRAIL_APPLY_TO_OPTIONS } from "@/lib/types/guardrails";
import { Plus, ShieldCheck } from "lucide-react";

interface GuardrailsEmptyStateProps {
	pluginEnabled: boolean;
	onAddClick: () => void;
	onTogglePlugin: (enabled: boolean) => void;
}

export function GuardrailsEmptyState({ pluginEnabled, onAddClick }: GuardrailsEmptyStateProps) {
	return (
		<Card className="mx-auto w-full max-w-3xl">
			<CardHeader className="items-center text-center">
				<ShieldCheck className="text-primary mb-2 h-12 w-12" />
				<CardTitle>No guardrail rules yet</CardTitle>
				<CardDescription>
					Create your first rule to block or redact sensitive content. Rules run in order and later rules see text modified by earlier ones.
				</CardDescription>
			</CardHeader>
			<CardContent className="flex flex-col items-center gap-6">
				<Button data-testid="guardrails-rule-add" onClick={onAddClick}>
					<Plus className="mr-2 h-4 w-4" />
					Create Rule
				</Button>
				<div className="grid w-full grid-cols-1 gap-4 text-sm sm:grid-cols-2">
					<div className="rounded-md border p-3">
						<p className="mb-1 font-medium">Detectors</p>
						<p className="text-muted-foreground">
							Regex/keyword patterns and local PII detection (email, phone, credit card, IP, API keys).
						</p>
					</div>
					<div className="rounded-md border p-3">
						<p className="mb-1 font-medium">Actions</p>
						<p className="text-muted-foreground">{GUARDRAIL_ACTION_OPTIONS.map((o) => o.label).join(", ")}.</p>
					</div>
					<div className="rounded-md border p-3">
						<p className="mb-1 font-medium">Scope</p>
						<p className="text-muted-foreground">
							{GUARDRAIL_APPLY_TO_OPTIONS.map((o) => o.label).join(", ")} — requests and/or responses.
						</p>
					</div>
					<div className="rounded-md border p-3">
						<p className="mb-1 font-medium">Streaming</p>
						<p className="text-muted-foreground">Responses are buffered and evaluated before any content reaches the client.</p>
					</div>
				</div>
				{!pluginEnabled && (
					<p className="text-muted-foreground text-xs">Tip: enable the plugin from the header once your rules are ready.</p>
				)}
			</CardContent>
		</Card>
	);
}