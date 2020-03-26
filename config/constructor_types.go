package config

import (
	"fmt"
	"reflect"

	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/mux"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	"github.com/libp2p/go-libp2p-core/pnet"
	"github.com/libp2p/go-libp2p-core/sec"
	"github.com/libp2p/go-libp2p-core/transport"

	tptu "github.com/libp2p/go-libp2p-transport-upgrader"
	filter "github.com/libp2p/go-maddr-filter"
)

var (
	// interfaces
	hostType      = reflect.TypeOf((*host.Host)(nil)).Elem()
	networkType   = reflect.TypeOf((*network.Network)(nil)).Elem()
	transportType = reflect.TypeOf((*transport.Transport)(nil)).Elem()
	muxType       = reflect.TypeOf((*mux.Multiplexer)(nil)).Elem()
	securityType  = reflect.TypeOf((*sec.SecureTransport)(nil)).Elem()
	privKeyType   = reflect.TypeOf((*crypto.PrivKey)(nil)).Elem()
	pubKeyType    = reflect.TypeOf((*crypto.PubKey)(nil)).Elem()
	pstoreType    = reflect.TypeOf((*peerstore.Peerstore)(nil)).Elem()

	// concrete types
	peerIDType   = reflect.TypeOf((peer.ID)(""))
	filtersType  = reflect.TypeOf((*filter.Filters)(nil))
	upgraderType = reflect.TypeOf((*tptu.Upgrader)(nil))
	pskType      = reflect.TypeOf((pnet.PSK)(nil))
)

var argTypes = map[reflect.Type]constructor{
	upgraderType: func(h host.Host, u *tptu.Upgrader) interface{} { return u },
	hostType:     func(h host.Host, u *tptu.Upgrader) interface{} { return h },
	networkType:  func(h host.Host, u *tptu.Upgrader) interface{} { return h.Network() },
	muxType:      func(h host.Host, u *tptu.Upgrader) interface{} { return u.Muxer },
	securityType: func(h host.Host, u *tptu.Upgrader) interface{} { return u.Secure },
	pskType:      func(h host.Host, u *tptu.Upgrader) interface{} { return u.PSK },
	filtersType:  func(h host.Host, u *tptu.Upgrader) interface{} { return u.Filters },
	peerIDType:   func(h host.Host, u *tptu.Upgrader) interface{} { return h.ID() },
	privKeyType:  func(h host.Host, u *tptu.Upgrader) interface{} { return h.Peerstore().PrivKey(h.ID()) },
	pubKeyType:   func(h host.Host, u *tptu.Upgrader) interface{} { return h.Peerstore().PubKey(h.ID()) },
	pstoreType:   func(h host.Host, u *tptu.Upgrader) interface{} { return h.Peerstore() },
}

func newArgTypeSet(types ...reflect.Type) map[reflect.Type]constructor {
	result := make(map[reflect.Type]constructor, len(types))
	for _, ty := range types {
		c, ok := argTypes[ty]
		if !ok {
			panic(fmt.Sprintf("missing constructor for type %s", ty))
		}
		result[ty] = c
	}
	return result
}
