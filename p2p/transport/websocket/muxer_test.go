package websocket

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/stretchr/testify/require"
)

func newSecureMuxer(t *testing.T) (peer.ID, []sec.SecureTransport) {
	t.Helper()
	priv, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	noiseTpt, err := noise.New(noise.ID, priv, nil)
	require.NoError(t, err)
	return id, []sec.SecureTransport{noiseTpt}
}
