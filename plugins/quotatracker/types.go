// Package quotatracker polls provider-side subscription quota windows (e.g. the
// GLM Coding Plan 5h session / weekly windows exposed by the Z.ai and BigModel
// quota monitor APIs) and turns them into routing signals:
//
//   - a KeyPoolFilter that vetoes keys whose quota window is exhausted until the
//     window's nextResetTime, so weighted routing stops sending traffic into a
//     guaranteed 429;
//   - snapshot views (VisitSnapshots) for observability exporters such as the
//     otel plugin's quota gauges.
//
// The tracker is pull-based: it never counts tokens itself, it only queries the
// provider's quota API with the key's own credential. All failures fail open —
// a broken quota endpoint must never take a working key out of rotation.
package quotatracker

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Config controls the tracker. Zero values are replaced by defaults in
// applyDefaults; see ConfigFromEnv for the deployment-facing entry point.
type Config struct {
	// Enabled gates everything. A disabled tracker's filter is a no-op.
	Enabled bool

	// PollInterval is how often each tracked key's quota endpoint is queried.
	// GLM windows move on the order of hours, so the default 60s is generous.
	PollInterval time.Duration

	// VetoThreshold is the remaining-percentage (0-100) at or below which a
	// window is treated as exhausted. 0 (default) means only a fully drained
	// window vetoes; raise it to preempt in-flight 429s near the end of a window.
	VetoThreshold float64

	// StaleAfter is the age at which a snapshot stops being trusted for veto
	// decisions (the key falls open). Defaults to 3x PollInterval.
	StaleAfter time.Duration

	// HTTPTimeout bounds a single quota API call.
	HTTPTimeout time.Duration

	// TrackedHosts is the exact set of base-URL hosts (lowercase) whose keys are
	// tracked. Empty means the builtin GLM hosts. Unknown hosts are never polled.
	TrackedHosts []string

	// QuotaURL maps a tracked base-URL host to the provider's quota endpoint.
	// Nil falls back to the builtin GLM mapping. The bool reports whether the
	// host is known — an unrecognized tracked host is skipped with a warning.
	QuotaURL func(host string) (string, bool)

	// Fetch performs one authenticated quota GET and returns the raw body plus
	// the HTTP status code. Nil falls back to the default net/http fetch.
	// Injectable so tests can run without a network.
	Fetch func(ctx context.Context, url, apiKey string) (body []byte, statusCode int, err error)

	// Now is the clock used for staleness and reset-window decisions. Nil means
	// time.Now.
	Now func() time.Time
}

// TrackedKey is one provider credential the tracker should poll.
type TrackedKey struct {
	ID    string
	Name  string
	Value string // plaintext credential; empty values are skipped
}

// ProviderKeys groups the tracked keys of one provider along with the base URL
// used to decide whether the provider is quota-tracked at all.
type ProviderKeys struct {
	Provider string
	BaseURL  string
	Keys     []TrackedKey
}

// KeySource returns the current provider/key inventory. It is called once per
// poll tick so config reloads (keys added/removed) are picked up without a
// restart; implementations must be safe for concurrent calls.
type KeySource func() []ProviderKeys

// KeyRef identifies a tracked key.
type KeyRef struct {
	Provider string
	KeyID    string
}

// WindowState is one quota window (session, weekly, tokens, ...) of a snapshot.
type WindowState struct {
	// WindowKey is a stable lowercase label: "session" (or "session_<n>h" when
	// the session length is not the conventional 5h), "weekly", "tokens", or
	// "limit_<n>" for unknown credit-limit units.
	WindowKey string

	// UsedPercent / RemainingPercent are the display percentages. GLM reports
	// usage as a percentage of the window, never raw counts.
	UsedPercent      float64
	RemainingPercent float64

	// ResetAt is when the window rolls over; zero when the provider did not
	// report a reset time.
	ResetAt time.Time
}

// Fetch status codes for observability. Aliases over the cross-plugin
// contract in core/schemas (the otel plugin renders these as gauges without
// importing this module).
const (
	StatusOK       = schemas.QuotaFetchOK
	StatusAuth     = schemas.QuotaFetchAuth
	StatusHTTP     = schemas.QuotaFetchHTTP
	StatusNetwork  = schemas.QuotaFetchNetwork
	StatusParse    = schemas.QuotaFetchParse
	StatusNotFound = schemas.QuotaFetchNotFound
	StatusStale    = schemas.QuotaFetchStale
)

// SnapshotView is the per-window projection the tracker hands to observers
// (otel gauges, tests). Alias over the shared contract type so tracker code
// and tests read naturally.
type SnapshotView = schemas.QuotaSnapshotView

// KeySnapshot is the parsed result of one poll of one key.
type KeySnapshot struct {
	Key     KeyRef
	Plan    string
	Windows []WindowState

	FetchedAt time.Time

	// Status is the fetch outcome; anything other than StatusOK means the
	// snapshot carries no authoritative window data and vetoes fail open.
	Status schemas.QuotaFetchStatus
}
