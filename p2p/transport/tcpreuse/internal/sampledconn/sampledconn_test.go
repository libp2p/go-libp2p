package sampledconn

import (
	"io"
	"syscall"
	"testing"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"github.com/stretchr/testify/assert"
)

func TestSampledConn(t *testing.T) {
	testCases := []string{
		"platform",
		"fallback",
	}

	// Start a TCP server
	listener, err := manet.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0"))
	assert.NoError(t, err)
	defer listener.Close()

	serverAddr := listener.Multiaddr()

	// Server goroutine
	go func() {
		for i := 0; i < len(testCases); i++ {
			conn, err := listener.Accept()
			assert.NoError(t, err)
			defer conn.Close()

			// Write some data to the connection
			_, err = conn.Write([]byte("hello"))
			assert.NoError(t, err)
		}
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	for _, tc := range testCases {
		t.Run(tc, func(t *testing.T) {
			// Create a TCP client
			clientConn, err := manet.Dial(serverAddr)
			assert.NoError(t, err)
			defer clientConn.Close()

			if tc == "platform" {
				// Wrap the client connection in SampledConn
				peeked, clientConn, err := PeekBytes(clientConn.(interface {
					manet.Conn
					syscall.Conn
				}))
				assert.NoError(t, err)
				assert.Equal(t, "hel", string(peeked[:]))

				buf := make([]byte, 5)
				_, err = io.ReadFull(clientConn, buf)
				assert.NoError(t, err)
				assert.Equal(t, "hello", string(buf))
			} else {
				// Wrap the client connection in SampledConn
				sample, sampledConn, err := newFallbackSampledConn(clientConn.(ManetTCPConnInterface))
				assert.NoError(t, err)
				assert.Equal(t, "hel", string(sample[:]))

				buf := make([]byte, 5)
				_, err = io.ReadFull(sampledConn, buf)
				assert.NoError(t, err)
				assert.Equal(t, "hello", string(buf))

			}
		})
	}
}
