package identify

import (
	"crypto/rand"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/record"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestSnapshotEquality(t *testing.T) {
	addr1 := ma.StringCast("/ip4/127.0.0.1/tcp/1234")
	addr2 := ma.StringCast("/ip4/127.0.0.1/udp/1234/quic-v1")

	_, pubKey1, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	_, pubKey2, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	record1 := &record.Envelope{PublicKey: pubKey1}
	record2 := &record.Envelope{PublicKey: pubKey2}

	for _, tc := range []struct {
		s1, s2 *identifySnapshot
		result bool
	}{
		{s1: &identifySnapshot{record: record1}, s2: &identifySnapshot{record: record1}, result: true},
		{s1: &identifySnapshot{record: record1}, s2: &identifySnapshot{record: record2}, result: false},
		{s1: &identifySnapshot{addrs: []ma.Multiaddr{addr1}}, s2: &identifySnapshot{addrs: []ma.Multiaddr{addr1}}, result: true},
		{s1: &identifySnapshot{addrs: []ma.Multiaddr{addr1}}, s2: &identifySnapshot{addrs: []ma.Multiaddr{addr2}}, result: false},
		{s1: &identifySnapshot{addrs: []ma.Multiaddr{addr1, addr2}}, s2: &identifySnapshot{addrs: []ma.Multiaddr{addr2}}, result: false},
		{s1: &identifySnapshot{addrs: []ma.Multiaddr{addr1}}, s2: &identifySnapshot{addrs: []ma.Multiaddr{addr1, addr2}}, result: false},
		{s1: &identifySnapshot{protocols: []protocol.ID{"/foo"}}, s2: &identifySnapshot{protocols: []protocol.ID{"/foo"}}, result: true},
		{s1: &identifySnapshot{protocols: []protocol.ID{"/foo"}}, s2: &identifySnapshot{protocols: []protocol.ID{"/bar"}}, result: false},
		{s1: &identifySnapshot{protocols: []protocol.ID{"/foo", "/bar"}}, s2: &identifySnapshot{protocols: []protocol.ID{"/bar"}}, result: false},
		{s1: &identifySnapshot{protocols: []protocol.ID{"/foo"}}, s2: &identifySnapshot{protocols: []protocol.ID{"/foo", "/bar"}}, result: false},
	} {
		if tc.result {
			require.Truef(t, tc.s1.Equal(tc.s2), "expected equal: %+v and %+v", tc.s1, tc.s2)
		} else {
			require.Falsef(t, tc.s1.Equal(tc.s2), "expected unequal: %+v and %+v", tc.s1, tc.s2)
		}
	}
}
