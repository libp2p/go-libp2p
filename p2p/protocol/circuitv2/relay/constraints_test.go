package relay

import (
	"crypto/rand"
	"fmt"
	"math"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/test"

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
	expiry := time.Now().Add(30 * time.Minute)

	t.Run("total reservations", func(t *testing.T) {
		res := infResources()
		res.MaxReservations = limit
		c := newConstraints(res)
		for i := 0; i < limit; i++ {
			if err := c.Reserve(test.RandPeerIDFatal(t), randomIPv4Addr(t), expiry); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.Reserve(test.RandPeerIDFatal(t), randomIPv4Addr(t), expiry); err != errTooManyReservations {
			t.Fatalf("expected to run into total reservation limit, got %v", err)
		}
	})

	t.Run("updates reservations on the same peer", func(t *testing.T) {
		p := test.RandPeerIDFatal(t)
		p2 := test.RandPeerIDFatal(t)
		res := infResources()
		res.MaxReservationsPerIP = 1
		c := newConstraints(res)

		ipAddr := randomIPv4Addr(t)
		if err := c.Reserve(p, ipAddr, expiry); err != nil {
			t.Fatal(err)
		}
		if err := c.Reserve(p2, ipAddr, expiry); err != errTooManyReservationsForIP {
			t.Fatalf("expected to run into IP reservation limit as this IP has already been reserved by a different peer, got %v", err)
		}
		if err := c.Reserve(p, randomIPv4Addr(t), expiry); err != nil {
			t.Fatalf("expected to update existing reservation for peer, got %v", err)
		}
		if err := c.Reserve(p2, ipAddr, expiry); err != nil {
			t.Fatalf("expected reservation for different peer to be possible, got %v", err)
		}
	})

	t.Run("reservations per IP", func(t *testing.T) {
		ip := randomIPv4Addr(t)
		res := infResources()
		res.MaxReservationsPerIP = limit
		c := newConstraints(res)
		for i := 0; i < limit; i++ {
			if err := c.Reserve(test.RandPeerIDFatal(t), ip, expiry); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.Reserve(test.RandPeerIDFatal(t), ip, expiry); err != errTooManyReservationsForIP {
			t.Fatalf("expected to run into total reservation limit, got %v", err)
		}
		if err := c.Reserve(test.RandPeerIDFatal(t), randomIPv4Addr(t), expiry); err != nil {
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
			if err := c.Reserve(test.RandPeerIDFatal(t), addr, expiry); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.Reserve(test.RandPeerIDFatal(t), getAddr(t, net.ParseIP(fmt.Sprintf("%s%d", ipv6Prefix, 42))), expiry); err != errTooManyReservationsForASN {
			t.Fatalf("expected to run into total reservation limit, got %v", err)
		}
		if err := c.Reserve(test.RandPeerIDFatal(t), randomIPv4Addr(t), expiry); err != nil {
			t.Fatalf("expected reservation for different IP to be possible, got %v", err)
		}
	})
}

func TestConstraintsCleanup(t *testing.T) {
	const limit = 7
	validity := 500 * time.Millisecond
	expiry := time.Now().Add(validity)
	res := &Resources{
		MaxReservations:        limit,
		MaxReservationsPerPeer: math.MaxInt32,
		MaxReservationsPerIP:   math.MaxInt32,
		MaxReservationsPerASN:  math.MaxInt32,
	}
	c := newConstraints(res)
	for i := 0; i < limit; i++ {
		if err := c.Reserve(test.RandPeerIDFatal(t), randomIPv4Addr(t), expiry); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Reserve(test.RandPeerIDFatal(t), randomIPv4Addr(t), expiry); err != errTooManyReservations {
		t.Fatalf("expected to run into total reservation limit, got %v", err)
	}

	time.Sleep(validity + time.Millisecond)
	if err := c.Reserve(test.RandPeerIDFatal(t), randomIPv4Addr(t), expiry); err != nil {
		t.Fatalf("expected old reservations to have been garbage collected, %v", err)
	}
}
