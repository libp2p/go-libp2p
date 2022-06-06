package autorelay

import (
	"context"
	"sync"

	basic "github.com/libp2p/go-libp2p/p2p/host/basic"

	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
)

var log = logging.Logger("autorelay")

type AutoRelay struct {
	refCount  sync.WaitGroup
	ctx       context.Context
	ctxCancel context.CancelFunc

	conf *config

	mx     sync.Mutex
	status network.Reachability

	relayFinder *relayFinder

	peerChanOut chan peer.AddrInfo // capacity 20

	host   host.Host
	addrsF basic.AddrsFactory
}

func NewAutoRelay(bhost *basic.BasicHost, opts ...Option) (*AutoRelay, error) {
	r := &AutoRelay{
		host:   bhost,
		addrsF: bhost.AddrsFactory,
		status: network.ReachabilityUnknown,
	}
	conf := defaultConfig
	for _, opt := range opts {
		if err := opt(&conf); err != nil {
			return nil, err
		}
	}
	r.ctx, r.ctxCancel = context.WithCancel(context.Background())
	r.peerChanOut = make(chan peer.AddrInfo, conf.maxCandidates)
	r.conf = &conf
	r.relayFinder = newRelayFinder(bhost, r.peerChanOut, &conf)
	bhost.AddrsFactory = r.hostAddrs

	r.refCount.Add(1)
	go func() {
		defer r.refCount.Done()
		r.background()
	}()
	return r, nil
}

func (r *AutoRelay) background() {
	subReachability, err := r.host.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	if err != nil {
		log.Debug("failed to subscribe to the EvtLocalReachabilityChanged")
		return
	}
	defer subReachability.Close()

	var peerChan <-chan peer.AddrInfo
	if len(r.conf.staticRelays) == 0 {
		peerChan = r.conf.peerChan
	} else {
		pc := make(chan peer.AddrInfo)
		peerChan = pc
		r.refCount.Add(1)
		go func() {
			defer r.refCount.Done()
			for _, sr := range r.conf.staticRelays {
				select {
				case pc <- sr:
				case <-r.ctx.Done():
					return
				}
			}
		}()
	}

	for {
		select {
		case <-r.ctx.Done():
			return
		case ev, ok := <-subReachability.Out():
			if !ok {
				return
			}
			// TODO: push changed addresses
			evt := ev.(event.EvtLocalReachabilityChanged)
			switch evt.Reachability {
			case network.ReachabilityPrivate, network.ReachabilityUnknown:
				if err := r.relayFinder.Start(); err != nil {
					log.Errorw("failed to start relay finder", "error", err)
				}
			case network.ReachabilityPublic:
				r.relayFinder.Stop()
			}
			r.mx.Lock()
			r.status = evt.Reachability
			r.mx.Unlock()
		case pi := <-peerChan:
			select {
			case r.peerChanOut <- pi: // if there's space in the channel, great
			default:
				// no space left in the channel. Drop the oldest entry.
				select {
				case <-r.peerChanOut:
				default: // The consumer might just have emptied the channel. Make sure we don't block in that case.
				}
				r.peerChanOut <- pi
			}
		}
	}
}

func (r *AutoRelay) hostAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	return r.relayAddrs(r.addrsF(addrs))
}

func (r *AutoRelay) relayAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	r.mx.Lock()
	defer r.mx.Unlock()

	if r.status != network.ReachabilityPrivate {
		return addrs
	}
	return r.relayFinder.relayAddrs(addrs)
}

func (r *AutoRelay) Close() error {
	r.ctxCancel()
	err := r.relayFinder.Stop()
	r.refCount.Wait()
	return err
}
