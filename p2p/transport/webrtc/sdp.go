package libp2pwebrtc

import (
	"crypto"
	"fmt"
	"net"
	"strings"

	"github.com/multiformats/go-multihash"
)

// clientSDP describes an SDP format string which can be used
// to infer a client's SDP offer from the incoming STUN message.
// The fingerprint used to render a client SDP is arbitrary since
// it fingerprint verification is disabled in favour of a noise
// handshake. The max message size is fixed to 16384 bytes.
const clientSDP = `v=0
o=- 0 0 IN %[1]s %[2]s
s=-
c=IN %[1]s %[2]s
t=0 0

m=application %[3]d UDP/DTLS/SCTP webrtc-datachannel
a=mid:0
a=ice-options:ice2
a=ice-ufrag:%[4]s
a=ice-pwd:%[4]s
a=fingerprint:sha-256 ba:78:16:bf:8f:01:cf:ea:41:41:40:de:5d:ae:22:23:b0:03:61:a3:96:17:7a:9c:b4:10:ff:61:f2:00:15:ad
a=setup:actpass
a=sctp-port:5000
a=max-message-size:16384
`

func createClientSDP(addr *net.UDPAddr, ufrag string) string {
	ipVersion := "IP4"
	if addr.IP.To4() == nil {
		ipVersion = "IP6"
	}
	return fmt.Sprintf(
		clientSDP,
		ipVersion,
		addr.IP,
		addr.Port,
		ufrag,
	)
}

// serverSDP defines an SDP format string used by a dialer
// to infer the SDP answer of a server based on the provided
// multiaddr, and the locally set ICE credentials. The max
// message size is fixed to 16384 bytes.
const serverSDP = `v=0
o=- 0 0 IN %[1]s %[2]s
s=-
t=0 0
a=ice-lite
m=application %[3]d UDP/DTLS/SCTP webrtc-datachannel
c=IN %[1]s %[2]s
a=mid:0
a=ice-options:ice2
a=ice-ufrag:%[4]s
a=ice-pwd:%[4]s
a=fingerprint:%[5]s

a=setup:passive
a=sctp-port:5000
a=max-message-size:16384
a=candidate:1 1 UDP 1 %[2]s %[3]d typ host
a=end-of-candidates
`

func createServerSDP(addr *net.UDPAddr, ufrag string, fingerprint multihash.DecodedMultihash) (string, error) {
	ipVersion := "IP4"
	if addr.IP.To4() == nil {
		ipVersion = "IP6"
	}

	sdpString, err := getSupportedSDPString(fingerprint.Code)
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	builder.Grow(len(fingerprint.Digest)*3 + 8)
	builder.WriteString(sdpString)
	builder.WriteByte(' ')
	builder.WriteString(encodeInterspersedHex(fingerprint.Digest))
	fp := builder.String()

	return fmt.Sprintf(
		serverSDP,
		ipVersion,
		addr.IP,
		addr.Port,
		ufrag,
		fp,
	), nil
}

// getSupportedSDPHash converts a multihash code to the
// corresponding crypto.Hash for supported protocols. If a
// crypto.Hash cannot be found, it returns `(0, false)`
func getSupportedSDPHash(code uint64) (crypto.Hash, bool) {
	switch code {
	case multihash.MD5:
		return crypto.MD5, true
	case multihash.SHA1:
		return crypto.SHA1, true
	case multihash.SHA3_224:
		return crypto.SHA3_224, true
	case multihash.SHA2_256:
		return crypto.SHA256, true
	case multihash.SHA3_384:
		return crypto.SHA3_384, true
	case multihash.SHA2_512:
		return crypto.SHA512, true
	default:
		return 0, false
	}
}

// getSupportedSDPString converts a multihash code
// to a string format recognised by pion for fingerprint
// algorithms
func getSupportedSDPString(code uint64) (string, error) {
	// values based on (cryto.Hash).String()
	switch code {
	case multihash.MD5:
		return "md5", nil
	case multihash.SHA1:
		return "sha-1", nil
	case multihash.SHA3_224:
		return "sha3-224", nil
	case multihash.SHA2_256:
		return "sha-256", nil
	case multihash.SHA3_384:
		return "sha3-384", nil
	case multihash.SHA2_512:
		return "sha-512", nil
	default:
		return "", fmt.Errorf("unsupported hash code (%d)", code)
	}
}
