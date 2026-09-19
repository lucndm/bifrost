package otel

import (
	"context"
	"sync/atomic"

	"github.com/maximhq/bifrost/core/schemas"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Quota metric names. These expose provider-side subscription quota windows
// (e.g. the GLM coding plan's 5h session and weekly limits) so operators can
// watch and alert on them from Grafana instead of discovering exhaustion
// through client-facing 429s.
const (
	quotaRemainingGauge = "bifrost_provider_quota_remaining_percent"
	quotaResetGauge     = "bifrost_provider_quota_reset_timestamp"
	quotaVetoGauge      = "bifrost_provider_quota_veto_active"
)

// quotaSource holds the process-wide tracker. The transport installs it once
// at bootstrap; otel plugin re-inits (config reloads) pick it up because the
// gauges' callback reads it at scrape time instead of capturing it. The view
// type is schemas.QuotaSnapshotView — a cross-plugin contract, so otel keeps
// no module dependency on the tracker itself.
// quotaSourceBox wraps the interface so a nil tracker can be stored
// (atomic.Value rejects a bare nil interface store).
type quotaSourceBox struct{ src schemas.QuotaSnapshotSource }

var quotaSource atomic.Pointer[quotaSourceBox]

// SetQuotaSource installs the tracker whose snapshots feed the quota gauges.
// Safe to call before or after the otel plugin is initialized — a nil source
// simply means the gauges report nothing for that interval.
func SetQuotaSource(src schemas.QuotaSnapshotSource) {
	if src == nil {
		quotaSource.Store(nil)
		return
	}
	quotaSource.Store(&quotaSourceBox{src: src})
}

// loadQuotaSource returns the installed tracker, or nil.
func loadQuotaSource() schemas.QuotaSnapshotSource {
	if box := quotaSource.Load(); box != nil {
		return box.src
	}
	return nil
}

// initQuotaGauges registers the observable quota gauges on the exporter's
// meter. The callback pulls from the package-level source so registration
// order (tracker vs plugin) never matters.
// initQuotaGauges is called by the plugin after NewMetricsExporter with the
// plugin logger.
func (m *MetricsExporter) initQuotaGauges(logger schemas.Logger) {
	remaining, err := m.meter.Float64ObservableGauge(quotaRemainingGauge,
		metric.WithDescription("Remaining percentage of each provider-side subscription quota window, as reported by the provider quota tracker"),
		metric.WithUnit("1"),
	)
	if err != nil {
		logQuotaError(logger, "failed to create quota remaining gauge: %v", err)
		return
	}
	reset, err := m.meter.Float64ObservableGauge(quotaResetGauge,
		metric.WithDescription("Unix timestamp (seconds) when the provider-side quota window resets; 0 when the provider did not report one"),
		metric.WithUnit("s"),
	)
	if err != nil {
		logQuotaError(logger, "failed to create quota reset gauge: %v", err)
		return
	}
	veto, err := m.meter.Float64ObservableGauge(quotaVetoGauge,
		metric.WithDescription("1 when the quota tracker currently vetoes the key from routing (window exhausted), 0 otherwise"),
		metric.WithUnit("1"),
	)
	if err != nil {
		logQuotaError(logger, "failed to create quota veto gauge: %v", err)
		return
	}

	if _, err := m.meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		src := loadQuotaSource()
		if src == nil {
			return nil
		}
		src.VisitSnapshots(func(view schemas.QuotaSnapshotView) {
			attrs := metric.WithAttributeSet(attribute.NewSet(
				attribute.String("provider", view.Provider),
				attribute.String("key_id", view.KeyID),
				attribute.String("key_name", view.KeyName),
				attribute.String("window", view.Window),
				attribute.String("plan", view.Plan),
				attribute.Int("status", int(view.Status)),
			))
			observer.ObserveFloat64(remaining, view.RemainingPercent, attrs)
			observer.ObserveFloat64(reset, view.ResetAtUnix, attrs)
			if view.Vetoed {
				observer.ObserveFloat64(veto, 1, attrs)
			} else {
				observer.ObserveFloat64(veto, 0, attrs)
			}
		})
		return nil
	}, remaining, reset, veto); err != nil {
		logQuotaError(logger, "failed to register quota gauge callback: %v", err)
	}
}

// logQuotaError routes through the plugin's logger when available.
func logQuotaError(logger schemas.Logger, format string, args ...any) {
	if logger != nil {
		logger.Error(format, args...)
	}
}
