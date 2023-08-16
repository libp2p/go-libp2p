package libp2phttp

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
