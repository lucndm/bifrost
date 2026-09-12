package guardrails

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

type executionsKeyType struct{}

var executionsKey executionsKeyType

type Plugin struct {
	config *Config
	rules  []compiledRule
	logger schemas.Logger
}

func Init(config *Config, logger schemas.Logger) (*Plugin, error) {
	if config == nil {
		config = &Config{}
	}
	if logger == nil {
		logger = noopLogger{}
	}
	rules, err := compileConfig(config)
	if err != nil {
		return nil, fmt.Errorf("invalid guardrails plugin configuration: %w", err)
	}
	return &Plugin{config: config, rules: rules, logger: logger}, nil
}

func (p *Plugin) GetName() string {
	return PluginName
}

func (p *Plugin) Cleanup() error {
	return nil
}

func (p *Plugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if !p.active() || !supportedRequestType(req) {
		return req, nil, nil
	}

	texts := extractRequestTexts(req)
	if len(texts) == 0 {
		return req, nil, nil
	}

	outcome := runRules(p.rules, phaseInput, valuesOf(texts))
	recordExecutions(ctx, outcome.executions)

	if outcome.blocked != nil {
		return req, &schemas.LLMPluginShortCircuit{
			Error: p.blockError(outcome.blocked, outcome.blockPhase),
		}, nil
	}

	if len(outcome.replacements) > 0 {
		applyReplacements(texts, outcome.replacements)
		recordRedactions(ctx, schemas.RedactionPhaseInput, outcome.replacements)
	}

	if req.RequestType == schemas.ChatCompletionStreamRequest {
		if sc := p.pauseForOutputGuarding(ctx); sc != nil {
			return req, sc, nil
		}
	}
	return req, nil, nil
}

func (p *Plugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if !p.active() || bifrostErr != nil || resp == nil || resp.ChatResponse == nil {
		return resp, bifrostErr, nil
	}

	if isChatStreamResponse(resp) {
		out, outErr := p.evaluateStreamChunk(ctx, resp)
		return out, outErr, nil
	}

	texts := extractResponseTexts(resp)
	if len(texts) == 0 {
		return resp, bifrostErr, nil
	}

	outcome := runRules(p.rules, phaseOutput, valuesOf(texts))
	recordExecutions(ctx, outcome.executions)

	if outcome.blocked != nil {
		return nil, p.blockError(outcome.blocked, outcome.blockPhase), nil
	}

	if len(outcome.replacements) > 0 {
		applyReplacements(texts, outcome.replacements)
		recordRedactions(ctx, schemas.RedactionPhaseOutput, outcome.replacements)
	}
	return resp, bifrostErr, nil
}

type noopLogger struct{}

func (noopLogger) Debug(string, ...any)                                            {}
func (noopLogger) Info(string, ...any)                                             {}
func (noopLogger) Warn(string, ...any)                                             {}
func (noopLogger) Error(string, ...any)                                            {}
func (noopLogger) Fatal(string, ...any)                                            {}
func (noopLogger) SetLevel(schemas.LogLevel)                                       {}
func (noopLogger) SetOutputType(schemas.LoggerOutputType)                          {}
func (noopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder { return nil }

func (p *Plugin) active() bool {
	return p.config.Enabled && len(p.rules) > 0
}

func (p *Plugin) blockError(rule *compiledRule, phase phase) *schemas.BifrostError {
	statusCode := 400
	return &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Type:           schemas.Ptr("guardrail_blocked"),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr("guardrail_blocked"),
			Message: fmt.Sprintf("content blocked by guardrail rule %q during %s evaluation", rule.rule.Name, phase),
		},
		AllowFallbacks: schemas.Ptr(false),
	}
}

func recordExecutions(ctx *schemas.BifrostContext, executions []execution) {
	if ctx == nil || len(executions) == 0 {
		return
	}
	existing, _ := ctx.Value(executionsKey).([]execution)
	merged := make([]execution, 0, len(existing)+len(executions))
	merged = append(merged, existing...)
	merged = append(merged, executions...)
	ctx.SetValue(executionsKey, merged)
}

func recordRedactions(ctx *schemas.BifrostContext, phase schemas.RedactionPhase, replacements map[string]string) {
	if ctx == nil || len(replacements) == 0 {
		return
	}
	data, _ := schemas.RedactionDataFromContext(ctx)
	data.LiteralReplacements.MergePhase(phase, replacements)
	schemas.SetRedactionDataOnContext(ctx, data)
}

var _ schemas.LLMPlugin = (*Plugin)(nil)
