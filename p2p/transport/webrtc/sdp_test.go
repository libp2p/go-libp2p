package libp2pwebrtc

import (
	"encoding/hex"
	"net"
	"testing"

	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/require"
)

const expectedServerSDP = `v=0
o=- 0 0 IN IP4 0.0.0.0
s=-
t=0 0
a=ice-lite
m=application 37826 UDP/DTLS/SCTP webrtc-datachannel
c=IN IP4 0.0.0.0
a=mid:0
a=ice-options:ice2
a=ice-ufrag:d2c0fc07-8bb3-42ae-bae2-a6fce8a0b581
a=ice-pwd:d2c0fc07-8bb3-42ae-bae2-a6fce8a0b581
a=fingerprint:sha-256 ba:78:16:bf:8f:01:cf:ea:41:41:40:de:5d:ae:22:23:b0:03:61:a3:96:17:7a:9c:b4:10:ff:61:f2:00:15:ad

a=setup:passive
a=sctp-port:5000
a=max-message-size:16384
a=candidate:1 1 UDP 1 0.0.0.0 37826 typ host
a=end-of-candidates
`

func TestRenderServerSDP(t *testing.T) {
	encoded, err := hex.DecodeString("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	require.NoError(t, err)

	testMultihash := multihash.DecodedMultihash{
		Code:   multihash.SHA2_256,
		Name:   multihash.Codes[multihash.SHA2_256],
		Digest: encoded,
		Length: len(encoded),
	}
	addr := &net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 37826}
	ufrag := "d2c0fc07-8bb3-42ae-bae2-a6fce8a0b581"
	fingerprint := testMultihash

	sdp, err := createServerSDP(addr, ufrag, fingerprint)
	require.NoError(t, err)
	require.Equal(t, expectedServerSDP, sdp)
}

const expectedClientSDP = `v=0
o=- 0 0 IN IP4 0.0.0.0
s=-
c=IN IP4 0.0.0.0
t=0 0

m=application 37826 UDP/DTLS/SCTP webrtc-datachannel
a=mid:0
a=ice-options:ice2
a=ice-ufrag:d2c0fc07-8bb3-42ae-bae2-a6fce8a0b581
a=ice-pwd:d2c0fc07-8bb3-42ae-bae2-a6fce8a0b581
a=fingerprint:sha-256 ba:78:16:bf:8f:01:cf:ea:41:41:40:de:5d:ae:22:23:b0:03:61:a3:96:17:7a:9c:b4:10:ff:61:f2:00:15:ad
a=setup:actpass
a=sctp-port:5000
a=max-message-size:16384
`

func TestRenderClientSDP(t *testing.T) {
	addr := &net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 37826}
	ufrag := "d2c0fc07-8bb3-42ae-bae2-a6fce8a0b581"
	sdp := createClientSDP(addr, ufrag)
	require.Equal(t, expectedClientSDP, sdp)
}

func BenchmarkRenderClientSDP(b *testing.B) {
	addr := &net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 37826}
	ufrag := "d2c0fc07-8bb3-42ae-bae2-a6fce8a0b581"

	for i := 0; i < b.N; i++ {
		createClientSDP(addr, ufrag)
	}
}

func BenchmarkRenderServerSDP(b *testing.B) {
	encoded, _ := hex.DecodeString("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")

	testMultihash := multihash.DecodedMultihash{
		Code:   multihash.SHA2_256,
		Name:   multihash.Codes[multihash.SHA2_256],
		Digest: encoded,
		Length: len(encoded),
	}
	addr := &net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 37826}
	ufrag := "d2c0fc07-8bb3-42ae-bae2-a6fce8a0b581"
	fingerprint := testMultihash

	for i := 0; i < b.N; i++ {
		createServerSDP(addr, ufrag, fingerprint)
	}

}
