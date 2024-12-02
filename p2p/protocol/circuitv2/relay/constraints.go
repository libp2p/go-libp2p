package relay

import (
	"errors"
	"slices"
	"sync"
	"time"

	asnutil "github.com/libp2p/go-libp2p-asn-util"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var (
	errTooManyReservations       = errors.New("too many reservations")
	errTooManyReservationsForIP  = errors.New("too many peers for IP address")
	errTooManyReservationsForASN = errors.New("too many peers for ASN")
)

type peerWithExpiry struct {
	Expiry time.Time
	Peer   peer.ID
}

// constraints implements various reservation constraints
type constraints struct {
	rc *Resources

	mutex sync.Mutex
	total []peerWithExpiry
	ips   map[string][]peerWithExpiry
	asns  map[uint32][]peerWithExpiry
}

// newConstraints creates a new constraints object.
// The methods are *not* thread-safe; an external lock must be held if synchronization
// is required.
func newConstraints(rc *Resources) *constraints {
	return &constraints{
		rc:   rc,
		ips:  make(map[string][]peerWithExpiry),
		asns: make(map[uint32][]peerWithExpiry),
	}
}

// Reserve adds a reservation for a given peer with a given multiaddr.
// If adding this reservation violates IP, ASN, or total reservation constraints, an error is returned.
func (c *constraints) Reserve(p peer.ID, a ma.Multiaddr, expiry time.Time) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	now := time.Now()
	c.cleanup(now)
	// To handle refreshes correctly, remove the existing reservation for the peer.
	c.cleanupPeer(p)

	if len(c.total) >= c.rc.MaxReservations {
		return errTooManyReservations
	}

	ip, err := manet.ToIP(a)
	if err != nil {
		return errors.New("no IP address associated with peer")
	}

	ipReservations := c.ips[ip.String()]
	if len(ipReservations) >= c.rc.MaxReservationsPerIP {
		return errTooManyReservationsForIP
	}

	var asnReservations []peerWithExpiry
	var asn uint32
	if ip.To4() == nil {
		asn = asnutil.AsnForIPv6(ip)
		if asn != 0 {
			asnReservations = c.asns[asn]
			if len(asnReservations) >= c.rc.MaxReservationsPerASN {
				return errTooManyReservationsForASN
			}
		}
	}

	c.total = append(c.total, peerWithExpiry{Expiry: expiry, Peer: p})

	ipReservations = append(ipReservations, peerWithExpiry{Expiry: expiry, Peer: p})
	c.ips[ip.String()] = ipReservations

	if asn != 0 {
		asnReservations = append(asnReservations, peerWithExpiry{Expiry: expiry, Peer: p})
		c.asns[asn] = asnReservations
	}
	return nil
}

func (c *constraints) cleanup(now time.Time) {
	expireFunc := func(pe peerWithExpiry) bool {
		return pe.Expiry.Before(now)
	}
	c.total = slices.DeleteFunc(c.total, expireFunc)
	for k, ipReservations := range c.ips {
		c.ips[k] = slices.DeleteFunc(ipReservations, expireFunc)
		if len(c.ips[k]) == 0 {
			delete(c.ips, k)
		}
	}
	for k, asnReservations := range c.asns {
		c.asns[k] = slices.DeleteFunc(asnReservations, expireFunc)
		if len(c.asns[k]) == 0 {
			delete(c.asns, k)
		}
	}
}

func (c *constraints) cleanupPeer(p peer.ID) {
	removeFunc := func(pe peerWithExpiry) bool {
		return pe.Peer == p
	}
	c.total = slices.DeleteFunc(c.total, removeFunc)
	for k, ipReservations := range c.ips {
		c.ips[k] = slices.DeleteFunc(ipReservations, removeFunc)
		if len(c.ips[k]) == 0 {
			delete(c.ips, k)
		}
	}
	for k, asnReservations := range c.asns {
		c.asns[k] = slices.DeleteFunc(asnReservations, removeFunc)
		if len(c.asns[k]) == 0 {
			delete(c.asns, k)
		}
	}
}
