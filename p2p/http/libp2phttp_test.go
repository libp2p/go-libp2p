package libp2phttp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p"
	host "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2phttp "github.com/libp2p/go-libp2p/p2p/http"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestHTTPOverStreams(t *testing.T) {
	serverHost, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
	)
	require.NoError(t, err)

	httpHost, err := libp2phttp.New(libp2phttp.StreamHost(serverHost))
	require.NoError(t, err)

	httpHost.SetHttpHandler("/hello", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	clientRT := libp2phttp.NewStreamRoundTripper(clientHost, serverHost.ID())

	client := &http.Client{Transport: clientRT}

	resp, err := client.Get("/hello")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, "hello", string(body))
}

func TestRoundTrippers(t *testing.T) {
	serverHost, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
	)
	require.NoError(t, err)

	streamListener, err := libp2phttp.StreamHostListen(serverHost)
	require.NoError(t, err)
	defer streamListener.Close()

	httpHost, err := libp2phttp.New(
		libp2phttp.StreamHost(serverHost),
		libp2phttp.ListenAddrs([]ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/http")}))
	require.NoError(t, err)

	httpHost.SetHttpHandler("/hello", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))

	// Start stream based server
	go httpHost.Serve()
	defer httpHost.Close()

	serverMultiaddrs := httpHost.Addrs()
	serverHTTPAddr := serverMultiaddrs[1]

	testCases := []struct {
		name                     string
		setupRoundTripper        func(t *testing.T, clientStreamHost host.Host, clientHTTPHost *libp2phttp.HTTPHost) http.RoundTripper
		expectStreamRoundTripper bool
	}{
		{
			name: "HTTP preferred",
			setupRoundTripper: func(t *testing.T, clientStreamHost host.Host, clientHTTPHost *libp2phttp.HTTPHost) http.RoundTripper {
				rt, err := clientHTTPHost.NewRoundTripper(peer.AddrInfo{
					ID:    serverHost.ID(),
					Addrs: serverMultiaddrs,
				}, libp2phttp.RoundTripperPreferHTTPTransport)
				require.NoError(t, err)
				return rt
			},
		},
		{
			name: "HTTP first",
			setupRoundTripper: func(t *testing.T, clientStreamHost host.Host, clientHTTPHost *libp2phttp.HTTPHost) http.RoundTripper {
				rt, err := clientHTTPHost.NewRoundTripper(peer.AddrInfo{
					ID:    serverHost.ID(),
					Addrs: []ma.Multiaddr{serverHTTPAddr, serverHost.Addrs()[0]},
				})
				require.NoError(t, err)
				return rt
			},
		},
		{
			name: "No HTTP transport",
			setupRoundTripper: func(t *testing.T, clientStreamHost host.Host, clientHTTPHost *libp2phttp.HTTPHost) http.RoundTripper {
				rt, err := clientHTTPHost.NewRoundTripper(peer.AddrInfo{
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
			setupRoundTripper: func(t *testing.T, clientStreamHost host.Host, clientHTTPHost *libp2phttp.HTTPHost) http.RoundTripper {
				rt, err := clientHTTPHost.NewRoundTripper(peer.AddrInfo{
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
			setupRoundTripper: func(t *testing.T, clientStreamHost host.Host, clientHTTPHost *libp2phttp.HTTPHost) http.RoundTripper {
				clientStreamHost.Connect(context.Background(), peer.AddrInfo{
					ID:    serverHost.ID(),
					Addrs: serverHost.Addrs(),
				})
				rt, err := clientHTTPHost.NewRoundTripper(peer.AddrInfo{
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

			clientHttpHost, err := libp2phttp.New(libp2phttp.StreamHost(clientStreamHost))
			require.NoError(t, err)

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
						h, err := libp2phttp.New()
						require.NoError(t, err)
						nrt, err := h.NamespaceRoundTripper(rt, "/hello", serverHost.ID())
						require.NoError(t, err)
						client := &http.Client{Transport: &nrt}
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

			// Read the .well-known/libp2p resource
			wk, err := clientHttpHost.GetAndStorePeerProtoMap(rt, serverHost.ID())
			require.NoError(t, err)

			expectedMap := make(libp2phttp.WellKnownProtoMap)
			expectedMap["/hello"] = libp2phttp.WellKnownProtocolMeta{Path: "/hello/"}
			require.Equal(t, expectedMap, wk)
		})
	}
}

// TODO test with a native Go HTTP server
func TestPlainOldHTTPServer(t *testing.T) {
	mux := http.NewServeMux()
	wk := libp2phttp.WellKnownHandler{}
	mux.Handle("/.well-known/libp2p", &wk)

	mux.Handle("/ping/", libp2phttp.Ping{})
	wk.AddProtocolMapping(libp2phttp.PingProtocolID, "/ping/")

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
		getWellKnown func(*testing.T) (libp2phttp.WellKnownProtoMap, error)
	}{
		{
			name: "using libp2phttp",
			do: func(t *testing.T, request *http.Request) (*http.Response, error) {
				clientHttpHost, err := libp2phttp.New()
				require.NoError(t, err)
				rt, err := clientHttpHost.NewRoundTripper(peer.AddrInfo{Addrs: []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/" + serverAddrParts[1] + "/http")}})
				require.NoError(t, err)

				client := &http.Client{Transport: rt}
				return client.Do(request)
			},
			getWellKnown: func(t *testing.T) (libp2phttp.WellKnownProtoMap, error) {
				clientHttpHost, err := libp2phttp.New()
				require.NoError(t, err)
				rt, err := clientHttpHost.NewRoundTripper(peer.AddrInfo{Addrs: []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/" + serverAddrParts[1] + "/http")}})
				require.NoError(t, err)
				return clientHttpHost.GetAndStorePeerProtoMap(rt, "")
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
			getWellKnown: func(t *testing.T) (libp2phttp.WellKnownProtoMap, error) {
				client := http.Client{}
				resp, err := client.Get("http://" + l.Addr().String() + "/.well-known/libp2p")
				require.NoError(t, err)

				b, err := io.ReadAll(resp.Body)
				require.NoError(t, err)

				var out libp2phttp.WellKnownProtoMap
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

			expectedMap := make(libp2phttp.WellKnownProtoMap)
			expectedMap[libp2phttp.PingProtocolID] = libp2phttp.WellKnownProtocolMeta{Path: "/ping/"}
			require.Equal(t, expectedMap, protoMap)
		})
	}
}

// TODO test with tls
