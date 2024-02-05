package websocket

import (
	"github.com/libp2p/go-reuseport"
)

func reuseportIsAvailable() bool {
	return reuseport.Available()
}
