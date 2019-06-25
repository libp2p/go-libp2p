package identify

import "github.com/libp2p/go-libp2p-core/network"

// IDPush is the protocol.ID of the Identify push protocol. It sends full identify messages containing
// the current state of the peer.
//
// It is in the process of being replaced by identify delta, which sends only diffs for better
// resource utilisation.
const IDPush = "/ipfs/id/push/1.0.0"

// Push pushes a full identify message to all peers containing the current state.
func (ids *IDService) Push() {
	ids.broadcast(IDPush, ids.requestHandler)
}

// pushHandler handles incoming identify push streams. The behaviour is identical to the ordinary identify protocol.
func (ids *IDService) pushHandler(s network.Stream) {
	ids.responseHandler(s)
}
