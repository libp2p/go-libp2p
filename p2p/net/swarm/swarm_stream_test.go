package swarm

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/stretchr/testify/require"
)

type asyncStreamWrapper struct {
	network.MuxedStream
	beforeClose func()
}

func (s *asyncStreamWrapper) AsyncClose(onDone func()) error {
	s.beforeClose()
	err := s.Close()
	onDone()
	return err
}

func TestStreamAsyncCloser(t *testing.T) {
	s1 := makeSwarm(t)
	s2 := makeSwarm(t)

	s1.Peerstore().AddAddrs(s2.LocalPeer(), s2.ListenAddresses(), peerstore.TempAddrTTL)
	s, err := s1.NewStream(context.Background(), s2.LocalPeer())
	require.NoError(t, err)
	ss, ok := s.(*Stream)
	require.True(t, ok)

	var called atomic.Bool
	as := &asyncStreamWrapper{
		MuxedStream: ss.stream,
		beforeClose: func() {
			called.Store(true)
		},
	}
	ss.stream = as
	ss.Close()
	require.True(t, called.Load())
}
