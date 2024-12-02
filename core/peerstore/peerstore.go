// Package peerstore provides types and interfaces for local storage of address information,
// metadata, and public key material about libp2p peers.
package peerstore

import (
	"context"
	"errors"
	"io"
	"math"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/record"

	ma "github.com/multiformats/go-multiaddr"
)

var ErrNotFound = errors.New("item not found")

var (
	// AddressTTL is the expiration time of addresses.
	AddressTTL = time.Hour

	// TempAddrTTL is the ttl used for a short-lived address.
	TempAddrTTL = time.Minute * 2

	// RecentlyConnectedAddrTTL is used when we recently connected to a peer.
	// It means that we are reasonably certain of the peer's address.
	RecentlyConnectedAddrTTL = time.Minute * 15

	// OwnObservedAddrTTL is used for our own external addresses observed by peers.
	//
	// Deprecated: observed addresses are maintained till we disconnect from the peer which provided it
	OwnObservedAddrTTL = time.Minute * 30
)

// Permanent TTLs (distinct so we can distinguish between them, constant as they
// are, in fact, permanent)
const (
	// PermanentAddrTTL is the ttl for a "permanent address" (e.g. bootstrap nodes).
	PermanentAddrTTL = math.MaxInt64 - iota

	// ConnectedAddrTTL is the ttl used for the addresses of a peer to whom
	// we're connected directly. This is basically permanent, as we will
	// clear them + re-add under a TempAddrTTL after disconnecting.
	ConnectedAddrTTL
)

// Peerstore provides a thread-safe store of Peer related
// information.
type Peerstore interface {
	io.Closer

	AddrBook
	KeyBook
	PeerMetadata
	Metrics
	ProtoBook

	// PeerInfo returns a peer.PeerInfo struct for given peer.ID.
	// This is a small slice of the information Peerstore has on
	// that peer, useful to other services.
	PeerInfo(peer.ID) peer.AddrInfo

	// Peers returns all the peer IDs stored across all inner stores.
	Peers() peer.IDSlice

	// RemovePeer removes all the peer related information except its addresses. To remove the
	// addresses use `AddrBook.ClearAddrs` or set the address ttls to 0.
	RemovePeer(peer.ID)
}

// PeerMetadata can handle values of any type. Serializing values is
// up to the implementation. Dynamic type introspection may not be
// supported, in which case explicitly enlisting types in the
// serializer may be required.
//
// Refer to the docs of the underlying implementation for more
// information.
type PeerMetadata interface {
	// Get / Put is a simple registry for other peer-related key/value pairs.
	// If we find something we use often, it should become its own set of
	// methods. This is a last resort.
	Get(p peer.ID, key string) (interface{}, error)
	Put(p peer.ID, key string, val interface{}) error

	// RemovePeer removes all values stored for a peer.
	RemovePeer(peer.ID)
}

// AddrBook holds the multiaddrs of peers.
type AddrBook interface {
	// AddAddr calls AddAddrs(p, []ma.Multiaddr{addr}, ttl)
	AddAddr(p peer.ID, addr ma.Multiaddr, ttl time.Duration)

	// AddAddrs gives this AddrBook addresses to use, with a given ttl
	// (time-to-live), after which the address is no longer valid.
	// If the manager has a longer TTL, the operation is a no-op for that address
	AddAddrs(p peer.ID, addrs []ma.Multiaddr, ttl time.Duration)

	// SetAddr calls mgr.SetAddrs(p, addr, ttl)
	SetAddr(p peer.ID, addr ma.Multiaddr, ttl time.Duration)

	// SetAddrs sets the ttl on addresses. This clears any TTL there previously.
	// This is used when we receive the best estimate of the validity of an address.
	SetAddrs(p peer.ID, addrs []ma.Multiaddr, ttl time.Duration)

	// UpdateAddrs updates the addresses associated with the given peer that have
	// the given oldTTL to have the given newTTL.
	UpdateAddrs(p peer.ID, oldTTL time.Duration, newTTL time.Duration)

	// Addrs returns all known (and valid) addresses for a given peer.
	Addrs(p peer.ID) []ma.Multiaddr

	// AddrStream returns a channel that gets all addresses for a given
	// peer sent on it. If new addresses are added after the call is made
	// they will be sent along through the channel as well.
	AddrStream(context.Context, peer.ID) <-chan ma.Multiaddr

	// ClearAddresses removes all previously stored addresses.
	ClearAddrs(p peer.ID)

	// PeersWithAddrs returns all the peer IDs stored in the AddrBook.
	PeersWithAddrs() peer.IDSlice
}

// CertifiedAddrBook manages signed peer records and "self-certified" addresses
// contained within them.
// Use this interface with an `AddrBook`.
//
// To test whether a given AddrBook / Peerstore implementation supports
// certified addresses, callers should use the GetCertifiedAddrBook helper or
// type-assert on the CertifiedAddrBook interface:
//
//	if cab, ok := aPeerstore.(CertifiedAddrBook); ok {
//	    cab.ConsumePeerRecord(signedPeerRecord, aTTL)
//	}
type CertifiedAddrBook interface {
	// ConsumePeerRecord stores a signed peer record and the contained addresses for
	// ttl duration.
	// The addresses contained in the signed peer record will expire after ttl. If any
	// address is already present in the peer store, it'll expire at max of existing ttl and
	// provided ttl.
	// The signed peer record itself will be expired when all the addresses associated with the peer,
	// self-certified or not, are removed from the AddrBook.
	//
	// To delete the signed peer record, use `AddrBook.UpdateAddrs`,`AddrBook.SetAddrs`, or
	// `AddrBook.ClearAddrs` with ttl 0.
	// Note: Future calls to ConsumePeerRecord will not expire self-certified addresses from the
	// previous calls.
	//
	// The `accepted` return value indicates that the record was successfully processed. If
	// `accepted` is false but no error is returned, it means that the record was ignored, most
	// likely because a newer record exists for the same peer with a greater seq value.
	//
	// The Envelopes containing the signed peer records can be retrieved by calling
	// GetPeerRecord(peerID).
	ConsumePeerRecord(s *record.Envelope, ttl time.Duration) (accepted bool, err error)

	// GetPeerRecord returns an Envelope containing a peer record for the
	// peer, or nil if no record exists.
	GetPeerRecord(p peer.ID) *record.Envelope
}

// GetCertifiedAddrBook is a helper to "upcast" an AddrBook to a
// CertifiedAddrBook by using type assertion. If the given AddrBook
// is also a CertifiedAddrBook, it will be returned, and the ok return
// value will be true. Returns (nil, false) if the AddrBook is not a
// CertifiedAddrBook.
//
// Note that since Peerstore embeds the AddrBook interface, you can also
// call GetCertifiedAddrBook(myPeerstore).
func GetCertifiedAddrBook(ab AddrBook) (cab CertifiedAddrBook, ok bool) {
	cab, ok = ab.(CertifiedAddrBook)
	return cab, ok
}

// KeyBook tracks the keys of Peers.
type KeyBook interface {
	// PubKey returns the public key of a peer.
	PubKey(peer.ID) ic.PubKey

	// AddPubKey stores the public key of a peer.
	AddPubKey(peer.ID, ic.PubKey) error

	// PrivKey returns the private key of a peer, if known. Generally this might only be our own
	// private key, see
	// https://discuss.libp2p.io/t/what-is-the-purpose-of-having-map-peer-id-privatekey-in-peerstore/74.
	PrivKey(peer.ID) ic.PrivKey

	// AddPrivKey stores the private key of a peer.
	AddPrivKey(peer.ID, ic.PrivKey) error

	// PeersWithKeys returns all the peer IDs stored in the KeyBook.
	PeersWithKeys() peer.IDSlice

	// RemovePeer removes all keys associated with a peer.
	RemovePeer(peer.ID)
}

// Metrics tracks metrics across a set of peers.
type Metrics interface {
	// RecordLatency records a new latency measurement
	RecordLatency(peer.ID, time.Duration)

	// LatencyEWMA returns an exponentially-weighted moving avg.
	// of all measurements of a peer's latency.
	LatencyEWMA(peer.ID) time.Duration

	// RemovePeer removes all metrics stored for a peer.
	RemovePeer(peer.ID)
}

// ProtoBook tracks the protocols supported by peers.
type ProtoBook interface {
	GetProtocols(peer.ID) ([]protocol.ID, error)
	AddProtocols(peer.ID, ...protocol.ID) error
	SetProtocols(peer.ID, ...protocol.ID) error
	RemoveProtocols(peer.ID, ...protocol.ID) error

	// SupportsProtocols returns the set of protocols the peer supports from among the given protocols.
	// If the returned error is not nil, the result is indeterminate.
	SupportsProtocols(peer.ID, ...protocol.ID) ([]protocol.ID, error)

	// FirstSupportedProtocol returns the first protocol that the peer supports among the given protocols.
	// If the peer does not support any of the given protocols, this function will return an empty protocol.ID and a nil error.
	// If the returned error is not nil, the result is indeterminate.
	FirstSupportedProtocol(peer.ID, ...protocol.ID) (protocol.ID, error)

	// RemovePeer removes all protocols associated with a peer.
	RemovePeer(peer.ID)
}
