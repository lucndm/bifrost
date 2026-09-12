package guardrails

type phase string

const (
	phaseInput  phase = "input"
	phaseOutput phase = "output"
)

type executionStatus string

const (
	statusSuccess    executionStatus = "success"
	statusIntervened executionStatus = "intervened"
)

type Finding struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type execution struct {
	RuleID   string          `json:"rule_id"`
	RuleName string          `json:"rule_name"`
	Phase    phase           `json:"phase"`
	Action   Action          `json:"action"`
	Status   executionStatus `json:"status"`
	Findings int             `json:"findings"`
}

type detector interface {
	inspect(text string, maxHits int) []Finding
}
