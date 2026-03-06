package mixnet

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsExporter exposes Mixnet metrics via Prometheus.
type MetricsExporter struct {
	metrics  *MetricsCollector
	registry *prometheus.Registry
	handler  http.Handler
	once     sync.Once
}

// NewMetricsExporter creates a Prometheus exporter for the provided MetricsCollector.
func NewMetricsExporter(metrics *MetricsCollector) *MetricsExporter {
	exporter := &MetricsExporter{
		metrics:  metrics,
		registry: prometheus.NewRegistry(),
	}
	exporter.registerCollectors()
	exporter.handler = promhttp.HandlerFor(exporter.registry, promhttp.HandlerOpts{})
	return exporter
}

func (e *MetricsExporter) registerCollectors() {
	e.once.Do(func() {
		e.registry.MustRegister(
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{
				Name: "mixnet_avg_rtt_seconds",
				Help: "Average RTT across all active circuits.",
			}, func() float64 {
				return e.metrics.AverageRTT().Seconds()
			}),
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{
				Name: "mixnet_circuits_active",
				Help: "Number of active circuits.",
			}, func() float64 {
				return float64(e.metrics.ActiveCircuits())
			}),
			prometheus.NewCounterFunc(prometheus.CounterOpts{
				Name: "mixnet_circuit_success_total",
				Help: "Total number of successfully established circuits.",
			}, func() float64 {
				return float64(e.metrics.CircuitSuccesses())
			}),
			prometheus.NewCounterFunc(prometheus.CounterOpts{
				Name: "mixnet_circuit_failure_total",
				Help: "Total number of failed circuit establishments.",
			}, func() float64 {
				return float64(e.metrics.CircuitFailures())
			}),
			prometheus.NewCounterFunc(prometheus.CounterOpts{
				Name: "mixnet_recovery_events_total",
				Help: "Total number of circuit recovery events.",
			}, func() float64 {
				return float64(e.metrics.RecoveryEvents())
			}),
			prometheus.NewCounterFunc(prometheus.CounterOpts{
				Name: "mixnet_throughput_bytes_total",
				Help: "Total bytes transmitted through mixnet.",
			}, func() float64 {
				return float64(e.metrics.TotalThroughput())
			}),
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{
				Name: "mixnet_compression_ratio",
				Help: "Average compression ratio (compressed/original).",
			}, func() float64 {
				return e.metrics.CompressionRatio()
			}),
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{
				Name: "mixnet_resource_utilization_percent",
				Help: "Current resource utilization percentage.",
			}, func() float64 {
				return e.metrics.CurrentResourceUtilization()
			}),
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{
				Name: "mixnet_resource_utilization_max_percent",
				Help: "Peak resource utilization percentage.",
			}, func() float64 {
				return e.metrics.MaxResourceUtilization()
			}),
		)
	})
}

// Handler returns the HTTP handler that serves Prometheus metrics.
func (e *MetricsExporter) Handler() http.Handler {
	return e.handler
}

// Start starts an HTTP server exposing the metrics at /metrics.
func (e *MetricsExporter) Start(addr string) error {
	if addr == "" {
		return fmt.Errorf("metrics address is empty")
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", e.handler)
	return http.ListenAndServe(addr, mux)
}

// MetricsHandler returns the HTTP handler for Prometheus metrics, if configured.
func (m *Mixnet) MetricsHandler() http.Handler {
	if m == nil || m.metricsExporter == nil {
		return nil
	}
	return m.metricsExporter.Handler()
}

// StartMetricsEndpoint starts an HTTP server exposing Prometheus metrics at /metrics.
func (m *Mixnet) StartMetricsEndpoint(addr string) error {
	if m == nil || m.metricsExporter == nil {
		return fmt.Errorf("metrics exporter not configured")
	}
	return m.metricsExporter.Start(addr)
}
