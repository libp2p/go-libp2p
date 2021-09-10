package util

import (
	ma "github.com/multiformats/go-multiaddr"
)

func IsRelayAddr(a ma.Multiaddr) bool {
	_, err := a.ValueForProtocol(ma.P_CIRCUIT)
	return err == nil
}
