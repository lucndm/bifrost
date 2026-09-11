/**
 * Guardrail Rule Sheet
 * Create/edit form for guardrail rules (regex/keyword + PII detectors).
 */

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage } from "@/lib/store";
import {
	GUARDRAIL_ACTION_OPTIONS,
	GUARDRAIL_APPLY_TO_OPTIONS,
	GUARDRAIL_PII_ENTITIES,
	GuardrailAction,
	GuardrailApplyTo,
	GuardrailDetector,
	GuardrailRule,
} from "@/lib/types/guardrails";
import { guardrailRuleFormSchema, GuardrailRuleFormSchema } from "@/lib/types/schemas";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useMemo } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";

interface GuardrailRuleSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	editingRule: GuardrailRule | null;
	existingIds: string[];
	onSave: (rule: GuardrailRule) => Promise<void>;
}

const DEFAULT_FORM: GuardrailRuleFormSchema = {
	name: "",
	enabled: true,
	applyTo: "input",
	action: "block",
	detector: "regex",
	patternsText: "",
	caseSensitive: false,
	entities: [],
	replacement: "",
};

function slugify(name: string): string {
	return name
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "")
		.slice(0, 40);
}

function uniqueId(name: string, existingIds: string[]): string {
	const base = slugify(name) || "rule";
	if (!existingIds.includes(base)) return base;
	let n = 2;
	while (existingIds.includes(`${base}-${n}`)) n++;
	return `${base}-${n}`;
}

export function GuardrailRuleSheet({ open, onOpenChange, editingRule, existingIds, onSave }: GuardrailRuleSheetProps) {
	const form = useForm<GuardrailRuleFormSchema>({
		resolver: zodResolver(guardrailRuleFormSchema),
		defaultValues: DEFAULT_FORM,
	});

	const { register, handleSubmit, watch, setValue, reset, formState } = form;
	const { errors, isSubmitting } = formState;

	const detector = watch("detector");
	const action = watch("action");
	const applyTo = watch("applyTo");
	const name = watch("name");
	const patternsText = watch("patternsText");
	const entities = watch("entities");

	const idPreview = useMemo(() => {
		if (editingRule) return editingRule.id;
		return uniqueId(name || "rule", existingIds);
	}, [name, editingRule, existingIds]);

	useEffect(() => {
		if (!open) return;
		if (editingRule) {
			reset({
				name: editingRule.name,
				enabled: editingRule.enabled,
				applyTo: editingRule.apply_to,
				action: editingRule.action,
				detector: editingRule.detector,
				patternsText: (editingRule.patterns ?? []).join("\n"),
				caseSensitive: editingRule.case_sensitive ?? false,
				entities: editingRule.entities ?? [],
				replacement: editingRule.replacement ?? "",
			});
		} else {
			reset(DEFAULT_FORM);
		}
	}, [open, editingRule, reset]);

	const patternCount = patternsText
		.split("\n")
		.map((p) => p.trim())
		.filter(Boolean).length;

	const onSubmit = async (data: GuardrailRuleFormSchema) => {
		const rule: GuardrailRule = {
			id: editingRule?.id ?? uniqueId(data.name, existingIds),
			name: data.name.trim(),
			enabled: data.enabled,
			apply_to: data.applyTo as GuardrailApplyTo,
			action: data.action as GuardrailAction,
			detector: data.detector as GuardrailDetector,
		};
		if (data.detector === "regex") {
			rule.patterns = data.patternsText
				.split("\n")
				.map((p) => p.trim())
				.filter(Boolean);
			rule.case_sensitive = data.caseSensitive;
			if (data.replacement.trim()) rule.replacement = data.replacement.trim();
		} else {
			rule.entities = data.entities;
			if (data.replacement.trim()) rule.replacement = data.replacement.trim();
		}
		try {
			await onSave(rule);
			toast.success(editingRule ? "Rule updated" : "Rule created");
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	};

	const toggleEntity = (value: string) => {
		const next = entities.includes(value) ? entities.filter((e) => e !== value) : [...entities, value];
		setValue("entities", next, { shouldDirty: true });
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col gap-6 overflow-y-auto sm:max-w-lg" data-testid="guardrails-rule-sheet">
				<SheetHeader>
					<SheetTitle>{editingRule ? "Edit guardrail rule" : "New guardrail rule"}</SheetTitle>
					<SheetDescription>
						Rules run in order; later rules see content modified by earlier redaction rules. A block rule stops all further evaluation.
					</SheetDescription>
				</SheetHeader>

				<form onSubmit={handleSubmit(onSubmit)} className="flex flex-1 flex-col gap-5">
					<div className="flex flex-col gap-2">
						<Label htmlFor="guardrail-rule-name">Rule name</Label>
						<Input
							id="guardrail-rule-name"
							data-testid="guardrails-rule-name-input"
							placeholder="e.g. Redact customer PII"
							{...register("name")}
						/>
						{errors.name && <p className="text-destructive text-sm">{errors.name.message}</p>}
						{name.trim() && !editingRule && <p className="text-muted-foreground font-mono text-xs">id: {idPreview}</p>}
					</div>

					<div className="flex items-center justify-between rounded-md border p-3">
						<div>
							<Label htmlFor="guardrail-rule-enabled">Enabled</Label>
							<p className="text-muted-foreground text-xs">Disabled rules are skipped at runtime</p>
						</div>
						<Switch
							id="guardrail-rule-enabled"
							data-testid="guardrails-rule-enabled-input"
							checked={watch("enabled")}
							onCheckedChange={(checked) => setValue("enabled", checked, { shouldDirty: true })}
						/>
					</div>

					<div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
						<div className="flex flex-col gap-2">
							<Label>Detects</Label>
							<Select value={detector} onValueChange={(v) => setValue("detector", v as GuardrailDetector, { shouldDirty: true })}>
								<SelectTrigger data-testid="guardrails-rule-detector-select">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value="regex">Regex</SelectItem>
									<SelectItem value="pii">PII</SelectItem>
								</SelectContent>
							</Select>
						</div>
						<div className="flex flex-col gap-2">
							<Label>Action</Label>
							<Select value={action} onValueChange={(v) => setValue("action", v as GuardrailAction, { shouldDirty: true })}>
								<SelectTrigger data-testid="guardrails-rule-action-select">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{GUARDRAIL_ACTION_OPTIONS.map((opt) => (
										<SelectItem key={opt.value} value={opt.value}>
											{opt.label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							<p className="text-muted-foreground text-xs">{GUARDRAIL_ACTION_OPTIONS.find((o) => o.value === action)?.description}</p>
						</div>
						<div className="flex flex-col gap-2">
							<Label>Applies to</Label>
							<Select value={applyTo} onValueChange={(v) => setValue("applyTo", v as GuardrailApplyTo, { shouldDirty: true })}>
								<SelectTrigger data-testid="guardrails-rule-applyto-select">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{GUARDRAIL_APPLY_TO_OPTIONS.map((opt) => (
										<SelectItem key={opt.value} value={opt.value}>
											{opt.label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
					</div>

					{detector === "regex" ? (
						<div className="flex flex-col gap-2">
							<Label htmlFor="guardrail-rule-patterns">Patterns (one per line)</Label>
							<Textarea
								id="guardrail-rule-patterns"
								data-testid="guardrails-rule-patterns-input"
								rows={4}
								placeholder={"sk-[A-Za-z0-9]{16,}\ninternal-project-codename"}
								{...register("patternsText")}
							/>
							<div className="flex items-center justify-between">
								<p className="text-muted-foreground text-xs">
									{patternCount} pattern{patternCount === 1 ? "" : "s"} — RE2 syntax, case-insensitive by default
								</p>
							</div>
							{errors.patternsText && <p className="text-destructive text-sm">{errors.patternsText.message}</p>}
							<div className="flex items-center justify-between rounded-md border p-3">
								<div>
									<Label htmlFor="guardrail-rule-case">Case sensitive</Label>
									<p className="text-muted-foreground text-xs">Match patterns exactly as written</p>
								</div>
								<Switch
									id="guardrail-rule-case"
									data-testid="guardrails-rule-case-input"
									checked={watch("caseSensitive")}
									onCheckedChange={(checked) => setValue("caseSensitive", checked, { shouldDirty: true })}
								/>
							</div>
						</div>
					) : (
						<div className="flex flex-col gap-2">
							<Label>PII entities</Label>
							<div className="grid grid-cols-2 gap-2 rounded-md border p-3" data-testid="guardrails-rule-entities">
								{GUARDRAIL_PII_ENTITIES.map((entity) => (
									<label key={entity.value} className="flex items-center gap-2 text-sm">
										<Checkbox checked={entities.includes(entity.value)} onCheckedChange={() => toggleEntity(entity.value)} />
										{entity.label}
									</label>
								))}
							</div>
							<p className="text-muted-foreground text-xs">Leave all unchecked to detect every supported entity.</p>
						</div>
					)}

					<div className="flex flex-col gap-2">
						<Label htmlFor="guardrail-rule-replacement">Replacement (optional)</Label>
						<Input
							id="guardrail-rule-replacement"
							data-testid="guardrails-rule-replacement-input"
							placeholder="[REDACTED]"
							{...register("replacement")}
						/>
						<p className="text-muted-foreground text-xs">
							Used by redact rules. Leave empty for numbered tokens like <span className="font-mono">[EMAIL_1]</span>.
						</p>
					</div>

					<SheetFooter className="mt-auto flex-row justify-end gap-2">
						<Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
							Cancel
						</Button>
						<Button type="submit" data-testid="guardrails-rule-save" disabled={isSubmitting}>
							{editingRule ? "Save changes" : "Create rule"}
						</Button>
					</SheetFooter>
				</form>
			</SheetContent>
		</Sheet>
	);
}