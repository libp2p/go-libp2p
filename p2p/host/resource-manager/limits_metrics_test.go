package rcmgr

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestReportSystemLimits(t *testing.T) {
	// Register the metrics
	reg := prometheus.NewRegistry()
	reg.MustRegister(limits)

	// Create a simple limiter with known limits
	limiter := NewFixedLimiter(ConcreteLimitConfig{
		system: BaseLimit{
			Memory:          1024 * 1024 * 1024, // 1GB
			FD:              256,
			Conns:           100,
			ConnsInbound:    50,
			ConnsOutbound:   50,
			Streams:         200,
			StreamsInbound:  100,
			StreamsOutbound: 100,
		},
		transient: BaseLimit{
			Memory:          512 * 1024 * 1024, // 512MB
			FD:              128,
			Conns:           50,
			ConnsInbound:    25,
			ConnsOutbound:   25,
			Streams:         100,
			StreamsInbound:  50,
			StreamsOutbound: 50,
		},
	})

	// Create a stats reporter
	reporter, err := NewStatsTraceReporter()
	if err != nil {
		t.Fatal(err)
	}

	// Report the limits
	reporter.ReportSystemLimits(limiter)

	// Verify that metrics were set
	metrics, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	// Find the limits metric
	var limitsMetric *dto.MetricFamily
	for _, m := range metrics {
		if m.GetName() == "libp2p_rcmgr_limit" {
			limitsMetric = m
			break
		}
	}

	if limitsMetric == nil {
		t.Fatal("limits metric not found")
	}

	// Verify we have metrics for both system and transient scopes
	foundSystem := false
	foundTransient := false
	for _, metric := range limitsMetric.GetMetric() {
		for _, label := range metric.GetLabel() {
			if label.GetName() == "scope" {
				if label.GetValue() == "system" {
					foundSystem = true
				}
				if label.GetValue() == "transient" {
					foundTransient = true
				}
			}
		}
	}

	if !foundSystem {
		t.Error("system scope limits not reported")
	}
	if !foundTransient {
		t.Error("transient scope limits not reported")
	}

	// Verify specific limit values
	expectedLimits := map[string]map[string]float64{
		"system": {
			"memory":           1024 * 1024 * 1024,
			"fd":               256,
			"conns":            100,
			"conns_inbound":    50,
			"conns_outbound":   50,
			"streams":          200,
			"streams_inbound":  100,
			"streams_outbound": 100,
		},
		"transient": {
			"memory":           512 * 1024 * 1024,
			"fd":               128,
			"conns":            50,
			"conns_inbound":    25,
			"conns_outbound":   25,
			"streams":          100,
			"streams_inbound":  50,
			"streams_outbound": 50,
		},
	}

	for _, metric := range limitsMetric.GetMetric() {
		var scope, resource string
		for _, label := range metric.GetLabel() {
			if label.GetName() == "scope" {
				scope = label.GetValue()
			}
			if label.GetName() == "resource" {
				resource = label.GetValue()
			}
		}

		if scope == "" || resource == "" {
			continue
		}

		expectedValue, ok := expectedLimits[scope][resource]
		if !ok {
			continue
		}

		actualValue := metric.GetGauge().GetValue()
		if actualValue != expectedValue {
			t.Errorf("limit mismatch for %s/%s: expected %v, got %v", scope, resource, expectedValue, actualValue)
		}
	}
}

func TestReportSystemLimitsNilLimiter(t *testing.T) {
	reporter, err := NewStatsTraceReporter()
	if err != nil {
		t.Fatal(err)
	}

	// Should not panic with nil limiter
	reporter.ReportSystemLimits(nil)
}
