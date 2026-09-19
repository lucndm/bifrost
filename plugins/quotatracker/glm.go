package quotatracker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Builtin GLM deployments. The quota monitor API lives on the same hosts as
// the chat APIs: international (Z.ai) and China (BigModel).
const (
	hostZai      = "api.z.ai"
	hostBigModel = "open.bigmodel.cn"

	urlZaiQuota      = "https://api.z.ai/api/monitor/usage/quota/limit"
	urlBigModelQuota = "https://open.bigmodel.cn/api/monitor/usage/quota/limit"
)

// window unit codes reported by GLM. 3 is the rolling session window (the
// coding plan's 5h quota), 6 is the weekly window. Values outside the table
// fall back to a generic "limit_<number>" label.
const (
	unitSession = 3
	unitWeekly  = 6
)

// Limit types that carry meaningful quota state. Everything else in
// data.limits is skipped, mirroring the reference implementation.
const (
	typeTokensLimit = "TOKENS_LIMIT"
	typeCreditLimit = "CREDIT_LIMIT"
)

// builtinQuotaURL is the default Config.QuotaURL mapping.
func builtinQuotaURL(host string) (string, bool) {
	switch strings.ToLower(host) {
	case hostZai:
		return urlZaiQuota, true
	case hostBigModel:
		return urlBigModelQuota, true
	}
	return "", false
}

// builtinTrackedHosts is the default Config.TrackedHosts.
func builtinTrackedHosts() []string {
	return []string{hostZai, hostBigModel}
}

// glmQuotaResponse mirrors the GLM quota monitor payload:
//
//	{"code":200,"msg":"Operation successful","success":true,"data":{
//	  "level":"pro","limits":[{"type":"CREDIT_LIMIT","unit":3,"number":5,
//	  "percentage":25,"nextResetTime":1787905548392}]}}
//
// data and limits may be absent; percentage is percent USED, nextResetTime is
// unix epoch milliseconds (0 = not reported). Raw usage/remaining counters are
// deliberately not modeled — the tracker reasons in percentages only.
type glmQuotaResponse struct {
	Data *struct {
		Level  string     `json:"level"`
		Limits []glmLimit `json:"limits"`
	} `json:"data"`
}

type glmLimit struct {
	Type          string   `json:"type"`
	Unit          *float64 `json:"unit,omitempty"`
	Number        *float64 `json:"number,omitempty"`
	Percentage    *float64 `json:"percentage,omitempty"`
	NextResetTime *float64 `json:"nextResetTime,omitempty"`
}

// defaultHTTPFetch is the Config.Fetch default: a plain GET with a Bearer
// credential. The quota monitor is a low-frequency control-plane call, so
// net/http with a bounded timeout is appropriate (the fasthttp mandate covers
// the provider hot path, not this).
func defaultHTTPFetch(timeout time.Duration) func(ctx context.Context, url, apiKey string) ([]byte, int, error) {
	client := &http.Client{Timeout: timeout}
	return func(ctx context.Context, rawURL, apiKey string) ([]byte, int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, resp.StatusCode, err
		}
		return body, resp.StatusCode, nil
	}
}

// fetchKeySnapshot polls one key's quota endpoint and folds the outcome into a
// KeySnapshot. It never returns an error: every failure mode lands in Status.
func (t *Tracker) fetchKeySnapshot(ctx context.Context, ref KeyRef, quotaURL, apiKey string) KeySnapshot {
	snap := KeySnapshot{Key: ref, FetchedAt: t.now(), Status: StatusOK}

	body, status, err := t.cfg.Fetch(ctx, quotaURL, apiKey)
	if err != nil {
		snap.Status = StatusNetwork
		t.logger.Debug("quota tracker: fetch failed for %s/%s: %v", ref.Provider, ref.KeyID, err)
		return snap
	}
	if status == http.StatusUnauthorized {
		snap.Status = StatusAuth
		t.logger.Warn("quota tracker: quota API rejected the credential for %s/%s (401); key left in rotation but metrics will be missing", ref.Provider, ref.KeyID)
		return snap
	}
	if status != http.StatusOK {
		snap.Status = StatusHTTP
		t.logger.Debug("quota tracker: quota API returned %d for %s/%s", status, ref.Provider, ref.KeyID)
		return snap
	}

	plan, windows, perr := ParseQuotaResponse(body)
	if perr != nil {
		snap.Status = StatusParse
		t.logger.Debug("quota tracker: unparseable quota response for %s/%s: %v", ref.Provider, ref.KeyID, perr)
		return snap
	}
	snap.Plan = plan
	snap.Windows = windows
	return snap
}

// ParseQuotaResponse turns a GLM quota monitor body into the tracker's window
// model. It is the faithful port of the reference parser: filter to
// TOKENS_LIMIT / CREDIT_LIMIT, percentage is percent used (remaining =
// 100-used), nextResetTime is epoch milliseconds (0 or <1s epoch → no reset),
// label by unit (3 → session window of `number` hours, 6 → weekly, token
// limits → "tokens", unknown units → "limit_<number>"), and capitalize the
// plan level. An empty limits array is valid: ("Unknown"/level, no windows).
func ParseQuotaResponse(body []byte) (plan string, windows []WindowState, err error) {
	var resp glmQuotaResponse
	if uerr := json.Unmarshal(body, &resp); uerr != nil {
		return "", nil, uerr
	}

	plan = "Unknown"
	windows = []WindowState{}
	if resp.Data == nil {
		return plan, windows, nil
	}
	if resp.Data.Level != "" {
		plan = capitalize(resp.Data.Level)
	}

	for _, limit := range resp.Data.Limits {
		if limit.Type != typeTokensLimit && limit.Type != typeCreditLimit {
			continue
		}

		used := toNumber(limit.Percentage)
		resetMs := toNumber(limit.NextResetTime)

		windows = append(windows, WindowState{
			WindowKey:        windowKey(limit),
			UsedPercent:      used,
			RemainingPercent: clampPercent(100 - used),
			ResetAt:          epochMsToTime(resetMs),
		})
	}
	return plan, windows, nil
}

// windowKey labels a limit by its unit code, falling back to the type and
// finally the raw limit number so distinct unknown windows never overwrite
// each other.
func windowKey(limit glmLimit) string {
	number := int(toNumber(limit.Number))
	switch {
	case limit.Unit != nil && int(*limit.Unit) == unitSession:
		if number > 0 && number != 5 {
			return fmt.Sprintf("session_%dh", number)
		}
		return "session"
	case limit.Unit != nil && int(*limit.Unit) == unitWeekly:
		return "weekly"
	case limit.Type == typeTokensLimit:
		return "tokens"
	default:
		return fmt.Sprintf("limit_%d", number)
	}
}

// toNumber accepts nil, JSON numbers and numeric strings, falling back to 0 —
// the quota API is loose about which fields it includes.
func toNumber(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// epochMsToTime converts the provider's epoch-milliseconds reset stamp; the
// value 0 (or anything below a plausible second-epoch) means "not reported".
func epochMsToTime(ms float64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(ms))
}

func clampPercent(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

// hostOf extracts the lowercase hostname from a provider base URL, tolerating
// empty/unparseable values (they simply never match a tracked host).
func hostOf(baseURL string) string {
	if strings.TrimSpace(baseURL) == "" {
		return ""
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}
