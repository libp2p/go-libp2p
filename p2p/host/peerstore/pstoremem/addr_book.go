package pstoremem

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/record"

	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
)

var log = logging.Logger("peerstore")

type expiringAddr struct {
	Addr   ma.Multiaddr
	TTL    time.Duration
	Expiry time.Time
	Peer   peer.ID
	// to sort by expiry time, -1 means it's not in the heap
	heapIndex int
}

func (e *expiringAddr) ExpiredBy(t time.Time) bool {
	return !t.Before(e.Expiry)
}

func (e *expiringAddr) IsConnected() bool {
	return ttlIsConnected(e.TTL)
}

// ttlIsConnected returns true if the TTL is at least as long as the connected
// TTL.
func ttlIsConnected(ttl time.Duration) bool {
	return ttl >= peerstore.ConnectedAddrTTL
}

var expiringAddrPool = sync.Pool{New: func() any { return &expiringAddr{} }}

func getExpiringAddrs() *expiringAddr {
	a := expiringAddrPool.Get().(*expiringAddr)
	a.heapIndex = -1
	return a
}

func putExpiringAddrs(ea *expiringAddr) {
	if ea == nil {
		return
	}
	*ea = expiringAddr{}
	expiringAddrPool.Put(ea)
}

type peerRecordState struct {
	Envelope *record.Envelope
	Seq      uint64
}

// Essentially Go stdlib's Priority Queue example
var _ heap.Interface = &peerAddrs{}

type peerAddrs struct {
	Addrs map[peer.ID]map[string]*expiringAddr // peer.ID -> addr.Bytes() -> *expiringAddr
	// expiringHeap only stores non-connected addresses. Since connected address
	// basically have an infinite TTL
	expiringHeap []*expiringAddr
}

func newPeerAddrs() peerAddrs {
	return peerAddrs{
		Addrs: make(map[peer.ID]map[string]*expiringAddr),
	}
}

func (pa *peerAddrs) Len() int { return len(pa.expiringHeap) }
func (pa *peerAddrs) Less(i, j int) bool {
	return pa.expiringHeap[i].Expiry.Before(pa.expiringHeap[j].Expiry)
}
func (pa *peerAddrs) Swap(i, j int) {
	pa.expiringHeap[i], pa.expiringHeap[j] = pa.expiringHeap[j], pa.expiringHeap[i]
	pa.expiringHeap[i].heapIndex = i
	pa.expiringHeap[j].heapIndex = j
}
func (pa *peerAddrs) Push(x any) {
	a := x.(*expiringAddr)
	a.heapIndex = len(pa.expiringHeap)
	pa.expiringHeap = append(pa.expiringHeap, a)
}
func (pa *peerAddrs) Pop() any {
	a := pa.expiringHeap[len(pa.expiringHeap)-1]
	a.heapIndex = -1
	pa.expiringHeap = pa.expiringHeap[0 : len(pa.expiringHeap)-1]
	return a
}

func (pa *peerAddrs) Delete(a *expiringAddr) {
	if ea, ok := pa.Addrs[a.Peer][string(a.Addr.Bytes())]; ok {
		if ea.heapIndex != -1 {
			heap.Remove(pa, a.heapIndex)
		}
		delete(pa.Addrs[a.Peer], string(a.Addr.Bytes()))
		if len(pa.Addrs[a.Peer]) == 0 {
			delete(pa.Addrs, a.Peer)
		}
	}
}

func (pa *peerAddrs) FindAddr(p peer.ID, addr ma.Multiaddr) (*expiringAddr, bool) {
	if m, ok := pa.Addrs[p]; ok {
		v, ok := m[string(addr.Bytes())]
		return v, ok
	}
	return nil, false
}

func (pa *peerAddrs) NextExpiry() time.Time {
	if len(pa.expiringHeap) == 0 {
		return time.Time{}
	}
	return pa.expiringHeap[0].Expiry
}

func (pa *peerAddrs) PopIfExpired(now time.Time) (*expiringAddr, bool) {
	// Use `!Before` instead of `After` to ensure that we expire *at* now, and not *just after now*.
	if len(pa.expiringHeap) > 0 && !now.Before(pa.NextExpiry()) {
		ea := heap.Pop(pa).(*expiringAddr)
		delete(pa.Addrs[ea.Peer], string(ea.Addr.Bytes()))
		if len(pa.Addrs[ea.Peer]) == 0 {
			delete(pa.Addrs, ea.Peer)
		}
		return ea, true
	}
	return nil, false
}

func (pa *peerAddrs) Update(a *expiringAddr) {
	if a.heapIndex == -1 {
		return
	}
	if a.IsConnected() {
		heap.Remove(pa, a.heapIndex)
	} else {
		heap.Fix(pa, a.heapIndex)
	}
}

func (pa *peerAddrs) Insert(a *expiringAddr) {
	a.heapIndex = -1
	if _, ok := pa.Addrs[a.Peer]; !ok {
		pa.Addrs[a.Peer] = make(map[string]*expiringAddr)
	}
	pa.Addrs[a.Peer][string(a.Addr.Bytes())] = a
	// don't add connected addr to heap.
	if a.IsConnected() {
		return
	}
	heap.Push(pa, a)
}

func (pa *peerAddrs) NumUnconnectedAddrs() int {
	return len(pa.expiringHeap)
}

type clock interface {
	Now() time.Time
}

type realclock struct{}

func (rc realclock) Now() time.Time {
	return time.Now()
}

const (
	defaultMaxSignedPeerRecords = 100_000
	defaultMaxUnconnectedAddrs  = 1_000_000
)

// memoryAddrBook manages addresses.
type memoryAddrBook struct {
	mu                   sync.RWMutex
	addrs                peerAddrs
	signedPeerRecords    map[peer.ID]*peerRecordState
	maxUnconnectedAddrs  int
	maxSignedPeerRecords int

	refCount sync.WaitGroup
	cancel   func()

	subManager *AddrSubManager
	clock      clock
}

var _ peerstore.AddrBook = (*memoryAddrBook)(nil)
var _ peerstore.CertifiedAddrBook = (*memoryAddrBook)(nil)

func NewAddrBook(opts ...AddrBookOption) *memoryAddrBook {
	ctx, cancel := context.WithCancel(context.Background())

	ab := &memoryAddrBook{
		addrs:                newPeerAddrs(),
		signedPeerRecords:    make(map[peer.ID]*peerRecordState),
		subManager:           NewAddrSubManager(),
		cancel:               cancel,
		clock:                realclock{},
		maxUnconnectedAddrs:  defaultMaxUnconnectedAddrs,
		maxSignedPeerRecords: defaultMaxSignedPeerRecords,
	}
	for _, opt := range opts {
		opt(ab)
	}

	ab.refCount.Add(1)
	go ab.background(ctx)
	return ab
}

type AddrBookOption func(book *memoryAddrBook) error

func WithClock(clock clock) AddrBookOption {
	return func(book *memoryAddrBook) error {
		book.clock = clock
		return nil
	}
}

// WithMaxAddresses sets the maximum number of unconnected addresses to store.
// The maximum number of connected addresses is bounded by the connection
// limits in the Connection Manager and Resource Manager.
func WithMaxAddresses(n int) AddrBookOption {
	return func(b *memoryAddrBook) error {
		b.maxUnconnectedAddrs = n
		return nil
	}
}

func WithMaxSignedPeerRecords(n int) AddrBookOption {
	return func(b *memoryAddrBook) error {
		b.maxSignedPeerRecords = n
		return nil
	}
}

// background periodically schedules a gc
func (mab *memoryAddrBook) background(ctx context.Context) {
	defer mab.refCount.Done()
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			mab.gc()
		case <-ctx.Done():
			return
		}
	}
}

func (mab *memoryAddrBook) Close() error {
	mab.cancel()
	mab.refCount.Wait()
	return nil
}

// gc garbage collects the in-memory address book.
func (mab *memoryAddrBook) gc() {
	now := mab.clock.Now()
	mab.mu.Lock()
	defer mab.mu.Unlock()
	for {
		ea, ok := mab.addrs.PopIfExpired(now)
		if !ok {
			return
		}
		putExpiringAddrs(ea)
		mab.maybeDeleteSignedPeerRecordUnlocked(ea.Peer)
	}
}

func (mab *memoryAddrBook) PeersWithAddrs() peer.IDSlice {
	mab.mu.RLock()
	defer mab.mu.RUnlock()
	peers := make(peer.IDSlice, 0, len(mab.addrs.Addrs))
	for pid := range mab.addrs.Addrs {
		peers = append(peers, pid)
	}
	return peers
}

// AddAddr calls AddAddrs(p, []ma.Multiaddr{addr}, ttl)
func (mab *memoryAddrBook) AddAddr(p peer.ID, addr ma.Multiaddr, ttl time.Duration) {
	mab.AddAddrs(p, []ma.Multiaddr{addr}, ttl)
}

// AddAddrs adds `addrs` for peer `p`, which will expire after the given `ttl`.
// This function never reduces the TTL or expiration of an address.
func (mab *memoryAddrBook) AddAddrs(p peer.ID, addrs []ma.Multiaddr, ttl time.Duration) {
	mab.addAddrs(p, addrs, ttl)
}

// ConsumePeerRecord adds addresses from a signed peer.PeerRecord, which will expire after the given TTL.
// See https://godoc.org/github.com/libp2p/go-libp2p/core/peerstore#CertifiedAddrBook for more details.
func (mab *memoryAddrBook) ConsumePeerRecord(recordEnvelope *record.Envelope, ttl time.Duration) (bool, error) {
	r, err := recordEnvelope.Record()
	if err != nil {
		return false, err
	}
	rec, ok := r.(*peer.PeerRecord)
	if !ok {
		return false, fmt.Errorf("unable to process envelope: not a PeerRecord")
	}
	if !rec.PeerID.MatchesPublicKey(recordEnvelope.PublicKey) {
		return false, fmt.Errorf("signing key does not match PeerID in PeerRecord")
	}

	mab.mu.Lock()
	defer mab.mu.Unlock()

	// ensure seq is greater than or equal to the last received
	lastState, found := mab.signedPeerRecords[rec.PeerID]
	if found && lastState.Seq > rec.Seq {
		return false, nil
	}
	// check if we are over the max signed peer record limit
	if !found && len(mab.signedPeerRecords) >= mab.maxSignedPeerRecords {
		return false, errors.New("too many signed peer records")
	}
	mab.signedPeerRecords[rec.PeerID] = &peerRecordState{
		Envelope: recordEnvelope,
		Seq:      rec.Seq,
	}
	mab.addAddrsUnlocked(rec.PeerID, rec.Addrs, ttl)
	return true, nil
}

func (mab *memoryAddrBook) maybeDeleteSignedPeerRecordUnlocked(p peer.ID) {
	if len(mab.addrs.Addrs[p]) == 0 {
		delete(mab.signedPeerRecords, p)
	}
}

func (mab *memoryAddrBook) addAddrs(p peer.ID, addrs []ma.Multiaddr, ttl time.Duration) {
	mab.mu.Lock()
	defer mab.mu.Unlock()

	mab.addAddrsUnlocked(p, addrs, ttl)
}

func (mab *memoryAddrBook) addAddrsUnlocked(p peer.ID, addrs []ma.Multiaddr, ttl time.Duration) {
	defer mab.maybeDeleteSignedPeerRecordUnlocked(p)

	// if ttl is zero, exit. nothing to do.
	if ttl <= 0 {
		return
	}

	// we are over limit, drop these addrs.
	if !ttlIsConnected(ttl) && mab.addrs.NumUnconnectedAddrs() >= mab.maxUnconnectedAddrs {
		return
	}

	exp := mab.clock.Now().Add(ttl)
	for _, addr := range addrs {
		// Remove suffix of /p2p/peer-id from address
		addr, addrPid := peer.SplitAddr(addr)
		if addr == nil {
			log.Warnw("Was passed nil multiaddr", "peer", p)
			continue
		}
		if addrPid != "" && addrPid != p {
			log.Warnf("Was passed p2p address with a different peerId. found: %s, expected: %s", addrPid, p)
			continue
		}
		a, found := mab.addrs.FindAddr(p, addr)
		if !found {
			// not found, announce it.
			entry := getExpiringAddrs()
			*entry = expiringAddr{Addr: addr, Expiry: exp, TTL: ttl, Peer: p}
			mab.addrs.Insert(entry)
			mab.subManager.BroadcastAddr(p, addr)
		} else {
			// update ttl & exp to whichever is greater between new and existing entry
			var changed bool
			if ttl > a.TTL {
				changed = true
				a.TTL = ttl
			}
			if exp.After(a.Expiry) {
				changed = true
				a.Expiry = exp
			}
			if changed {
				mab.addrs.Update(a)
			}
		}
	}
}

// SetAddr calls mgr.SetAddrs(p, addr, ttl)
func (mab *memoryAddrBook) SetAddr(p peer.ID, addr ma.Multiaddr, ttl time.Duration) {
	mab.SetAddrs(p, []ma.Multiaddr{addr}, ttl)
}

// SetAddrs sets the ttl on addresses. This clears any TTL there previously.
// This is used when we receive the best estimate of the validity of an address.
func (mab *memoryAddrBook) SetAddrs(p peer.ID, addrs []ma.Multiaddr, ttl time.Duration) {
	mab.mu.Lock()
	defer mab.mu.Unlock()

	defer mab.maybeDeleteSignedPeerRecordUnlocked(p)

	exp := mab.clock.Now().Add(ttl)
	for _, addr := range addrs {
		addr, addrPid := peer.SplitAddr(addr)
		if addr == nil {
			log.Warnw("was passed nil multiaddr", "peer", p)
			continue
		}
		if addrPid != "" && addrPid != p {
			log.Warnf("was passed p2p address with a different peerId, found: %s wanted: %s", addrPid, p)
			continue
		}

		if a, found := mab.addrs.FindAddr(p, addr); found {
			if ttl > 0 {
				if a.IsConnected() && !ttlIsConnected(ttl) && mab.addrs.NumUnconnectedAddrs() >= mab.maxUnconnectedAddrs {
					mab.addrs.Delete(a)
					putExpiringAddrs(a)
				} else {
					a.Addr = addr
					a.Expiry = exp
					a.TTL = ttl
					mab.addrs.Update(a)
					mab.subManager.BroadcastAddr(p, addr)
				}
			} else {
				mab.addrs.Delete(a)
				putExpiringAddrs(a)
			}
		} else {
			if ttl > 0 {
				if !ttlIsConnected(ttl) && mab.addrs.NumUnconnectedAddrs() >= mab.maxUnconnectedAddrs {
					continue
				}
				entry := getExpiringAddrs()
				*entry = expiringAddr{Addr: addr, Expiry: exp, TTL: ttl, Peer: p}
				mab.addrs.Insert(entry)
				mab.subManager.BroadcastAddr(p, addr)
			}
		}
	}
}

// UpdateAddrs updates the addresses associated with the given peer that have
// the given oldTTL to have the given newTTL.
func (mab *memoryAddrBook) UpdateAddrs(p peer.ID, oldTTL time.Duration, newTTL time.Duration) {
	mab.mu.Lock()
	defer mab.mu.Unlock()

	defer mab.maybeDeleteSignedPeerRecordUnlocked(p)

	exp := mab.clock.Now().Add(newTTL)
	for _, a := range mab.addrs.Addrs[p] {
		if oldTTL == a.TTL {
			if newTTL == 0 {
				mab.addrs.Delete(a)
				putExpiringAddrs(a)
			} else {
				// We are over limit, drop these addresses.
				if ttlIsConnected(oldTTL) && !ttlIsConnected(newTTL) && mab.addrs.NumUnconnectedAddrs() >= mab.maxUnconnectedAddrs {
					mab.addrs.Delete(a)
					putExpiringAddrs(a)
				} else {
					a.TTL = newTTL
					a.Expiry = exp
					mab.addrs.Update(a)
				}
			}
		}
	}
}

// Addrs returns all known (and valid) addresses for a given peer
func (mab *memoryAddrBook) Addrs(p peer.ID) []ma.Multiaddr {
	mab.mu.RLock()
	defer mab.mu.RUnlock()
	if _, ok := mab.addrs.Addrs[p]; !ok {
		return nil
	}
	return validAddrs(mab.clock.Now(), mab.addrs.Addrs[p])
}

func validAddrs(now time.Time, amap map[string]*expiringAddr) []ma.Multiaddr {
	good := make([]ma.Multiaddr, 0, len(amap))
	if amap == nil {
		return good
	}
	for _, m := range amap {
		if !m.ExpiredBy(now) {
			good = append(good, m.Addr)
		}
	}
	return good
}

// GetPeerRecord returns a Envelope containing a PeerRecord for the
// given peer id, if one exists.
// Returns nil if no signed PeerRecord exists for the peer.
func (mab *memoryAddrBook) GetPeerRecord(p peer.ID) *record.Envelope {
	mab.mu.RLock()
	defer mab.mu.RUnlock()

	if _, ok := mab.addrs.Addrs[p]; !ok {
		return nil
	}
	// The record may have expired, but not gargage collected.
	if len(validAddrs(mab.clock.Now(), mab.addrs.Addrs[p])) == 0 {
		return nil
	}

	state := mab.signedPeerRecords[p]
	if state == nil {
		return nil
	}
	return state.Envelope
}

// ClearAddrs removes all previously stored addresses
func (mab *memoryAddrBook) ClearAddrs(p peer.ID) {
	mab.mu.Lock()
	defer mab.mu.Unlock()

	delete(mab.signedPeerRecords, p)
	for _, a := range mab.addrs.Addrs[p] {
		mab.addrs.Delete(a)
		putExpiringAddrs(a)
	}
}

// AddrStream returns a channel on which all new addresses discovered for a
// given peer ID will be published.
func (mab *memoryAddrBook) AddrStream(ctx context.Context, p peer.ID) <-chan ma.Multiaddr {
	var initial []ma.Multiaddr

	mab.mu.RLock()
	if m, ok := mab.addrs.Addrs[p]; ok {
		initial = make([]ma.Multiaddr, 0, len(m))
		for _, a := range m {
			initial = append(initial, a.Addr)
		}
	}
	mab.mu.RUnlock()

	return mab.subManager.AddrStream(ctx, p, initial)
}

type addrSub struct {
	pubch chan ma.Multiaddr
	ctx   context.Context
}

func (s *addrSub) pubAddr(a ma.Multiaddr) {
	select {
	case s.pubch <- a:
	case <-s.ctx.Done():
	}
}

// An abstracted, pub-sub manager for address streams. Extracted from
// memoryAddrBook in order to support additional implementations.
type AddrSubManager struct {
	mu   sync.RWMutex
	subs map[peer.ID][]*addrSub
}

// NewAddrSubManager initializes an AddrSubManager.
func NewAddrSubManager() *AddrSubManager {
	return &AddrSubManager{
		subs: make(map[peer.ID][]*addrSub),
	}
}

// Used internally by the address stream coroutine to remove a subscription
// from the manager.
func (mgr *AddrSubManager) removeSub(p peer.ID, s *addrSub) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	subs := mgr.subs[p]
	if len(subs) == 1 {
		if subs[0] != s {
			return
		}
		delete(mgr.subs, p)
		return
	}

	for i, v := range subs {
		if v == s {
			subs[i] = subs[len(subs)-1]
			subs[len(subs)-1] = nil
			mgr.subs[p] = subs[:len(subs)-1]
			return
		}
	}
}

// BroadcastAddr broadcasts a new address to all subscribed streams.
func (mgr *AddrSubManager) BroadcastAddr(p peer.ID, addr ma.Multiaddr) {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if subs, ok := mgr.subs[p]; ok {
		for _, sub := range subs {
			sub.pubAddr(addr)
		}
	}
}

// AddrStream creates a new subscription for a given peer ID, pre-populating the
// channel with any addresses we might already have on file.
func (mgr *AddrSubManager) AddrStream(ctx context.Context, p peer.ID, initial []ma.Multiaddr) <-chan ma.Multiaddr {
	sub := &addrSub{pubch: make(chan ma.Multiaddr), ctx: ctx}
	out := make(chan ma.Multiaddr)

	mgr.mu.Lock()
	mgr.subs[p] = append(mgr.subs[p], sub)
	mgr.mu.Unlock()

	sort.Sort(addrList(initial))

	go func(buffer []ma.Multiaddr) {
		defer close(out)

		sent := make(map[string]struct{}, len(buffer))
		for _, a := range buffer {
			sent[string(a.Bytes())] = struct{}{}
		}

		var outch chan ma.Multiaddr
		var next ma.Multiaddr
		if len(buffer) > 0 {
			next = buffer[0]
			buffer = buffer[1:]
			outch = out
		}

		for {
			select {
			case outch <- next:
				if len(buffer) > 0 {
					next = buffer[0]
					buffer = buffer[1:]
				} else {
					outch = nil
					next = nil
				}
			case naddr := <-sub.pubch:
				if _, ok := sent[string(naddr.Bytes())]; ok {
					continue
				}
				sent[string(naddr.Bytes())] = struct{}{}

				if next == nil {
					next = naddr
					outch = out
				} else {
					buffer = append(buffer, naddr)
				}
			case <-ctx.Done():
				mgr.removeSub(p, sub)
				return
			}
		}
	}(initial)

	return out
}
