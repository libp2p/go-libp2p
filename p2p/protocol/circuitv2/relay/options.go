package relay

import (
	"net/netip"

	"github.com/multiformats/go-multiaddr"
)

type Option func(*Relay) error

// WithResources is a Relay option that sets specific relay resources for the relay.
func WithResources(rc Resources) Option {
	return func(r *Relay) error {
		r.rc = rc
		return nil
	}
}

// WithLimit is a Relay option that sets only the relayed connection limits for the relay.
func WithLimit(limit *RelayLimit) Option {
	return func(r *Relay) error {
		r.rc.Limit = limit
		return nil
	}
}

// If host's addresses are inside any of the passed prefixes
// Those addresses will be used in the advertised ones passed to the client.
// Can be called multiple times
func WithRelaySubnets(prefixes ...netip.Prefix) (option Option) {
	return func(r *Relay) (err error) {
		r.publicSubnets = append(r.publicSubnets, prefixes...)
		return nil
	}
}

// If the relay has statically assigned addresses that it can advertise
// It can specify those addresses directly here.
// This is useful when we already know our address and we want to directly specify it
// /ip4/192.168.0.10/...
func WithRelayAddresses(addresses ...multiaddr.Multiaddr) (option Option) {
	return func(r *Relay) (err error) {
		r.publicAddresses = append(r.publicAddresses, addresses...)
		return nil
	}
}

// WithInfiniteLimits is a Relay option that disables limits.
func WithInfiniteLimits() Option {
	return func(r *Relay) error {
		r.rc.Limit = nil
		return nil
	}
}

// WithACL is a Relay option that supplies an ACLFilter for access control.
func WithACL(acl ACLFilter) Option {
	return func(r *Relay) error {
		r.acl = acl
		return nil
	}
}

// WithMetricsTracer is a Relay option that supplies a MetricsTracer for metrics
func WithMetricsTracer(mt MetricsTracer) Option {
	return func(r *Relay) error {
		r.metricsTracer = mt
		return nil
	}
}
