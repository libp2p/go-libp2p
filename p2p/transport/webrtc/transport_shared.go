package libp2pwebrtc

import (
	"net"

	ma "github.com/multiformats/go-multiaddr"
)

type ListenUDPFn func(network string, laddr *net.UDPAddr) (net.PacketConn, error)

// IsWebRTCDirectMultiaddr returns whether addr is a /webrtc-direct multiaddr with the count of certhashes
// in addr
func IsWebRTCDirectMultiaddr(addr ma.Multiaddr) (bool, int) {
	var foundUDP, foundWebRTC bool
	certHashCount := 0
	ma.ForEach(addr, func(c ma.Component) bool {
		if !foundUDP {
			if c.Protocol().Code == ma.P_UDP {
				foundUDP = true
			}
			return true
		}
		if !foundWebRTC && foundUDP {
			// protocol after udp must be webrtc-direct
			if c.Protocol().Code != ma.P_WEBRTC_DIRECT {
				return false
			}
			foundWebRTC = true
			return true
		}
		if foundWebRTC {
			if c.Protocol().Code == ma.P_CERTHASH {
				certHashCount++
			} else {
				return false
			}
		}
		return true
	})
	return foundUDP && foundWebRTC, certHashCount
}
