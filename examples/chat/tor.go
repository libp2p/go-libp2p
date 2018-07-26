package main

import (
	"errors"
	"fmt"

	onion "github.com/OpenBazaar/go-onion-transport"
	torkeys "github.com/OpenBazaar/openbazaar-go/net"
	"github.com/libp2p/go-addr-util"
	"github.com/libp2p/go-libp2p-crypto"
	"github.com/multiformats/go-multiaddr"
	"golang.org/x/net/proxy"
)

func findOrCreateTorServiceAddress(cfg *TorConfig) (multiaddr.Multiaddr, error) {
	addrStr, err := torkeys.MaybeCreateHiddenServiceKey(cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	addrStr = fmt.Sprintf("/onion/%s:%d", addrStr, cfg.Port)
	return multiaddr.NewMultiaddr(addrStr)
}

func newOnionTransport(cfg *TorConfig, privkey crypto.PrivKey) (onion.OnionTransportC, error) {
	if cfg == nil {
		return nil, errors.New("tor config not found")
	}

	torAuth := &proxy.Auth{User: cfg.Username, Password: cfg.Password}
	return onion.NewOnionTransportC(cfg.ControlNet, cfg.ControlAddress, cfg.ControlPassword, torAuth, cfg.KeyPath, cfg.OnlyOnion), nil
}

// From https://github.com/OpenBazaar/go-onion-transport/issues/1
// NOTE: This will not work until `go-libp2p` has been patched. Pre-patch, the only way to get tor working is to
//       construct a swarm by hand.
func addTorTransportToAddrUtil() error {
	addrutil.SupportedTransportStrings = append(addrutil.SupportedTransportStrings, "/onion")
	t, err := multiaddr.ProtocolsWithString("/onion")
	if err != nil {
		return err
	}
	addrutil.SupportedTransportProtocols = append(addrutil.SupportedTransportProtocols, t)
	if err != nil {
		return err
	}
	return nil
}
