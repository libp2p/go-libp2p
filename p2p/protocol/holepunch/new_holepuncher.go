package holepunch

import (
	"context"
	"math/rand"
	"time"
	"fmt"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/network"
	manet "github.com/multiformats/go-multiaddr/net"
)

func (hp *holePuncher) directConnectFixed(rp peer.ID) error {
	if getDirectConnection(hp.host, rp) != nil {
		log.Debug("already connected", "source_peer", hp.host.ID(), "destination_peer", rp)
		return nil
	}

	if hp.attemptDirectDial(rp) {
		return nil
	}

	log.Debug("got inbound proxy conn", "destination_peer", rp)

	for i := 1; i <= maxRetries; i++ {
		addrs, obsAddrs, rtt, err := hp.initiateHolePunch(rp)
		if err != nil {
			hp.tracer.ProtocolError(rp, err)
			return err
		}

		synTime := rtt / 2
		log.Debug("peer RTT and starting hole punch", "rtt", rtt, "syn_time", synTime)

		timer := time.NewTimer(synTime)
		select {
		case start := <-timer.C:
			pi := peer.AddrInfo{
				ID:    rp,
				Addrs: addrs,
			}
			hp.tracer.StartHolePunch(rp, addrs, rtt)
			hp.tracer.HolePunchAttempt(pi.ID)

			ctx, cancel := context.WithTimeout(hp.ctx, hp.directDialTimeout)
			err := hp.performBackwardsCompatibleInitiatorHolePunch(ctx, pi)
			cancel()

			dt := time.Since(start)
			hp.tracer.EndHolePunch(rp, dt, err)
			if err == nil {
				log.Debug("hole punching successful", "destination_peer", rp, "duration", dt)
				hp.tracer.HolePunchFinished("initiator", i, addrs, obsAddrs, getDirectConnection(hp.host, rp))
				return nil
			}
		case <-hp.ctx.Done():
			timer.Stop()
			return hp.ctx.Err()
		}
		if i == maxRetries {
			hp.tracer.HolePunchFinished("initiator", maxRetries, addrs, obsAddrs, nil)
		}
	}
	return fmt.Errorf("all retries for hole punch with peer %s failed", rp)
}

func (hp *holePuncher) attemptDirectDial(rp peer.ID) bool {
	log.Debug("attempting direct dial", "source_peer", hp.host.ID(), "destination_peer", rp, "addrs", hp.host.Peerstore().Addrs(rp))
	
	for _, a := range hp.host.Peerstore().Addrs(rp) {
		if !isRelayAddress(a) && manet.IsPublicAddr(a) {
			forceDirectConnCtx := network.WithForceDirectDial(hp.ctx, "hole-punching")
			dialCtx, cancel := context.WithTimeout(forceDirectConnCtx, hp.directDialTimeout)

			tstart := time.Now()
			err := hp.host.Connect(dialCtx, peer.AddrInfo{ID: rp})
			dt := time.Since(tstart)
			cancel()

			if err != nil {
				hp.tracer.DirectDialFailed(rp, dt, err)
				return false
			}
			hp.tracer.DirectDialSuccessful(rp, dt)
			log.Debug("direct connection to peer successful, no need for a hole punch", "destination_peer", rp)
			return true
		}
	}
	return false
}

func (hp *holePuncher) performBackwardsCompatibleInitiatorHolePunch(ctx context.Context, pi peer.AddrInfo) error {
	if hp.legacyBehavior {
		isClient := false
		return holePunchConnect(ctx, hp.host, pi, isClient)
	} else {
		return hp.performSpecCompliantInitiatorHolePunch(ctx, pi)
	}
}

func (hp *holePuncher) performSpecCompliantInitiatorHolePunch(ctx context.Context, pi peer.AddrInfo) error {
	clientCtx, clientCancel := context.WithTimeout(ctx, 2*time.Second)
	defer clientCancel()

	isClient := true
	err := holePunchConnect(clientCtx, hp.host, pi, isClient)
	if err == nil {
		return nil
	}

	randomWait := time.Duration(rand.Int63n(int64(maxRoleSwapWait-minRoleSwapWait))) + minRoleSwapWait
	
	select {
	case <-time.After(randomWait):
		log.Debug("switching to server role for backwards compatibility", "peer", pi.ID, "wait", randomWait)
		isClient = false
		return holePunchConnect(ctx, hp.host, pi, isClient)
	case <-ctx.Done():
		return ctx.Err()
	}
}
