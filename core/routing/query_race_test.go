package routing

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// TestPublishQueryEventResponsesRace shows that PublishQueryEvent forwards
// QueryEvent.Responses to subscribers by pointer. A publisher that mutates
// the AddrInfo it handed over (e.g. appending to AddrInfo.Addrs) races
// with any consumer reading Responses.
//
// This is a real hazard: DHT implementations publish a []*peer.AddrInfo
// of "closer peers" as a PeerResponse event and then continue processing
// the same slice, enriching AddrInfo.Addrs from a peerstore.
//
// Expected to fail under -race.
func TestPublishQueryEventResponsesRace(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx, events := RegisterForQueryEvents(ctx)

	ai := &peer.AddrInfo{
		Addrs: []ma.Multiaddr{ma.StringCast("/ip4/1.2.3.4/tcp/4001")},
	}
	more := ma.StringCast("/ip4/5.6.7.8/tcp/4001")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			for _, pi := range ev.Responses {
				_ = len(pi.Addrs) // racy read of the AddrInfo.Addrs header
			}
		}
	}()

	for range 1000 {
		PublishQueryEvent(ctx, &QueryEvent{
			Type:      PeerResponse,
			Responses: []*peer.AddrInfo{ai},
		})
		ai.Addrs = append(ai.Addrs, more)
	}

	cancel()
	<-done
}
