package basichost

import (
	"maps"

	"github.com/libp2p/go-libp2p/p2p/metricshelper"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/prometheus/client_golang/prometheus"
)

const metricNamespace = "libp2p_host_addrs"

var (
	reachableAddrs = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "reachable",
			Help:      "Number of reachable addresses by transport",
		},
		[]string{"ipv", "transport"},
	)
	unreachableAddrs = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "unreachable",
			Help:      "Number of unreachable addresses by transport",
		},
		[]string{"ipv", "transport"},
	)
	unknownAddrs = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "unknown",
			Help:      "Number of addresses with unknown reachability by transport",
		},
		[]string{"ipv", "transport"},
	)
	collectors = []prometheus.Collector{
		reachableAddrs,
		unreachableAddrs,
		unknownAddrs,
	}
)

// MetricsTracker tracks autonatv2 reachability metrics
type MetricsTracker interface {
	// ConfirmedAddrsChanged updates metrics with current address reachability status
	ConfirmedAddrsChanged(reachable, unreachable, unknown []ma.Multiaddr)
	// ReachabilityTrackerClosed updated metrics on host close
	ReachabilityTrackerClosed()
}

type metricsTracker struct {
	prevReachableCounts   map[metricKey]int
	prevUnreachableCounts map[metricKey]int
	prevUnknownCounts     map[metricKey]int
	currentReachable      map[metricKey]int
	currentUnreachable    map[metricKey]int
	currentUnknown        map[metricKey]int
}

var _ MetricsTracker = &metricsTracker{}

type metricsTrackerSetting struct {
	reg prometheus.Registerer
}

type metricsTrackerOption func(*metricsTrackerSetting)

// withRegisterer sets the prometheus registerer for the metrics
func withRegisterer(reg prometheus.Registerer) metricsTrackerOption {
	return func(s *metricsTrackerSetting) {
		if reg != nil {
			s.reg = reg
		}
	}
}

type metricKey struct {
	ipv       string
	transport string
}

// newMetricsTracker creates a new metrics tracker for autonatv2
func newMetricsTracker(opts ...metricsTrackerOption) MetricsTracker {
	setting := &metricsTrackerSetting{reg: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		opt(setting)
	}
	metricshelper.RegisterCollectors(setting.reg, collectors...)
	return &metricsTracker{
		prevReachableCounts:   make(map[metricKey]int),
		prevUnreachableCounts: make(map[metricKey]int),
		prevUnknownCounts:     make(map[metricKey]int),
		currentReachable:      make(map[metricKey]int),
		currentUnreachable:    make(map[metricKey]int),
		currentUnknown:        make(map[metricKey]int),
	}
}

func (t *metricsTracker) ReachabilityTrackerClosed() {
	resetMetric(reachableAddrs, t.currentReachable, t.prevReachableCounts)
	resetMetric(unreachableAddrs, t.currentUnreachable, t.prevUnreachableCounts)
	resetMetric(unknownAddrs, t.currentUnknown, t.prevUnknownCounts)
}

// ConfirmedAddrsChanged updates the metrics with current address reachability counts by transport
func (t *metricsTracker) ConfirmedAddrsChanged(reachable, unreachable, unknown []ma.Multiaddr) {
	updateMetric(reachableAddrs, reachable, t.currentReachable, t.prevReachableCounts)
	updateMetric(unreachableAddrs, unreachable, t.currentUnreachable, t.prevUnreachableCounts)
	updateMetric(unknownAddrs, unknown, t.currentUnknown, t.prevUnknownCounts)
}

func updateMetric(metric *prometheus.GaugeVec, addrs []ma.Multiaddr, current map[metricKey]int, prev map[metricKey]int) {
	clear(prev)
	maps.Copy(prev, current)
	clear(current)
	for _, addr := range addrs {
		transport := metricshelper.GetTransport(addr)
		ipv := metricshelper.GetIPVersion(addr)
		key := metricKey{ipv: ipv, transport: transport}
		current[key]++
	}

	tags := metricshelper.GetStringSlice()
	defer metricshelper.PutStringSlice(tags)

	for k, v := range current {
		*tags = append(*tags, k.ipv, k.transport)
		metric.WithLabelValues(*tags...).Set(float64(v))
		*tags = (*tags)[:0]
	}
	for k := range prev {
		if _, ok := current[k]; ok {
			continue
		}
		*tags = append(*tags, k.ipv, k.transport)
		metric.WithLabelValues(*tags...).Set(0)
		*tags = (*tags)[:0]
	}
}

func resetMetric(metric *prometheus.GaugeVec, current map[metricKey]int, prev map[metricKey]int) {
	tags := metricshelper.GetStringSlice()
	defer metricshelper.PutStringSlice(tags)
	for k := range current {
		*tags = append(*tags, k.ipv, k.transport)
		metric.WithLabelValues(*tags...).Set(0)
		*tags = (*tags)[:0]
	}
	clear(current)
	clear(prev)
}
