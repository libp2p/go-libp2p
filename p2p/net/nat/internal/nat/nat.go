// Package nat implements NAT handling facilities
package nat

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-netroute"
)

var log = logging.Logger("internal/nat")

var ErrNoExternalAddress = errors.New("no external address")
var ErrNoInternalAddress = errors.New("no internal address")

type ErrNoNATFound struct {
	Errs []error
}

func (e ErrNoNATFound) Error() string {
	var errStrs []string
	for _, err := range e.Errs {
		errStrs = append(errStrs, err.Error())
	}
	return fmt.Sprintf("no NAT found: [%s]", strings.Join(errStrs, "; "))
}

// protocol is either "udp" or "tcp"
type NAT interface {
	// Type returns the kind of NAT port mapping service that is used
	Type() string

	// GetDeviceAddress returns the internal address of the gateway device.
	GetDeviceAddress() (addr net.IP, err error)

	// GetExternalAddress returns the external address of the gateway device.
	GetExternalAddress() (addr net.IP, err error)

	// GetInternalAddress returns the address of the local host.
	GetInternalAddress() (addr net.IP, err error)

	// AddPortMapping maps a port on the local host to an external port.
	AddPortMapping(ctx context.Context, protocol string, internalPort int, description string, timeout time.Duration) (mappedExternalPort int, err error)

	// DeletePortMapping removes a port mapping.
	DeletePortMapping(ctx context.Context, protocol string, internalPort int) (err error)
}

type result[V any] struct {
	Value V
	Err   error
}

// discoverNATs returns all NATs discovered in the network.
func discoverNATs(ctx context.Context) ([]NAT, []error) {
	var nats []NAT
	var errs []error

	type natsAndErrs struct {
		nats []NAT
		errs []error
	}
	resCh := make(chan natsAndErrs)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		nats, errs := discoverUPNP_IG1(ctx)
		select {
		case resCh <- natsAndErrs{nats, errs}:
		case <-ctx.Done():
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		nats, errs := discoverUPNP_IG2(ctx)
		select {
		case resCh <- natsAndErrs{nats, errs}:
		case <-ctx.Done():
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		nat, err := discoverNATPMP(ctx)
		var nats []NAT
		var errs []error
		if err != nil {
			errs = append(errs, err)
		} else {
			nats = append(nats, nat)
		}
		select {
		case resCh <- natsAndErrs{nats, errs}:
		case <-ctx.Done():
		}
	}()

	allDone := make(chan struct{})
	go func() {
		defer close(allDone)
		wg.Wait()
	}()

	for {
		select {
		case res := <-resCh:
			nats = append(nats, res.nats...)
			errs = append(errs, res.errs...)
			return nats, errs
		case <-allDone:
			return nats, errs
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
			return nats, errs
		}
	}
}

// DiscoverGateway attempts to find a gateway device.
func DiscoverGateway(ctx context.Context) (NAT, error) {
	nats, errs := discoverNATs(ctx)

	switch len(nats) {
	case 0:
		return nil, ErrNoNATFound{Errs: errs}
	case 1:
		if len(errs) > 0 {
			log.Debugf("NAT found, but some potentially unrelated errors occurred: %v", errs)
		}

		return nats[0], nil
	}
	gw, _ := getDefaultGateway()
	bestNAT := nats[0]
	natGw, _ := bestNAT.GetDeviceAddress()
	bestNATIsGw := gw != nil && natGw.Equal(gw)
	// 1. Prefer gateways discovered _last_. This is an OK heuristic for
	// discovering the most-upstream (furthest) NAT.
	// 2. Prefer gateways that actually match our known gateway address.
	// Some relays like to claim to be NATs even if they aren't.
	for _, nat := range nats[1:] {
		natGw, _ := nat.GetDeviceAddress()
		natIsGw := gw != nil && natGw.Equal(gw)

		if bestNATIsGw && !natIsGw {
			continue
		}

		bestNATIsGw = natIsGw
		bestNAT = nat
	}

	if len(errs) > 0 {
		log.Debugf("NAT found, but some potentially unrelated errors occurred: %v", errs)
	}
	return bestNAT, nil
}

var random = rand.New(rand.NewSource(time.Now().UnixNano()))

func randomPort() int {
	return random.Intn(math.MaxUint16-10000) + 10000
}

func getDefaultGateway() (net.IP, error) {
	router, err := netroute.New()
	if err != nil {
		return nil, err
	}

	_, ip, _, err := router.Route(net.IPv4zero)
	return ip, err
}
