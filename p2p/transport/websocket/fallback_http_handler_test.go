package websocket

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
	"github.com/libp2p/go-libp2p/core/network"
	"golang.org/x/net/http2"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

// listenerHostPort returns a "host:port" string for a listener multiaddr,
// suitable for building an HTTP URL.
func listenerHostPort(t *testing.T, m ma.Multiaddr) string {
	t.Helper()
	host, err := m.ValueForProtocol(ma.P_IP4)
	require.NoError(t, err)
	port, err := m.ValueForProtocol(ma.P_TCP)
	require.NoError(t, err)
	return net.JoinHostPort(host, port)
}

// httpsClient returns an http.Client that trusts self-signed certs and
// negotiates the requested ALPN protocols. Pass {"h2", "http/1.1"} to allow
// HTTP/2 with HTTP/1.1 fallback, or {"http/1.1"} to force HTTP/1.1 only.
//
// Note: the Go http.Transport adds h2 itself when TLSClientConfig.NextProtos
// is empty, which is why we always set NextProtos explicitly here.
func httpsClient(nextProtos []string) *http.Client {
	tlsConf := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         nextProtos,
	}
	tr := &http.Transport{
		ForceAttemptHTTP2: slices.Contains(nextProtos, "h2"),
		TLSClientConfig:   tlsConf,
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: tr}
}

// startWSSListener spins up a /tls/ws listener bound to 127.0.0.1 with the
// given fallback handler (may be nil). Returns the host:port string of the
// listener; the listener is closed via t.Cleanup.
func startWSSListener(t *testing.T, fallback http.Handler) string {
	t.Helper()
	tlsConf := getTLSConf(t, net.ParseIP("127.0.0.1"), time.Now(), time.Now().Add(time.Hour))
	_, u := newSecureUpgrader(t)
	opts := []Option{WithTLSConfig(tlsConf)}
	if fallback != nil {
		opts = append(opts, WithFallbackHTTPHandler(fallback))
	}
	tpt, err := New(u, &network.NullResourceManager{}, nil, opts...)
	require.NoError(t, err)

	l, err := tpt.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0/tls/ws"))
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })

	// Drain accepted libp2p conns so they don't deadlock the listener.
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	return listenerHostPort(t, l.Multiaddr())
}

// TestFallbackHTTPHandler_TLS_HTTP1 confirms that a /tls/ws listener serves
// the fallback handler over HTTPS/1.1 too. The transport does not pre-screen
// HTTP versions; maximal interop with curl, legacy clients, and any HTTP-only
// tooling is the priority. Multiplexing-sensitive clients should opt into h2
// by offering it in ALPN; this listener will accept whichever ALPN they pick.
func TestFallbackHTTPHandler_TLS_HTTP1(t *testing.T) {
	const body = "hello from fallback"
	var hits atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("X-Test", "ok")
		fmt.Fprint(w, body)
	})

	hostPort := startWSSListener(t, handler)

	resp, err := httpsClient([]string{"http/1.1"}).Get("https://" + hostPort + "/anything")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, "HTTP/1.1", resp.Proto)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "ok", resp.Header.Get("X-Test"))
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(got))
	require.Equal(t, int32(1), hits.Load())
}

// TestFallbackHTTPHandler_HTTP2 confirms ALPN selects "h2" and the fallback
// handler still serves the request. This is the censorship-resistance shape:
// HTTP/2 traffic on the same port as /tls/ws.
func TestFallbackHTTPHandler_HTTP2(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "proto=%s", r.Proto)
	})

	hostPort := startWSSListener(t, handler)

	resp, err := httpsClient([]string{"h2", "http/1.1"}).Get("https://" + hostPort + "/h2-test")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, "HTTP/2.0", resp.Proto, "client should negotiate h2 via ALPN")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "proto=HTTP/2.0", string(got))
}

// TestFallbackHTTPHandler_WebSocketStillWorks confirms that installing a
// fallback handler does not break the original /tls/ws traffic.
func TestFallbackHTTPHandler_WebSocketStillWorks(t *testing.T) {
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("fallback should not be called for a websocket upgrade, got %s %s", r.Method, r.URL.Path)
	})

	hostPort := startWSSListener(t, handler)

	dialer := gws.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
		HandshakeTimeout: 5 * time.Second,
	}
	conn, _, err := dialer.Dial("wss://"+hostPort+"/", nil)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

// TestFallbackHTTPHandler_NotSetReturns404 preserves the historical behaviour:
// if no fallback handler is configured, non-upgrade requests get a 404.
func TestFallbackHTTPHandler_NotSetReturns404(t *testing.T) {
	hostPort := startWSSListener(t, nil)

	resp, err := httpsClient([]string{"http/1.1"}).Get("https://" + hostPort + "/")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestFallbackHTTPHandler_PlainWS_H2C confirms that a plain /ws listener
// also serves the fallback handler over HTTP/2 cleartext (h2c). This is
// what reverse proxies that speak h2c to backends (Caddy, Traefik, nginx
// with the right directives) use to get connection multiplexing all the
// way to the kubo node, instead of degrading to HTTP/1.1 just because the
// last hop is plaintext.
func TestFallbackHTTPHandler_PlainWS_H2C(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "proto=%s", r.Proto)
	})

	_, u := newSecureUpgrader(t)
	tpt, err := New(u, &network.NullResourceManager{}, nil, WithFallbackHTTPHandler(handler))
	require.NoError(t, err)

	l, err := tpt.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0/ws"))
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })

	hostPort := listenerHostPort(t, l.Multiaddr())

	// http2.Transport with AllowHTTP and a custom DialTLSContext that does
	// a plain TCP dial is the canonical way to drive prior-knowledge h2c
	// from a Go client.
	h2cTransport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(_ context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return net.Dial(network, addr)
		},
	}
	defer h2cTransport.CloseIdleConnections()
	client := &http.Client{Transport: h2cTransport, Timeout: 5 * time.Second}

	resp, err := client.Get("http://" + hostPort + "/h2c-test")
	require.NoError(t, err, "h2c request must reach the fallback handler")
	defer resp.Body.Close()

	require.Equal(t, "HTTP/2.0", resp.Proto, "client should be talking h2c, not h1")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "proto=HTTP/2.0", string(got))
}

// TestFallbackHTTPHandler_PlainWS confirms that on a plain /ws listener
// (no TLS), the fallback handler accepts HTTP/1.1 traffic. The HTTP/2
// requirement only applies to /tls/ws listeners; plain /ws is meant to sit
// behind a reverse proxy that terminates TLS upstream and may forward
// HTTP/1.1.
func TestFallbackHTTPHandler_PlainWS(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "plain")
	})

	_, u := newSecureUpgrader(t)
	tpt, err := New(u, &network.NullResourceManager{}, nil, WithFallbackHTTPHandler(handler))
	require.NoError(t, err)

	l, err := tpt.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0/ws"))
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })

	hostPort := listenerHostPort(t, l.Multiaddr())
	resp, err := http.Get("http://" + hostPort + "/")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "plain", string(got))
}

// TestModernBrowserWSSFlow is the load-bearing test for "browsers must keep
// being able to open wss:// against this listener". This is a hard
// requirement when the listener also serves an HTTP/2 fallback handler.
//
// What modern browsers (Chrome, Firefox, Safari) do for `wss://`:
//
//  1. Open a TLS handshake offering ALPN ["h2", "http/1.1"].
//  2. If the server picks "h2", read the server's first SETTINGS frame and
//     check for SETTINGS_ENABLE_CONNECT_PROTOCOL=1 (RFC 8441 §3). If absent,
//     close the HTTP/2 connection.
//  3. Open a brand-new TLS handshake offering ALPN ["http/1.1"] only and
//     perform the classic HTTP/1.1 Upgrade dance.
//
// This listener does not (and cannot today, see the comment in newListener)
// advertise ENABLE_CONNECT_PROTOCOL, so the contract is:
//
//   - The h2 SETTINGS frame on the first attempt MUST NOT contain
//     SettingEnableConnectProtocol; this is the signal browsers use to know
//     they should fall back.
//   - The HTTP/1.1 attempt MUST succeed — the WebSocket upgrade goes through
//     gorilla as before.
//
// If either check fails, browser wss:// breaks. Do not relax this test
// without also updating gorilla/websocket and the listener to actually
// handle WS-over-HTTP/2 (RFC 8441 ext-CONNECT) end to end.
func TestModernBrowserWSSFlow(t *testing.T) {
	hostPort := startWSSListener(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "fallback")
	}))

	// Step 1: simulate the browser's first TLS handshake.
	t.Run("h2_does_not_advertise_ext_connect", func(t *testing.T) {
		conn, err := tls.Dial("tcp", hostPort, &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{"h2", "http/1.1"},
		})
		require.NoError(t, err)
		defer conn.Close()
		require.Equal(t, "h2", conn.ConnectionState().NegotiatedProtocol,
			"server must select h2 when client offers it; this is the path browsers take")

		// Send the HTTP/2 client preface plus our own (empty) SETTINGS so
		// the server is free to write its own SETTINGS frame back.
		_, err = conn.Write([]byte(http2.ClientPreface))
		require.NoError(t, err)
		framer := http2.NewFramer(conn, conn)
		require.NoError(t, framer.WriteSettings())

		// Read frames until we see the server's initial (non-ACK) SETTINGS.
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
		var serverSettings *http2.SettingsFrame
		for i := 0; i < 8 && serverSettings == nil; i++ {
			f, err := framer.ReadFrame()
			require.NoError(t, err, "did not receive server SETTINGS in time")
			if sf, ok := f.(*http2.SettingsFrame); ok && !sf.IsAck() {
				serverSettings = sf
			}
		}
		require.NotNil(t, serverSettings, "server never sent a non-ACK SETTINGS frame")

		_, hasExtConnect := serverSettings.Value(http2.SettingEnableConnectProtocol)
		require.False(t, hasExtConnect,
			"server must NOT advertise SETTINGS_ENABLE_CONNECT_PROTOCOL; browsers rely on its absence to fall back to HTTP/1.1 for wss://")
	})

	// Step 2: simulate the browser's second attempt with ALPN restricted to
	// HTTP/1.1, where the WebSocket upgrade actually happens.
	t.Run("http1_fallback_wss_upgrade_succeeds", func(t *testing.T) {
		dialer := gws.Dialer{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				NextProtos:         []string{"http/1.1"},
			},
			HandshakeTimeout: 5 * time.Second,
		}
		conn, resp, err := dialer.Dial("wss://"+hostPort+"/", nil)
		require.NoError(t, err, "browser HTTP/1.1 fallback must succeed; this is the actual WSS path")
		defer conn.Close()
		require.Equal(t, "HTTP/1.1", resp.Proto)
		require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	})
}

// TestFallbackHTTPHandler_HTTP2NeverReachesWSPath locks down the dispatch
// invariant that lets h2 and WSS coexist on the same listener: every HTTP/2
// request must reach the fallback handler, never the WebSocket upgrade path.
//
// This holds today because gorilla/websocket's IsWebSocketUpgrade only looks
// at HTTP/1 Upgrade headers, which the HTTP/2 framer (RFC 7540 §8.1.2.2)
// refuses to carry. If a future version of gorilla/websocket adds RFC 8441
// ("Bootstrapping WebSockets with HTTP/2", extended CONNECT) and silently
// extends IsWebSocketUpgrade to detect it, this test will start failing,
// which is the right cue to revisit the dispatch comment in
// listener.ServeHTTP and decide whether to embrace WS-over-h2 or pin the
// behaviour.
func TestFallbackHTTPHandler_HTTP2NeverReachesWSPath(t *testing.T) {
	var fallbackHits atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		// Record the protocol so a regression makes the failure obvious.
		w.Header().Set("X-Proto", r.Proto)
		fmt.Fprint(w, "fallback")
	})

	hostPort := startWSSListener(t, handler)

	// Force ALPN to h2 only, so the connection MUST be HTTP/2 or fail.
	resp, err := httpsClient([]string{"h2"}).Get("https://" + hostPort + "/")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, "HTTP/2.0", resp.Proto)
	require.Equal(t, "HTTP/2.0", resp.Header.Get("X-Proto"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, int32(1), fallbackHits.Load(),
		"HTTP/2 request must reach the fallback handler, not the WebSocket upgrade path")
}

// TestFallbackHTTPHandler_KeepAlive ensures the handshake-timeout AfterFunc
// is disarmed once a fallback request arrives. Otherwise the connection
// would be force-closed at handshakeTimeout (default 15s) regardless of
// active traffic.
//
// We exercise this with two HTTP/2 requests on a single shared connection,
// spaced further apart than the handshake-timeout window. HTTP/2 keeps both
// streams on one TCP connection by design, so if the timer were not
// disarmed, the second request would race against the closer.
func TestFallbackHTTPHandler_KeepAlive(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "ok")
	})

	tlsConf := getTLSConf(t, net.ParseIP("127.0.0.1"), time.Now(), time.Now().Add(time.Hour))
	_, u := newSecureUpgrader(t)
	tpt, err := New(u, &network.NullResourceManager{}, nil,
		WithTLSConfig(tlsConf),
		WithHandshakeTimeout(200*time.Millisecond),
		WithFallbackHTTPHandler(handler),
	)
	require.NoError(t, err)
	l, err := tpt.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0/tls/ws"))
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	hostPort := listenerHostPort(t, l.Multiaddr())

	tr := &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{"h2"},
		},
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	resp, err := client.Get("https://" + hostPort + "/first")
	require.NoError(t, err)
	require.Equal(t, "HTTP/2.0", resp.Proto)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Wait past the handshake-timeout window. If Unwrap had not been
	// called, the AfterFunc would have closed the underlying TCP
	// connection that the HTTP/2 transport pools.
	time.Sleep(400 * time.Millisecond)

	resp, err = client.Get("https://" + hostPort + "/second")
	require.NoError(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "HTTP/2.0", resp.Proto, "second request must reuse the same h2 connection")
}
