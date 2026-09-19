package otel

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// fakeQuotaSource replays canned snapshots into the gauges.
type fakeQuotaSource struct{ views []schemas.QuotaSnapshotView }

func (f *fakeQuotaSource) VisitSnapshots(fn func(view schemas.QuotaSnapshotView)) {
	for _, v := range f.views {
		fn(v)
	}
}

// gaugeTestExporter builds a MetricsExporter over an in-memory meter with a
// manual reader, bypassing the OTLP exporter entirely.
func gaugeTestExporter(t *testing.T) (*MetricsExporter, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	m := &MetricsExporter{
		provider: provider,
		meter:    provider.Meter("quotagauges-test"),
	}
	return m, reader
}

// TestQuotaGaugesReportFromSource pins the scrape path: SetQuotaSource feeds
// the registered observable gauges, one series per (key, window), carrying
// the remaining percentage, reset stamp and veto flag.
func TestQuotaGaugesReportFromSource(t *testing.T) {
	SetQuotaSource(&fakeQuotaSource{views: []schemas.QuotaSnapshotView{
		{
			Provider: "zai", KeyID: "k1", KeyName: "glm-main", Plan: "Pro",
			Window: "session", RemainingPercent: 75, ResetAtUnix: 1787905548,
			Vetoed: false, Status: schemas.QuotaFetchOK,
		},
		{
			Provider: "zai", KeyID: "k2", KeyName: "glm-drained", Plan: "Pro",
			Window: "session", RemainingPercent: 0, ResetAtUnix: 1788492142,
			Vetoed: true, Status: schemas.QuotaFetchOK,
		},
	}})
	t.Cleanup(func() { SetQuotaSource(nil) })

	m, reader := gaugeTestExporter(t)
	m.initQuotaGauges(bifrost.NewNoOpLogger())

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	type series struct {
		value    float64
		attrs    map[string]string
		observed bool
	}
	gauges := map[string]map[string]*series{}
	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			gauge, ok := metric.Data.(metricdata.Gauge[float64])
			require.True(t, ok, "unexpected metric data type for %s", metric.Name)
			for _, dp := range gauge.DataPoints {
				attrs := map[string]string{}
				for _, kv := range dp.Attributes.ToSlice() {
					attrs[string(kv.Key)] = kv.Value.Emit()
				}
				id := metric.Name + "|" + attrs["key_id"] + "|" + attrs["window"]
				if gauges[metric.Name] == nil {
					gauges[metric.Name] = map[string]*series{}
				}
				gauges[metric.Name][id] = &series{value: dp.Value, attrs: attrs, observed: true}
			}
		}
	}

	require.Len(t, gauges["bifrost_provider_quota_remaining_percent"], 2)

	healthy := gauges["bifrost_provider_quota_remaining_percent"]["bifrost_provider_quota_remaining_percent|k1|session"]
	require.NotNil(t, healthy)
	assert.Equal(t, 75.0, healthy.value)
	assert.Equal(t, "Pro", healthy.attrs["plan"])
	assert.Equal(t, "0", healthy.attrs["status"])

	drained := gauges["bifrost_provider_quota_remaining_percent"]["bifrost_provider_quota_remaining_percent|k2|session"]
	require.NotNil(t, drained)
	assert.Equal(t, 0.0, drained.value)

	reset := gauges["bifrost_provider_quota_reset_timestamp"]["bifrost_provider_quota_reset_timestamp|k1|session"]
	require.NotNil(t, reset)
	assert.Equal(t, 1787905548.0, reset.value)

	veto := gauges["bifrost_provider_quota_veto_active"]
	assert.Equal(t, 0.0, veto["bifrost_provider_quota_veto_active|k1|session"].value)
	assert.Equal(t, 1.0, veto["bifrost_provider_quota_veto_active|k2|session"].value)
}

// TestQuotaGaugesSilentWithoutSource: no installed tracker means no quota
// series — the gauges must not fabricate points.
func TestQuotaGaugesSilentWithoutSource(t *testing.T) {
	SetQuotaSource(nil)

	m, reader := gaugeTestExporter(t)
	m.initQuotaGauges(bifrost.NewNoOpLogger())

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			// Only the semconv http metric is dotted; everything else here is
			// ours and must be absent.
			assert.NotContains(t, metric.Name, "bifrost_provider_quota_", "quota gauges must stay silent without a source")
		}
	}
}
