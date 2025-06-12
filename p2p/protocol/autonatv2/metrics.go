package autonatv2

import (
	"strconv"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/metricshelper"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/prometheus/client_golang/prometheus"
)

type MetricsTracer interface {
	CompletedRequest(EventDialRequestCompleted)
	ClientCompletedRequest([]Request, Result, error)
}

const metricNamespace = "libp2p_autonatv2"

var (
	requestsCompleted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "requests_completed_total",
			Help:      "Requests Completed",
		},
		[]string{"server_error", "response_status", "dial_status", "dial_data_required", "ip_or_dns_version", "transport"},
	)
	clientRequestsCompleted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "client_requests_completed_total",
			Help:      "Client Requests Completed",
		},
		[]string{"ip_or_dns_version", "transport", "addr_count", "dial_refused", "reachability"},
	)
	clientRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "client_requests_total",
			Help:      "Client Requests Total",
		},
		[]string{"outcome"},
	)
)

type metricsTracer struct {
}

func NewMetricsTracer(reg prometheus.Registerer) MetricsTracer {
	metricshelper.RegisterCollectors(reg, requestsCompleted, clientRequestsCompleted, clientRequestsTotal)
	return &metricsTracer{}
}

func (m *metricsTracer) CompletedRequest(e EventDialRequestCompleted) {
	labels := metricshelper.GetStringSlice()
	defer metricshelper.PutStringSlice(labels)

	errStr := getErrString(e.Error)

	dialData := "false"
	if e.DialDataRequired {
		dialData = "true"
	}

	var ip, transport string
	if e.DialedAddr != nil {
		ip = getIPOrDNSVersion(e.DialedAddr)
		transport = metricshelper.GetTransport(e.DialedAddr)
	}

	*labels = append(*labels,
		errStr,
		pb.DialResponse_ResponseStatus_name[int32(e.ResponseStatus)],
		pb.DialStatus_name[int32(e.DialStatus)],
		dialData,
		ip,
		transport,
	)
	requestsCompleted.WithLabelValues(*labels...).Inc()
}

func (m *metricsTracer) ClientCompletedRequest(reqs []Request, result Result, err error) {
	labels := metricshelper.GetStringSlice()
	defer metricshelper.PutStringSlice(labels)

	if err != nil {
		clientRequestsTotal.WithLabelValues("failure").Inc()
		return
	}
	clientRequestsTotal.WithLabelValues("success").Inc()

	addrCount := len(reqs)
	dialRefused := "false"
	if result.AllAddrsRefused {
		dialRefused = "true"
	}
	reachability := "unknown"
	switch result.Reachability {
	case network.ReachabilityPublic:
		reachability = "public"
	case network.ReachabilityPrivate:
		reachability = "private"
	}

	ipOrDNSVersion := "unknown"
	transport := "unknown"
	if result.Addr != nil {
		ipOrDNSVersion = getIPOrDNSVersion(result.Addr)
		transport = metricshelper.GetTransport(result.Addr)
	}

	*labels = append(*labels,
		ipOrDNSVersion,
		transport,
		strconv.Itoa(addrCount),
		dialRefused,
		reachability,
	)
	clientRequestsCompleted.WithLabelValues(*labels...).Inc()
}

func getIPOrDNSVersion(a ma.Multiaddr) string {
	if len(a) == 0 {
		return ""
	}
	res := "unknown"
	switch a[0].Protocol().Code {
	case ma.P_DNS, ma.P_DNSADDR:
		res = "dns"
	case ma.P_DNS4:
		res = "dns4"
	case ma.P_DNS6:
		res = "dns6"
	case ma.P_IP4:
		res = "ip4"
	case ma.P_IP6:
		res = "ip6"
	}
	return res
}

func getErrString(e error) string {
	var errStr string
	switch e {
	case nil:
		errStr = "nil"
	case errBadRequest, errDialDataRefused, errResourceLimitExceeded:
		errStr = e.Error()
	default:
		errStr = "other"
	}
	return errStr
}
