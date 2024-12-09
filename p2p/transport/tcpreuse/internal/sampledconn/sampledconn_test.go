package sampledconn

import (
	"io"
	"syscall"
	"testing"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
				sample, sampledConn, err := newWrappedSampledConn(clientConn.(ManetTCPConnInterface))
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

func spawnServerAndClientConn(t *testing.T) (serverConn manet.Conn, clientConn manet.Conn) {
	serverConnChan := make(chan manet.Conn, 1)

	listener, err := manet.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0"))
	assert.NoError(t, err)
	defer listener.Close()

	serverAddr := listener.Multiaddr()

	// Server goroutine
	go func() {
		conn, err := listener.Accept()
		assert.NoError(t, err)
		serverConnChan <- conn
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Create a TCP client
	clientConn, err = manet.Dial(serverAddr)
	assert.NoError(t, err)

	return <-serverConnChan, clientConn
}

func TestHandleNoBytes(t *testing.T) {
	serverConn, clientConn := spawnServerAndClientConn(t)
	defer clientConn.Close()

	// Server goroutine
	go func() {
		serverConn.Close()
	}()
	_, _, err := PeekBytes(clientConn.(interface {
		manet.Conn
		syscall.Conn
	}))
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestHandle1ByteAndClose(t *testing.T) {
	serverConn, clientConn := spawnServerAndClientConn(t)
	defer clientConn.Close()

	// Server goroutine
	go func() {
		defer serverConn.Close()
		_, err := serverConn.Write([]byte("h"))
		assert.NoError(t, err)
	}()
	_, _, err := PeekBytes(clientConn.(interface {
		manet.Conn
		syscall.Conn
	}))
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestSlowBytes(t *testing.T) {
	serverConn, clientConn := spawnServerAndClientConn(t)

	interval := 100 * time.Millisecond

	// Server goroutine
	go func() {
		defer serverConn.Close()

		time.Sleep(interval)
		_, err := serverConn.Write([]byte("h"))
		assert.NoError(t, err)
		time.Sleep(interval)
		_, err = serverConn.Write([]byte("e"))
		assert.NoError(t, err)
		time.Sleep(interval)
		_, err = serverConn.Write([]byte("l"))
		assert.NoError(t, err)
		time.Sleep(interval)
		_, err = serverConn.Write([]byte("lo"))
		assert.NoError(t, err)
	}()

	defer clientConn.Close()

	err := clientConn.SetReadDeadline(time.Now().Add(interval * 10))
	require.NoError(t, err)

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
}
