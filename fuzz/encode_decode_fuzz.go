package fuzz

import (
	"github.com/libp2p/go-libp2p/core/peer"
)

func FuzzEncodeDecodeID(data []byte) int {
	var id peer.ID
	if err := id.UnmarshalText(data); err == nil {
		encoded := peer.Encode(id)
		id2, err := peer.Decode(encoded)
		if err != nil {
			return 1
		}
		if id != id2 {
			return 1
		}
	}
	return 0
}
