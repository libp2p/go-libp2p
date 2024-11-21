package metricsserver

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2phttp "github.com/libp2p/go-libp2p/p2p/http"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"
)

const ProtocolID = "/libp2p/exp/metrics/0.0.1"

var log = logging.Logger("libp2p/exp/metrics-server")

func WithMetricsServer(opts []libp2p.Option, allowedPeers []peer.ID) []libp2p.Option {
	opts = append(opts, libp2p.WithFxOption(
		fx.Provide(func(params constructorParams) *MetricsServer {

			var ownHTTPHost bool
			httpHost := params.HTTPHost
			if httpHost == nil {
				// No http host available. We'll make our own
				ownHTTPHost = true
				httpHost = &libp2phttp.Host{
					StreamHost: params.StreamHost,
				}
			}

			if httpHost.StreamHost == nil {
				log.Warn("No StreamHost set for the MetricsServer's HTTP host. MetricsServer will not be able to serve HTTP requests over libp2p streams.")
			}

			m := NewMetricsServer(params.Registerer, params.Gatherer)
			m.AllowedPeers = allowedPeers

			params.L.Append(fx.StartStopHook(func() error {
				err := m.Start(httpHost)
				if err != nil {
					return err
				}

				if ownHTTPHost {
					go httpHost.Serve()
				}
				return nil
			}, func() error {
				return m.Close()
			}))

			return m
		},
		),
		// We want the metrics server started even if nothing else depends on it.
		fx.Invoke(func(m *MetricsServer) { _ = m })))
	return opts
}

type MetricsServer struct {
	AllowedPeers []peer.ID
	handler      http.Handler
	closed       chan struct{}
}

func NewMetricsServer(r prometheus.Registerer, g prometheus.Gatherer) *MetricsServer {
	if r == nil {
		r = prometheus.DefaultRegisterer
	}
	if g == nil {
		g = prometheus.DefaultGatherer
	}

	m := &MetricsServer{
		handler: promhttp.InstrumentMetricHandler(r, promhttp.HandlerFor(g, promhttp.HandlerOpts{})),
		closed:  make(chan struct{}),
	}
	return m
}

func (m *MetricsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case <-m.closed:
		http.Error(w, "server closed", http.StatusServiceUnavailable)
	default:
	}

	client := libp2phttp.ClientPeerID(r)
	if client == "" {
		http.Error(w, "no client peer ID", http.StatusForbidden)
	}
	var found bool
	for _, allowedPeer := range m.AllowedPeers {
		if client == allowedPeer {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "not allowed", http.StatusForbidden)
	}

	m.handler.ServeHTTP(w, r)
}

func (m *MetricsServer) Start(httpHost *libp2phttp.Host) error {
	m.closed = make(chan struct{})
	httpHost.SetHTTPHandler(ProtocolID, m)

	if len(m.AllowedPeers) == 0 {
		log.Info("No allowed peers specified. Generting a key for a single peer.")

		sk, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return err
		}
		skBytes, err := sk.Raw()
		if err != nil {
			return err
		}
		skAsHex := hex.EncodeToString(skBytes)

		m.AllowedPeers = make([]peer.ID, 1)
		m.AllowedPeers[0], err = peer.IDFromPrivateKey(sk)
		if err != nil {
			return err
		}
		log.Info("Metrics server started. Login using key %s", skAsHex)
	}

	return nil
}

func (m *MetricsServer) Close() error {
	log.Info("Metrics server closed.")
	m.AllowedPeers = nil
	return nil
}

type constructorParams struct {
	fx.In
	L          fx.Lifecycle
	StreamHost host.Host
	HTTPHost   *libp2phttp.Host      `optional:"true"`
	Registerer prometheus.Registerer `optional:"true"`
	Gatherer   prometheus.Gatherer   `optional:"true"`
}
