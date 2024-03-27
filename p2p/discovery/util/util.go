package util

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/discovery"
	"github.com/libp2p/go-libp2p/core/peer"

	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger("discovery-util")

// FindPeers is a utility function that synchronously collects peers from a Discoverer.
func FindPeers(ctx context.Context, d discovery.Discoverer, ns string, opts ...discovery.Option) ([]peer.AddrInfo, error) {

	ch, err := d.FindPeers(ctx, ns, opts...)
	if err != nil {
		return nil, err
	}

	res := make([]peer.AddrInfo, 0, len(ch))
	for pi := range ch {
		res = append(res, pi)
	}

	return res, nil
}

func FindPeersRegularly(ctx context.Context, d discovery.Discoverer, ns string, interval time.Duration, opts ...discovery.Option) <-chan peer.AddrInfo {
	peerChan := make(chan peer.AddrInfo, 10)
	go func() {
		defer close(peerChan)
		t := time.NewTicker(interval)
		defer t.Stop()

		var (
			fctx   context.Context
			cancel context.CancelFunc
		)
		var ch <-chan peer.AddrInfo
		for {
			select {
			case <-ctx.Done():
				return
			case pi := <-ch:
				select {
				case peerChan <- pi:
				case <-fctx.Done():
					continue
				}
			case <-t.C:
				if cancel != nil {
					cancel()
				}
				fctx, cancel = context.WithCancel(ctx)
				var err error
				ch, err = d.FindPeers(fctx, ns, opts...)
				if err != nil {
					log.Errorf("FindPeers failed: %s", err)
					return
				}
			}
		}
	}()
	return peerChan
}

// Advertise is a utility function that persistently advertises a service through an Advertiser.
func Advertise(ctx context.Context, a discovery.Advertiser, ns string, opts ...discovery.Option) {
	go func() {
		for {
			ttl, err := a.Advertise(ctx, ns, opts...)
			if err != nil {
				log.Debugf("Error advertising %s: %s", ns, err.Error())
				if ctx.Err() != nil {
					return
				}

				select {
				case <-time.After(2 * time.Minute):
					continue
				case <-ctx.Done():
					return
				}
			}

			wait := 7 * ttl / 8
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
		}
	}()
}
