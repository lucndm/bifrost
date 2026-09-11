/**
 * Guardrails Rules Table
 * Lists configured guardrail rules with inline enable toggle and row actions.
 */

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { GuardrailRule, GUARDRAIL_PII_ENTITIES } from "@/lib/types/guardrails";
import { Edit, MoreHorizontal, Search, ShieldCheck, Trash2 } from "lucide-react";
import { useState } from "react";

interface GuardrailsRulesTableProps {
	rules: GuardrailRule[];
	isLoading: boolean;
	pluginEnabled: boolean;
	onEdit: (rule: GuardrailRule) => void;
	onToggle: (rule: GuardrailRule, enabled: boolean) => void;
	onDelete: (rule: GuardrailRule) => void;
}

const ACTION_BADGE_CLASS: Record<string, string> = {
	block: "bg-red-500/15 text-red-600 dark:text-red-400",
	redact: "bg-amber-500/15 text-amber-600 dark:text-amber-400",
	log: "bg-blue-500/15 text-blue-600 dark:text-blue-400",
};

function detectorSummary(rule: GuardrailRule): string {
	if (rule.detector === "regex") {
		const count = rule.patterns?.length ?? 0;
		return `${count} pattern${count === 1 ? "" : "s"}`;
	}
	if (!rule.entities || rule.entities.length === 0) return "All entities";
	return rule.entities.map((e) => GUARDRAIL_PII_ENTITIES.find((def) => def.value === e)?.label ?? e).join(", ");
}

export function GuardrailsRulesTable({ rules, isLoading, pluginEnabled, onEdit, onToggle, onDelete }: GuardrailsRulesTableProps) {
	const [search, setSearch] = useState("");

	const filtered = rules.filter((rule) => {
		if (!search.trim()) return true;
		const q = search.toLowerCase();
		return rule.name.toLowerCase().includes(q) || rule.id.toLowerCase().includes(q);
	});

	return (
		<div className="flex flex-col gap-4">
			<div className="relative w-full max-w-sm">
				<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
				<input
					data-testid="guardrails-rule-search"
					value={search}
					onChange={(e) => setSearch(e.target.value)}
					placeholder="Search rules..."
					className="border-input focus-visible:ring-ring h-9 w-full rounded-md border bg-transparent pr-3 pl-9 text-sm shadow-sm outline-none focus-visible:ring-1"
				/>
			</div>

			<div className="rounded-md border">
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead>Rule</TableHead>
							<TableHead>Detects</TableHead>
							<TableHead>Action</TableHead>
							<TableHead>Applies to</TableHead>
							<TableHead>Enabled</TableHead>
							<TableHead className="w-12" />
						</TableRow>
					</TableHeader>
					<TableBody>
						{isLoading ? (
							<TableRow>
								<TableCell colSpan={6} className="text-muted-foreground h-24 text-center">
									Loading rules...
								</TableCell>
							</TableRow>
						) : filtered.length === 0 ? (
							<TableRow>
								<TableCell colSpan={6} className="text-muted-foreground h-24 text-center">
									{rules.length === 0 ? "No rules configured yet." : "No rules match your search."}
								</TableCell>
							</TableRow>
						) : (
							filtered.map((rule) => (
								<TableRow key={rule.id} data-testid="guardrails-rule-row">
									<TableCell>
										<div className="flex flex-col">
											<span className="font-medium" data-testid="guardrails-rule-name">
												{rule.name}
											</span>
											<span className="text-muted-foreground font-mono text-xs">{rule.id}</span>
										</div>
									</TableCell>
									<TableCell>
										<div className="flex flex-col gap-0.5">
											<span className="text-sm capitalize">{rule.detector === "regex" ? "Regex" : "PII"}</span>
											<span className="text-muted-foreground text-xs">{detectorSummary(rule)}</span>
										</div>
									</TableCell>
									<TableCell>
										<Badge
											variant="secondary"
											className={`capitalize ${ACTION_BADGE_CLASS[rule.action] ?? ""}`}
											data-testid="guardrails-rule-action"
										>
											{rule.action}
										</Badge>
									</TableCell>
									<TableCell>
										<Badge variant="outline" className="capitalize">
											{rule.apply_to}
										</Badge>
									</TableCell>
									<TableCell>
										<Switch
											data-testid="guardrails-rule-enabled"
											checked={rule.enabled}
											onCheckedChange={(enabled) => onToggle(rule, enabled)}
											disabled={!pluginEnabled}
											aria-label={`Toggle rule ${rule.name}`}
										/>
									</TableCell>
									<TableCell>
										<DropdownMenu>
											<DropdownMenuTrigger asChild>
												<Button variant="ghost" size="icon" data-testid="guardrails-rule-actions" aria-label={`Actions for ${rule.name}`}>
													<MoreHorizontal className="h-4 w-4" />
												</Button>
											</DropdownMenuTrigger>
											<DropdownMenuContent align="end">
												<DropdownMenuItem data-testid="guardrails-rule-edit" onClick={() => onEdit(rule)}>
													<Edit className="mr-2 h-4 w-4" />
													Edit
												</DropdownMenuItem>
												<DropdownMenuItem data-testid="guardrails-rule-delete" className="text-destructive" onClick={() => onDelete(rule)}>
													<Trash2 className="mr-2 h-4 w-4" />
													Delete
												</DropdownMenuItem>
											</DropdownMenuContent>
										</DropdownMenu>
									</TableCell>
								</TableRow>
							))
						)}
					</TableBody>
				</Table>
			</div>

			{!pluginEnabled && rules.length > 0 && (
				<div className="flex items-center gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400">
					<ShieldCheck className="h-4 w-4 shrink-0" />
					The guardrails plugin is disabled — rules are saved but not evaluated until you enable it.
				</div>
			)}
		</div>
	);
}