package libp2phttp

import (
	"bufio"
	"context"
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
	manet "github.com/multiformats/go-multiaddr/net"
)

const ProtocolIDForMultistreamSelect = "/http/1.1"
const PeerMetadataLimit = 8 << 10 // 8KB
const PeerMetadataLRUSize = 256   // How many different peer's metadata to keep in our LRU cache

// TODOs:
// - integrate with the conn gater and resource manager
// - skip client auth
// - Support listenAddr option to accept a multiaddr to listen on (could handle h3 as well)
// - Support withStreamHost option to accept a streamHost to listen on

type WellKnownProtocolMeta struct {
	Path string `json:"path"`
}

type WellKnownProtoMap map[protocol.ID]WellKnownProtocolMeta

type wellKnownHandler struct {
	wellknownMapMu   sync.Mutex
	wellKnownMapping WellKnownProtoMap
}

// StreamHostListen retuns a net.Listener that listens on libp2p streams for HTTP/1.1 messages.
func StreamHostListen(streamHost host.Host) (net.Listener, error) {
	return gostream.Listen(streamHost, ProtocolIDForMultistreamSelect)
}

func NewWellKnownHandler() wellKnownHandler {
	return wellKnownHandler{wellKnownMapping: make(map[protocol.ID]WellKnownProtocolMeta)}
}

func (h *wellKnownHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

func (h *wellKnownHandler) AddProtocolMapping(p protocol.ID, path string) {
	h.wellknownMapMu.Lock()
	h.wellKnownMapping[p] = WellKnownProtocolMeta{Path: path}
	h.wellknownMapMu.Unlock()
}

func (h *wellKnownHandler) RmProtocolMapping(p protocol.ID, path string) {
	h.wellknownMapMu.Lock()
	delete(h.wellKnownMapping, p)
	h.wellknownMapMu.Unlock()
}

type HTTPHost struct {
	rootHandler      http.ServeMux
	wk               wellKnownHandler
	httpRoundTripper http.RoundTripper
	recentHTTPAddrs  *lru.Cache[peer.ID, httpAddr]
	peerMetadata     *lru.Cache[peer.ID, WellKnownProtoMap]
}

type httpAddr struct {
	addr   string
	scheme string
}

// New creates a new HTTPHost. Use HTTPHost.Serve to start serving HTTP requests (both over libp2p streams and HTTP transport).
func New() *HTTPHost {
	recentConnsLimit := http.DefaultTransport.(*http.Transport).MaxIdleConns
	if recentConnsLimit < 1 {
		recentConnsLimit = 32
	}

	recentHTTP, err := lru.New[peer.ID, httpAddr](recentConnsLimit)
	peerMetadata, err2 := lru.New[peer.ID, WellKnownProtoMap](PeerMetadataLRUSize)
	if err != nil || err2 != nil {
		// Only happens if size is < 1. We set it to 32, so this should never happen.
		panic(err)
	}

	h := &HTTPHost{
		wk:               NewWellKnownHandler(),
		rootHandler:      http.ServeMux{},
		httpRoundTripper: http.DefaultTransport,
		recentHTTPAddrs:  recentHTTP,
		peerMetadata:     peerMetadata,
	}
	h.rootHandler.Handle("/.well-known/libp2p", &h.wk)

	return h
}

// Serve starts serving HTTP requests using the given listener. You may call this method multiple times with different listeners.
func (h *HTTPHost) Serve(l net.Listener) error {
	return http.Serve(l, &h.rootHandler)
}

// ServeTLS starts serving TLS+HTTP requests using the given listener. You may call this method multiple times with different listeners.
func (h *HTTPHost) ServeTLS(l net.Listener, certFile, keyFile string) error {
	return http.ServeTLS(l, &h.rootHandler, certFile, keyFile)
}

// SetHttpHandler sets the HTTP handler for a given protocol. Automatically
// manages the .well-known/libp2p mapping.
func (h *HTTPHost) SetHttpHandler(p protocol.ID, handler http.Handler) {
	path := string(p) + "/"
	h.SetHttpHandlerAtPath(p, path, handler)
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
	httpHost         *HTTPHost
	server           peer.ID
	targetServerAddr string
	scheme           string
}

// RoundTrip implements http.RoundTripper.
func (rt *roundTripperForSpecificServer) RoundTrip(r *http.Request) (*http.Response, error) {
	if (r.URL.Scheme != "" && r.URL.Scheme != rt.scheme) || (r.URL.Host != "" && r.URL.Host != rt.targetServerAddr) {
		return nil, fmt.Errorf("this transport is only for requests to %s://%s", rt.scheme, rt.targetServerAddr)
	}
	r.URL.Scheme = rt.scheme
	r.URL.Host = rt.targetServerAddr
	r.Host = rt.targetServerAddr
	resp, err := rt.RoundTripper.RoundTrip(r)
	if err == nil && rt.server != "" {
		rt.httpHost.recentHTTPAddrs.Add(rt.server, httpAddr{addr: rt.targetServerAddr, scheme: rt.scheme})
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

	u, err := url.Parse(v.Path)
	if err != nil {
		return namespacedRoundTripper{}, fmt.Errorf("invalid path %s for protocol %s for server %s", v.Path, p, server)
	}

	return namespacedRoundTripper{
		RoundTripper:      roundtripper,
		protocolPrefix:    u.Path,
		protocolPrefixRaw: u.RawPath,
	}, nil
}

func RoundTripperPreferHTTPTransport(o roundTripperOpts) roundTripperOpts {
	o.preferHTTPTransport = true
	return o
}

type RoundTripperOptsFn func(o roundTripperOpts) roundTripperOpts

func (h *HTTPHost) NewRoundTripper(streamHost host.Host, server peer.AddrInfo, opts ...RoundTripperOptsFn) (http.RoundTripper, error) {
	options := roundTripperOpts{}
	for _, o := range opts {
		options = o(options)
	}

	// Do we have a recent HTTP transport connection to this peer?
	if a, ok := h.recentHTTPAddrs.Get(server.ID); server.ID != "" && ok {
		return &roundTripperForSpecificServer{
			RoundTripper:     h.httpRoundTripper,
			httpHost:         h,
			server:           server.ID,
			targetServerAddr: a.addr,
			scheme:           a.scheme,
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
	if server.ID != "" {
		existingStreamConn = len(streamHost.Network().ConnsToPeer(server.ID)) > 0
	}

	if len(httpAddrs) > 0 && (options.preferHTTPTransport || (firstAddrIsHTTP && !existingStreamConn)) {
		useHTTPS := false
		withoutHTTP, _ := ma.SplitFunc(httpAddrs[0], func(c ma.Component) bool {
			return c.Protocol().Code == ma.P_HTTP
		})
		// Check if we need to pop the TLS component at the end as well
		maybeWithoutTLS, maybeTLS := ma.SplitLast(withoutHTTP)
		if maybeTLS != nil && maybeTLS.Protocol().Code == ma.P_TLS {
			useHTTPS = true
			withoutHTTP = maybeWithoutTLS
		}
		na, err := manet.ToNetAddr(withoutHTTP)
		if err != nil {
			return nil, err
		}
		scheme := "http"
		if useHTTPS {
			scheme = "https"
		}

		return &roundTripperForSpecificServer{
			RoundTripper:     http.DefaultTransport,
			httpHost:         h,
			server:           server.ID,
			targetServerAddr: na.String(),
			scheme:           scheme,
		}, nil
	}

	// Otherwise use a stream based transport
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

	client := &http.Client{Transport: roundtripper}
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
