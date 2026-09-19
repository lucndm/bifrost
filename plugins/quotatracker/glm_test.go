package quotatracker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseQuotaResponse_SessionAndWeeklyCredits is spec vector 1: a Lite plan
// reporting both the 5h session window (unit 3) and the weekly window (unit 6)
// as CREDIT_LIMIT entries.
func TestParseQuotaResponse_SessionAndWeeklyCredits(t *testing.T) {
	body := []byte(`{
		"code": 200, "msg": "Operation successful", "success": true,
		"data": {
			"level": "lite",
			"limits": [
				{"type": "CREDIT_LIMIT", "unit": 3, "number": 5, "percentage": 25, "nextResetTime": 1787905548392},
				{"type": "CREDIT_LIMIT", "unit": 6, "number": 1, "percentage": 10, "nextResetTime": 1788492142997}
			]
		}
	}`)

	plan, windows, err := ParseQuotaResponse(body)
	require.NoError(t, err)
	assert.Equal(t, "Lite", plan)
	require.Len(t, windows, 2)

	session := windows[0]
	assert.Equal(t, "session", session.WindowKey)
	assert.Equal(t, 25.0, session.UsedPercent)
	assert.Equal(t, 75.0, session.RemainingPercent)
	assert.Equal(t, time.UnixMilli(1787905548392).UTC(), session.ResetAt.UTC())

	weekly := windows[1]
	assert.Equal(t, "weekly", weekly.WindowKey)
	assert.Equal(t, 10.0, weekly.UsedPercent)
	assert.Equal(t, 90.0, weekly.RemainingPercent)
	assert.Equal(t, time.UnixMilli(1788492142997).UTC(), weekly.ResetAt.UTC())
}

// TestParseQuotaResponse_TokensLimit is spec vector 2: a TOKENS_LIMIT entry
// with no unit/number, labeled "tokens".
func TestParseQuotaResponse_TokensLimit(t *testing.T) {
	body := []byte(`{
		"data": {
			"level": "standard",
			"limits": [
				{"type": "TOKENS_LIMIT", "percentage": 40, "nextResetTime": 1787905548392}
			]
		}
	}`)

	plan, windows, err := ParseQuotaResponse(body)
	require.NoError(t, err)
	assert.Equal(t, "Standard", plan)
	require.Len(t, windows, 1)
	assert.Equal(t, "tokens", windows[0].WindowKey)
	assert.Equal(t, 40.0, windows[0].UsedPercent)
	assert.Equal(t, 60.0, windows[0].RemainingPercent)
}

// TestParseQuotaResponse_UnknownUnitFallsBack is spec vector 3: a credit
// window with an unrecognized unit keeps its own "limit_<number>" label and a
// zero reset time stays zero.
func TestParseQuotaResponse_UnknownUnitFallsBack(t *testing.T) {
	body := []byte(`{
		"data": {
			"level": "pro",
			"limits": [
				{"type": "CREDIT_LIMIT", "unit": 99, "number": 12, "percentage": 5, "nextResetTime": 0}
			]
		}
	}`)

	plan, windows, err := ParseQuotaResponse(body)
	require.NoError(t, err)
	assert.Equal(t, "Pro", plan)
	require.Len(t, windows, 1)
	assert.Equal(t, "limit_12", windows[0].WindowKey)
	assert.Equal(t, 5.0, windows[0].UsedPercent)
	assert.Equal(t, 95.0, windows[0].RemainingPercent)
	assert.True(t, windows[0].ResetAt.IsZero(), "nextResetTime 0 must map to a zero reset time")
}

// TestParseQuotaResponse_SessionLengthInLabel pins that a non-5h session
// window carries its length in the label instead of silently reading as the
// conventional 5h window.
func TestParseQuotaResponse_SessionLengthInLabel(t *testing.T) {
	_, windows, err := ParseQuotaResponse([]byte(`{
		"data": {"level": "max", "limits": [
			{"type": "CREDIT_LIMIT", "unit": 3, "number": 8, "percentage": 0, "nextResetTime": 1787905548392}
		]}
	}`))
	require.NoError(t, err)
	require.Len(t, windows, 1)
	assert.Equal(t, "session_8h", windows[0].WindowKey)
}

// TestParseQuotaResponse_UnsupportedTypesSkipped: only TOKENS_LIMIT and
// CREDIT_LIMIT are modeled; anything else is ignored.
func TestParseQuotaResponse_UnsupportedTypesSkipped(t *testing.T) {
	body := []byte(`{
		"data": {
			"level": "pro",
			"limits": [
				{"type": "CONCURRENCY_LIMIT", "percentage": 100},
				{"type": "CREDIT_LIMIT", "unit": 3, "number": 5, "percentage": 1, "nextResetTime": 1787905548392},
				null
			]
		}
	}`)

	_, windows, err := ParseQuotaResponse(body)
	require.NoError(t, err)
	require.Len(t, windows, 1)
	assert.Equal(t, "session", windows[0].WindowKey)
}

// TestParseQuotaResponse_MissingDataIsStillValid: a null/absent data object is
// an empty, healthy answer — not an error.
func TestParseQuotaResponse_MissingDataIsStillValid(t *testing.T) {
	plan, windows, err := ParseQuotaResponse([]byte(`{"code": 200, "success": true}`))
	require.NoError(t, err)
	assert.Equal(t, "Unknown", plan)
	assert.Empty(t, windows)
}

func TestParseQuotaResponse_GarbageIsAnError(t *testing.T) {
	_, _, err := ParseQuotaResponse([]byte(`not json`))
	assert.Error(t, err)
}

// TestParseQuotaResponse_Clamps runaway percentages so a provider-side glitch
// (percentage > 100 or negative remaining) can never produce a nonsensical
// remaining value.
func TestParseQuotaResponse_Clamps(t *testing.T) {
	_, windows, err := ParseQuotaResponse([]byte(`{
		"data": {"level": "lite", "limits": [
			{"type": "CREDIT_LIMIT", "unit": 3, "number": 5, "percentage": 130, "nextResetTime": 0},
			{"type": "CREDIT_LIMIT", "unit": 6, "number": 1, "percentage": -5, "nextResetTime": 0}
		]}
	}`))
	require.NoError(t, err)
	require.Len(t, windows, 2)
	assert.Equal(t, 0.0, windows[0].RemainingPercent)
	assert.Equal(t, 100.0, windows[1].RemainingPercent)
}

func TestBuiltinQuotaURL(t *testing.T) {
	url, ok := builtinQuotaURL("api.z.ai")
	assert.True(t, ok)
	assert.Equal(t, "https://api.z.ai/api/monitor/usage/quota/limit", url)

	url, ok = builtinQuotaURL("open.bigmodel.cn")
	assert.True(t, ok)
	assert.Equal(t, "https://open.bigmodel.cn/api/monitor/usage/quota/limit", url)

	_, ok = builtinQuotaURL("api.anthropic.com")
	assert.False(t, ok)
}

func TestHostOf(t *testing.T) {
	assert.Equal(t, "api.z.ai", hostOf("https://api.z.ai/api/coding/paas/v4"))
	assert.Equal(t, "api.z.ai", hostOf("https://API.Z.AI/v1"))
	assert.Equal(t, "", hostOf(""))
	assert.Equal(t, "", hostOf("::not a url::"))
}
