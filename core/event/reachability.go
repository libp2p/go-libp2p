package event

import (
	"github.com/libp2p/go-libp2p/core/network"
	ma "github.com/multiformats/go-multiaddr"
)

// EvtLocalReachabilityChanged is an event struct to be emitted when the local's
// node reachability changes state.
//
// This event is usually emitted by the AutoNAT subsystem.
type EvtLocalReachabilityChanged struct {
	Reachability network.Reachability
}

// EvtHostReachableAddrsChanged is sent when host's reachable or unreachable addresses change
// Reachable, Unreachable, and Unknown only contain Public IP or DNS addresses
//
// Experimental: This API is unstable. Any changes to this event will be done without a deprecation notice.
type EvtHostReachableAddrsChanged struct {
	Reachable   []ma.Multiaddr
	Unreachable []ma.Multiaddr
	Unknown     []ma.Multiaddr
}
