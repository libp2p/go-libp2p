package httppeeridauth

import (
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/p2p/http/auth/internal/handshake"
)

const PeerIDAuthScheme = handshake.PeerIDAuthScheme
const ProtocolID = "/http-peer-id-auth/1.0.0"

var log = logging.Logger("http-peer-id-auth")
