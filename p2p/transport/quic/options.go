package libp2pquic

import p2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"

// Option is a function that configures the QUIC transport.
type Option func(o *transportConfig) error

type transportConfig struct {
	tlsIdentityOpts []p2ptls.IdentityOption
}

// WithTLSIdentityOption passes the given p2ptls.IdentityOption through to the
// TLS identity used by the QUIC transport.
func WithTLSIdentityOption(opt p2ptls.IdentityOption) Option {
	return func(c *transportConfig) error {
		c.tlsIdentityOpts = append(c.tlsIdentityOpts, opt)
		return nil
	}
}
