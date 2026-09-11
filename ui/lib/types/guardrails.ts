// Guardrails plugin types matching plugins/guardrails Go config structures

export const GUARDRAILS_PLUGIN_NAME = "guardrails";

export type GuardrailApplyTo = "input" | "output" | "both";
export type GuardrailAction = "block" | "redact" | "log";
export type GuardrailDetector = "regex" | "pii";
export type GuardrailPiiEntity = "email" | "phone" | "credit_card" | "ip_address" | "api_key";

export const GUARDRAIL_APPLY_TO_OPTIONS: { value: GuardrailApplyTo; label: string; description: string }[] = [
	{ value: "input", label: "Input", description: "Evaluate request content before it reaches the provider" },
	{ value: "output", label: "Output", description: "Evaluate assistant content before it reaches the client" },
	{ value: "both", label: "Both", description: "Evaluate request and response content" },
];

export const GUARDRAIL_ACTION_OPTIONS: { value: GuardrailAction; label: string; description: string }[] = [
	{ value: "block", label: "Block", description: "Reject the request or response with a content-policy error" },
	{ value: "redact", label: "Redact", description: "Replace matched content with stable tokens like [EMAIL_1]" },
	{ value: "log", label: "Log only", description: "Record findings without changing or blocking content" },
];

export const GUARDRAIL_PII_ENTITIES: { value: GuardrailPiiEntity; label: string }[] = [
	{ value: "email", label: "Email" },
	{ value: "phone", label: "Phone" },
	{ value: "credit_card", label: "Credit card" },
	{ value: "ip_address", label: "IP address" },
	{ value: "api_key", label: "API key" },
];

export interface GuardrailRule {
	id: string;
	name: string;
	enabled: boolean;
	apply_to: GuardrailApplyTo;
	action: GuardrailAction;
	detector: GuardrailDetector;
	patterns?: string[];
	case_sensitive?: boolean;
	entities?: string[];
	replacement?: string;
}

export interface GuardrailsPluginConfig {
	enabled: boolean;
	rules: GuardrailRule[];
}

export const DEFAULT_GUARDRAILS_CONFIG: GuardrailsPluginConfig = {
	enabled: true,
	rules: [],
};

export function parseGuardrailsConfig(raw: unknown): GuardrailsPluginConfig {
	if (!raw || typeof raw !== "object") {
		return { ...DEFAULT_GUARDRAILS_CONFIG };
	}
	const cfg = raw as Partial<GuardrailsPluginConfig>;
	return {
		enabled: cfg.enabled ?? true,
		rules: Array.isArray(cfg.rules) ? cfg.rules : [],
	};
}