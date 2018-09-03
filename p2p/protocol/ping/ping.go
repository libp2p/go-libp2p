package ping

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	u "github.com/ipfs/go-ipfs-util"
	logging "github.com/ipfs/go-log"
	host "github.com/libp2p/go-libp2p-host"
	inet "github.com/libp2p/go-libp2p-net"
	peer "github.com/libp2p/go-libp2p-peer"
)

var log = logging.Logger("ping")

// PingSize is the size of ping in bytes
const PingSize = 32

// ID is the protocol ID of the ping/2.0.0 protocol
// This version uses a new stream for every ping.
const ID = "/ipfs/ping/2.0.0"

// IDLegacy is the protocol ID of the ping/1.0.0 protocol
// This version uses a single stream for all pings.
const IDLegacy = "/ipfs/ping/1.0.0"

type PingService struct {
	Host host.Host
}

// NewPingService creates a new PingService.
// It uses supports both ping/1.0.0 and ping/2.0.0 for incoming pings
func NewPingService(h host.Host) *PingService {
	ps := &PingService{h}
	h.SetStreamHandler(IDLegacy, ps.PingHandlerLegacy)
	h.SetStreamHandler(ID, ps.PingHandler)
	return ps
}

// PingHandler handles a ping/2.0.0 ping.
func (ps *PingService) PingHandler(s inet.Stream) {
	defer s.Close()

	buf := make([]byte, PingSize)
	if _, err := io.ReadFull(s, buf); err != nil {
		log.Debug(err)
		return
	}

	if _, err := s.Write(buf); err != nil {
		log.Debug(err)
		return
	}
}

// Ping pings a peer.
// It first attempts to use ping/2.0.0, and falls back to ping/1.0.0
// if the peer doesn't support it yet.
func (ps *PingService) Ping(ctx context.Context, p peer.ID) (<-chan time.Duration, error) {
	s, err := ps.Host.NewStream(ctx, p, ID, IDLegacy)
	if err != nil {
		return nil, err
	}
	if s.Protocol() == IDLegacy {
		return ps.PingLegacy(ctx, p)
	}

	out := make(chan time.Duration)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			t, err := ping(s)
			if err != nil {
				log.Debugf("ping error: %s", err)
				return
			}

			ps.Host.Peerstore().RecordLatency(p, t)
			select {
			case out <- t:
				s, err = ps.Host.NewStream(ctx, p, ID)
				if err != nil {
					log.Debugf("ping error: %s", err)
					return
				}
				defer s.Close()
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}

func ping(s inet.Stream) (time.Duration, error) {
	defer s.Close()

	buf := make([]byte, PingSize)
	u.NewTimeSeededRand().Read(buf)

	before := time.Now()
	if _, err := s.Write(buf); err != nil {
		return 0, err
	}

	rbuf := make([]byte, PingSize)
	if _, err := io.ReadFull(s, rbuf); err != nil {
		return 0, err
	}

	if !bytes.Equal(buf, rbuf) {
		return 0, errors.New("ping packet was incorrect")
	}

	return time.Since(before), nil
}
