package libp2pwebtransport

import (
	"fmt"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multihash"
)

var log = logging.Logger("webtransport")

const webtransportHTTPEndpoint = "/.well-known/libp2p-webtransport"

const errorCodeConnectionGating = 0x47415445 // GATE in ASCII

const certValidity = 14 * 24 * time.Hour

type common struct {
	privKey ic.PrivKey
	pid     peer.ID

	noise *noise.Transport
	gater connmgr.ConnectionGater
	rcmgr network.ResourceManager
}

// extractSNI returns what the SNI should be for the given maddr. If there is an
// SNI component in the multiaddr, then it will be returned and
// foundSniComponent will be true. If there's no SNI component, but there is a
// DNS-like component, then that will be returned for the sni and
// foundSniComponent will be false (since we didn't find an actual sni component).
func extractSNI(maddr ma.Multiaddr) (sni string, foundSniComponent bool) {
	ma.ForEach(maddr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_SNI:
			sni = c.Value()
			foundSniComponent = true
			return false
		case ma.P_DNS, ma.P_DNS4, ma.P_DNS6, ma.P_DNSADDR:
			sni = c.Value()
			// Keep going in case we find an `sni` component
			return true
		}
		return true
	})
	return sni, foundSniComponent
}

func decodeCertHashesFromProtobuf(b [][]byte) ([]multihash.DecodedMultihash, error) {
	hashes := make([]multihash.DecodedMultihash, 0, len(b))
	for _, h := range b {
		dh, err := multihash.Decode(h)
		if err != nil {
			return nil, fmt.Errorf("failed to decode hash: %w", err)
		}
		hashes = append(hashes, *dh)
	}
	return hashes, nil
}
