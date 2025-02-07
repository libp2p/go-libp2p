package autorelay

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"

	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger("autorelay")

type AutoRelay struct {
	refCount  sync.WaitGroup
	ctx       context.Context
	ctxCancel context.CancelFunc

	mx     sync.Mutex
	status network.Reachability

	relayFinder *relayFinder

	host host.Host

	metricsTracer MetricsTracer
}

func NewAutoRelay(host host.Host, opts ...Option) (*AutoRelay, error) {
	r := &AutoRelay{
		host:   host,
		status: network.ReachabilityUnknown,
	}
	conf := defaultConfig
	for _, opt := range opts {
		if err := opt(&conf); err != nil {
			return nil, err
		}
	}
	r.ctx, r.ctxCancel = context.WithCancel(context.Background())
	rf, err := newRelayFinder(host, &conf)
	if err != nil {
		return nil, fmt.Errorf("failed to create autorelay: %w", err)
	}
	r.relayFinder = rf
	r.metricsTracer = &wrappedMetricsTracer{conf.metricsTracer}

	return r, nil
}

func (r *AutoRelay) Start() {
	r.refCount.Add(1)
	go func() {
		defer r.refCount.Done()
		r.background()
	}()
}

func (r *AutoRelay) background() {
	subReachability, err := r.host.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged), eventbus.Name("autorelay (background)"))
	if err != nil {
		log.Debug("failed to subscribe to the EvtLocalReachabilityChanged")
		return
	}
	defer subReachability.Close()

	for {
		select {
		case <-r.ctx.Done():
			return
		case ev, ok := <-subReachability.Out():
			if !ok {
				return
			}
			evt := ev.(event.EvtLocalReachabilityChanged)
			switch evt.Reachability {
			case network.ReachabilityPrivate, network.ReachabilityUnknown:
				err := r.relayFinder.Start()
				if errors.Is(err, errAlreadyRunning) {
					log.Debug("tried to start already running relay finder")
				} else if err != nil {
					log.Errorw("failed to start relay finder", "error", err)
				} else {
					r.metricsTracer.RelayFinderStatus(true)
				}
			case network.ReachabilityPublic:
				r.relayFinder.Stop()
				r.metricsTracer.RelayFinderStatus(false)
			}
			r.mx.Lock()
			r.status = evt.Reachability
			r.mx.Unlock()
		}
	}
}

func (r *AutoRelay) Close() error {
	r.ctxCancel()
	err := r.relayFinder.Stop()
	r.refCount.Wait()
	return err
}
