package schemas

// Cross-plugin contract for provider-side subscription quota tracking. The
// quota tracker (plugins/quotatracker) polls provider quota endpoints (e.g.
// the GLM coding plan's 5h session / weekly windows) and projects each result
// into QuotaSnapshotView; the otel plugin renders these as
// bifrost_provider_quota_* observable gauges. The type lives here so the two
// plugins share it without importing each other — a module edge between
// fork-local plugins breaks `go work sync` inside the fork's Docker build.
//
// Compile-time contract only: nothing here is wire-visible.

// QuotaFetchStatus describes the outcome of one quota poll. Bounded set —
// safe as a metric label value.
type QuotaFetchStatus int

const (
	QuotaFetchOK       QuotaFetchStatus = 0
	QuotaFetchAuth     QuotaFetchStatus = 1 // 401: credential rejected by the quota API
	QuotaFetchHTTP     QuotaFetchStatus = 2 // any other non-200
	QuotaFetchNetwork  QuotaFetchStatus = 3 // transport-level failure
	QuotaFetchParse    QuotaFetchStatus = 4 // 200 but the body could not be parsed
	QuotaFetchNotFound QuotaFetchStatus = 5 // no snapshot (never fetched or key left the source)
	QuotaFetchStale    QuotaFetchStatus = 6 // snapshot exists but is older than the tracker's staleness window
)

// QuotaSnapshotView is the per-window projection the tracker hands to
// observers. One key with N quota windows yields N views; a key whose last
// fetch failed yields one status-only view (no windows).
type QuotaSnapshotView struct {
	Provider string
	KeyID    string
	KeyName  string
	Plan     string // capitalized plan level: "Lite", "Pro", ... or "Unknown"

	// Window is a stable lowercase label: "session" (or "session_<n>h" when
	// the session length is not the conventional 5h), "weekly", "tokens",
	// "limit_<n>", or "none" for status-only views.
	Window string

	// RemainingPercent is 100 minus the provider-reported used percentage.
	RemainingPercent float64

	// ResetAtUnix is the window reset time as unix seconds; 0 when the
	// provider did not report one.
	ResetAtUnix float64

	// Vetoed reports whether the tracker currently excludes this key from
	// routing because of this (or another) exhausted window.
	Vetoed bool

	// Status is one of the QuotaFetch* constants describing snapshot freshness.
	Status QuotaFetchStatus
}

// QuotaSnapshotSource is the read side of the tracker, as consumed by the
// otel plugin's quota gauges. Implemented by *quotatracker.Tracker.
type QuotaSnapshotSource interface {
	VisitSnapshots(fn func(view QuotaSnapshotView))
}
