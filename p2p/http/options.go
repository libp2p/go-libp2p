package libp2phttp

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/libp2p/go-libp2p/core/host"
	ma "github.com/multiformats/go-multiaddr"
)

type HTTPHostOption func(*HTTPHost) error

// ListenAddrs sets the listen addresses for the HTTP transport.
func ListenAddrs(addrs []ma.Multiaddr) HTTPHostOption {
	return func(h *HTTPHost) error {
		// assert that each addr contains a /http component
		for _, addr := range addrs {
			if _, err := addr.ValueForProtocol(ma.P_HTTP); err != nil {
				return fmt.Errorf("address %s does not contain a /http component", addr)
			}
		}

		h.httpTransport.requestedListenAddrs = addrs

		return nil
	}
}

// TLSConfig sets the server TLS config for the HTTP transport.
func TLSConfig(tlsConfig *tls.Config) HTTPHostOption {
	return func(h *HTTPHost) error {
		h.httpTransport.tlsConfig = tlsConfig
		return nil
	}
}

// StreamHost sets the stream host to use for HTTP over libp2p streams.
func StreamHost(streamHost host.Host) HTTPHostOption {
	return func(h *HTTPHost) error {
		h.streamHost = streamHost
		return nil
	}
}

// WithTLSClientConfig sets the TLS client config for the native HTTP transport.
func WithTLSClientConfig(tlsConfig *tls.Config) HTTPHostOption {
	return func(h *HTTPHost) error {
		h.httpRoundTripper.TLSClientConfig = tlsConfig
		return nil
	}
}

// CustomRootHandler sets the root handler for the HTTP transport. It *does not*
// set up a .well-known/libp2p handler. Users assume the responsibility of
// handling .well-known/libp2p, as well as keeping it up to date.
func CustomRootHandler(mux http.Handler) HTTPHostOption {
	return func(h *HTTPHost) error {
		h.rootHandler = http.ServeMux{}
		h.rootHandler.Handle("/", mux)
		return nil
	}
}

type RoundTripperOptsFn func(o roundTripperOpts) roundTripperOpts

type roundTripperOpts struct {
	// todo SkipClientAuth bool
	preferHTTPTransport          bool
	ServerMustAuthenticatePeerID bool
}

func RoundTripperPreferHTTPTransport(o roundTripperOpts) roundTripperOpts {
	o.preferHTTPTransport = true
	return o
}

func ServerMustAuthenticatePeerID(o roundTripperOpts) roundTripperOpts {
	o.ServerMustAuthenticatePeerID = true
	return o
}
