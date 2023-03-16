package webrtc_w3c

import (
	"errors"
	"strings"

	"github.com/multiformats/go-multiaddr"
)

/*
* getBaseMultiaddr removes the webrtc w3c component of the provided multiaddr
* and returns the base multiaddress to which we can connect.
 */
func getBaseMultiaddr(addr multiaddr.Multiaddr) (multiaddr.Multiaddr, error) {
	addrString := addr.String()
	splitResult := strings.Split(addrString, MULTIADDR_PROTOCOL)
	if len(splitResult) < 2 {
		return nil, errors.New("address does not contain w3c protocol")
	}
	return multiaddr.NewMultiaddr(splitResult[0])
}
