package tcpreuse

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/multiformats/go-multistream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func selfSignedTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	certTemplate := x509.Certificate{
		SerialNumber: &big.Int{},
		Subject: pkix.Name{
			Organization: []string{"Test"},
		},
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &certTemplate, &certTemplate, &priv.PublicKey, priv)
	require.NoError(t, err)

	cert := tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	return tlsConfig
}

type wsHandler struct{ conns chan *websocket.Conn }

func (wh wsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := websocket.Upgrader{}
	c, _ := u.Upgrade(w, r, http.Header{})
	wh.conns <- c
}

func TestListenerSingle(t *testing.T) {
	listenAddr := ma.StringCast("/ip4/0.0.0.0/tcp/0")
	const N = 64
	for _, enableReuseport := range []bool{true, false} {
		t.Run(fmt.Sprintf("multistream-reuseport:%v", enableReuseport), func(t *testing.T) {
			cm := NewConnMgr(enableReuseport, nil, nil)
			l, err := cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_MultistreamSelect)
			require.NoError(t, err)
			go func() {
				d := net.Dialer{}
				for i := 0; i < N; i++ {
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						conn, err := d.DialContext(ctx, l.Addr().Network(), l.Addr().String())
						if err != nil {
							t.Error("failed to dial", err, i)
							return
						}
						lconn := multistream.NewMSSelect(conn, "a")
						buf := make([]byte, 10)
						_, err = lconn.Write([]byte("hello-multistream"))
						if err != nil {
							t.Error(err)
						}
						_, err = lconn.Read(buf)
						if err == nil {
							t.Error("expected EOF got nil")
						}
					}()
				}
			}()

			var wg sync.WaitGroup
			for i := 0; i < N; i++ {
				c, err := l.Accept()
				require.NoError(t, err)
				wg.Add(1)
				go func() {
					defer wg.Done()
					cc := multistream.NewMSSelect(c, "a")
					defer cc.Close()
					buf := make([]byte, 30)
					n, err := cc.Read(buf)
					if !assert.NoError(t, err) {
						return
					}
					if !assert.Equal(t, "hello-multistream", string(buf[:n])) {
						return
					}
				}()
			}
			wg.Wait()
		})

		t.Run(fmt.Sprintf("WebSocket-reuseport:%v", enableReuseport), func(t *testing.T) {
			cm := NewConnMgr(enableReuseport, nil, nil)
			l, err := cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_HTTP)
			require.NoError(t, err)
			wh := wsHandler{conns: make(chan *websocket.Conn, acceptQueueSize)}
			go func() {
				http.Serve(manet.NetListener(l), wh)
			}()
			go func() {
				d := websocket.Dialer{}
				for i := 0; i < N; i++ {
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						conn, _, err := d.DialContext(ctx, fmt.Sprintf("ws://%s", l.Addr().String()), http.Header{})
						if err != nil {
							t.Error("failed to dial", err, i)
							return
						}
						err = conn.WriteMessage(websocket.TextMessage, []byte("hello"))
						if err != nil {
							t.Error(err)
						}
						_, _, err = conn.ReadMessage()
						if err == nil {
							t.Error("expected EOF got nil")
						}
					}()
				}
			}()
			var wg sync.WaitGroup
			for i := 0; i < N; i++ {
				c := <-wh.conns
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer c.Close()
					msgType, buf, err := c.ReadMessage()
					if !assert.NoError(t, err) {
						return
					}
					if !assert.Equal(t, msgType, websocket.TextMessage) {
						return
					}
					if !assert.Equal(t, "hello", string(buf)) {
						return
					}
				}()
			}
			wg.Wait()
		})

		t.Run(fmt.Sprintf("WebSocketTLS-reuseport:%v", enableReuseport), func(t *testing.T) {
			cm := NewConnMgr(enableReuseport, nil, nil)
			l, err := cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_TLS)
			require.NoError(t, err)
			defer l.Close()
			wh := wsHandler{conns: make(chan *websocket.Conn, acceptQueueSize)}
			go func() {
				s := http.Server{Handler: wh, TLSConfig: selfSignedTLSConfig(t)}
				s.ServeTLS(manet.NetListener(l), "", "")
			}()
			go func() {
				d := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
				for i := 0; i < N; i++ {
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						conn, _, err := d.DialContext(ctx, fmt.Sprintf("wss://%s", l.Addr().String()), http.Header{})
						if err != nil {
							t.Error("failed to dial", err, i)
							return
						}
						err = conn.WriteMessage(websocket.TextMessage, []byte("hello"))
						if err != nil {
							t.Error(err)
						}
						_, _, err = conn.ReadMessage()
						if err == nil {
							t.Error("expected EOF got nil")
						}
					}()
				}
			}()
			var wg sync.WaitGroup
			for i := 0; i < N; i++ {
				c := <-wh.conns
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer c.Close()
					msgType, buf, err := c.ReadMessage()
					if !assert.NoError(t, err) {
						return
					}
					if !assert.Equal(t, msgType, websocket.TextMessage) {
						return
					}
					if !assert.Equal(t, "hello", string(buf)) {
						return
					}
				}()
			}
			wg.Wait()
		})
	}
}

func TestListenerMultiplexed(t *testing.T) {
	listenAddr := ma.StringCast("/ip4/0.0.0.0/tcp/0")
	const N = 20
	for _, enableReuseport := range []bool{true, false} {
		cm := NewConnMgr(enableReuseport, nil, nil)
		msl, err := cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_MultistreamSelect)
		require.NoError(t, err)
		defer msl.Close()

		wsl, err := cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_HTTP)
		require.NoError(t, err)
		defer wsl.Close()
		require.Equal(t, wsl.Multiaddr(), msl.Multiaddr())
		wh := wsHandler{conns: make(chan *websocket.Conn, acceptQueueSize)}
		go func() {
			http.Serve(manet.NetListener(wsl), wh)
		}()

		wssl, err := cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_TLS)
		require.NoError(t, err)
		defer wssl.Close()
		require.Equal(t, wssl.Multiaddr(), wsl.Multiaddr())
		whs := wsHandler{conns: make(chan *websocket.Conn, acceptQueueSize)}
		go func() {
			s := http.Server{Handler: whs, TLSConfig: selfSignedTLSConfig(t)}
			s.ServeTLS(manet.NetListener(wssl), "", "")
		}()

		// multistream connections
		go func() {
			d := net.Dialer{}
			for i := 0; i < N; i++ {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					conn, err := d.DialContext(ctx, msl.Addr().Network(), msl.Addr().String())
					if err != nil {
						t.Error("failed to dial", err, i)
						return
					}
					lconn := multistream.NewMSSelect(conn, "a")
					buf := make([]byte, 10)
					_, err = lconn.Write([]byte("multistream"))
					if err != nil {
						t.Error(err)
					}
					_, err = lconn.Read(buf)
					if err == nil {
						t.Error("expected EOF got nil")
					}
				}()
			}
		}()

		// ws connections
		go func() {
			d := websocket.Dialer{}
			for i := 0; i < N; i++ {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					conn, _, err := d.DialContext(ctx, fmt.Sprintf("ws://%s", msl.Addr().String()), http.Header{})
					if err != nil {
						t.Error("failed to dial", err, i)
						return
					}
					err = conn.WriteMessage(websocket.TextMessage, []byte("websocket"))
					if err != nil {
						t.Error(err)
					}
					_, _, err = conn.ReadMessage()
					if err == nil {
						t.Error("expected EOF got nil")
					}
				}()
			}
		}()

		// wss connections
		go func() {
			d := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
			for i := 0; i < N; i++ {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					conn, _, err := d.DialContext(ctx, fmt.Sprintf("wss://%s", msl.Addr().String()), http.Header{})
					if err != nil {
						t.Error("failed to dial", err, i)
						return
					}
					err = conn.WriteMessage(websocket.TextMessage, []byte("websocket-tls"))
					if err != nil {
						t.Error(err)
					}
					_, _, err = conn.ReadMessage()
					if err == nil {
						t.Error("expected EOF got nil")
					}
				}()
			}
		}()

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < N; i++ {
				c, err := msl.Accept()
				if !assert.NoError(t, err) {
					return
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					cc := multistream.NewMSSelect(c, "a")
					defer cc.Close()
					buf := make([]byte, 20)
					n, err := cc.Read(buf)
					if !assert.NoError(t, err) {
						return
					}
					if !assert.Equal(t, "multistream", string(buf[:n])) {
						return
					}
				}()
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < N; i++ {
				c := <-wh.conns
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer c.Close()
					msgType, buf, err := c.ReadMessage()
					if !assert.NoError(t, err) {
						return
					}
					if !assert.Equal(t, msgType, websocket.TextMessage) {
						return
					}
					if !assert.Equal(t, "websocket", string(buf)) {
						return
					}
				}()
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < N; i++ {
				c := <-whs.conns
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer c.Close()
					msgType, buf, err := c.ReadMessage()
					if !assert.NoError(t, err) {
						return
					}
					if !assert.Equal(t, msgType, websocket.TextMessage) {
						return
					}
					if !assert.Equal(t, "websocket-tls", string(buf)) {
						return
					}
				}()
			}
		}()
		wg.Wait()
	}
}

func TestListenerClose(t *testing.T) {
	testClose := func(listenAddr ma.Multiaddr) {
		// listen on port 0
		cm := NewConnMgr(false, nil, nil)
		ml, err := cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_MultistreamSelect)
		require.NoError(t, err)
		wl, err := cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_HTTP)
		require.NoError(t, err)
		require.Equal(t, wl.Multiaddr(), ml.Multiaddr())
		wl.Close()

		wl, err = cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_HTTP)
		require.NoError(t, err)
		require.Equal(t, wl.Multiaddr(), ml.Multiaddr())

		ml.Close()

		mll, err := cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_MultistreamSelect)
		require.NoError(t, err)
		require.Equal(t, wl.Multiaddr(), ml.Multiaddr())

		mll.Close()
		wl.Close()

		ml, err = cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_MultistreamSelect)
		require.NoError(t, err)

		// Now listen on the specific port previously used
		listenAddr = ml.Multiaddr()
		wl, err = cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_HTTP)
		require.NoError(t, err)
		require.Equal(t, wl.Multiaddr(), ml.Multiaddr())
		wl.Close()

		wl, err = cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_HTTP)
		require.NoError(t, err)
		require.Equal(t, wl.Multiaddr(), ml.Multiaddr())

		ml.Close()
		wl.Close()
	}
	listenAddrs := []ma.Multiaddr{ma.StringCast("/ip4/0.0.0.0/tcp/0"), ma.StringCast("/ip6/::/tcp/0")}
	for _, listenAddr := range listenAddrs {
		testClose(listenAddr)
	}
}

func setDeferReset[T any](t testing.TB, ptr *T, val T) {
	t.Helper()
	orig := *ptr
	*ptr = val
	t.Cleanup(func() { *ptr = orig })
}

// TestHitTimeout asserts that we don't panic in case we fail to peek at the connection.
func TestHitTimeout(t *testing.T) {
	setDeferReset(t, &identifyConnTimeout, 100*time.Millisecond)
	// listen on port 0
	cm := NewConnMgr(false, nil, nil)

	listenAddr := ma.StringCast("/ip4/127.0.0.1/tcp/0")
	ml, err := cm.DemultiplexedListen(listenAddr, DemultiplexedConnType_MultistreamSelect)
	require.NoError(t, err)
	defer ml.Close()

	tcpConn, err := net.Dial(ml.Addr().Network(), ml.Addr().String())
	require.NoError(t, err)

	// Stall tcp conn for over the timeout.
	time.Sleep(identifyConnTimeout + 100*time.Millisecond)

	tcpConn.Close()
}
