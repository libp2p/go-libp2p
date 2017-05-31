// Package ping provides a ping service for libp2p hosts. It allows to measure
// round-trip latencies by sending data to a destination which echoes it
// back to the source.
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

// PingSize determines the size of the data written to the inet.Stream.
const PingSize = 32

// ID is the protocol ID for the PingService
const ID = "/ipfs/ping/1.0.0"

const pingTimeout = time.Second * 60

// PingService enables sending and responding to Ping requests.
type PingService struct {
	Host host.Host
}

// NewPingService creates a PinService on the given
// host by enabling it to perform and respond to pings.
func NewPingService(h host.Host) *PingService {
	ps := &PingService{h}
	h.SetStreamHandler(ID, ps.PingHandler)
	return ps
}

// PingHandler is a Stream handler which reads data from a
// stream and echoes it back.
func (p *PingService) PingHandler(s inet.Stream) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buf := make([]byte, PingSize)

	timer := time.NewTimer(pingTimeout)
	defer timer.Stop()

	go func() {
		select {
		case <-timer.C:
		case <-ctx.Done():
		}

		s.Close()
	}()

	for {
		_, err := io.ReadFull(s, buf)
		if err != nil {
			log.Debug(err)
			return
		}

		_, err = s.Write(buf)
		if err != nil {
			log.Debug(err)
			return
		}

		timer.Reset(pingTimeout)
	}
}

// Ping triggers pings to a given peer. It provides a from which latencies
// for each ping can be read. Pings happen continuosly until the given context
// is cancelled.
func (p *PingService) Ping(ctx context.Context, pid peer.ID) (<-chan time.Duration, error) {
	s, err := p.Host.NewStream(ctx, pid, ID)
	if err != nil {
		return nil, err
	}

	out := make(chan time.Duration)
	go func() {
		defer close(out)
		defer s.Close()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				t, err := ping(s)
				if err != nil {
					log.Debugf("ping error: %s", err)
					return
				}

				p.Host.Peerstore().RecordLatency(pid, t)
				select {
				case out <- t:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}

func ping(s inet.Stream) (time.Duration, error) {
	buf := make([]byte, PingSize)
	u.NewTimeSeededRand().Read(buf)

	before := time.Now()
	_, err := s.Write(buf)
	if err != nil {
		return 0, err
	}

	rbuf := make([]byte, PingSize)
	_, err = io.ReadFull(s, rbuf)
	if err != nil {
		return 0, err
	}

	if !bytes.Equal(buf, rbuf) {
		return 0, errors.New("ping packet was incorrect")
	}

	return time.Since(before), nil
}
