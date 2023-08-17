// HTTP semantics with libp2p. Can use a libp2p stream transport or stock HTTP
// transports. This API is experimental and will likely change soon. Implements [libp2p spec #508](https://github.com/libp2p/specs/pull/508).
package libp2phttp

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
	logging "github.com/ipfs/go-log/v2"
	host "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	gostream "github.com/libp2p/go-libp2p/p2p/gostream"
	ma "github.com/multiformats/go-multiaddr"
)

var log = logging.Logger("libp2phttp")

const ProtocolIDForMultistreamSelect = "/http/1.1"
const peerMetadataLimit = 8 << 10 // 8KB
const peerMetadataLRUSize = 256   // How many different peer's metadata to keep in our LRU cache

// TODOs:
// - integrate with the conn gater and resource manager

// ProtocolMeta is metadata about a protocol.
type ProtocolMeta struct {
	// Path defines the HTTP Path prefix used for this protocol
	Path string `json:"path"`
}

type PeerMeta map[protocol.ID]ProtocolMeta

// WellKnownHandler is an http.Handler that serves the .well-known/libp2p resource
type WellKnownHandler struct {
	wellknownMapMu   sync.Mutex
	wellKnownMapping PeerMeta
}

// streamHostListen retuns a net.Listener that listens on libp2p streams for HTTP/1.1 messages.
func streamHostListen(streamHost host.Host) (net.Listener, error) {
	return gostream.Listen(streamHost, ProtocolIDForMultistreamSelect)
}

func (h *WellKnownHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check if the requests accepts JSON
	accepts := r.Header.Get("Accept")
	if accepts != "" && !(strings.Contains(accepts, "application/json") || strings.Contains(accepts, "*/*")) {
		http.Error(w, "Only application/json is supported", http.StatusNotAcceptable)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Only GET requests are supported", http.StatusMethodNotAllowed)
		return
	}

	// Return a JSON object with the well-known protocols
	h.wellknownMapMu.Lock()
	mapping, err := json.Marshal(h.wellKnownMapping)
	h.wellknownMapMu.Unlock()
	if err != nil {
		http.Error(w, "Marshal error", http.StatusInternalServerError)
		return
	}
	w.Header().Add("Content-Type", "application/json")
	w.Header().Add("Content-Length", strconv.Itoa(len(mapping)))
	w.Write(mapping)
}

func (h *WellKnownHandler) AddProtocolMapping(p protocol.ID, protocolMeta ProtocolMeta) {
	h.wellknownMapMu.Lock()
	if h.wellKnownMapping == nil {
		h.wellKnownMapping = make(map[protocol.ID]ProtocolMeta)
	}
	h.wellKnownMapping[p] = protocolMeta
	h.wellknownMapMu.Unlock()
}

func (h *WellKnownHandler) RemoveProtocolMapping(p protocol.ID) {
	h.wellknownMapMu.Lock()
	if h.wellKnownMapping != nil {
		delete(h.wellKnownMapping, p)
	}
	h.wellknownMapMu.Unlock()
}

// HTTPHost is a libp2p host for request/responses with HTTP semantics. This is
// in contrast to a stream-oriented host like the host.Host interface. Its
// zero-value (&HTTPHost{}) is usable. Do not copy by value.
// See examples for usage.
//
//	Warning, this is experimental. The API will likely change.
type HTTPHost struct {
	// StreamHost is a stream based libp2p host used to do HTTP over libp2p streams. May be nil
	StreamHost host.Host
	// ListenAddrs are the requested addresses to listen on. Multiaddrs must be a valid HTTP(s) multiaddr.
	ListenAddrs []ma.Multiaddr
	// TLSConfig is the TLS config for the server to use
	TLSConfig *tls.Config
	// ServeInsecureHTTP indicates if the server should serve unencrypted HTTP requests over TCP.
	ServeInsecureHTTP bool
	// ServeMux is the http.ServeMux used by the server to serve requests
	ServeMux http.ServeMux

	// DefaultClientRoundTripper is the default http.RoundTripper for clients
	DefaultClientRoundTripper *http.Transport

	wk WellKnownHandler
	// peerMetadata is an lru cache of a peer's well-known protocol map.
	peerMetadata *lru.Cache[peer.ID, PeerMeta]
	// createHTTPTransport is used to lazily create the httpTransport in a thread-safe way.
	createHTTPTransport sync.Once
	httpTransport       *httpTransport
}

type httpTransport struct {
	listenAddrs         []ma.Multiaddr
	listeners           []net.Listener
	closeListeners      chan struct{}
	waitingForListeners chan struct{}
}

func newHTTPTransport() *httpTransport {
	return &httpTransport{
		closeListeners:      make(chan struct{}),
		waitingForListeners: make(chan struct{}),
	}
}

func newPeerMetadataCache() *lru.Cache[peer.ID, PeerMeta] {
	peerMetadata, err := lru.New[peer.ID, PeerMeta](peerMetadataLRUSize)
	if err != nil {
		// Only happens if size is < 1. We make sure to not do that, so this should never happen.
		panic(err)
	}
	return peerMetadata
}

func (h *HTTPHost) Addrs() []ma.Multiaddr {
	h.createHTTPTransport.Do(func() {
		h.httpTransport = newHTTPTransport()
	})
	<-h.httpTransport.waitingForListeners
	return h.httpTransport.listenAddrs
}

var ErrNoListeners = errors.New("nothing to listen on")

// Serve starts the HTTP transport listeners. Always returns a non-nil error.
// If there are no listeners, returns ErrNoListeners.
func (h *HTTPHost) Serve() error {
	// assert that each addr contains a /http component
	for _, addr := range h.ListenAddrs {
		_, isHTTP := normalizeHTTPMultiaddr(addr)
		if !isHTTP {
			return fmt.Errorf("address %s does not contain a /http or /https component", addr)
		}
	}

	h.ServeMux.Handle("/.well-known/libp2p", &h.wk)

	h.createHTTPTransport.Do(func() {
		h.httpTransport = newHTTPTransport()
	})

	closedWaitingForListeners := false
	defer func() {
		if !closedWaitingForListeners {
			close(h.httpTransport.waitingForListeners)
		}
	}()

	if len(h.ListenAddrs) == 0 && h.StreamHost == nil {
		return ErrNoListeners
	}

	h.httpTransport.listeners = make([]net.Listener, 0, len(h.ListenAddrs)+1) // +1 for stream host

	streamHostAddrsCount := 0
	if h.StreamHost != nil {
		streamHostAddrsCount = len(h.StreamHost.Addrs())
	}
	h.httpTransport.listenAddrs = make([]ma.Multiaddr, 0, len(h.ListenAddrs)+streamHostAddrsCount)

	errCh := make(chan error)

	if h.StreamHost != nil {
		listener, err := streamHostListen(h.StreamHost)
		if err != nil {
			return err
		}
		h.httpTransport.listeners = append(h.httpTransport.listeners, listener)
		h.httpTransport.listenAddrs = append(h.httpTransport.listenAddrs, h.StreamHost.Addrs()...)

		go func() {
			errCh <- http.Serve(listener, &h.ServeMux)
		}()
	}

	closeAllListeners := func() {
		for _, l := range h.httpTransport.listeners {
			l.Close()
		}
	}

	err := func() error {
		for _, addr := range h.ListenAddrs {
			parsedAddr := parseMultiaddr(addr)
			// resolve the host
			ipaddr, err := net.ResolveIPAddr("ip", parsedAddr.host)
			if err != nil {
				return err
			}

			host := ipaddr.String()
			l, err := net.Listen("tcp", host+":"+parsedAddr.port)
			if err != nil {
				return err
			}
			h.httpTransport.listeners = append(h.httpTransport.listeners, l)

			// get resolved port
			_, port, err := net.SplitHostPort(l.Addr().String())
			if err != nil {
				return err
			}

			var listenAddr ma.Multiaddr
			if parsedAddr.useHTTPS && parsedAddr.sni != "" && parsedAddr.sni != host {
				listenAddr = ma.StringCast(fmt.Sprintf("/ip4/%s/tcp/%s/tls/sni/%s/http", host, port, parsedAddr.sni))
			} else {
				scheme := "http"
				if parsedAddr.useHTTPS {
					scheme = "https"
				}
				listenAddr = ma.StringCast(fmt.Sprintf("/ip4/%s/tcp/%s/%s", host, port, scheme))

			}

			if parsedAddr.useHTTPS {
				go func() {
					srv := http.Server{
						Handler:   &h.ServeMux,
						TLSConfig: h.TLSConfig,
					}
					errCh <- srv.ServeTLS(l, "", "")
				}()
				h.httpTransport.listenAddrs = append(h.httpTransport.listenAddrs, listenAddr)
			} else if h.ServeInsecureHTTP {
				go func() {
					errCh <- http.Serve(l, &h.ServeMux)
				}()
				h.httpTransport.listenAddrs = append(h.httpTransport.listenAddrs, listenAddr)
			} else {
				// We are not serving insecure HTTP
				log.Warnf("Not serving insecure HTTP on %s. Prefer an HTTPS endpoint.", listenAddr)
			}
		}
		return nil
	}()
	if err != nil {
		closeAllListeners()
		return err
	}

	close(h.httpTransport.waitingForListeners)
	closedWaitingForListeners = true

	if len(h.httpTransport.listeners) == 0 || len(h.httpTransport.listenAddrs) == 0 {
		closeAllListeners()
		return ErrNoListeners
	}

	expectedErrCount := len(h.httpTransport.listeners)
	select {
	case <-h.httpTransport.closeListeners:
	case err = <-errCh:
		expectedErrCount--
	}

	// Close all listeners
	closeAllListeners()
	for i := 0; i < expectedErrCount; i++ {
		<-errCh
	}
	close(errCh)

	return err
}

func (h *HTTPHost) Close() error {
	h.createHTTPTransport.Do(func() {
		h.httpTransport = newHTTPTransport()
	})
	close(h.httpTransport.closeListeners)
	return nil
}

// SetHTTPHandler sets the HTTP handler for a given protocol. Automatically
// manages the .well-known/libp2p mapping.
// http.StripPrefix is called on the handler, so the handler will be unaware of
// it's prefix path.
func (h *HTTPHost) SetHTTPHandler(p protocol.ID, handler http.Handler) {
	h.SetHTTPHandlerAtPath(p, string(p), handler)
}

// SetHTTPHandlerAtPath sets the HTTP handler for a given protocol using the
// given path. Automatically manages the .well-known/libp2p mapping.
// http.StripPrefix is called on the handler, so the handler will be unaware of
// it's prefix path.
func (h *HTTPHost) SetHTTPHandlerAtPath(p protocol.ID, path string, handler http.Handler) {
	if path[len(path)-1] != '/' {
		// We are nesting this handler under this path, so it should end with a slash.
		path += "/"
	}
	h.wk.AddProtocolMapping(p, ProtocolMeta{Path: path})
	h.ServeMux.Handle(path, http.StripPrefix(path, handler))
}

// PeerMetadataGetter lets RoundTrippers implement a specific way of caching a peer's protocol mapping.
type PeerMetadataGetter interface {
	GetPeerMetadata() (PeerMeta, error)
}

type streamRoundTripper struct {
	server   peer.ID
	h        host.Host
	httpHost *HTTPHost
}

type streamReadCloser struct {
	io.ReadCloser
	s network.Stream
}

func (s *streamReadCloser) Close() error {
	s.s.Close()
	return s.ReadCloser.Close()
}

func (rt *streamRoundTripper) GetPeerMetadata() (PeerMeta, error) {
	return rt.httpHost.getAndStorePeerMetadata(rt, rt.server)
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

// roundTripperForSpecificServer is an http.RoundTripper targets a specific server. Still reuses the underlying RoundTripper for the requests.
// The underlying RoundTripper MUST be an HTTP Transport.
type roundTripperForSpecificServer struct {
	http.RoundTripper
	ownRoundtripper  bool
	httpHost         *HTTPHost
	server           peer.ID
	targetServerAddr string
	sni              string
	scheme           string
	cachedProtos     PeerMeta
}

func (rt *roundTripperForSpecificServer) GetPeerMetadata() (PeerMeta, error) {
	// Do we already have the peer's protocol mapping?
	if rt.cachedProtos != nil {
		return rt.cachedProtos, nil
	}

	// if the underlying roundtripper implements GetPeerMetadata, use that
	if g, ok := rt.RoundTripper.(PeerMetadataGetter); ok {
		wk, err := g.GetPeerMetadata()
		if err == nil {
			rt.cachedProtos = wk
			return wk, nil
		}
	}

	wk, err := rt.httpHost.getAndStorePeerMetadata(rt, rt.server)
	if err == nil {
		rt.cachedProtos = wk
		return wk, nil
	}
	return wk, err
}

// RoundTrip implements http.RoundTripper.
func (rt *roundTripperForSpecificServer) RoundTrip(r *http.Request) (*http.Response, error) {
	if (r.URL.Scheme != "" && r.URL.Scheme != rt.scheme) || (r.URL.Host != "" && r.URL.Host != rt.targetServerAddr) {
		return nil, fmt.Errorf("this transport is only for requests to %s://%s", rt.scheme, rt.targetServerAddr)
	}
	r.URL.Scheme = rt.scheme
	r.URL.Host = rt.targetServerAddr
	r.Host = rt.sni
	return rt.RoundTripper.RoundTrip(r)
}

func (rt *roundTripperForSpecificServer) CloseIdleConnections() {
	if rt.ownRoundtripper {
		// Safe to close idle connections, since we own the RoundTripper. We
		// aren't closing other's idle connections.
		type closeIdler interface {
			CloseIdleConnections()
		}
		if tr, ok := rt.RoundTripper.(closeIdler); ok {
			tr.CloseIdleConnections()
		}
	}
	// No-op, since we don't want users thinking they are closing idle
	// connections for this server, when in fact they are closing all idle
	// connections
}

type namespacedRoundTripper struct {
	http.RoundTripper
	protocolPrefix    string
	protocolPrefixRaw string
}

func (rt *namespacedRoundTripper) GetPeerMetadata() (PeerMeta, error) {
	if g, ok := rt.RoundTripper.(PeerMetadataGetter); ok {
		return g.GetPeerMetadata()
	}

	return nil, fmt.Errorf("can not get peer protocol map. Inner roundtripper does not implement GetPeerMetadata")
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
	protos, err := h.getAndStorePeerMetadata(roundtripper, server)
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

// NamespacedClient returns an http.Client that is scoped to the given protocol
// on the given server. It creates a new RoundTripper for each call. If you are
// creating many namespaced clients, consider creating a round tripper directly
// and namespacing the roundripper yourself, then creating clients from the
// namespace round tripper.
func (h *HTTPHost) NamespacedClient(p protocol.ID, server peer.AddrInfo, opts ...roundTripperOptsFn) (http.Client, error) {
	rt, err := h.NewRoundTripper(server, opts...)
	if err != nil {
		return http.Client{}, err
	}

	nrt, err := h.NamespaceRoundTripper(rt, p, server.ID)
	if err != nil {
		return http.Client{}, err
	}

	return http.Client{Transport: &nrt}, nil
}

// NewRoundTripper returns an http.RoundTripper that can fulfill and HTTP
// request to the given server. It may use an HTTP transport or a stream based
// transport. It is valid to pass an empty server.ID.
func (h *HTTPHost) NewRoundTripper(server peer.AddrInfo, opts ...roundTripperOptsFn) (http.RoundTripper, error) {
	options := roundTripperOpts{}
	for _, o := range opts {
		options = o(options)
	}

	if options.ServerMustAuthenticatePeerID && server.ID == "" {
		return nil, fmt.Errorf("server must authenticate peer ID, but no peer ID provided")
	}

	httpAddrs := make([]ma.Multiaddr, 0, 1) // The common case of a single http address
	nonHTTPAddrs := make([]ma.Multiaddr, 0, len(server.Addrs))

	firstAddrIsHTTP := false

	for i, addr := range server.Addrs {
		addr, isHTTP := normalizeHTTPMultiaddr(addr)
		if isHTTP {
			if i == 0 {
				firstAddrIsHTTP = true
			}
			httpAddrs = append(httpAddrs, addr)
		} else {
			nonHTTPAddrs = append(nonHTTPAddrs, addr)
		}
	}

	// Do we have an existing connection to this peer?
	existingStreamConn := false
	if server.ID != "" && h.StreamHost != nil {
		existingStreamConn = len(h.StreamHost.Network().ConnsToPeer(server.ID)) > 0
	}

	// Currently the HTTP transport can not authenticate peer IDs.
	if !options.ServerMustAuthenticatePeerID && len(httpAddrs) > 0 && (options.preferHTTPTransport || (firstAddrIsHTTP && !existingStreamConn)) {
		parsed := parseMultiaddr(httpAddrs[0])
		scheme := "http"
		if parsed.useHTTPS {
			scheme = "https"
		}

		rt := h.DefaultClientRoundTripper
		if rt == nil {
			rt = http.DefaultTransport.(*http.Transport)
		}
		ownRoundtripper := false
		if parsed.sni != parsed.host {
			// We have a different host and SNI (e.g. using an IP address but specifying a SNI)
			// We need to make our own transport to support this.
			rt = rt.Clone()
			rt.TLSClientConfig.ServerName = parsed.sni
			ownRoundtripper = true
		}

		return &roundTripperForSpecificServer{
			RoundTripper:     rt,
			ownRoundtripper:  ownRoundtripper,
			httpHost:         h,
			server:           server.ID,
			targetServerAddr: parsed.host + ":" + parsed.port,
			sni:              parsed.sni,
			scheme:           scheme,
		}, nil
	}

	// Otherwise use a stream based transport
	if h.StreamHost == nil {
		return nil, fmt.Errorf("can not use the HTTP transport (either no address or PeerID auth is required), and no stream host provided")
	}
	if !existingStreamConn {
		if server.ID == "" {
			return nil, fmt.Errorf("can not use the HTTP transport, and no server peer ID provided")
		}
		err := h.StreamHost.Connect(context.Background(), peer.AddrInfo{ID: server.ID, Addrs: nonHTTPAddrs})
		if err != nil {
			return nil, fmt.Errorf("failed to connect to peer: %w", err)
		}
	}

	return &streamRoundTripper{h: h.StreamHost, server: server.ID, httpHost: h}, nil
}

type httpMultiaddr struct {
	useHTTPS bool
	host     string
	port     string
	sni      string
}

func parseMultiaddr(addr ma.Multiaddr) httpMultiaddr {
	out := httpMultiaddr{}
	ma.ForEach(addr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_IP4, ma.P_IP6, ma.P_DNS, ma.P_DNS4, ma.P_DNS6:
			out.host = c.Value()
		case ma.P_TCP, ma.P_UDP:
			out.port = c.Value()
		case ma.P_TLS, ma.P_HTTPS:
			out.useHTTPS = true
		case ma.P_SNI:
			out.sni = c.Value()

		}
		return out.host == "" || out.port == "" || !out.useHTTPS || out.sni == ""
	})

	if out.useHTTPS && out.sni == "" {
		out.sni = out.host
	}
	return out
}

var httpComponent, _ = ma.NewComponent("http", "")
var tlsComponent, _ = ma.NewComponent("tls", "")

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
	if afterHTTPS == nil {
		return ma.Join(beforeHTTPS, tlsComponent, httpComponent), isHTTPMultiaddr
	}

	return ma.Join(beforeHTTPS, tlsComponent, httpComponent, afterHTTPS), isHTTPMultiaddr
}

// ProtocolPathPrefix looks up the protocol path in the well-known mapping and
// returns it. Will only store the peer's protocol mapping if the server ID is
// provided.
func (h *HTTPHost) getAndStorePeerMetadata(roundtripper http.RoundTripper, server peer.ID) (PeerMeta, error) {
	if h.peerMetadata == nil {
		h.peerMetadata = newPeerMetadataCache()
	}
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

	body := [peerMetadataLimit]byte{}
	bytesRead := 0
	for {
		n, err := resp.Body.Read(body[bytesRead:])
		bytesRead += n
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if bytesRead >= peerMetadataLimit {
			return nil, fmt.Errorf("peer metadata too large")
		}
	}

	meta := PeerMeta{}
	json.Unmarshal(body[:bytesRead], &meta)
	if server != "" {
		h.peerMetadata.Add(server, meta)
	}

	return meta, nil
}

// AddPeerMetadata adds a peer's protocol metadata to the http host. Useful if
// you have out-of-band knowledge of a peer's protocol mapping.
func (h *HTTPHost) AddPeerMetadata(server peer.ID, meta PeerMeta) {
	if h.peerMetadata == nil {
		h.peerMetadata = newPeerMetadataCache()
	}
	h.peerMetadata.Add(server, meta)
}

// GetPeerMetadata gets a peer's cached protocol metadata from the http host.
func (h *HTTPHost) GetPeerMetadata(server peer.ID) (PeerMeta, bool) {
	if h.peerMetadata == nil {
		return nil, false
	}
	return h.peerMetadata.Get(server)
}

// RemovePeerMetadata removes a peer's protocol metadata from the http host
func (h *HTTPHost) RemovePeerMetadata(server peer.ID, meta PeerMeta) {
	if h.peerMetadata == nil {
		return
	}
	h.peerMetadata.Remove(server)
}
