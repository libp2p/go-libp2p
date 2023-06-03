package transport_integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/config"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/muxer/mplex"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	tls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/stretchr/testify/require"
)

type TransportTestCase struct {
	Name          string
	HostGenerator func(t *testing.T, opts TransportTestCaseOpts) host.Host
}

type TransportTestCaseOpts struct {
	NoListen        bool
	NoRcmgr         bool
	ConnGater       connmgr.ConnectionGater
	ResourceManager network.ResourceManager
}

func transformOpts(opts TransportTestCaseOpts) []config.Option {
	var libp2pOpts []libp2p.Option

	if opts.NoRcmgr {
		libp2pOpts = append(libp2pOpts, libp2p.ResourceManager(&network.NullResourceManager{}))
	}
	if opts.ConnGater != nil {
		libp2pOpts = append(libp2pOpts, libp2p.ConnectionGater(opts.ConnGater))
	}

	if opts.ResourceManager != nil {
		libp2pOpts = append(libp2pOpts, libp2p.ResourceManager(opts.ResourceManager))
	}
	return libp2pOpts
}

var transportsToTest = []TransportTestCase{
	{
		Name: "TCP / Noise / Yamux",
		HostGenerator: func(t *testing.T, opts TransportTestCaseOpts) host.Host {
			libp2pOpts := transformOpts(opts)
			libp2pOpts = append(libp2pOpts, libp2p.Security(noise.ID, noise.New))
			libp2pOpts = append(libp2pOpts, libp2p.Muxer(yamux.ID, yamux.DefaultTransport))
			if opts.NoListen {
				libp2pOpts = append(libp2pOpts, libp2p.NoListenAddrs)
			} else {
				libp2pOpts = append(libp2pOpts, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
			}
			h, err := libp2p.New(libp2pOpts...)
			require.NoError(t, err)
			return h
		},
	},
	{
		Name: "TCP / TLS / Yamux",
		HostGenerator: func(t *testing.T, opts TransportTestCaseOpts) host.Host {
			libp2pOpts := transformOpts(opts)
			libp2pOpts = append(libp2pOpts, libp2p.Security(tls.ID, tls.New))
			libp2pOpts = append(libp2pOpts, libp2p.Muxer(yamux.ID, yamux.DefaultTransport))
			if opts.NoListen {
				libp2pOpts = append(libp2pOpts, libp2p.NoListenAddrs)
			} else {
				libp2pOpts = append(libp2pOpts, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
			}
			h, err := libp2p.New(libp2pOpts...)
			require.NoError(t, err)
			return h
		},
	},
	{
		Name: "TCP / Noise / mplex",
		HostGenerator: func(t *testing.T, opts TransportTestCaseOpts) host.Host {
			libp2pOpts := transformOpts(opts)
			libp2pOpts = append(libp2pOpts, libp2p.Security(noise.ID, noise.New))
			libp2pOpts = append(libp2pOpts, libp2p.Muxer(mplex.ID, mplex.DefaultTransport))
			if opts.NoListen {
				libp2pOpts = append(libp2pOpts, libp2p.NoListenAddrs)
			} else {
				libp2pOpts = append(libp2pOpts, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
			}
			h, err := libp2p.New(libp2pOpts...)
			require.NoError(t, err)
			return h
		},
	},
	{
		Name: "WebSocket",
		HostGenerator: func(t *testing.T, opts TransportTestCaseOpts) host.Host {
			libp2pOpts := transformOpts(opts)
			if opts.NoListen {
				libp2pOpts = append(libp2pOpts, libp2p.NoListenAddrs)
			} else {
				libp2pOpts = append(libp2pOpts, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0/ws"))
			}
			h, err := libp2p.New(libp2pOpts...)
			require.NoError(t, err)
			return h
		},
	},
	{
		Name: "QUIC",
		HostGenerator: func(t *testing.T, opts TransportTestCaseOpts) host.Host {
			libp2pOpts := transformOpts(opts)
			if opts.NoListen {
				libp2pOpts = append(libp2pOpts, libp2p.NoListenAddrs)
			} else {
				libp2pOpts = append(libp2pOpts, libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"))
			}
			h, err := libp2p.New(libp2pOpts...)
			require.NoError(t, err)
			return h
		},
	},
	{
		Name: "WebTransport",
		HostGenerator: func(t *testing.T, opts TransportTestCaseOpts) host.Host {
			libp2pOpts := transformOpts(opts)
			if opts.NoListen {
				libp2pOpts = append(libp2pOpts, libp2p.NoListenAddrs)
			} else {
				libp2pOpts = append(libp2pOpts, libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1/webtransport"))
			}
			h, err := libp2p.New(libp2pOpts...)
			require.NoError(t, err)
			return h
		},
	},
}

func TestPing(t *testing.T) {
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			h1 := tc.HostGenerator(t, TransportTestCaseOpts{})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})

			require.NoError(t, h2.Connect(context.Background(), peer.AddrInfo{
				ID:    h1.ID(),
				Addrs: h1.Addrs(),
			}))

			ctx := context.Background()
			res := <-ping.Ping(ctx, h2, h1.ID())
			require.NoError(t, res.Error)
		})
	}
}

func TestBigPing(t *testing.T) {
	// 64k buffers
	sendBuf := make([]byte, 64<<10)
	recvBuf := make([]byte, 64<<10)
	const totalSends = 64

	// Fill with random bytes
	_, err := rand.Read(sendBuf)
	require.NoError(t, err)

	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			h1 := tc.HostGenerator(t, TransportTestCaseOpts{})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})

			require.NoError(t, h2.Connect(context.Background(), peer.AddrInfo{
				ID:    h1.ID(),
				Addrs: h1.Addrs(),
			}))

			h1.SetStreamHandler("/BIG-ping/1.0.0", func(s network.Stream) {
				io.Copy(s, s)
				s.Close()
			})

			errCh := make(chan error, 1)
			allocs := testing.AllocsPerRun(10, func() {
				s, err := h2.NewStream(context.Background(), h1.ID(), "/BIG-ping/1.0.0")
				require.NoError(t, err)
				defer s.Close()

				go func() {
					for i := 0; i < totalSends; i++ {
						_, err := io.ReadFull(s, recvBuf)
						if err != nil {
							errCh <- err
							return
						}
						if !bytes.Equal(sendBuf, recvBuf) {
							errCh <- fmt.Errorf("received data does not match sent data")
						}

					}
					_, err = s.Read([]byte{0})
					errCh <- err
				}()

				for i := 0; i < totalSends; i++ {
					s.Write(sendBuf)
				}
				s.CloseWrite()
				require.ErrorIs(t, <-errCh, io.EOF)
			})

			if int(allocs) > (len(sendBuf)*totalSends)/4 {
				t.Logf("Expected fewer allocs, got: %f", allocs)
			}
		})
	}
}

func TestManyStreams(t *testing.T) {
	const streamCount = 128
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			h1 := tc.HostGenerator(t, TransportTestCaseOpts{NoRcmgr: true})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true, NoRcmgr: true})

			require.NoError(t, h2.Connect(context.Background(), peer.AddrInfo{
				ID:    h1.ID(),
				Addrs: h1.Addrs(),
			}))

			h1.SetStreamHandler("echo", func(s network.Stream) {
				io.Copy(s, s)
				s.CloseWrite()
			})

			streams := make([]network.Stream, streamCount)
			for i := 0; i < streamCount; i++ {
				s, err := h2.NewStream(context.Background(), h1.ID(), "echo")
				require.NoError(t, err)
				streams[i] = s
			}

			wg := sync.WaitGroup{}
			wg.Add(streamCount)
			errCh := make(chan error, 1)
			for _, s := range streams {
				go func(s network.Stream) {
					defer wg.Done()

					s.Write([]byte("hello"))
					s.CloseWrite()
					b, err := io.ReadAll(s)
					if err == nil {
						if !bytes.Equal(b, []byte("hello")) {
							err = fmt.Errorf("received data does not match sent data")
						}
					}
					if err != nil {
						select {
						case errCh <- err:
						default:
						}
					}
				}(s)
			}
			wg.Wait()
			close(errCh)

			require.NoError(t, <-errCh)
			for _, s := range streams {
				require.NoError(t, s.Close())
			}
		})
	}
}

func TestListenerStreamResets(t *testing.T) {
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			h1 := tc.HostGenerator(t, TransportTestCaseOpts{})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})

			require.NoError(t, h2.Connect(context.Background(), peer.AddrInfo{
				ID:    h1.ID(),
				Addrs: h1.Addrs(),
			}))

			h1.SetStreamHandler("reset", func(s network.Stream) {
				s.Reset()
			})

			s, err := h2.NewStream(context.Background(), h1.ID(), "reset")
			if err != nil {
				require.ErrorIs(t, err, network.ErrReset)
				return
			}

			_, err = s.Read([]byte{0})
			require.ErrorIs(t, err, network.ErrReset)
		})
	}
}

func TestDialerStreamResets(t *testing.T) {
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			h1 := tc.HostGenerator(t, TransportTestCaseOpts{})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})

			require.NoError(t, h2.Connect(context.Background(), peer.AddrInfo{
				ID:    h1.ID(),
				Addrs: h1.Addrs(),
			}))

			errCh := make(chan error, 1)
			acceptedCh := make(chan struct{}, 1)
			h1.SetStreamHandler("echo", func(s network.Stream) {
				acceptedCh <- struct{}{}
				_, err := io.Copy(s, s)
				errCh <- err
			})

			s, err := h2.NewStream(context.Background(), h1.ID(), "echo")
			require.NoError(t, err)
			s.Write([]byte{})
			<-acceptedCh
			s.Reset()
			require.ErrorIs(t, <-errCh, network.ErrReset)
		})
	}
}

func TestStreamReadDeadline(t *testing.T) {
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			h1 := tc.HostGenerator(t, TransportTestCaseOpts{})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})

			require.NoError(t, h2.Connect(context.Background(), peer.AddrInfo{
				ID:    h1.ID(),
				Addrs: h1.Addrs(),
			}))

			h1.SetStreamHandler("echo", func(s network.Stream) {
				io.Copy(s, s)
			})

			s, err := h2.NewStream(context.Background(), h1.ID(), "echo")
			require.NoError(t, err)
			require.NoError(t, s.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
			_, err = s.Read([]byte{0})
			require.Error(t, err)
			require.Contains(t, err.Error(), "deadline")
			nerr, ok := err.(net.Error)
			require.True(t, ok, "expected a net.Error")
			require.True(t, nerr.Timeout(), "expected net.Error.Timeout() == true")
			// now test that the stream is still usable
			s.SetReadDeadline(time.Time{})
			_, err = s.Write([]byte("foobar"))
			require.NoError(t, err)
			b := make([]byte, 6)
			_, err = s.Read(b)
			require.Equal(t, "foobar", string(b))
			require.NoError(t, err)
		})
	}
}
