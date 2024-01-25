package benchmark

import (
	"context"
	crand "crypto/rand"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	tls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/stretchr/testify/assert"
)

type Factory func(*testing.B, crypto.PrivKey) sec.SecureTransport

func benchmarkThroughput(b *testing.B, size int, factory Factory) {
	privA, pubA, err := crypto.GenerateEd25519Key(crand.Reader)
	assert.NoError(b, err)
	idA, err := peer.IDFromPublicKey(pubA)
	assert.NoError(b, err)
	tptA := factory(b, privA)

	privB, pubB, err := crypto.GenerateEd25519Key(crand.Reader)
	assert.NoError(b, err)
	idB, err := peer.IDFromPublicKey(pubB)
	assert.NoError(b, err)
	tptB := factory(b, privB)

	// pipe here serialize the decryption and encryption, we might want both parallelised to reduce context switching impact on the benchmark.
	// https://github.com/golang/go/issues/34502 would be ideal for the parallel usecase.
	p1, p2 := net.Pipe()
	var ready sync.Mutex    // wait for completed handshake
	var finished sync.Mutex // wait until all data has been received
	ready.Lock()
	finished.Lock()
	go func() {
		defer finished.Unlock()
		conn, err := tptB.SecureInbound(context.Background(), p2, idA)
		assert.NoError(b, err)
		ready.Unlock()

		_, err = io.Copy(io.Discard, conn)
		assert.NoError(b, err)
	}()

	conn, err := tptA.SecureOutbound(context.Background(), p1, idB)
	assert.NoError(b, err)
	ready.Lock()

	buf := make([]byte, size)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()

	for i := b.N; i != 0; i-- {
		_, err = conn.Write(buf[:])
		assert.NoError(b, err)
	}
	conn.Close()

	finished.Lock()
}
func benchmarkHandshakes(b *testing.B, factory Factory) {
	privA, pubA, err := crypto.GenerateEd25519Key(crand.Reader)
	assert.NoError(b, err)
	idA, err := peer.IDFromPublicKey(pubA)
	assert.NoError(b, err)
	tptA := factory(b, privA)

	privB, pubB, err := crypto.GenerateEd25519Key(crand.Reader)
	assert.NoError(b, err)
	idB, err := peer.IDFromPublicKey(pubB)
	assert.NoError(b, err)
	tptB := factory(b, privB)

	pipes := make(chan net.Conn, 1)

	var finished sync.Mutex // wait until all data has been transferred
	finished.Lock()
	go func() {
		defer finished.Unlock()
		var throwAway [1]byte
		for p := range pipes {
			conn, err := tptB.SecureInbound(context.Background(), p, idA)
			assert.NoError(b, err)
			_, err = conn.Read(throwAway[:]) // read because currently the tls transport handshake when calling Read.
			assert.ErrorIs(b, err, io.EOF)
		}
	}()
	b.ResetTimer()

	for i := b.N; i != 0; i-- {
		p1, p2 := net.Pipe()
		pipes <- p2
		conn, err := tptA.SecureOutbound(context.Background(), p1, idB)
		assert.NoError(b, err)
		assert.NoError(b, conn.Close())
	}
	close(pipes)

	finished.Lock()
}

func bench(b *testing.B, factory Factory) {
	b.Run("throughput", func(b *testing.B) {
		b.Run("32KiB", func(b *testing.B) { benchmarkThroughput(b, 32*1024, factory) })
		b.Run("1MiB", func(b *testing.B) { benchmarkThroughput(b, 1024*1024, factory) })
	})
	b.Run("handshakes", func(b *testing.B) { benchmarkHandshakes(b, factory) })
}

func BenchmarkNoise(b *testing.B) {
	bench(b, func(b *testing.B, priv crypto.PrivKey) sec.SecureTransport {
		tpt, err := noise.New("", priv, nil)
		assert.NoError(b, err)
		return tpt
	})
}

func BenchmarkTLS(b *testing.B) {
	bench(b, func(b *testing.B, priv crypto.PrivKey) sec.SecureTransport {
		tpt, err := tls.New("", priv, nil)
		assert.NoError(b, err)
		return tpt
	})
}
