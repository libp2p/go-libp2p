package metricshelper

import ma "github.com/multiformats/go-multiaddr"

var transports = [...]int{ma.P_CIRCUIT, ma.P_WEBRTC, ma.P_WEBRTC_DIRECT, ma.P_WEBTRANSPORT, ma.P_QUIC, ma.P_QUIC_V1, ma.P_WSS, ma.P_WS, ma.P_TCP}

func GetTransport(a ma.Multiaddr) string {
	if a == nil {
		return "other"
	}
	for i := len(a) - 1; i >= 0; i-- {
		p := a[i].Protocol()
		for _, t := range transports {
			if p.Code == t {
				return p.Name
			}
		}
	}
	return "other"
}

func GetIPVersion(addr ma.Multiaddr) string {
	version := "unknown"
	if addr == nil {
		return version
	}
	ma.ForEach(addr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_IP4, ma.P_DNS4:
			version = "ip4"
		case ma.P_IP6, ma.P_DNS6:
			version = "ip6"
		}
		return false
	})
	return version
}
