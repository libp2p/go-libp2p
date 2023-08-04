package libp2phttp

import (
	"crypto/tls"
	"fmt"

	"github.com/libp2p/go-libp2p/core/host"
	ma "github.com/multiformats/go-multiaddr"
)

type HTTPHostOption func(*HTTPHost) error

// WithStreamHost sets the stream host to use for HTTP over libp2p streams.
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
