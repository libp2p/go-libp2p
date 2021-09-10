package relay

import (
	"crypto/rand"
	"fmt"
	"math"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/test"
	ma "github.com/multiformats/go-multiaddr"
)

func randomIPv4Addr(t *testing.T) ma.Multiaddr {
	t.Helper()
	b := make([]byte, 4)
	rand.Read(b)
	addr, err := ma.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/1234", net.IP(b)))
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestConstraints(t *testing.T) {
	infResources := func() *Resources {
		return &Resources{
			MaxReservations:        math.MaxInt32,
			MaxReservationsPerPeer: math.MaxInt32,
			MaxReservationsPerIP:   math.MaxInt32,
			MaxReservationsPerASN:  math.MaxInt32,
		}
	}
	const limit = 7

	t.Run("total reservations", func(t *testing.T) {
		res := infResources()
		res.MaxReservations = limit
		c := newConstraints(res)
		for i := 0; i < limit; i++ {
			if err := c.AddReservation(test.RandPeerIDFatal(t), randomIPv4Addr(t)); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.AddReservation(test.RandPeerIDFatal(t), randomIPv4Addr(t)); err != errTooManyReservations {
			t.Fatalf("expected to run into total reservation limit, got %v", err)
		}
	})

	t.Run("reservations per peer", func(t *testing.T) {
		p := test.RandPeerIDFatal(t)
		res := infResources()
		res.MaxReservationsPerPeer = limit
		c := newConstraints(res)
		for i := 0; i < limit; i++ {
			if err := c.AddReservation(p, randomIPv4Addr(t)); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.AddReservation(p, randomIPv4Addr(t)); err != errTooManyReservationsForPeer {
			t.Fatalf("expected to run into total reservation limit, got %v", err)
		}
		if err := c.AddReservation(test.RandPeerIDFatal(t), randomIPv4Addr(t)); err != nil {
			t.Fatalf("expected reservation for different peer to be possible, got %v", err)
		}
	})

	t.Run("reservations per IP", func(t *testing.T) {
		ip := randomIPv4Addr(t)
		res := infResources()
		res.MaxReservationsPerIP = limit
		c := newConstraints(res)
		for i := 0; i < limit; i++ {
			if err := c.AddReservation(test.RandPeerIDFatal(t), ip); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.AddReservation(test.RandPeerIDFatal(t), ip); err != errTooManyReservationsForIP {
			t.Fatalf("expected to run into total reservation limit, got %v", err)
		}
		if err := c.AddReservation(test.RandPeerIDFatal(t), randomIPv4Addr(t)); err != nil {
			t.Fatalf("expected reservation for different IP to be possible, got %v", err)
		}
	})

	t.Run("reservations per ASN", func(t *testing.T) {
		getAddr := func(t *testing.T, ip net.IP) ma.Multiaddr {
			t.Helper()
			addr, err := ma.NewMultiaddr(fmt.Sprintf("/ip6/%s/tcp/1234", ip))
			if err != nil {
				t.Fatal(err)
			}
			return addr
		}

		res := infResources()
		res.MaxReservationsPerASN = limit
		c := newConstraints(res)
		const ipv6Prefix = "2a03:2880:f003:c07:face:b00c::"
		for i := 0; i < limit; i++ {
			addr := getAddr(t, net.ParseIP(fmt.Sprintf("%s%d", ipv6Prefix, i+1)))
			if err := c.AddReservation(test.RandPeerIDFatal(t), addr); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.AddReservation(test.RandPeerIDFatal(t), getAddr(t, net.ParseIP(fmt.Sprintf("%s%d", ipv6Prefix, 42)))); err != errTooManyReservationsForASN {
			t.Fatalf("expected to run into total reservation limit, got %v", err)
		}
		if err := c.AddReservation(test.RandPeerIDFatal(t), randomIPv4Addr(t)); err != nil {
			t.Fatalf("expected reservation for different IP to be possible, got %v", err)
		}
	})
}

func TestConstraintsCleanup(t *testing.T) {
	origValidity := validity
	defer func() { validity = origValidity }()
	validity = 500 * time.Millisecond

	const limit = 7
	res := &Resources{
		MaxReservations:        limit,
		MaxReservationsPerPeer: math.MaxInt32,
		MaxReservationsPerIP:   math.MaxInt32,
		MaxReservationsPerASN:  math.MaxInt32,
	}
	c := newConstraints(res)
	for i := 0; i < limit; i++ {
		if err := c.AddReservation(test.RandPeerIDFatal(t), randomIPv4Addr(t)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.AddReservation(test.RandPeerIDFatal(t), randomIPv4Addr(t)); err != errTooManyReservations {
		t.Fatalf("expected to run into total reservation limit, got %v", err)
	}

	time.Sleep(validity + time.Millisecond)
	if err := c.AddReservation(test.RandPeerIDFatal(t), randomIPv4Addr(t)); err != nil {
		t.Fatalf("expected old reservations to have been garbage collected, %v", err)
	}
}
