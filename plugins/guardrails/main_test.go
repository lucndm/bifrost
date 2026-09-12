package guardrails

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func validConfig() *Config {
	return &Config{
		Enabled: true,
		Rules: []Rule{
			{
				ID: "block-keys", Name: "Block API keys", Enabled: true,
				ApplyTo: ApplyToInput, Action: ActionBlock, Detector: DetectorRegex,
				Patterns: []string{`sk\-[A-Za-z0-9]{16,}`},
			},
			{
				ID: "redact-pii", Name: "Redact PII", Enabled: true,
				ApplyTo: ApplyToBoth, Action: ActionRedact, Detector: DetectorPII,
				Entities: []string{"email", "credit_card"},
			},
		},
	}
}

func TestInitRejectsInvalidConfigs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"duplicate id", func(c *Config) { c.Rules[1].ID = c.Rules[0].ID }},
		{"empty id", func(c *Config) { c.Rules[0].ID = "" }},
		{"bad apply_to", func(c *Config) { c.Rules[0].ApplyTo = "sideways" }},
		{"bad action", func(c *Config) { c.Rules[0].Action = "explode" }},
		{"regex without patterns", func(c *Config) { c.Rules[0].Patterns = nil }},
		{"invalid regex", func(c *Config) { c.Rules[0].Patterns = []string{"([unclosed"} }},
		{"unknown pii entity", func(c *Config) { c.Rules[1].Entities = []string{"dna"} }},
		{"unknown detector", func(c *Config) { c.Rules[0].Detector = "vibes" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)
			if _, err := Init(cfg, nil); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestPreLLMHookBlocksInput(t *testing.T) {
	p, err := Init(validConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("my key is sk-abcdefghijklmnop1234 ok")}},
			},
		},
	}
	_, sc, err := p.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if sc == nil || sc.Error == nil {
		t.Fatal("expected short-circuit block error")
	}
	if sc.Error.AllowFallbacks == nil || *sc.Error.AllowFallbacks {
		t.Fatal("block must disable fallbacks")
	}
	if *sc.Error.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", *sc.Error.StatusCode)
	}
}

func TestPreLLMHookRedactsInputAndRecordsRedactionData(t *testing.T) {
	p, err := Init(validConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("contact jane@example.com now")}},
			},
		},
	}
	out, sc, err := p.PreLLMHook(ctx, req)
	if err != nil || sc != nil {
		t.Fatalf("err=%v sc=%v", err, sc)
	}
	got := *out.ChatRequest.Input[0].Content.ContentStr
	if got != "contact [EMAIL_1] now" {
		t.Fatalf("redacted = %q", got)
	}
	data, ok := schemas.RedactionDataFromContext(ctx)
	if !ok {
		t.Fatal("expected redaction data on ctx")
	}
	if data.LiteralReplacements.Input["jane@example.com"] != "[EMAIL_1]" {
		t.Fatalf("redaction map = %v", data.LiteralReplacements.Input)
	}
}

func TestPostLLMHookRedactsOutput(t *testing.T) {
	p, err := Init(validConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("email: sam@corp.org")}},
				}},
			},
		},
	}
	out, bifrostErr, err := p.PostLLMHook(ctx, resp, nil)
	if err != nil || bifrostErr != nil {
		t.Fatalf("err=%v bifrostErr=%v", err, bifrostErr)
	}
	got := *out.ChatResponse.Choices[0].Message.Content.ContentStr
	if got != "email: [EMAIL_1]" {
		t.Fatalf("redacted = %q", got)
	}
}

func TestPostLLMHookBlocksOutput(t *testing.T) {
	cfg := validConfig()
	cfg.Rules[0].ApplyTo = ApplyToOutput
	p, err := Init(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("leaked sk-abcdefghijklmnop1234")}},
				}},
			},
		},
	}
	out, bifrostErr, err := p.PostLLMHook(ctx, resp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Fatal("response must be invalidated on output block")
	}
	if bifrostErr == nil {
		t.Fatal("expected block error")
	}
}

func TestPipelineAppliesRuleOrderAndCascade(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Rules: []Rule{
			{
				ID: "redact-first", Enabled: true, ApplyTo: ApplyToInput, Action: ActionRedact,
				Detector: DetectorRegex, Patterns: []string{`supersecret`}, Replacement: "[MASKED]",
			},
			{
				ID: "block-second", Enabled: true, ApplyTo: ApplyToInput, Action: ActionBlock,
				Detector: DetectorRegex, Patterns: []string{`MASKED`},
			},
		},
	}
	p, err := Init(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("say supersecret loud")}},
			},
		},
	}
	_, sc, err := p.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if sc == nil {
		t.Fatal("second rule must see redacted text from first rule and block")
	}
}

func TestDisabledPluginPassthrough(t *testing.T) {
	cfg := validConfig()
	cfg.Enabled = false
	p, err := Init(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("sk-abcdefghijklmnop1234")}},
			},
		},
	}
	_, sc, err := p.PreLLMHook(ctx, req)
	if err != nil || sc != nil {
		t.Fatalf("disabled plugin must pass through, got err=%v sc=%v", err, sc)
	}
}

func TestUnsupportedRequestTypePassthrough(t *testing.T) {
	p, err := Init(validConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType:      schemas.EmbeddingRequest,
		EmbeddingRequest: &schemas.BifrostEmbeddingRequest{},
	}
	_, sc, err := p.PreLLMHook(ctx, req)
	if err != nil || sc != nil {
		t.Fatalf("unsupported type must pass through, got err=%v sc=%v", err, sc)
	}
}

func TestContentBlocksRedacted(t *testing.T) {
	p, err := Init(validConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("write to amy@corp.io please")},
					},
				}},
			},
		},
	}
	out, sc, err := p.PreLLMHook(ctx, req)
	if err != nil || sc != nil {
		t.Fatalf("err=%v sc=%v", err, sc)
	}
	got := *out.ChatRequest.Input[0].Content.ContentBlocks[0].Text
	if got != "write to [EMAIL_1] please" {
		t.Fatalf("redacted block = %q", got)
	}
}
