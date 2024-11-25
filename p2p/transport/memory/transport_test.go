package memory

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
	tr, err := NewTransport(key, nil, nil)
	require.NoError(t, err)
	return tr
}

func TestMemoryProtocol(t *testing.T) {
	t.Parallel()
	tr := getTransport(t)
	defer tr.(io.Closer).Close()

	protocols := tr.Protocols()
	if len(protocols) > 1 {
		t.Fatalf("expected at most one protocol, got %v", protocols)
	}

	if protocols[0] != ma.P_MEMORY {
		t.Fatalf("expected the supported protocol to be memory, got %d", protocols[0])
	}
}

func TestCanDial(t *testing.T) {
	t.Parallel()
	tr := getTransport(t)
	defer tr.(io.Closer).Close()

	invalid := []string{
		"/ip4/127.0.0.1/udp/1234",
		"/ip4/5.5.5.5/tcp/1234",
		"/dns/google.com/udp/443/quic-v1",
		"/ip4/127.0.0.1/udp/1234/quic",
	}
	valid := []string{
		"/memory/1234",
		"/memory/1337123",
	}
	for _, s := range invalid {
		invalidAddr, err := ma.NewMultiaddr(s)
		require.NoError(t, err)
		if tr.CanDial(invalidAddr) {
			t.Errorf("didn't expect to be able to dial a non-memory address (%s)", invalidAddr)
		}
	}
	for _, s := range valid {
		validAddr, err := ma.NewMultiaddr(s)
		require.NoError(t, err)
		if !tr.CanDial(validAddr) {
			t.Errorf("expected to be able to dial memory address (%s)", validAddr)
		}
	}
}
