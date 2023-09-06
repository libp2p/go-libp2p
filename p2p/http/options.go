package libp2phttp

type RoundTripperOption func(o roundTripperOpts) roundTripperOpts

type roundTripperOpts struct {
	preferHTTPTransport          bool
	serverMustAuthenticatePeerID bool
}

// PreferHTTPTransport tells the roundtripper constructor to prefer using an
// HTTP transport (as opposed to a libp2p stream transport). Useful, for
// example, if you want to attempt to leverage HTTP caching.
func PreferHTTPTransport(o roundTripperOpts) roundTripperOpts {
	o.preferHTTPTransport = true
	return o
}

// ServerMustAuthenticatePeerID tells the roundtripper constructor that we MUST
// authenticate the Server's PeerID. Note: this currently means we can not use a
// native HTTP transport (HTTP peer id authentication is not yet implemented: https://github.com/libp2p/specs/pull/564).
func ServerMustAuthenticatePeerID(o roundTripperOpts) roundTripperOpts {
	o.serverMustAuthenticatePeerID = true
	return o
}
