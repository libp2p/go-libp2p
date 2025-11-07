package builder_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"git.sr.ht/~marcopolo/di"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/transport"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/x/builder"
	"github.com/multiformats/go-multiaddr"
)

func ExampleDefaultConfig() {
	config := builder.DefaultConfig
	host, err := builder.NewHost(config)
	if err != nil {
		panic(err)
	}
	if len(host.Addrs()) > 0 {
		fmt.Println("listening on some transports")
	}
	// Output: listening on some transports
}

func ExampleDefaultConfig_extractEventBus() {
	config := builder.DefaultConfig
	type Result struct {
		Host      host.Host
		Bus       event.Bus
		Lifecycle *builder.Lifecycle
		_         []di.SideEffect
	}
	res, err := di.New[Result](config)
	if err != nil {
		panic(err)
	}

	// We must call Lifecycle.Start to start all the services.
	if err := res.Lifecycle.Start(); err != nil {
		panic(err)
	}

	// And we must remember to end the lifecycle of these services by calling
	// Lifecycle.Close()
	defer func() {
		if err := res.Lifecycle.Close(); err != nil {
			panic(err)
		}
	}()

	if len(res.Host.Addrs()) > 0 {
		fmt.Println("listening on some transports")
	}
	if res.Bus != nil {
		fmt.Println("and I have a reference to an event bus")
	}
	// Output:
	// listening on some transports
	// and I have a reference to an event bus
}

// ExampleDefaultConfig_onlyQUIC shows how to customize the default config to
// build a host with only a quic transport
func ExampleDefaultConfig_onlyQUIC() {
	config := builder.DefaultConfig
	config.Transports = di.MustProvide[[]transport.Transport](
		func(
			quic *libp2pquic.Transport,
		) (tpts []transport.Transport, err error) {
			if quic == nil {
				return nil, errors.New("quic transport is required")
			}
			return append(tpts, quic), nil
		},
	)
	type Result struct {
		Host      host.Host
		Lifecycle *builder.Lifecycle
		_         []di.SideEffect
	}
	res, err := di.New[Result](config)
	if err != nil {
		panic(err)
	}

	// We must call Lifecycle.Start to start all the services.
	if err := res.Lifecycle.Start(); err != nil {
		panic(err)
	}

	// And we must remember to end the lifecycle of these services by calling
	// Lifecycle.Close()
	defer func() {
		if err := res.Lifecycle.Close(); err != nil {
			panic(err)
		}
	}()

	addrs := res.Host.Addrs()
	onlyQuicAddrs := slices.DeleteFunc(addrs, func(m multiaddr.Multiaddr) bool {
		return !strings.Contains(m.String(), "quic-v1")
	})

	if len(onlyQuicAddrs) != len(addrs) {
		panic("should only be listening on QUIC addresses")
	}
	fmt.Println("I have a QUIC listener")
	// Output:
	// I have a QUIC listener
}
