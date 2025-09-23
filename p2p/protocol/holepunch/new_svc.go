package holepunch

import (
	"context"
	"math/rand"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	minRoleSwapWait = 100 * time.Millisecond
	maxRoleSwapWait = 300 * time.Millisecond
)

func (s *Service) handleNewStreamFixed(str network.Stream) {
	if str.Conn().Stat().Direction == network.DirInbound {
		str.Reset()
		return
	}

	if err := str.Scope().SetService(ServiceName); err != nil {
		log.Debug("error attaching stream to holepunch service", "err", err)
		str.Reset()
		return
	}

	rp := str.Conn().RemotePeer()
	rtt, addrs, ownAddrs, err := s.incomingHolePunch(str)
	if err != nil {
		s.tracer.ProtocolError(rp, err)
		log.Debug("error handling holepunching stream", "peer", rp, "err", err)
		str.Reset()
		return
	}
	str.Close()

	pi := peer.AddrInfo{
		ID:    rp,
		Addrs: addrs,
	}
	s.tracer.StartHolePunch(rp, addrs, rtt)
	log.Debug("starting hole punch", "peer", rp)

	s.performBackwardsCompatibleHolePunch(pi, ownAddrs)
}

func (s *Service) performBackwardsCompatibleHolePunch(pi peer.AddrInfo, ownAddrs []ma.Multiaddr) {
	ctx, cancel := context.WithTimeout(s.ctx, s.directDialTimeout)
	defer cancel()

	start := time.Now()
	s.tracer.HolePunchAttempt(pi.ID)

	var err error
	if s.legacyBehavior {
		isClient := true
		err = holePunchConnect(ctx, s.host, pi, isClient)
	} else {
		err = s.performSpecCompliantHolePunch(ctx, pi)
	}

	dt := time.Since(start)
	s.tracer.EndHolePunch(pi.ID, dt, err)
	s.tracer.HolePunchFinished("receiver", 1, pi.Addrs, ownAddrs, getDirectConnection(s.host, pi.ID))
}

func (s *Service) performSpecCompliantHolePunch(ctx context.Context, pi peer.AddrInfo) error {
	serverCtx, serverCancel := context.WithTimeout(ctx, 2*time.Second)
	defer serverCancel()

	isClient := false
	err := holePunchConnect(serverCtx, s.host, pi, isClient)
	if err == nil {
		return nil
	}

	randomWait := time.Duration(rand.Int63n(int64(maxRoleSwapWait-minRoleSwapWait))) + minRoleSwapWait
	
	select {
	case <-time.After(randomWait):
		log.Debug("switching to client role for backwards compatibility", "peer", pi.ID, "wait", randomWait)
		isClient = true
		return holePunchConnect(ctx, s.host, pi, isClient)
	case <-ctx.Done():
		return ctx.Err()
	}
}
