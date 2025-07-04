package rcmgr

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/prometheus/client_golang/prometheus"
)

// ResourceMonitor tracks resource usage and reports potential issues
type ResourceMonitor struct {
	sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc

	scope *resourceScope
	metrics *metrics

	// Prometheus metrics
	memoryUsageGauge prometheus.Gauge
	fdUsageGauge     prometheus.Gauge
	connCountGauge   *prometheus.GaugeVec
	streamCountGauge *prometheus.GaugeVec
	resourceErrors   *prometheus.CounterVec
}

// NewResourceMonitor creates a new resource monitor
func NewResourceMonitor(scope *resourceScope, metrics *metrics) *ResourceMonitor {
	ctx, cancel := context.WithCancel(context.Background())
	
	rm := &ResourceMonitor{
		ctx:    ctx,
		cancel: cancel,
		scope:  scope,
		metrics: metrics,

		memoryUsageGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "libp2p",
			Subsystem: "rcmgr",
			Name:      "memory_usage_bytes",
			Help:      "Current memory usage in bytes",
		}),

		fdUsageGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "libp2p",
			Subsystem: "rcmgr",
			Name:      "fd_usage_count",
			Help:      "Current file descriptor usage count",
		}),

		connCountGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "libp2p",
			Subsystem: "rcmgr",
			Name:      "connection_count",
			Help:      "Current connection count",
		}, []string{"direction"}),

		streamCountGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "libp2p",
			Subsystem: "rcmgr",
			Name:      "stream_count",
			Help:      "Current stream count",
		}, []string{"direction"}),

		resourceErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "libp2p",
			Subsystem: "rcmgr",
			Name:      "resource_errors_total",
			Help:      "Total number of resource management errors",
		}, []string{"type"}),
	}

	// Register metrics
	prometheus.MustRegister(
		rm.memoryUsageGauge,
		rm.fdUsageGauge,
		rm.connCountGauge,
		rm.streamCountGauge,
		rm.resourceErrors,
	)

	go rm.monitorLoop()
	return rm
}

func (rm *ResourceMonitor) monitorLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-rm.ctx.Done():
			return
		case <-ticker.C:
			rm.updateMetrics()
			rm.checkThresholds()
		}
	}
}

func (rm *ResourceMonitor) updateMetrics() {
	stat := rm.scope.Stat()

	rm.memoryUsageGauge.Set(float64(stat.Memory))
	rm.fdUsageGauge.Set(float64(stat.NumFD))

	rm.connCountGauge.WithLabelValues("inbound").Set(float64(stat.NumConnsInbound))
	rm.connCountGauge.WithLabelValues("outbound").Set(float64(stat.NumConnsOutbound))

	rm.streamCountGauge.WithLabelValues("inbound").Set(float64(stat.NumStreamsInbound))
	rm.streamCountGauge.WithLabelValues("outbound").Set(float64(stat.NumStreamsOutbound))
}

func (rm *ResourceMonitor) checkThresholds() {
	stat := rm.scope.Stat()
	limit := rm.scope.rc.limit

	// Check memory usage (80% threshold)
	memLimit := limit.GetMemoryLimit()
	if memLimit > 0 && float64(stat.Memory) > float64(memLimit)*0.8 {
		log.Warnw("High memory usage",
			"current", stat.Memory,
			"limit", memLimit,
			"usage_percent", float64(stat.Memory)/float64(memLimit)*100)
		rm.resourceErrors.WithLabelValues("memory_near_limit").Inc()
	}

	// Check FD usage (80% threshold)
	fdLimit := limit.GetFDLimit()
	if fdLimit > 0 && float64(stat.NumFD) > float64(fdLimit)*0.8 {
		log.Warnw("High file descriptor usage",
			"current", stat.NumFD,
			"limit", fdLimit,
			"usage_percent", float64(stat.NumFD)/float64(fdLimit)*100)
		rm.resourceErrors.WithLabelValues("fd_near_limit").Inc()
	}

	// Check connection counts
	connLimit := limit.GetConnTotalLimit()
	totalConns := stat.NumConnsInbound + stat.NumConnsOutbound
	if connLimit > 0 && float64(totalConns) > float64(connLimit)*0.8 {
		log.Warnw("High connection count",
			"current", totalConns,
			"limit", connLimit,
			"usage_percent", float64(totalConns)/float64(connLimit)*100)
		rm.resourceErrors.WithLabelValues("conn_near_limit").Inc()
	}
}

// Close stops the resource monitor and unregisters metrics
func (rm *ResourceMonitor) Close() error {
	rm.Lock()
	defer rm.Unlock()

	if rm.ctx.Err() != nil {
		// Already closed
		return nil
	}

	rm.cancel()

	// Unregister all metrics to prevent memory leaks
	prometheus.Unregister(rm.memoryUsageGauge)
	prometheus.Unregister(rm.fdUsageGauge)
	prometheus.Unregister(rm.connCountGauge)
	prometheus.Unregister(rm.streamCountGauge)
	prometheus.Unregister(rm.resourceErrors)

	return nil
}

// Reset resets all metrics to zero
func (rm *ResourceMonitor) Reset() {
	rm.Lock()
	defer rm.Unlock()

	rm.memoryUsageGauge.Set(0)
	rm.fdUsageGauge.Set(0)
	rm.connCountGauge.Reset()
	rm.streamCountGauge.Reset()
}

// ReportError allows external components to report resource-related errors
func (rm *ResourceMonitor) ReportError(errorType string) {
	rm.resourceErrors.WithLabelValues(errorType).Inc()
} 