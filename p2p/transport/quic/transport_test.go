package libp2pquic

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"io"
	"testing"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	tpt "github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func getTransport(t *testing.T) tpt.Transport {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	key, err := ic.UnmarshalRsaPrivateKey(x509.MarshalPKCS1PrivateKey(rsaKey))
	require.NoError(t, err)
	tr, err := NewTransport(key, newConnManager(t), nil, nil, nil)
	require.NoError(t, err)
	return tr
}

func TestQUICProtocol(t *testing.T) {
	tr := getTransport(t)
	defer tr.(io.Closer).Close()

	protocols := tr.Protocols()
	if len(protocols) > 1 {
		t.Fatalf("expected at most one protocol, got %v", protocols)
	}
	if protocols[0] != ma.P_QUIC_V1 {
		t.Fatalf("expected the supported protocol to be QUIC v1, got %d", protocols[0])
	}
}

func TestCanDial(t *testing.T) {
	tr := getTransport(t)
	defer tr.(io.Closer).Close()

	invalid := []string{
		"/ip4/127.0.0.1/udp/1234",
		"/ip4/5.5.5.5/tcp/1234",
		"/dns/google.com/udp/443/quic-v1",
		"/ip4/127.0.0.1/udp/1234/quic",
	}
	valid := []string{
		"/ip4/127.0.0.1/udp/1234/quic-v1",
		"/ip4/5.5.5.5/udp/0/quic-v1",
	}
	for _, s := range invalid {
		invalidAddr, err := ma.NewMultiaddr(s)
		require.NoError(t, err)
		if tr.CanDial(invalidAddr) {
			t.Errorf("didn't expect to be able to dial a non-quic address (%s)", invalidAddr)
		}
	}
	for _, s := range valid {
		validAddr, err := ma.NewMultiaddr(s)
		require.NoError(t, err)
		if !tr.CanDial(validAddr) {
			t.Errorf("expected to be able to dial QUIC address (%s)", validAddr)
		}
	}
}
