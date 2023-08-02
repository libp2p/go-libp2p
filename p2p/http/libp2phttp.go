// HTTP semantics with libp2p. Can use a libp2p stream transport or stock HTTP
// transports. This API is experimental and will likely change soon. Implements libp2p spec #508.
package libp2phttp

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
	host "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	gostream "github.com/libp2p/go-libp2p/p2p/gostream"
	ma "github.com/multiformats/go-multiaddr"
)

const ProtocolIDForMultistreamSelect = "/http/1.1"
const PeerMetadataLimit = 8 << 10 // 8KB
const PeerMetadataLRUSize = 256   // How many different peer's metadata to keep in our LRU cache

// TODOs:
// - integrate with the conn gater and resource manager
// - Support listenAddr option to accept a multiaddr to listen on (could handle h3 as well)
// - Support withStreamHost option to accept a streamHost to listen on

// Dev notes
// Would be nice to have an .Addrs method on the httpHost
// Would be nice to have the httpHost manage the listener (listenAddr option above)

type WellKnownProtocolMeta struct {
	Path string `json:"path"`
}

type WellKnownProtoMap map[protocol.ID]WellKnownProtocolMeta

// WellKnownHandler is an http.Handler that serves the .well-known/libp2p resource
type WellKnownHandler struct {
	wellknownMapMu   sync.Mutex
	wellKnownMapping WellKnownProtoMap
}

// StreamHostListen retuns a net.Listener that listens on libp2p streams for HTTP/1.1 messages.
func StreamHostListen(streamHost host.Host) (net.Listener, error) {
	return gostream.Listen(streamHost, ProtocolIDForMultistreamSelect)
}

func (h *WellKnownHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check if the requests accepts JSON
	accepts := r.Header.Get("Accept")
	if accepts != "" && !(strings.Contains(accepts, "application/json") || strings.Contains(accepts, "*/*")) {
		http.Error(w, "Only application/json is supported", http.StatusNotAcceptable)
	}

	if r.Method != "GET" {
		http.Error(w, "Only GET requests are supported", http.StatusMethodNotAllowed)
	}

	// Return a JSON object with the well-known protocols
	h.wellknownMapMu.Lock()
	mapping, err := json.Marshal(h.wellKnownMapping)
	h.wellknownMapMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Header().Add("Content-Type", "application/json")
	w.Header().Add("Content-Length", strconv.Itoa(len(mapping)))
	w.Write(mapping)
}

func (h *WellKnownHandler) AddProtocolMapping(p protocol.ID, path string) {
	h.wellknownMapMu.Lock()
	if h.wellKnownMapping == nil {
		h.wellKnownMapping = make(map[protocol.ID]WellKnownProtocolMeta)
	}
	h.wellKnownMapping[p] = WellKnownProtocolMeta{Path: path}
	h.wellknownMapMu.Unlock()
}

func (h *WellKnownHandler) RmProtocolMapping(p protocol.ID, path string) {
	h.wellknownMapMu.Lock()
	if h.wellKnownMapping != nil {
		delete(h.wellKnownMapping, p)
	}
	h.wellknownMapMu.Unlock()
}

// HTTPHost is a libp2p host for request/responses with HTTP semantics. This is
// in contrast to a stream-oriented host like the host.Host interface. Warning,
// this is experimental. The API will likely change.
type HTTPHost struct {
	rootHandler      http.ServeMux
	wk               WellKnownHandler
	httpRoundTripper *http.Transport
	recentHTTPAddrs  *lru.Cache[peer.ID, httpAddr]
	peerMetadata     *lru.Cache[peer.ID, WellKnownProtoMap]
}

type httpAddr struct {
	addr   string
	scheme string
	sni    string
	rt     http.RoundTripper // optional, if this needed its own transport
}

type HTTPHostOption func(*HTTPHost) error

// WithTLSClientConfig sets the TLS client config for the native HTTP transport.
func WithTLSClientConfig(tlsConfig *tls.Config) HTTPHostOption {
	return func(h *HTTPHost) error {
		h.httpRoundTripper.TLSClientConfig = tlsConfig
		return nil
	}
}

// New creates a new HTTPHost. Use HTTPHost.Serve to start serving HTTP requests (both over libp2p streams and HTTP transport).
func New(opts ...HTTPHostOption) (*HTTPHost, error) {
	httpRoundTripper := http.DefaultTransport.(*http.Transport).Clone()
	recentConnsLimit := httpRoundTripper.MaxIdleConns
	if recentConnsLimit < 1 {
		recentConnsLimit = 32
	}

	recentHTTP, err := lru.New[peer.ID, httpAddr](recentConnsLimit)
	peerMetadata, err2 := lru.New[peer.ID, WellKnownProtoMap](PeerMetadataLRUSize)
	if err != nil || err2 != nil {
		// Only happens if size is < 1. We make sure to not do that, so this should never happen.
		panic(err)
	}

	h := &HTTPHost{
		wk:               WellKnownHandler{},
		rootHandler:      http.ServeMux{},
		httpRoundTripper: httpRoundTripper,
		recentHTTPAddrs:  recentHTTP,
		peerMetadata:     peerMetadata,
	}
	h.rootHandler.Handle("/.well-known/libp2p", &h.wk)
	for _, opt := range opts {
		err := opt(h)
		if err != nil {
			return nil, err
		}
	}

	return h, nil
}

// Serve starts serving HTTP requests using the given listener. You may call this method multiple times with different listeners.
func (h *HTTPHost) Serve(l net.Listener) error {
	return http.Serve(l, &h.rootHandler)
}

// ServeTLS starts serving TLS+HTTP requests using the given listener. You may call this method multiple times with different listeners.
func (h *HTTPHost) ServeTLS(l net.Listener, c *tls.Config) error {
	srv := http.Server{
		Handler:   &h.rootHandler,
		TLSConfig: c,
	}
	return srv.ServeTLS(l, "", "")
}

// SetHttpHandler sets the HTTP handler for a given protocol. Automatically
// manages the .well-known/libp2p mapping.
func (h *HTTPHost) SetHttpHandler(p protocol.ID, handler http.Handler) {
	h.SetHttpHandlerAtPath(p, string(p)+"/", handler)
}

// SetHttpHandlerAtPath sets the HTTP handler for a given protocol using the
// given path. Automatically manages the .well-known/libp2p mapping.
func (h *HTTPHost) SetHttpHandlerAtPath(p protocol.ID, path string, handler http.Handler) {
	h.wk.AddProtocolMapping(p, path)
	h.rootHandler.Handle(path, handler)
}

type roundTripperOpts struct {
	// todo SkipClientAuth bool
	preferHTTPTransport bool
}

type streamRoundTripper struct {
	server peer.ID
	h      host.Host
}

type streamReadCloser struct {
	io.ReadCloser
	s network.Stream
}

func (s *streamReadCloser) Close() error {
	s.s.Close()
	return s.ReadCloser.Close()
}

// RoundTrip implements http.RoundTripper.
func (rt *streamRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	s, err := rt.h.NewStream(r.Context(), rt.server, ProtocolIDForMultistreamSelect)
	if err != nil {
		return nil, err
	}

	go func() {
		defer s.CloseWrite()
		r.Write(s)
		if r.Body != nil {
			r.Body.Close()
		}
	}()

	resp, err := http.ReadResponse(bufio.NewReader(s), r)
	if err != nil {
		return nil, err
	}
	resp.Body = &streamReadCloser{resp.Body, s}

	return resp, nil
}

// roundTripperForSpecificHost is an http.RoundTripper targets a specific server. Still reuses the underlying RoundTripper for the requests.
type roundTripperForSpecificServer struct {
	http.RoundTripper
	ownRoundtripper  bool
	httpHost         *HTTPHost
	server           peer.ID
	targetServerAddr string
	sni              string
	scheme           string
}

// RoundTrip implements http.RoundTripper.
func (rt *roundTripperForSpecificServer) RoundTrip(r *http.Request) (*http.Response, error) {
	if (r.URL.Scheme != "" && r.URL.Scheme != rt.scheme) || (r.URL.Host != "" && r.URL.Host != rt.targetServerAddr) {
		return nil, fmt.Errorf("this transport is only for requests to %s://%s", rt.scheme, rt.targetServerAddr)
	}
	r.URL.Scheme = rt.scheme
	r.URL.Host = rt.targetServerAddr
	r.Host = rt.sni
	resp, err := rt.RoundTripper.RoundTrip(r)
	if err == nil && rt.server != "" {
		ha := httpAddr{addr: rt.targetServerAddr, scheme: rt.scheme, sni: rt.sni}
		if rt.ownRoundtripper {
			ha.rt = rt.RoundTripper
		}
		rt.httpHost.recentHTTPAddrs.Add(rt.server, ha)
	}
	return resp, err
}

type namespacedRoundTripper struct {
	http.RoundTripper
	protocolPrefix    string
	protocolPrefixRaw string
}

// RoundTrip implements http.RoundTripper.
func (rt *namespacedRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(r.URL.Path, rt.protocolPrefix) {
		r.URL.Path = rt.protocolPrefix + r.URL.Path
	}
	if !strings.HasPrefix(r.URL.RawPath, rt.protocolPrefixRaw) {
		r.URL.RawPath = rt.protocolPrefixRaw + r.URL.Path
	}

	return rt.RoundTripper.RoundTrip(r)
}

// NamespaceRoundTripper returns an http.RoundTripper that are scoped to the given protocol on the given server.
func (h *HTTPHost) NamespaceRoundTripper(roundtripper http.RoundTripper, p protocol.ID, server peer.ID) (namespacedRoundTripper, error) {
	protos, err := h.GetAndStorePeerProtoMap(roundtripper, server)
	if err != nil {
		return namespacedRoundTripper{}, err
	}

	v, ok := protos[p]
	if !ok {
		return namespacedRoundTripper{}, fmt.Errorf("no protocol %s for server %s", p, server)
	}

	path := v.Path
	if path[len(path)-1] == '/' {
		// Trim the trailing slash, since it's common to make requests starting with a leading forward slash for the path
		path = path[:len(path)-1]
	}

	u, err := url.Parse(path)
	if err != nil {
		return namespacedRoundTripper{}, fmt.Errorf("invalid path %s for protocol %s for server %s", v.Path, p, server)
	}

	return namespacedRoundTripper{
		RoundTripper:      roundtripper,
		protocolPrefix:    u.Path,
		protocolPrefixRaw: u.RawPath,
	}, nil
}

// NamespacedClient returns an http.Client that is scoped to the given protocol on the given server.
func (h *HTTPHost) NamespacedClient(streamHost host.Host, p protocol.ID, server peer.AddrInfo, opts ...RoundTripperOptsFn) (http.Client, error) {
	rt, err := h.NewRoundTripper(streamHost, server, opts...)
	if err != nil {
		return http.Client{}, err
	}

	nrt, err := h.NamespaceRoundTripper(rt, p, server.ID)
	if err != nil {
		return http.Client{}, err
	}

	return http.Client{Transport: &nrt}, nil
}

func RoundTripperPreferHTTPTransport(o roundTripperOpts) roundTripperOpts {
	o.preferHTTPTransport = true
	return o
}

type RoundTripperOptsFn func(o roundTripperOpts) roundTripperOpts

// NewRoundTripper returns an http.RoundTripper that can fulfill and HTTP
// request to the given server. It may use an HTTP transport or a stream based
// transport. It is valid to pass an empty server.ID and a nil streamHost.
func (h *HTTPHost) NewRoundTripper(streamHost host.Host, server peer.AddrInfo, opts ...RoundTripperOptsFn) (http.RoundTripper, error) {
	options := roundTripperOpts{}
	for _, o := range opts {
		options = o(options)
	}

	// Do we have a recent HTTP transport connection to this peer?
	if a, ok := h.recentHTTPAddrs.Get(server.ID); server.ID != "" && ok {
		var rt http.RoundTripper = h.httpRoundTripper
		ownRoundtripper := false
		if a.rt != nil {
			ownRoundtripper = true
			rt = a.rt
		}
		return &roundTripperForSpecificServer{
			RoundTripper:     rt,
			ownRoundtripper:  ownRoundtripper,
			httpHost:         h,
			server:           server.ID,
			targetServerAddr: a.addr,
			scheme:           a.scheme,
			sni:              a.sni,
		}, nil
	}

	httpAddrs := make([]ma.Multiaddr, 0, 1) // The common case of a single http address
	nonHttpAddrs := make([]ma.Multiaddr, 0, len(server.Addrs))

	firstAddrIsHTTP := false

	for i, addr := range server.Addrs {
		addr, isHttp := normalizeHTTPMultiaddr(addr)
		if isHttp {
			if i == 0 {
				firstAddrIsHTTP = true
			}
			httpAddrs = append(httpAddrs, addr)
		} else {
			nonHttpAddrs = append(nonHttpAddrs, addr)
		}
	}

	// Do we have an existing connection to this peer?
	existingStreamConn := false
	if server.ID != "" && streamHost != nil {
		existingStreamConn = len(streamHost.Network().ConnsToPeer(server.ID)) > 0
	}

	if len(httpAddrs) > 0 && (options.preferHTTPTransport || (firstAddrIsHTTP && !existingStreamConn)) {
		var useHTTPS bool
		var host string
		var port string
		var sni string

		ma.ForEach(httpAddrs[0], func(c ma.Component) bool {
			p := c.Protocol()
			if p.Code == ma.P_IP4 || p.Code == ma.P_IP6 || p.Code == ma.P_DNS || p.Code == ma.P_DNS4 || p.Code == ma.P_DNS6 {
				host = c.Value()
			} else if p.Code == ma.P_TCP || p.Code == ma.P_UDP {
				port = c.Value()
			} else if p.Code == ma.P_TLS {
				useHTTPS = true
			} else if p.Code == ma.P_SNI {
				sni = c.Value()
			}

			return host == "" || port == "" || !useHTTPS || sni == ""
		})
		scheme := "http"
		if useHTTPS {
			scheme = "https"
			if sni == "" {
				sni = host
			}
		}

		rt := h.httpRoundTripper
		ownRoundtripper := false
		if sni != host {
			// We have a different host and SNI (e.g. using an IP address but specifying a SNI)
			// We need to make our own transport to support this.
			rt = rt.Clone()
			rt.TLSClientConfig = h.httpRoundTripper.TLSClientConfig.Clone()
			rt.TLSClientConfig.ServerName = sni
			ownRoundtripper = true
		}

		return &roundTripperForSpecificServer{
			RoundTripper:     rt,
			ownRoundtripper:  ownRoundtripper,
			httpHost:         h,
			server:           server.ID,
			targetServerAddr: host + ":" + port,
			sni:              sni,
			scheme:           scheme,
		}, nil
	}

	// Otherwise use a stream based transport
	if streamHost == nil {
		return nil, fmt.Errorf("no http addresses for peer, and no stream host provided")
	}
	if !existingStreamConn {
		if server.ID == "" {
			return nil, fmt.Errorf("no http addresses for peer, and no server peer ID provided")
		}
		err := streamHost.Connect(context.Background(), peer.AddrInfo{ID: server.ID, Addrs: nonHttpAddrs})
		if err != nil {
			return nil, fmt.Errorf("failed to connect to peer: %w", err)
		}
	}

	return NewStreamRoundTripper(streamHost, server.ID), nil
}

func NewStreamRoundTripper(streamHost host.Host, server peer.ID) http.RoundTripper {
	return &streamRoundTripper{h: streamHost, server: server}
}

var httpComponent, _ = ma.NewComponent("http", "")
var tlsComponent, _ = ma.NewComponent("http", "")

// normalizeHTTPMultiaddr converts an https multiaddr to a tls/http one.
// Returns a bool indicating if the input multiaddr has an http (or https) component.
func normalizeHTTPMultiaddr(addr ma.Multiaddr) (ma.Multiaddr, bool) {
	isHTTPMultiaddr := false
	beforeHTTPS, afterIncludingHTTPS := ma.SplitFunc(addr, func(c ma.Component) bool {
		if c.Protocol().Code == ma.P_HTTP {
			isHTTPMultiaddr = true
		}

		if c.Protocol().Code == ma.P_HTTPS {
			isHTTPMultiaddr = true
			return true
		}
		return false
	})

	if afterIncludingHTTPS == nil {
		// No HTTPS component, just return the original
		return addr, isHTTPMultiaddr
	}

	_, afterHTTPS := ma.SplitFirst(afterIncludingHTTPS)

	return ma.Join(beforeHTTPS, tlsComponent, httpComponent, afterHTTPS), isHTTPMultiaddr
}

// ProtocolPathPrefix looks up the protocol path in the well-known mapping and returns it
func (h *HTTPHost) GetAndStorePeerProtoMap(roundtripper http.RoundTripper, server peer.ID) (WellKnownProtoMap, error) {
	if meta, ok := h.peerMetadata.Get(server); server != "" && ok {
		return meta, nil
	}

	req, err := http.NewRequest("GET", "/.well-known/libp2p", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	client := http.Client{Transport: roundtripper}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body := [PeerMetadataLimit]byte{}
	bytesRead := 0
	for {
		n, err := resp.Body.Read(body[bytesRead:])
		bytesRead += n
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		if bytesRead >= PeerMetadataLimit {
			return nil, fmt.Errorf("peer metadata too large")
		}
	}

	meta := WellKnownProtoMap{}
	json.Unmarshal(body[:bytesRead], &meta)
	if server != "" {
		h.peerMetadata.Add(server, meta)
	}

	return meta, nil
}
