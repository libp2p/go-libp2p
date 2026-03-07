package mixnet

import (
	"encoding/binary"
	"fmt"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/mixnet/circuit"
)

// encryptOnionHeader builds a layered onion header that wraps only control data.
// The payload data is forwarded unchanged across hops.
func encryptOnionHeader(controlHeader []byte, c *circuit.Circuit, dest peer.ID, hopKeys [][]byte) ([]byte, error) {
	if c == nil || len(c.Peers) == 0 {
		return nil, fmt.Errorf("empty circuit")
	}
	if len(hopKeys) != len(c.Peers) {
		return nil, fmt.Errorf("hop key count mismatch")
	}

	current := controlHeader
	for i := len(c.Peers) - 1; i >= 0; i-- {
		isFinal := byte(0)
		nextHop := ""
		if i == len(c.Peers)-1 {
			isFinal = 1
			nextHop = dest.String()
		} else {
			nextHop = c.Peers[i+1].String()
		}
		plain, err := buildHopPayload(isFinal, nextHop, current)
		if err != nil {
			return nil, err
		}
		enc, err := encryptHopPayload(hopKeys[i], plain)
		if err != nil {
			return nil, err
		}
		current = enc
	}
	return current, nil
}

// buildHeaderOnlyPayload builds the header-only packet body.
// Format: [header_len(4)][encrypted_header][payload]
func buildHeaderOnlyPayload(encryptedHeader []byte, payload []byte) []byte {
	buf := make([]byte, 4+len(encryptedHeader)+len(payload))
	binary.LittleEndian.PutUint32(buf[:4], uint32(len(encryptedHeader)))
	copy(buf[4:], encryptedHeader)
	copy(buf[4+len(encryptedHeader):], payload)
	return buf
}
