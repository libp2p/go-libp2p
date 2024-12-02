package libp2phttp_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	host "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2phttp "github.com/libp2p/go-libp2p/p2p/http"
	httpping "github.com/libp2p/go-libp2p/p2p/http/ping"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPOverStreams(t *testing.T) {
	serverHost, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
	)
	require.NoError(t, err)

	httpHost := libp2phttp.Host{StreamHost: serverHost}

	httpHost.SetHTTPHandler("/hello", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))

	// Start server
	go httpHost.Serve()
	defer httpHost.Close()

	// Start client
	clientHost, err := libp2p.New(libp2p.NoListenAddrs)
	require.NoError(t, err)
	clientHost.Connect(context.Background(), peer.AddrInfo{
		ID:    serverHost.ID(),
		Addrs: serverHost.Addrs(),
	})

	clientRT, err := (&libp2phttp.Host{StreamHost: clientHost}).NewConstrainedRoundTripper(peer.AddrInfo{ID: serverHost.ID()})
	require.NoError(t, err)

	client := &http.Client{Transport: clientRT}

	resp, err := client.Get("/hello")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, "hello", string(body))
}

func TestHTTPOverStreamsSendsConnectionClose(t *testing.T) {
	serverHost, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
	)
	require.NoError(t, err)

	httpHost := libp2phttp.Host{StreamHost: serverHost}

	connectionHeaderVal := make(chan string, 1)
	httpHost.SetHTTPHandlerAtPath("/hello", "/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
		connectionHeaderVal <- r.Header.Get("Connection")
	}))

	// Start server
	go httpHost.Serve()
	defer httpHost.Close()

	// run client
	clientHost, err := libp2p.New(libp2p.NoListenAddrs)
	require.NoError(t, err)
	clientHost.Connect(context.Background(), peer.AddrInfo{
		ID:    serverHost.ID(),
		Addrs: serverHost.Addrs(),
	})
	clientHttpHost := libp2phttp.Host{StreamHost: clientHost}
	rt, err := clientHttpHost.NewConstrainedRoundTripper(peer.AddrInfo{ID: serverHost.ID()})
	require.NoError(t, err)
	client := &http.Client{Transport: rt}
	_, err = client.Get("/")
	require.NoError(t, err)

	select {
	case val := <-connectionHeaderVal:
		require.Equal(t, "close", strings.ToLower(val))
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for connection header")
	}
}

func TestHTTPOverStreamsContextAndClientTimeout(t *testing.T) {
	const clientTimeout = 200 * time.Millisecond

	serverHost, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
	)
	require.NoError(t, err)

	httpHost := libp2phttp.Host{StreamHost: serverHost}
	httpHost.SetHTTPHandler("/hello/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * clientTimeout)
		w.Write([]byte("hello"))
	}))

	// Start server
	go httpHost.Serve()
	defer httpHost.Close()

	// Start client
	clientHost, err := libp2p.New(libp2p.NoListenAddrs)
	require.NoError(t, err)
	clientHost.Connect(context.Background(), peer.AddrInfo{
		ID:    serverHost.ID(),
		Addrs: serverHost.Addrs(),
	})

	clientRT, err := (&libp2phttp.Host{StreamHost: clientHost}).NewConstrainedRoundTripper(peer.AddrInfo{ID: serverHost.ID()})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), clientTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/hello/", nil)
	require.NoError(t, err)

	client := &http.Client{Transport: clientRT}
	_, err = client.Do(req)
	require.Error(t, err)
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	t.Log("OK, deadline exceeded waiting for response as expected")

	// Make another request, this time using http.Client.Timeout.
	clientRT, err = (&libp2phttp.Host{StreamHost: clientHost}).NewConstrainedRoundTripper(peer.AddrInfo{ID: serverHost.ID()})
	require.NoError(t, err)

	client = &http.Client{
		Transport: clientRT,
		Timeout:   clientTimeout,
	}

	_, err = client.Get("/hello/")
	require.Error(t, err)
	var uerr *url.Error
	require.ErrorAs(t, err, &uerr)
	require.True(t, uerr.Timeout())
	t.Log("OK, timed out waiting for response as expected")
}

func TestHTTPOverStreamsReturnsConnectionClose(t *testing.T) {
	serverHost, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
	)
	require.NoError(t, err)

	httpHost := libp2phttp.Host{StreamHost: serverHost}

	httpHost.SetHTTPHandlerAtPath("/hello", "/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))

	// Start server
	go httpHost.Serve()
	defer httpHost.Close()

	// Start client
	clientHost, err := libp2p.New(libp2p.NoListenAddrs)
	require.NoError(t, err)
	clientHost.Connect(context.Background(), peer.AddrInfo{
		ID:    serverHost.ID(),
		Addrs: serverHost.Addrs(),
	})

	s, err := clientHost.NewStream(context.Background(), serverHost.ID(), libp2phttp.ProtocolIDForMultistreamSelect)
	require.NoError(t, err)
	_, err = s.Write([]byte("GET / HTTP/1.1\r\nHost: \r\n\r\n"))
	require.NoError(t, err)

	out := make([]byte, 1024)
	n, err := s.Read(out)
	if err != io.EOF {
		require.NoError(t, err)
	}

	require.Contains(t, strings.ToLower(string(out[:n])), "connection: close")
}

func TestRoundTrippers(t *testing.T) {
	serverHost, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
	)
	require.NoError(t, err)

	httpHost := libp2phttp.Host{
		InsecureAllowHTTP: true,
		StreamHost:        serverHost,
		ListenAddrs:       []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/http")},
	}

	httpHost.SetHTTPHandler("/hello", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))

	// Start stream based server
	go httpHost.Serve()
	defer httpHost.Close()

	serverMultiaddrs := httpHost.Addrs()
	serverHTTPAddr := serverMultiaddrs[1]

	testCases := []struct {
		name                     string
		setupRoundTripper        func(t *testing.T, clientStreamHost host.Host, clientHTTPHost *libp2phttp.Host) http.RoundTripper
		expectStreamRoundTripper bool
	}{
		{
			name: "HTTP preferred",
			setupRoundTripper: func(t *testing.T, clientStreamHost host.Host, clientHTTPHost *libp2phttp.Host) http.RoundTripper {
				rt, err := clientHTTPHost.NewConstrainedRoundTripper(peer.AddrInfo{
					ID:    serverHost.ID(),
					Addrs: serverMultiaddrs,
				}, libp2phttp.PreferHTTPTransport)
				require.NoError(t, err)
				return rt
			},
		},
		{
			name: "HTTP first",
			setupRoundTripper: func(t *testing.T, clientStreamHost host.Host, clientHTTPHost *libp2phttp.Host) http.RoundTripper {
				rt, err := clientHTTPHost.NewConstrainedRoundTripper(peer.AddrInfo{
					ID:    serverHost.ID(),
					Addrs: []ma.Multiaddr{serverHTTPAddr, serverHost.Addrs()[0]},
				})
				require.NoError(t, err)
				return rt
			},
		},
		{
			name: "No HTTP transport",
			setupRoundTripper: func(t *testing.T, clientStreamHost host.Host, clientHTTPHost *libp2phttp.Host) http.RoundTripper {
				rt, err := clientHTTPHost.NewConstrainedRoundTripper(peer.AddrInfo{
					ID:    serverHost.ID(),
					Addrs: []ma.Multiaddr{serverHost.Addrs()[0]},
				})
				require.NoError(t, err)
				return rt
			},
			expectStreamRoundTripper: true,
		},
		{
			name: "Stream transport first",
			setupRoundTripper: func(t *testing.T, clientStreamHost host.Host, clientHTTPHost *libp2phttp.Host) http.RoundTripper {
				rt, err := clientHTTPHost.NewConstrainedRoundTripper(peer.AddrInfo{
					ID:    serverHost.ID(),
					Addrs: []ma.Multiaddr{serverHost.Addrs()[0], serverHTTPAddr},
				})
				require.NoError(t, err)
				return rt
			},
			expectStreamRoundTripper: true,
		},
		{
			name: "Existing stream transport connection",
			setupRoundTripper: func(t *testing.T, clientStreamHost host.Host, clientHTTPHost *libp2phttp.Host) http.RoundTripper {
				clientStreamHost.Connect(context.Background(), peer.AddrInfo{
					ID:    serverHost.ID(),
					Addrs: serverHost.Addrs(),
				})
				rt, err := clientHTTPHost.NewConstrainedRoundTripper(peer.AddrInfo{
					ID:    serverHost.ID(),
					Addrs: []ma.Multiaddr{serverHTTPAddr, serverHost.Addrs()[0]},
				})
				require.NoError(t, err)
				return rt
			},
			expectStreamRoundTripper: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Start client
			clientStreamHost, err := libp2p.New(libp2p.NoListenAddrs)
			require.NoError(t, err)
			defer clientStreamHost.Close()

			clientHttpHost := &libp2phttp.Host{StreamHost: clientStreamHost}

			rt := tc.setupRoundTripper(t, clientStreamHost, clientHttpHost)
			if tc.expectStreamRoundTripper {
				// Hack to get the private type of this roundtripper
				typ := reflect.TypeOf(rt).String()
				require.Contains(t, typ, "streamRoundTripper", "Expected stream based round tripper")
			}

			for _, tc := range []bool{true, false} {
				name := ""
				if tc {
					name = "with namespaced roundtripper"
				}
				t.Run(name, func(t *testing.T) {
					var resp *http.Response
					var err error
					if tc {
						var h libp2phttp.Host
						require.NoError(t, err)
						nrt, err := h.NamespaceRoundTripper(rt, "/hello", serverHost.ID())
						require.NoError(t, err)
						client := &http.Client{Transport: nrt}
						resp, err = client.Get("/")
						require.NoError(t, err)
					} else {
						client := &http.Client{Transport: rt}
						resp, err = client.Get("/hello/")
						require.NoError(t, err)
					}
					defer resp.Body.Close()

					body, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					require.Equal(t, "hello", string(body))
				})
			}

			// Read the well-known resource
			wk, err := rt.(libp2phttp.PeerMetadataGetter).GetPeerMetadata()
			require.NoError(t, err)

			expectedMap := make(libp2phttp.PeerMeta)
			expectedMap["/hello"] = libp2phttp.ProtocolMeta{Path: "/hello/"}
			require.Equal(t, expectedMap, wk)
		})
	}
}

func TestPlainOldHTTPServer(t *testing.T) {
	mux := http.NewServeMux()
	wk := libp2phttp.WellKnownHandler{}
	mux.Handle(libp2phttp.WellKnownProtocols, &wk)

	mux.Handle("/ping/", httpping.Ping{})
	wk.AddProtocolMeta(httpping.PingProtocolID, libp2phttp.ProtocolMeta{Path: "/ping/"})

	server := &http.Server{Addr: "127.0.0.1:0", Handler: mux}

	l, err := net.Listen("tcp", server.Addr)
	require.NoError(t, err)

	go server.Serve(l)
	defer server.Close()

	// That's all for the server, now the client:

	serverAddrParts := strings.Split(l.Addr().String(), ":")

	testCases := []struct {
		name         string
		do           func(*testing.T, *http.Request) (*http.Response, error)
		getWellKnown func(*testing.T) (libp2phttp.PeerMeta, error)
	}{
		{
			name: "using libp2phttp",
			do: func(t *testing.T, request *http.Request) (*http.Response, error) {
				var clientHttpHost libp2phttp.Host
				rt, err := clientHttpHost.NewConstrainedRoundTripper(peer.AddrInfo{Addrs: []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/" + serverAddrParts[1] + "/http")}})
				require.NoError(t, err)

				client := &http.Client{Transport: rt}
				return client.Do(request)
			},
			getWellKnown: func(t *testing.T) (libp2phttp.PeerMeta, error) {
				var clientHttpHost libp2phttp.Host
				rt, err := clientHttpHost.NewConstrainedRoundTripper(peer.AddrInfo{Addrs: []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/" + serverAddrParts[1] + "/http")}})
				require.NoError(t, err)
				return rt.(libp2phttp.PeerMetadataGetter).GetPeerMetadata()
			},
		},
		{
			name: "using stock http client",
			do: func(t *testing.T, request *http.Request) (*http.Response, error) {
				request.URL.Scheme = "http"
				request.URL.Host = l.Addr().String()
				request.Host = l.Addr().String()

				client := http.Client{}
				return client.Do(request)
			},
			getWellKnown: func(t *testing.T) (libp2phttp.PeerMeta, error) {
				client := http.Client{}
				resp, err := client.Get("http://" + l.Addr().String() + libp2phttp.WellKnownProtocols)
				require.NoError(t, err)

				b, err := io.ReadAll(resp.Body)
				require.NoError(t, err)

				var out libp2phttp.PeerMeta
				err = json.Unmarshal(b, &out)
				return out, err
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body := [32]byte{}
			_, err = rand.Reader.Read(body[:])
			require.NoError(t, err)
			req, err := http.NewRequest(http.MethodPost, "/ping/", bytes.NewReader(body[:]))
			require.NoError(t, err)
			resp, err := tc.do(t, req)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			rBody := [32]byte{}
			_, err = io.ReadFull(resp.Body, rBody[:])
			require.NoError(t, err)
			require.Equal(t, body, rBody)

			// Make sure we can get the well known resource
			protoMap, err := tc.getWellKnown(t)
			require.NoError(t, err)

			expectedMap := make(libp2phttp.PeerMeta)
			expectedMap[httpping.PingProtocolID] = libp2phttp.ProtocolMeta{Path: "/ping/"}
			require.Equal(t, expectedMap, protoMap)
		})
	}
}

func TestHostZeroValue(t *testing.T) {
	server := libp2phttp.Host{
		InsecureAllowHTTP: true,
		ListenAddrs:       []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/http")},
	}
	server.SetHTTPHandler("/hello", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) }))
	go func() {
		server.Serve()
	}()
	defer server.Close()

	c := libp2phttp.Host{}
	client, err := c.NamespacedClient("/hello", peer.AddrInfo{Addrs: server.Addrs()})
	require.NoError(t, err)
	resp, err := client.Get("/")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, "hello", string(body), "expected response from server")
}

func TestHTTPS(t *testing.T) {
	server := libp2phttp.Host{
		TLSConfig:   selfSignedTLSConfig(t),
		ListenAddrs: []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/https")},
	}
	server.SetHTTPHandler(httpping.PingProtocolID, httpping.Ping{})
	go func() {
		server.Serve()
	}()
	defer server.Close()

	clientTransport := http.DefaultTransport.(*http.Transport).Clone()
	clientTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	client := libp2phttp.Host{
		DefaultClientRoundTripper: clientTransport,
	}
	httpClient, err := client.NamespacedClient(httpping.PingProtocolID, peer.AddrInfo{Addrs: server.Addrs()})
	require.NoError(t, err)
	err = httpping.SendPing(httpClient)
	require.NoError(t, err)
}

func selfSignedTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour)

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	require.NoError(t, err)

	certTemplate := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Test"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
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

func TestCustomServeMux(t *testing.T) {
	serveMux := http.NewServeMux()
	serveMux.Handle("/ping/", httpping.Ping{})

	server := libp2phttp.Host{
		ListenAddrs:       []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/http")},
		ServeMux:          serveMux,
		InsecureAllowHTTP: true,
	}
	server.WellKnownHandler.AddProtocolMeta(httpping.PingProtocolID, libp2phttp.ProtocolMeta{Path: "/ping/"})
	go func() {
		server.Serve()
	}()
	defer server.Close()

	addrs := server.Addrs()
	require.Len(t, addrs, 1)
	var clientHttpHost libp2phttp.Host
	rt, err := clientHttpHost.NewConstrainedRoundTripper(peer.AddrInfo{Addrs: addrs}, libp2phttp.PreferHTTPTransport)
	require.NoError(t, err)

	client := &http.Client{Transport: rt}
	body := [32]byte{}
	req, _ := http.NewRequest(http.MethodPost, "/ping/", bytes.NewReader(body[:]))
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
}

func TestSetHandlerAtPath(t *testing.T) {
	hf := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "text/plain")
		w.Write([]byte("Hello World"))
	}
	tests := []struct {
		prefix, rest string
		paths200     []string
		paths404     []string
	}{
		{
			prefix:   "/",
			rest:     "/",
			paths200: []string{"/", "/a/", "/b", "/a/b"},
		},
		{
			prefix:   "/a",
			rest:     "/b/",
			paths200: []string{"/a/b/", "///a///b/", "/a/b/c"},
			// Not being able to serve /a/b when handling /a/b/ is a rather annoying limitation
			// of http.StripPrefix mechanism. This happens because /a/b is redirected to /b/
			// as the prefix /a is stripped when the redirect happens
			paths404: []string{"/a/b", "/a", "/b", "/a/a"},
		},
		{
			prefix:   "/",
			rest:     "/b/",
			paths200: []string{"/b", "/b/c", "/b/c/"},
			paths404: []string{"/", "/a/b"},
		},
	}
	for i, tc := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			nestedMx := http.NewServeMux()
			nestedMx.HandleFunc(tc.rest, hf)
			server := libp2phttp.Host{
				ListenAddrs:       []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/http")},
				InsecureAllowHTTP: true,
			}
			server.SetHTTPHandlerAtPath("test", tc.prefix, nestedMx)
			go func() {
				server.Serve()
			}()
			defer server.Close()
			addrs := server.Addrs()
			require.Len(t, addrs, 1)
			port, err := addrs[0].ValueForProtocol(ma.P_TCP)
			require.NoError(t, err)
			httpAddr := fmt.Sprintf("http://127.0.0.1:%s", port)
			for _, p := range tc.paths200 {
				resp, err := http.Get(httpAddr + p)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode, "path:%s", p)
				resp.Body.Close()
			}
			for _, p := range tc.paths404 {
				resp, _ := http.Get(httpAddr + p)
				require.Equal(t, 404, resp.StatusCode, "path:%s", p)
				resp.Body.Close()
			}
		})
	}
}

func TestServerLegacyWellKnownResource(t *testing.T) {
	mkHTTPServer := func(wellKnown string) ma.Multiaddr {
		mux := http.NewServeMux()
		wk := libp2phttp.WellKnownHandler{}
		mux.Handle(wellKnown, &wk)

		mux.Handle("/ping/", httpping.Ping{})
		wk.AddProtocolMeta(httpping.PingProtocolID, libp2phttp.ProtocolMeta{Path: "/ping/"})

		server := &http.Server{Addr: "127.0.0.1:0", Handler: mux}

		l, err := net.Listen("tcp", server.Addr)
		require.NoError(t, err)

		go server.Serve(l)
		t.Cleanup(func() { server.Close() })
		addrPort, err := netip.ParseAddrPort(l.Addr().String())
		require.NoError(t, err)
		return ma.StringCast(fmt.Sprintf("/ip4/%s/tcp/%d/http", addrPort.Addr().String(), addrPort.Port()))
	}

	mkServerlibp2phttp := func(enableLegacyWellKnown bool) ma.Multiaddr {
		server := libp2phttp.Host{
			EnableCompatibilityWithLegacyWellKnownEndpoint: enableLegacyWellKnown,
			ListenAddrs:       []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/http")},
			InsecureAllowHTTP: true,
		}
		server.SetHTTPHandler(httpping.PingProtocolID, httpping.Ping{})
		go server.Serve()
		t.Cleanup(func() { server.Close() })
		return server.Addrs()[0]
	}

	type testCase struct {
		name       string
		client     libp2phttp.Host
		serverAddr ma.Multiaddr
		expectErr  bool
	}

	var testCases = []testCase{
		{
			name:       "legacy server, client with compat",
			client:     libp2phttp.Host{EnableCompatibilityWithLegacyWellKnownEndpoint: true},
			serverAddr: mkHTTPServer(libp2phttp.LegacyWellKnownProtocols),
		},
		{
			name:       "up-to-date http server, client with compat",
			client:     libp2phttp.Host{EnableCompatibilityWithLegacyWellKnownEndpoint: true},
			serverAddr: mkHTTPServer(libp2phttp.WellKnownProtocols),
		},
		{
			name:       "up-to-date http server, client without compat",
			client:     libp2phttp.Host{},
			serverAddr: mkHTTPServer(libp2phttp.WellKnownProtocols),
		},
		{
			name:       "libp2phttp server with compat, client with compat",
			client:     libp2phttp.Host{EnableCompatibilityWithLegacyWellKnownEndpoint: true},
			serverAddr: mkServerlibp2phttp(true),
		},
		{
			name:       "libp2phttp server without compat, client with compat",
			client:     libp2phttp.Host{EnableCompatibilityWithLegacyWellKnownEndpoint: true},
			serverAddr: mkServerlibp2phttp(false),
		},
		{
			name:       "libp2phttp server with compat, client without compat",
			client:     libp2phttp.Host{},
			serverAddr: mkServerlibp2phttp(true),
		},
		{
			name:       "legacy server, client without compat",
			client:     libp2phttp.Host{},
			serverAddr: mkHTTPServer(libp2phttp.LegacyWellKnownProtocols),
			expectErr:  true,
		},
	}

	for i := range testCases {
		tc := &testCases[i] // to not copy the lock in libp2phttp.Host
		t.Run(tc.name, func(t *testing.T) {
			if tc.expectErr {
				_, err := tc.client.NamespacedClient(httpping.PingProtocolID, peer.AddrInfo{Addrs: []ma.Multiaddr{tc.serverAddr}})
				require.Error(t, err)
				return
			}
			httpClient, err := tc.client.NamespacedClient(httpping.PingProtocolID, peer.AddrInfo{Addrs: []ma.Multiaddr{tc.serverAddr}})
			require.NoError(t, err)

			err = httpping.SendPing(httpClient)
			require.NoError(t, err)
		})
	}

}

func TestResponseWriterShouldNotHaveCancelledContext(t *testing.T) {
	h, err := libp2p.New()
	require.NoError(t, err)
	defer h.Close()
	httpHost := libp2phttp.Host{StreamHost: h}
	go httpHost.Serve()
	defer httpHost.Close()

	closeNotifyCh := make(chan bool, 1)
	httpHost.SetHTTPHandlerAtPath("/test", "/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Legacy code uses this to check if the connection was closed
		//lint:ignore SA1019 This is a test to assert we do the right thing since Go HTTP stdlib depends on this.
		ch := w.(http.CloseNotifier).CloseNotify()
		select {
		case <-ch:
			closeNotifyCh <- true
		case <-time.After(100 * time.Millisecond):
			closeNotifyCh <- false
		}
		w.WriteHeader(http.StatusOK)
	}))

	clientH, err := libp2p.New()
	require.NoError(t, err)
	defer clientH.Close()
	clientHost := libp2phttp.Host{StreamHost: clientH}

	rt, err := clientHost.NewConstrainedRoundTripper(peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()})
	require.NoError(t, err)
	httpClient := &http.Client{Transport: rt}
	_, err = httpClient.Get("/")
	require.NoError(t, err)

	require.False(t, <-closeNotifyCh)
}

func TestHTTPHostAsRoundTripper(t *testing.T) {
	serverHost, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
	)
	require.NoError(t, err)

	serverHttpHost := libp2phttp.Host{
		InsecureAllowHTTP: true,
		StreamHost:        serverHost,
		ListenAddrs:       []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/http")},
	}

	serverHttpHost.SetHTTPHandlerAtPath("/hello", "/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("hello"))
	}))

	// Different protocol.ID and mounted at a different path
	serverHttpHost.SetHTTPHandlerAtPath("/hello-again", "/hello2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))

	go serverHttpHost.Serve()
	defer serverHttpHost.Close()

	httpPathSuffix := "/http-path/hello2"
	var testCases []string
	for _, a := range serverHttpHost.Addrs() {
		if _, err := a.ValueForProtocol(ma.P_HTTP); err == nil {
			testCases = append(testCases, "multiaddr:"+a.String())
			testCases = append(testCases, "multiaddr:"+a.String()+httpPathSuffix)
			serverPort, err := a.ValueForProtocol(ma.P_TCP)
			require.NoError(t, err)
			testCases = append(testCases, "http://127.0.0.1:"+serverPort)
		} else {
			testCases = append(testCases, "multiaddr:"+a.String()+"/p2p/"+serverHost.ID().String())
			testCases = append(testCases, "multiaddr:"+a.String()+"/p2p/"+serverHost.ID().String()+httpPathSuffix)
		}
	}

	clientStreamHost, err := libp2p.New()
	require.NoError(t, err)
	defer clientStreamHost.Close()

	clientHttpHost := libp2phttp.Host{StreamHost: clientStreamHost}
	client := http.Client{Transport: &clientHttpHost}
	for _, tc := range testCases {
		t.Run(tc, func(t *testing.T) {
			resp, err := client.Get(tc)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, "hello", string(body))
		})
	}
}

func TestHTTPHostAsRoundTripperFailsWhenNoStreamHostPresent(t *testing.T) {
	clientHttpHost := libp2phttp.Host{}
	client := http.Client{Transport: &clientHttpHost}

	_, err := client.Get("multiaddr:/ip4/127.0.0.1/udp/1111/quic-v1")
	// Fails because we don't have a stream host available to make the request
	require.Error(t, err)
	require.ErrorContains(t, err, "Missing StreamHost")
}

// TestRedirects tests a client being redirected through multiple HTTP redirects
func TestRedirects(t *testing.T) {
	serverHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"))
	require.NoError(t, err)
	serverHttpHost := libp2phttp.Host{
		StreamHost:        serverHost,
		InsecureAllowHTTP: true,
		ListenAddrs:       []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/http")},
	}
	go serverHttpHost.Serve()
	defer serverHttpHost.Close()

	serverHttpHost.SetHTTPHandlerAtPath("/redirect-1/0.0.1", "/a", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/b/")
		w.WriteHeader(http.StatusMovedPermanently)
	}))

	serverHttpHost.SetHTTPHandlerAtPath("/redirect-2/0.0.1", "/b", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/c/")
		w.WriteHeader(http.StatusMovedPermanently)
	}))

	serverHttpHost.SetHTTPHandlerAtPath("/redirect-3/0.0.1", "/c", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/d/")
		w.WriteHeader(http.StatusMovedPermanently)
	}))

	serverHttpHost.SetHTTPHandlerAtPath("/redirect-4/0.0.1", "/d", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))

	clientStreamHost, err := libp2p.New(libp2p.NoListenAddrs, libp2p.Transport(libp2pquic.NewTransport))
	require.NoError(t, err)
	client := http.Client{Transport: &libp2phttp.Host{StreamHost: clientStreamHost}}

	type testCase struct {
		initialURI  string
		expectedURI string
	}
	var testCases []testCase
	for _, a := range serverHttpHost.Addrs() {
		if _, err := a.ValueForProtocol(ma.P_HTTP); err == nil {
			port, err := a.ValueForProtocol(ma.P_TCP)
			require.NoError(t, err)
			u := fmt.Sprintf("multiaddr:%s/http-path/a%%2f", a)
			f := fmt.Sprintf("http://127.0.0.1:%s/d/", port)
			testCases = append(testCases, testCase{u, f})
		} else {
			u := fmt.Sprintf("multiaddr:%s/p2p/%s/http-path/a%%2f", a, serverHost.ID())
			f := fmt.Sprintf("multiaddr:%s/p2p/%s/http-path/%%2Fd%%2F", a, serverHost.ID())
			testCases = append(testCases, testCase{u, f})
		}
	}

	for _, tc := range testCases {
		t.Run(tc.initialURI, func(t *testing.T) {
			resp, err := client.Get(tc.initialURI)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, "hello", string(body))

			finalReqURL := *resp.Request.URL
			finalReqURL.Opaque = "" // Clear the opaque so we can compare the URI
			require.Equal(t, tc.expectedURI, finalReqURL.String())
		})
	}
}

// TestMultiaddrURIRedirect tests that we can redirect using a multiaddr URI. We
// redirect from the http transport to the stream based transport
func TestMultiaddrURIRedirect(t *testing.T) {
	serverHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"))
	require.NoError(t, err)
	serverHttpHost := libp2phttp.Host{
		StreamHost:        serverHost,
		InsecureAllowHTTP: true,
		ListenAddrs:       []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/http")},
	}
	go serverHttpHost.Serve()
	defer serverHttpHost.Close()

	var httpMultiaddr ma.Multiaddr
	var streamMultiaddr ma.Multiaddr
	for _, a := range serverHttpHost.Addrs() {
		if _, err := a.ValueForProtocol(ma.P_HTTP); err == nil {
			httpMultiaddr = a
		} else {
			streamMultiaddr = a
		}
	}
	require.NotNil(t, httpMultiaddr)
	require.NotNil(t, streamMultiaddr)

	// Redirect to a whole other transport!
	serverHttpHost.SetHTTPHandlerAtPath("/redirect-1/0.0.1", "/a", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", fmt.Sprintf("multiaddr:%s/p2p/%s/http-path/b", streamMultiaddr, serverHost.ID()))
		w.WriteHeader(http.StatusMovedPermanently)
	}))

	serverHttpHost.SetHTTPHandlerAtPath("/redirect-2/0.0.1", "/b", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	clientStreamHost, err := libp2p.New(libp2p.NoListenAddrs, libp2p.Transport(libp2pquic.NewTransport))
	require.NoError(t, err)
	client := http.Client{Transport: &libp2phttp.Host{StreamHost: clientStreamHost}}

	resp, err := client.Get(fmt.Sprintf("multiaddr:%s/http-path/a", httpMultiaddr))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, strings.HasPrefix(resp.Request.URL.RawPath, streamMultiaddr.String()), "expected redirect to stream transport")
}

func TestImpliedHostIsSet(t *testing.T) {
	serverHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"))
	require.NoError(t, err)
	serverHttpHost := libp2phttp.Host{
		StreamHost:        serverHost,
		InsecureAllowHTTP: true,
		ListenAddrs:       []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/http")},
	}
	go serverHttpHost.Serve()
	defer serverHttpHost.Close()

	serverHttpHost.SetHTTPHandlerAtPath("/hi", "/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "localhost") && r.URL.Path == "/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	clientStreamHost, err := libp2p.New(libp2p.NoListenAddrs, libp2p.Transport(libp2pquic.NewTransport))
	require.NoError(t, err)
	client := http.Client{Transport: &libp2phttp.Host{StreamHost: clientStreamHost}}

	type testCase struct {
		uri string
	}
	var testCases []testCase
	for _, a := range serverHttpHost.Addrs() {
		if _, err := a.ValueForProtocol(ma.P_HTTP); err == nil {
			port, err := a.ValueForProtocol(ma.P_TCP)
			require.NoError(t, err)
			u := fmt.Sprintf("multiaddr:/dns/localhost/tcp/%s/http", port)
			testCases = append(testCases, testCase{u})
		} else {
			port, err := a.ValueForProtocol(ma.P_UDP)
			require.NoError(t, err)
			u := fmt.Sprintf("multiaddr:/dns/localhost/udp/%s/quic-v1/p2p/%s", port, serverHost.ID())
			testCases = append(testCases, testCase{u})
		}
	}

	for _, tc := range testCases {
		t.Run(tc.uri, func(t *testing.T) {
			resp, err := client.Get(tc.uri)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}

}

func TestErrServerClosed(t *testing.T) {
	server := libp2phttp.Host{
		InsecureAllowHTTP: true,
		ListenAddrs:       []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/http")},
	}

	done := make(chan struct{})
	go func() {
		err := server.Serve()
		assert.Equal(t, http.ErrServerClosed, err)
		close(done)
	}()

	server.Close()
	<-done
}
