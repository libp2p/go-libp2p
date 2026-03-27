package transport_integration

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestReadWriteDeadlines(t *testing.T) {
	// Send a lot of data so that writes have to flush (can't just buffer it all)
	sendBuf := make([]byte, 10<<20)
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			listener := tc.HostGenerator(t, TransportTestCaseOpts{})
			defer listener.Close()
			dialer := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})
			defer dialer.Close()

			require.NoError(t, dialer.Connect(context.Background(), peer.AddrInfo{
				ID:    listener.ID(),
				Addrs: listener.Addrs(),
			}))

			// This simply stalls
			listener.SetStreamHandler("/stall", func(s network.Stream) {
				time.Sleep(time.Hour)
				s.Close()
			})

			t.Run("ReadDeadline", func(t *testing.T) {
				s, err := dialer.NewStream(context.Background(), listener.ID(), "/stall")
				require.NoError(t, err)
				defer s.Close()

				start := time.Now()
				// Set a deadline
				s.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
				buf := make([]byte, 1)
				_, err = s.Read(buf)
				require.Error(t, err)
				var nerr net.Error
				require.ErrorAs(t, err, &nerr)
				require.True(t, nerr.Timeout())
				require.Less(t, time.Since(start), 1*time.Second)
			})

			t.Run("WriteDeadline", func(t *testing.T) {
				s, err := dialer.NewStream(context.Background(), listener.ID(), "/stall")
				require.NoError(t, err)
				defer s.Close()

				// Set a deadline
				s.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
				start := time.Now()
				_, err = s.Write(sendBuf)
				require.Error(t, err)
				require.True(t, err.(net.Error).Timeout())
				require.Less(t, time.Since(start), 1*time.Second)
			})

			// Like the above, but with SetDeadline
			t.Run("SetDeadline", func(t *testing.T) {
				for _, op := range []string{"Read", "Write"} {
					t.Run(op, func(t *testing.T) {
						s, err := dialer.NewStream(context.Background(), listener.ID(), "/stall")
						require.NoError(t, err)
						defer s.Close()

						// Set a deadline
						s.SetDeadline(time.Now().Add(10 * time.Millisecond))
						start := time.Now()

						if op == "Read" {
							buf := make([]byte, 1)
							_, err = s.Read(buf)
						} else {
							_, err = s.Write(sendBuf)
						}
						require.Error(t, err)
						var nerr net.Error
						require.ErrorAs(t, err, &nerr)
						require.True(t, nerr.Timeout())
						require.Less(t, time.Since(start), 1*time.Second)
					})
				}
			})
		})
	}
}
