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
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	logging "github.com/ipfs/go-log/v2"
	host "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	gostream "github.com/libp2p/go-libp2p/p2p/net/gostream"
	ma "github.com/multiformats/go-multiaddr"
)

var log = logging.Logger("libp2phttp")

var WellKnownRequestTimeout = 30 * time.Second

const ProtocolIDForMultistreamSelect = "/http/1.1"
const WellKnownProtocols = "/.well-known/libp2p/protocols"

// LegacyWellKnownProtocols refer to a the well-known resource used in an early
// draft of the libp2p+http spec. Some users have deployed this, and need backwards compatibility.
// Hopefully we can phase this out in the future. Context: https://github.com/libp2p/go-libp2p/pull/2797
const LegacyWellKnownProtocols = "/.well-known/libp2p"

const peerMetadataLimit = 8 << 10 // 8KB
const peerMetadataLRUSize = 256   // How many different peer's metadata to keep in our LRU cache

// ProtocolMeta is metadata about a protocol.
type ProtocolMeta struct {
	// Path defines the HTTP Path prefix used for this protocol
	Path string `json:"path"`
}

type PeerMeta map[protocol.ID]ProtocolMeta

// WellKnownHandler is an http.Handler that serves the well-known resource
type WellKnownHandler struct {
	wellknownMapMu   sync.Mutex
	wellKnownMapping PeerMeta
	wellKnownCache   []byte
}

// streamHostListen returns a net.Listener that listens on libp2p streams for HTTP/1.1 messages.
func streamHostListen(streamHost host.Host) (net.Listener, error) {
	return gostream.Listen(streamHost, ProtocolIDForMultistreamSelect, gostream.IgnoreEOF())
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
	mapping := h.wellKnownCache
	var err error
	if mapping == nil {
		mapping, err = json.Marshal(h.wellKnownMapping)
		if err == nil {
			h.wellKnownCache = mapping
		}
	}
	h.wellknownMapMu.Unlock()
	if err != nil {
		http.Error(w, "Marshal error", http.StatusInternalServerError)
		return
	}
	w.Header().Add("Content-Type", "application/json")
	w.Header().Add("Content-Length", strconv.Itoa(len(mapping)))
	w.Write(mapping)
}

func (h *WellKnownHandler) AddProtocolMeta(p protocol.ID, protocolMeta ProtocolMeta) {
	h.wellknownMapMu.Lock()
	if h.wellKnownMapping == nil {
		h.wellKnownMapping = make(map[protocol.ID]ProtocolMeta)
	}
	h.wellKnownMapping[p] = protocolMeta
	h.wellKnownCache = nil
	h.wellknownMapMu.Unlock()
}

func (h *WellKnownHandler) RemoveProtocolMeta(p protocol.ID) {
	h.wellknownMapMu.Lock()
	if h.wellKnownMapping != nil {
		delete(h.wellKnownMapping, p)
	}
	h.wellKnownCache = nil
	h.wellknownMapMu.Unlock()
}

// Host is a libp2p host for request/responses with HTTP semantics. This is
// in contrast to a stream-oriented host like the core host.Host interface. Its
// zero-value (&Host{}) is usable. Do not copy by value.
// See examples for usage.
//
//	Warning, this is experimental. The API will likely change.
type Host struct {
	// StreamHost is a stream based libp2p host used to do HTTP over libp2p streams. May be nil
	StreamHost host.Host
	// ListenAddrs are the requested addresses to listen on. Multiaddrs must be
	// valid HTTP(s) multiaddr. Only multiaddrs for an HTTP transport are
	// supported (must end with /http or /https).
	ListenAddrs []ma.Multiaddr
	// TLSConfig is the TLS config for the server to use
	TLSConfig *tls.Config
	// InsecureAllowHTTP indicates if the server is allowed to serve unencrypted
	// HTTP requests over TCP.
	InsecureAllowHTTP bool
	// ServeMux is the http.ServeMux used by the server to serve requests. If
	// nil, a new serve mux will be created. Users may manually add handlers to
	// this mux instead of using `SetHTTPHandler`, but if they do, they should
	// also update the WellKnownHandler's protocol mapping.
	ServeMux           *http.ServeMux
	initializeServeMux sync.Once

	// DefaultClientRoundTripper is the default http.RoundTripper for clients to
	// use when making requests over an HTTP transport. This must be an
	// `*http.Transport` type so that the transport can be cloned and the
	// `TLSClientConfig` field can be configured. If unset, it will create a new
	// `http.Transport` on first use.
	DefaultClientRoundTripper *http.Transport

	// WellKnownHandler is the http handler for the well-known
	// resource. It is responsible for sharing this node's protocol metadata
	// with other nodes. Users only care about this if they set their own
	// ServeMux with pre-existing routes. By default, new protocols are added
	// here when a user calls `SetHTTPHandler` or `SetHTTPHandlerAtPath`.
	WellKnownHandler WellKnownHandler

	// EnableCompatibilityWithLegacyWellKnownEndpoint allows compatibility with
	// an older version of the spec that defined the well-known resource as:
	// .well-known/libp2p.
	// For servers, this means hosting the well-known resource at both the
	// legacy and current paths.
	// For clients it means making two parallel requests and picking the first one that succeeds.
	//
	// Long term this should be deprecated once enough users have upgraded to a
	// newer go-libp2p version and we can remove all this code.
	EnableCompatibilityWithLegacyWellKnownEndpoint bool

	// peerMetadata is an LRU cache of a peer's well-known protocol map.
	peerMetadata *lru.Cache[peer.ID, PeerMeta]
	// createHTTPTransport is used to lazily create the httpTransport in a thread-safe way.
	createHTTPTransport sync.Once
	// createDefaultClientRoundTripper is used to lazily create the default
	// client round tripper in a thread-safe way.
	createDefaultClientRoundTripper sync.Once
	httpTransport                   *httpTransport
}

type httpTransport struct {
	listenAddrs         []ma.Multiaddr
	listeners           []net.Listener
	closeListeners      chan struct{}
	waitingForListeners chan struct{}
}

func newPeerMetadataCache() *lru.Cache[peer.ID, PeerMeta] {
	peerMetadata, err := lru.New[peer.ID, PeerMeta](peerMetadataLRUSize)
	if err != nil {
		// Only happens if size is < 1. We make sure to not do that, so this should never happen.
		panic(err)
	}
	return peerMetadata
}

func (h *Host) httpTransportInit() {
	h.createHTTPTransport.Do(func() {
		h.httpTransport = &httpTransport{
			closeListeners:      make(chan struct{}),
			waitingForListeners: make(chan struct{}),
		}
	})
}

func (h *Host) serveMuxInit() {
	h.initializeServeMux.Do(func() {
		if h.ServeMux == nil {
			h.ServeMux = http.NewServeMux()
		}
	})
}

func (h *Host) Addrs() []ma.Multiaddr {
	h.httpTransportInit()
	<-h.httpTransport.waitingForListeners
	return h.httpTransport.listenAddrs
}

// ID returns the peer ID of the underlying stream host, or the zero value if there is no stream host.
func (h *Host) PeerID() peer.ID {
	if h.StreamHost != nil {
		return h.StreamHost.ID()
	}
	return ""
}

var ErrNoListeners = errors.New("nothing to listen on")

func (h *Host) setupListeners(listenerErrCh chan error) error {
	for _, addr := range h.ListenAddrs {
		parsedAddr, err := parseMultiaddr(addr)
		if err != nil {
			return err
		}
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
					Handler:   h.ServeMux,
					TLSConfig: h.TLSConfig,
				}
				listenerErrCh <- srv.ServeTLS(l, "", "")
			}()
			h.httpTransport.listenAddrs = append(h.httpTransport.listenAddrs, listenAddr)
		} else if h.InsecureAllowHTTP {
			go func() {
				listenerErrCh <- http.Serve(l, h.ServeMux)
			}()
			h.httpTransport.listenAddrs = append(h.httpTransport.listenAddrs, listenAddr)
		} else {
			// We are not serving insecure HTTP
			log.Warnf("Not serving insecure HTTP on %s. Prefer an HTTPS endpoint.", listenAddr)
		}
	}
	return nil
}

// Serve starts the HTTP transport listeners. Always returns a non-nil error.
// If there are no listeners, returns ErrNoListeners.
func (h *Host) Serve() error {
	// assert that each addr contains a /http component
	for _, addr := range h.ListenAddrs {
		_, isHTTP := normalizeHTTPMultiaddr(addr)
		if !isHTTP {
			return fmt.Errorf("address %s does not contain a /http or /https component", addr)
		}
	}

	h.serveMuxInit()
	h.ServeMux.Handle(WellKnownProtocols, &h.WellKnownHandler)
	if h.EnableCompatibilityWithLegacyWellKnownEndpoint {
		h.ServeMux.Handle(LegacyWellKnownProtocols, &h.WellKnownHandler)
	}

	h.httpTransportInit()

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
			errCh <- http.Serve(listener, connectionCloseHeaderMiddleware(h.ServeMux))
		}()
	}

	closeAllListeners := func() {
		for _, l := range h.httpTransport.listeners {
			l.Close()
		}
	}

	err := h.setupListeners(errCh)
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
		err = http.ErrServerClosed
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

func (h *Host) Close() error {
	h.httpTransportInit()
	close(h.httpTransport.closeListeners)
	return nil
}

// SetHTTPHandler sets the HTTP handler for a given protocol. Automatically
// manages the well-known resource mapping.
// http.StripPrefix is called on the handler, so the handler will be unaware of
// its prefix path.
func (h *Host) SetHTTPHandler(p protocol.ID, handler http.Handler) {
	h.SetHTTPHandlerAtPath(p, string(p), handler)
}

// SetHTTPHandlerAtPath sets the HTTP handler for a given protocol using the
// given path. Automatically manages the well-known resource mapping.
// http.StripPrefix is called on the handler, so the handler will be unaware of
// its prefix path.
func (h *Host) SetHTTPHandlerAtPath(p protocol.ID, path string, handler http.Handler) {
	if path == "" || path[len(path)-1] != '/' {
		// We are nesting this handler under this path, so it should end with a slash.
		path += "/"
	}
	h.WellKnownHandler.AddProtocolMeta(p, ProtocolMeta{Path: path})
	h.serveMuxInit()
	// Do not trim the trailing / from path
	// This allows us to serve `/a/b` when we mount a handler for `/b` at path `/a`
	h.ServeMux.Handle(path, http.StripPrefix(strings.TrimSuffix(path, "/"), handler))
}

// PeerMetadataGetter lets RoundTrippers implement a specific way of caching a peer's protocol mapping.
type PeerMetadataGetter interface {
	GetPeerMetadata() (PeerMeta, error)
}

type streamRoundTripper struct {
	server peer.ID
	// if true, we won't add the server's addresses to the peerstore. This
	// should only be set when creating the struct.
	skipAddAddrs bool
	addrsAdded   sync.Once
	serverAddrs  []ma.Multiaddr
	h            host.Host
	httpHost     *Host
}

// streamReadCloser wraps an io.ReadCloser and closes the underlying stream when
// closed (as well as closing the wrapped ReadCloser). This is necessary because
// we have two things to close, the body and the stream. The stream isn't closed
// by the body automatically, as hinted at by the fact that `http.ReadResponse`
// takes a bufio.Reader.
type streamReadCloser struct {
	io.ReadCloser
	s network.Stream
}

func (s *streamReadCloser) Close() error {
	s.s.Close()
	return s.ReadCloser.Close()
}

func (rt *streamRoundTripper) GetPeerMetadata() (PeerMeta, error) {
	ctx := context.Background()
	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(WellKnownRequestTimeout))
	defer cancel()
	return rt.httpHost.getAndStorePeerMetadata(ctx, rt, rt.server)
}

// RoundTrip implements http.RoundTripper.
func (rt *streamRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	// Add the addresses we learned about for this server
	if !rt.skipAddAddrs {
		rt.addrsAdded.Do(func() {
			if len(rt.serverAddrs) > 0 {
				rt.h.Peerstore().AddAddrs(rt.server, rt.serverAddrs, peerstore.TempAddrTTL)
			}
			rt.serverAddrs = nil // may as well cleanup
		})
	}

	s, err := rt.h.NewStream(r.Context(), rt.server, ProtocolIDForMultistreamSelect)
	if err != nil {
		return nil, err
	}

	// Write connection: close header to ensure the stream is closed after the response
	r.Header.Add("connection", "close")

	go func() {
		defer s.CloseWrite()
		r.Write(s)
		if r.Body != nil {
			r.Body.Close()
		}
	}()

	if deadline, ok := r.Context().Deadline(); ok {
		s.SetReadDeadline(deadline)
	}

	resp, err := http.ReadResponse(bufio.NewReader(s), r)
	if err != nil {
		s.Close()
		return nil, err
	}
	resp.Body = &streamReadCloser{resp.Body, s}

	locUrl, err := resp.Location()
	if err == nil {
		// Location url in response. Is this a multiaddr uri? and is it relative?
		// If it's relative we want to convert it to an absolute multiaddr uri
		// so that the next request knows how to reach the endpoint.
		if locUrl.Scheme == "multiaddr" && resp.Request.URL.Scheme == "multiaddr" {
			// Check if it's a relative URI and turn it into an absolute one
			u, err := relativeMultiaddrURIToAbs(resp.Request.URL, locUrl)
			if err == nil {
				// It was a relative URI and we were able to convert it to an absolute one
				// Update the location header to be an absolute multiaddr uri
				resp.Header.Set("Location", u.String())
			}
		}
	}

	return resp, nil
}

var errNotRelative = errors.New("not relative")

// relativeMultiaddrURIToAbs takes a relative multiaddr URI and turns it into an
// absolute one. Useful, for example, when a server gives us a relative URI for a redirect.
// It allows the following request (the one after redirected) to reach the correct server.
func relativeMultiaddrURIToAbs(original *url.URL, relative *url.URL) (*url.URL, error) {
	// Is this a relative uri? We know if it is because non-relative URI's of the form:
	// "multiaddr:/ip4/1.2.3.4/tcp/9899" when parsed by Go's url package will have url.OmitHost == true
	// But if it is relative (just a path to an http resource e.g. /here-instead)
	// a redirect will inherit the multiaddr scheme, but set url.OmitHost == false. It will also stringify as something like
	// multiaddr://here-instead.
	if relative.OmitHost {
		// Not relative (at least we can't tell). Nothing we can do here
		return nil, errNotRelative
	}
	originalStr := original.RawPath
	if originalStr == "" {
		originalStr = original.Path
	}
	originalMa, err := ma.NewMultiaddr(originalStr)
	if err != nil {
		return nil, errors.New("original uri is not a multiaddr")
	}

	relativePathComponent, err := ma.NewComponent("http-path", relative.Path)
	if err != nil {
		return nil, errors.New("relative path is not a valid http-path")
	}

	withoutPath, _ := ma.SplitFunc(originalMa, func(c ma.Component) bool {
		return c.Protocol().Code == ma.P_HTTP_PATH
	})
	withNewPath := withoutPath.Encapsulate(relativePathComponent)
	return url.Parse("multiaddr:" + withNewPath.String())
}

// roundTripperForSpecificServer is an http.RoundTripper targets a specific server. Still reuses the underlying RoundTripper for the requests.
// The underlying RoundTripper MUST be an HTTP Transport.
type roundTripperForSpecificServer struct {
	http.RoundTripper
	ownRoundtripper  bool
	httpHost         *Host
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

	ctx := context.Background()
	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(WellKnownRequestTimeout))
	defer cancel()
	wk, err := rt.httpHost.getAndStorePeerMetadata(ctx, rt, rt.server)
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

// namespacedRoundTripper is a round tripper that prefixes all requests with a
// given path prefix. It is used to namespace requests to a specific protocol.
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
func (h *Host) NamespaceRoundTripper(roundtripper http.RoundTripper, p protocol.ID, server peer.ID) (*namespacedRoundTripper, error) {
	ctx := context.Background()
	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(WellKnownRequestTimeout))
	defer cancel()
	protos, err := h.getAndStorePeerMetadata(ctx, roundtripper, server)
	if err != nil {
		return &namespacedRoundTripper{}, err
	}

	v, ok := protos[p]
	if !ok {
		return &namespacedRoundTripper{}, fmt.Errorf("no protocol %s for server %s", p, server)
	}

	path := v.Path
	if path[len(path)-1] == '/' {
		// Trim the trailing slash, since it's common to make requests starting with a leading forward slash for the path
		path = path[:len(path)-1]
	}

	u, err := url.Parse(path)
	if err != nil {
		return &namespacedRoundTripper{}, fmt.Errorf("invalid path %s for protocol %s for server %s", v.Path, p, server)
	}

	return &namespacedRoundTripper{
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
func (h *Host) NamespacedClient(p protocol.ID, server peer.AddrInfo, opts ...RoundTripperOption) (http.Client, error) {
	rt, err := h.NewConstrainedRoundTripper(server, opts...)
	if err != nil {
		return http.Client{}, err
	}

	nrt, err := h.NamespaceRoundTripper(rt, p, server.ID)
	if err != nil {
		return http.Client{}, err
	}

	return http.Client{Transport: nrt}, nil
}
func (h *Host) initDefaultRT() {
	h.createDefaultClientRoundTripper.Do(func() {
		if h.DefaultClientRoundTripper == nil {
			tr, ok := http.DefaultTransport.(*http.Transport)
			if ok {
				h.DefaultClientRoundTripper = tr
			} else {
				h.DefaultClientRoundTripper = &http.Transport{}
			}
		}
	})
}

// RoundTrip implements http.RoundTripper for the HTTP Host.
// This allows you to use the Host as a Transport for an http.Client.
// See the example for idomatic usage.
func (h *Host) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.URL.Scheme {
	case "http", "https":
		h.initDefaultRT()
		return h.DefaultClientRoundTripper.RoundTrip(r)
	case "multiaddr":
		break
	default:
		return nil, fmt.Errorf("unsupported scheme %s", r.URL.Scheme)
	}

	addr, err := ma.NewMultiaddr(r.URL.String()[len("multiaddr:"):])
	if err != nil {
		return nil, err
	}
	addr, isHTTP := normalizeHTTPMultiaddr(addr)
	parsed, err := parseMultiaddr(addr)
	if err != nil {
		return nil, err
	}

	if isHTTP {
		scheme := "http"
		if parsed.useHTTPS {
			scheme = "https"
		}
		u := url.URL{
			Scheme: scheme,
			Host:   parsed.host + ":" + parsed.port,
			Path:   parsed.httpPath,
		}
		r.URL = &u

		h.initDefaultRT()
		rt := h.DefaultClientRoundTripper
		if parsed.sni != parsed.host {
			// We have a different host and SNI (e.g. using an IP address but specifying a SNI)
			// We need to make our own transport to support this.
			//
			// TODO: if we end up using this code path a lot, we could maintain
			// a pool of these transports.  For now though, it's here for
			// completeness, but I don't expect us to hit it often.
			rt = rt.Clone()
			rt.TLSClientConfig.ServerName = parsed.sni
		}

		return rt.RoundTrip(r)
	}

	if h.StreamHost == nil {
		return nil, fmt.Errorf("can not do HTTP over streams. Missing StreamHost")
	}

	if parsed.peer == "" {
		return nil, fmt.Errorf("no peer ID in multiaddr")
	}
	withoutHTTPPath, _ := ma.SplitFunc(addr, func(c ma.Component) bool {
		return c.Protocol().Code == ma.P_HTTP_PATH
	})
	h.StreamHost.Peerstore().AddAddrs(parsed.peer, []ma.Multiaddr{withoutHTTPPath}, peerstore.TempAddrTTL)

	// Set the Opaque field to the http-path so that the HTTP request only makes
	// a reference to that path and not the whole multiaddr uri
	r.URL.Opaque = parsed.httpPath
	if r.Host == "" {
		// Fill in the host if it's not already set
		r.Host = parsed.host + ":" + parsed.port
	}
	srt := streamRoundTripper{
		server:       parsed.peer,
		skipAddAddrs: true,
		httpHost:     h,
		h:            h.StreamHost,
	}
	return srt.RoundTrip(r)
}

// NewConstrainedRoundTripper returns an http.RoundTripper that can fulfill and HTTP
// request to the given server. It may use an HTTP transport or a stream based
// transport. It is valid to pass an empty server.ID.
// If there are multiple addresses for the server, it will pick the best
// transport (stream vs standard HTTP) using the following rules:
//   - If PreferHTTPTransport is set, use the HTTP transport.
//   - If ServerMustAuthenticatePeerID is set, use the stream transport, as the
//     HTTP transport does not do peer id auth yet.
//   - If we already have a connection on a stream transport, use that.
//   - Otherwise, if we have both, use the HTTP transport.
func (h *Host) NewConstrainedRoundTripper(server peer.AddrInfo, opts ...RoundTripperOption) (http.RoundTripper, error) {
	options := roundTripperOpts{}
	for _, o := range opts {
		options = o(options)
	}

	if options.serverMustAuthenticatePeerID && server.ID == "" {
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
	if !options.serverMustAuthenticatePeerID && len(httpAddrs) > 0 && (options.preferHTTPTransport || (firstAddrIsHTTP && !existingStreamConn)) {
		parsed, err := parseMultiaddr(httpAddrs[0])
		if err != nil {
			return nil, err
		}
		scheme := "http"
		if parsed.useHTTPS {
			scheme = "https"
		}

		h.initDefaultRT()
		rt := h.DefaultClientRoundTripper
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
	}

	return &streamRoundTripper{h: h.StreamHost, server: server.ID, serverAddrs: nonHTTPAddrs, httpHost: h}, nil
}

type explodedMultiaddr struct {
	useHTTPS bool
	host     string
	port     string
	sni      string
	httpPath string
	peer     peer.ID
}

func parseMultiaddr(addr ma.Multiaddr) (explodedMultiaddr, error) {
	out := explodedMultiaddr{}
	var err error
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
		case ma.P_HTTP_PATH:
			out.httpPath, err = url.QueryUnescape(c.Value())
			if err == nil && out.httpPath[0] != '/' {
				out.httpPath = "/" + out.httpPath
			}
		case ma.P_P2P:
			out.peer, err = peer.Decode(c.Value())
		}

		// stop if there is an error, otherwise iterate over all components in case this is a circuit address
		return err == nil
	})

	if out.useHTTPS && out.sni == "" {
		out.sni = out.host
	}

	if out.httpPath == "" {
		out.httpPath = "/"
	}
	return out, err
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
	if beforeHTTPS == nil || !isHTTPMultiaddr {
		return addr, false
	}

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

// getAndStorePeerMetadata looks up the protocol path in the well-known mapping and
// returns it. Will only store the peer's protocol mapping if the server ID is
// provided.
func (h *Host) getAndStorePeerMetadata(ctx context.Context, roundtripper http.RoundTripper, server peer.ID) (PeerMeta, error) {
	if h.peerMetadata == nil {
		h.peerMetadata = newPeerMetadataCache()
	}
	if meta, ok := h.peerMetadata.Get(server); server != "" && ok {
		return meta, nil
	}

	var meta PeerMeta
	var err error
	if h.EnableCompatibilityWithLegacyWellKnownEndpoint {
		type metaAndErr struct {
			m   PeerMeta
			err error
		}
		legacyRespCh := make(chan metaAndErr, 1)
		wellKnownRespCh := make(chan metaAndErr, 1)
		ctx, cancel := context.WithCancel(ctx)
		go func() {
			meta, err := requestPeerMeta(ctx, roundtripper, LegacyWellKnownProtocols)
			legacyRespCh <- metaAndErr{meta, err}
		}()
		go func() {
			meta, err := requestPeerMeta(ctx, roundtripper, WellKnownProtocols)
			wellKnownRespCh <- metaAndErr{meta, err}
		}()
		select {
		case resp := <-legacyRespCh:
			if resp.err != nil {
				resp = <-wellKnownRespCh
			}
			meta, err = resp.m, resp.err
		case resp := <-wellKnownRespCh:
			if resp.err != nil {
				legacyResp := <-legacyRespCh
				if legacyResp.err != nil {
					// If both endpoints error, return the error from the well
					// known resource (not the legacy well known resource)
					meta, err = resp.m, resp.err
				} else {
					meta, err = legacyResp.m, legacyResp.err
				}
			} else {
				meta, err = resp.m, resp.err
			}
		}
		cancel()
	} else {
		meta, err = requestPeerMeta(ctx, roundtripper, WellKnownProtocols)
	}
	if err != nil {
		return nil, err
	}

	if server != "" {
		h.peerMetadata.Add(server, meta)
	}

	return meta, nil
}

func requestPeerMeta(ctx context.Context, roundtripper http.RoundTripper, wellKnownResource string) (PeerMeta, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", wellKnownResource, nil)
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

	meta := PeerMeta{}
	err = json.NewDecoder(&io.LimitedReader{
		R: resp.Body,
		N: peerMetadataLimit,
	}).Decode(&meta)
	if err != nil {
		return nil, err
	}

	return meta, nil
}

// SetPeerMetadata adds a peer's protocol metadata to the http host. Useful if
// you have out-of-band knowledge of a peer's protocol mapping.
func (h *Host) SetPeerMetadata(server peer.ID, meta PeerMeta) {
	if h.peerMetadata == nil {
		h.peerMetadata = newPeerMetadataCache()
	}
	h.peerMetadata.Add(server, meta)
}

// AddPeerMetadata merges the given peer's protocol metadata to the http host. Useful if
// you have out-of-band knowledge of a peer's protocol mapping.
func (h *Host) AddPeerMetadata(server peer.ID, meta PeerMeta) {
	if h.peerMetadata == nil {
		h.peerMetadata = newPeerMetadataCache()
	}
	origMeta, ok := h.peerMetadata.Get(server)
	if !ok {
		h.peerMetadata.Add(server, meta)
		return
	}
	for proto, m := range meta {
		origMeta[proto] = m
	}
	h.peerMetadata.Add(server, origMeta)
}

// GetPeerMetadata gets a peer's cached protocol metadata from the http host.
func (h *Host) GetPeerMetadata(server peer.ID) (PeerMeta, bool) {
	if h.peerMetadata == nil {
		return nil, false
	}
	return h.peerMetadata.Get(server)
}

// RemovePeerMetadata removes a peer's protocol metadata from the http host
func (h *Host) RemovePeerMetadata(server peer.ID) {
	if h.peerMetadata == nil {
		return
	}
	h.peerMetadata.Remove(server)
}

func connectionCloseHeaderMiddleware(next http.Handler) http.Handler {
	// Sets connection: close. It's preferable to not reuse streams for HTTP.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		next.ServeHTTP(w, r)
	})
}
