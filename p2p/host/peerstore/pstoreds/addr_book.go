package pstoreds

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	pstore "github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/record"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoreds/pb"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"

	"github.com/hashicorp/golang-lru/arc/v2"
	ds "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/query"
	logging "github.com/ipfs/go-log/v2"
	b32 "github.com/multiformats/go-base32"
	ma "github.com/multiformats/go-multiaddr"
	"google.golang.org/protobuf/proto"
)

type ttlWriteMode int

const (
	ttlOverride ttlWriteMode = iota
	ttlExtend
)

var (
	log = logging.Logger("peerstore/ds")

	// Peer addresses are stored db key pattern:
	// /peers/addrs/<b32 peer id no padding>
	addrBookBase = ds.NewKey("/peers/addrs")
)

// addrsRecord decorates the AddrBookRecord with locks and metadata.
type addrsRecord struct {
	sync.RWMutex
	*pb.AddrBookRecord
	dirty bool
}

// flush writes the record to the datastore by calling ds.Put, unless the record is
// marked for deletion, in which case we call ds.Delete. To be called within a lock.
func (r *addrsRecord) flush(write ds.Write) (err error) {
	key := addrBookBase.ChildString(b32.RawStdEncoding.EncodeToString(r.Id))

	if len(r.Addrs) == 0 {
		if err = write.Delete(context.TODO(), key); err == nil {
			r.dirty = false
		}
		return err
	}

	data, err := proto.Marshal(r)
	if err != nil {
		return err
	}
	if err = write.Put(context.TODO(), key, data); err != nil {
		return err
	}
	// write succeeded; record is no longer dirty.
	r.dirty = false
	return nil
}

// clean is called on records to perform housekeeping. The return value indicates if the record was changed
// as a result of this call.
//
// clean does the following:
// * sorts addresses by expiration (soonest expiring first).
// * removes expired addresses.
//
// It short-circuits optimistically when there's nothing to do.
//
// clean is called from several points:
// * when accessing an entry.
// * when performing periodic GC.
// * after an entry has been modified (e.g. addresses have been added or removed, TTLs updated, etc.)
//
// If the return value is true, the caller should perform a flush immediately to sync the record with the store.
func (r *addrsRecord) clean(now time.Time) (chgd bool) {
	nowUnix := now.Unix()
	addrsLen := len(r.Addrs)

	if !r.dirty && !r.hasExpiredAddrs(nowUnix) {
		// record is not dirty, and we have no expired entries to purge.
		return false
	}

	if addrsLen == 0 {
		// this is a ghost record; let's signal it has to be written.
		// flush() will take care of doing the deletion.
		return true
	}

	if r.dirty && addrsLen > 1 {
		sort.Slice(r.Addrs, func(i, j int) bool {
			return r.Addrs[i].Expiry < r.Addrs[j].Expiry
		})
	}

	r.Addrs = removeExpired(r.Addrs, nowUnix)

	return r.dirty || len(r.Addrs) != addrsLen
}

func (r *addrsRecord) hasExpiredAddrs(now int64) bool {
	if len(r.Addrs) > 0 && r.Addrs[0].Expiry <= now {
		return true
	}
	return false
}

func removeExpired(entries []*pb.AddrBookRecord_AddrEntry, now int64) []*pb.AddrBookRecord_AddrEntry {
	// since addresses are sorted by expiration, we find the first
	// survivor and split the slice on its index.
	pivot := -1
	for i, addr := range entries {
		if addr.Expiry > now {
			break
		}
		pivot = i
	}

	return entries[pivot+1:]
}

// dsAddrBook is an address book backed by a Datastore with a GC procedure to purge expired entries. It uses an
// in-memory address stream manager. See the NewAddrBook for more information.
type dsAddrBook struct {
	ctx  context.Context
	opts Options

	cache       cache[peer.ID, *addrsRecord]
	ds          ds.Batching
	gc          *dsAddrBookGc
	subsManager *pstoremem.AddrSubManager

	// controls children goroutine lifetime.
	childrenDone sync.WaitGroup
	cancelFn     func()

	clock clock
}

type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realclock struct{}

func (rc realclock) Now() time.Time {
	return time.Now()
}

func (rc realclock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

var _ pstore.AddrBook = (*dsAddrBook)(nil)
var _ pstore.CertifiedAddrBook = (*dsAddrBook)(nil)

// NewAddrBook initializes a new datastore-backed address book. It serves as a drop-in replacement for pstoremem
// (memory-backed peerstore), and works with any datastore implementing the ds.Batching interface.
//
// Addresses and peer records are serialized into protobuf, storing one datastore entry per peer, along with metadata
// to control address expiration. To alleviate disk access and serde overhead, we internally use a read/write-through
// ARC cache, the size of which is adjustable via Options.CacheSize.
//
// The user has a choice of two GC algorithms:
//
//   - lookahead GC: minimises the amount of full store traversals by maintaining a time-indexed list of entries that
//     need to be visited within the period specified in Options.GCLookaheadInterval. This is useful in scenarios with
//     considerable TTL variance, coupled with datastores whose native iterators return entries in lexicographical key
//     order. Enable this mode by passing a value Options.GCLookaheadInterval > 0. Lookahead windows are jumpy, not
//     sliding. Purges operate exclusively over the lookahead window with periodicity Options.GCPurgeInterval.
//
//   - full-purge GC (default): performs a full visit of the store with periodicity Options.GCPurgeInterval. Useful when
//     the range of possible TTL values is small and the values themselves are also extreme, e.g. 10 minutes or
//     permanent, popular values used in other libp2p modules. In this cited case, optimizing with lookahead windows
//     makes little sense.
func NewAddrBook(ctx context.Context, store ds.Batching, opts Options) (ab *dsAddrBook, err error) {
	ctx, cancelFn := context.WithCancel(ctx)
	ab = &dsAddrBook{
		ctx:         ctx,
		ds:          store,
		opts:        opts,
		cancelFn:    cancelFn,
		subsManager: pstoremem.NewAddrSubManager(),
		clock:       realclock{},
	}

	if opts.Clock != nil {
		ab.clock = opts.Clock
	}

	if opts.CacheSize > 0 {
		if ab.cache, err = arc.NewARC[peer.ID, *addrsRecord](int(opts.CacheSize)); err != nil {
			return nil, err
		}
	} else {
		ab.cache = new(noopCache[peer.ID, *addrsRecord])
	}

	if ab.gc, err = newAddressBookGc(ctx, ab); err != nil {
		return nil, err
	}

	return ab, nil
}

func (ab *dsAddrBook) Close() error {
	ab.cancelFn()
	ab.childrenDone.Wait()
	return nil
}

// loadRecord is a read-through fetch. It fetches a record from cache, falling back to the
// datastore upon a miss, and returning a newly initialized record if the peer doesn't exist.
//
// loadRecord calls clean() on an existing record before returning it. If the record changes
// as a result and the update argument is true, the resulting state is saved in the datastore.
//
// If the cache argument is true, the record is inserted in the cache when loaded from the datastore.
func (ab *dsAddrBook) loadRecord(id peer.ID, cache bool, update bool) (pr *addrsRecord, err error) {
	if pr, ok := ab.cache.Get(id); ok {
		pr.Lock()
		defer pr.Unlock()

		if pr.clean(ab.clock.Now()) && update {
			err = pr.flush(ab.ds)
		}
		return pr, err
	}

	pr = &addrsRecord{AddrBookRecord: &pb.AddrBookRecord{}}
	key := addrBookBase.ChildString(b32.RawStdEncoding.EncodeToString([]byte(id)))
	data, err := ab.ds.Get(context.TODO(), key)

	switch err {
	case ds.ErrNotFound:
		err = nil
		pr.Id = []byte(id)
	case nil:
		if err := proto.Unmarshal(data, pr); err != nil {
			return nil, err
		}
		// this record is new and local for now (not in cache), so we don't need to lock.
		if pr.clean(ab.clock.Now()) && update {
			err = pr.flush(ab.ds)
		}
	default:
		return nil, err
	}

	if cache {
		ab.cache.Add(id, pr)
	}
	return pr, err
}

// AddAddr will add a new address if it's not already in the AddrBook.
func (ab *dsAddrBook) AddAddr(p peer.ID, addr ma.Multiaddr, ttl time.Duration) {
	ab.AddAddrs(p, []ma.Multiaddr{addr}, ttl)
}

// AddAddrs will add many new addresses if they're not already in the AddrBook.
func (ab *dsAddrBook) AddAddrs(p peer.ID, addrs []ma.Multiaddr, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	addrs = cleanAddrs(addrs, p)
	ab.setAddrs(p, addrs, ttl, ttlExtend, false)
}

// ConsumePeerRecord adds addresses from a signed peer.PeerRecord (contained in
// a record.Envelope), which will expire after the given TTL.
// See https://godoc.org/github.com/libp2p/go-libp2p/core/peerstore#CertifiedAddrBook for more details.
func (ab *dsAddrBook) ConsumePeerRecord(recordEnvelope *record.Envelope, ttl time.Duration) (bool, error) {
	r, err := recordEnvelope.Record()
	if err != nil {
		return false, err
	}
	rec, ok := r.(*peer.PeerRecord)
	if !ok {
		return false, fmt.Errorf("envelope did not contain PeerRecord")
	}
	if !rec.PeerID.MatchesPublicKey(recordEnvelope.PublicKey) {
		return false, fmt.Errorf("signing key does not match PeerID in PeerRecord")
	}

	// ensure that the seq number from envelope is >= any previously received seq no
	// update when equal to extend the ttls
	if ab.latestPeerRecordSeq(rec.PeerID) > rec.Seq {
		return false, nil
	}

	addrs := cleanAddrs(rec.Addrs, rec.PeerID)
	err = ab.setAddrs(rec.PeerID, addrs, ttl, ttlExtend, true)
	if err != nil {
		return false, err
	}

	err = ab.storeSignedPeerRecord(rec.PeerID, recordEnvelope, rec)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (ab *dsAddrBook) latestPeerRecordSeq(p peer.ID) uint64 {
	pr, err := ab.loadRecord(p, true, false)
	if err != nil {
		// We ignore the error because we don't want to fail storing a new record in this
		// case.
		log.Errorw("unable to load record", "peer", p, "error", err)
		return 0
	}
	pr.RLock()
	defer pr.RUnlock()

	if len(pr.Addrs) == 0 || pr.CertifiedRecord == nil || len(pr.CertifiedRecord.Raw) == 0 {
		return 0
	}
	return pr.CertifiedRecord.Seq
}

func (ab *dsAddrBook) storeSignedPeerRecord(p peer.ID, envelope *record.Envelope, rec *peer.PeerRecord) error {
	envelopeBytes, err := envelope.Marshal()
	if err != nil {
		return err
	}
	// reload record and add routing state
	// this has to be done after we add the addresses, since if
	// we try to flush a datastore record with no addresses,
	// it will just get deleted
	pr, err := ab.loadRecord(p, true, false)
	if err != nil {
		return err
	}
	pr.Lock()
	defer pr.Unlock()
	pr.CertifiedRecord = &pb.AddrBookRecord_CertifiedRecord{
		Seq: rec.Seq,
		Raw: envelopeBytes,
	}
	pr.dirty = true
	err = pr.flush(ab.ds)
	return err
}

// GetPeerRecord returns a record.Envelope containing a peer.PeerRecord for the
// given peer id, if one exists.
// Returns nil if no signed PeerRecord exists for the peer.
func (ab *dsAddrBook) GetPeerRecord(p peer.ID) *record.Envelope {
	pr, err := ab.loadRecord(p, true, false)
	if err != nil {
		log.Errorf("unable to load record for peer %s: %v", p, err)
		return nil
	}
	pr.RLock()
	defer pr.RUnlock()
	if pr.CertifiedRecord == nil || len(pr.CertifiedRecord.Raw) == 0 || len(pr.Addrs) == 0 {
		return nil
	}
	state, _, err := record.ConsumeEnvelope(pr.CertifiedRecord.Raw, peer.PeerRecordEnvelopeDomain)
	if err != nil {
		log.Errorf("error unmarshaling stored signed peer record for peer %s: %v", p, err)
		return nil
	}
	return state
}

// SetAddr will add or update the TTL of an address in the AddrBook.
func (ab *dsAddrBook) SetAddr(p peer.ID, addr ma.Multiaddr, ttl time.Duration) {
	ab.SetAddrs(p, []ma.Multiaddr{addr}, ttl)
}

// SetAddrs will add or update the TTLs of addresses in the AddrBook.
func (ab *dsAddrBook) SetAddrs(p peer.ID, addrs []ma.Multiaddr, ttl time.Duration) {
	addrs = cleanAddrs(addrs, p)
	if ttl <= 0 {
		ab.deleteAddrs(p, addrs)
		return
	}
	ab.setAddrs(p, addrs, ttl, ttlOverride, false)
}

// UpdateAddrs will update any addresses for a given peer and TTL combination to
// have a new TTL.
func (ab *dsAddrBook) UpdateAddrs(p peer.ID, oldTTL time.Duration, newTTL time.Duration) {
	pr, err := ab.loadRecord(p, true, false)
	if err != nil {
		log.Errorf("failed to update ttls for peer %s: %s\n", p, err)
		return
	}

	pr.Lock()
	defer pr.Unlock()

	newExp := ab.clock.Now().Add(newTTL).Unix()
	for _, entry := range pr.Addrs {
		if entry.Ttl != int64(oldTTL) {
			continue
		}
		entry.Ttl, entry.Expiry = int64(newTTL), newExp
		pr.dirty = true
	}

	if pr.clean(ab.clock.Now()) {
		pr.flush(ab.ds)
	}
}

// Addrs returns all of the non-expired addresses for a given peer.
func (ab *dsAddrBook) Addrs(p peer.ID) []ma.Multiaddr {
	pr, err := ab.loadRecord(p, true, true)
	if err != nil {
		log.Warnf("failed to load peerstore entry for peer %s while querying addrs, err: %v", p, err)
		return nil
	}

	pr.RLock()
	defer pr.RUnlock()

	addrs := make([]ma.Multiaddr, len(pr.Addrs))
	for i, a := range pr.Addrs {
		var err error
		addrs[i], err = ma.NewMultiaddrBytes(a.Addr)
		if err != nil {
			log.Warn("failed to parse peerstore entry for peer %v while querying addrs, err: %v", p, err)
			return nil
		}
	}
	return addrs
}

// Peers returns all of the peer IDs for which the AddrBook has addresses.
func (ab *dsAddrBook) PeersWithAddrs() peer.IDSlice {
	ids, err := uniquePeerIds(ab.ds, addrBookBase, func(result query.Result) string {
		return ds.RawKey(result.Key).Name()
	})
	if err != nil {
		log.Errorf("error while retrieving peers with addresses: %v", err)
	}
	return ids
}

// AddrStream returns a channel on which all new addresses discovered for a
// given peer ID will be published.
func (ab *dsAddrBook) AddrStream(ctx context.Context, p peer.ID) <-chan ma.Multiaddr {
	initial := ab.Addrs(p)
	return ab.subsManager.AddrStream(ctx, p, initial)
}

// ClearAddrs will delete all known addresses for a peer ID.
func (ab *dsAddrBook) ClearAddrs(p peer.ID) {
	ab.cache.Remove(p)

	key := addrBookBase.ChildString(b32.RawStdEncoding.EncodeToString([]byte(p)))
	if err := ab.ds.Delete(context.TODO(), key); err != nil {
		log.Errorf("failed to clear addresses for peer %s: %v", p, err)
	}
}

func (ab *dsAddrBook) setAddrs(p peer.ID, addrs []ma.Multiaddr, ttl time.Duration, mode ttlWriteMode, signed bool) (err error) {
	if len(addrs) == 0 {
		return nil
	}

	pr, err := ab.loadRecord(p, true, false)
	if err != nil {
		return fmt.Errorf("failed to load peerstore entry for peer %s while setting addrs, err: %v", p, err)
	}

	pr.Lock()
	defer pr.Unlock()

	// // if we have a signed PeerRecord, ignore attempts to add unsigned addrs
	// if !signed && pr.CertifiedRecord != nil {
	// 	return nil
	// }

	newExp := ab.clock.Now().Add(ttl).Unix()
	addrsMap := make(map[string]*pb.AddrBookRecord_AddrEntry, len(pr.Addrs))
	for _, addr := range pr.Addrs {
		addrsMap[string(addr.Addr)] = addr
	}

	updateExisting := func(incoming ma.Multiaddr) *pb.AddrBookRecord_AddrEntry {
		existingEntry := addrsMap[string(incoming.Bytes())]
		if existingEntry == nil {
			return nil
		}

		switch mode {
		case ttlOverride:
			existingEntry.Ttl = int64(ttl)
			existingEntry.Expiry = newExp
		case ttlExtend:
			if int64(ttl) > existingEntry.Ttl {
				existingEntry.Ttl = int64(ttl)
			}
			if newExp > existingEntry.Expiry {
				existingEntry.Expiry = newExp
			}
		default:
			panic("BUG: unimplemented ttl mode")
		}
		return existingEntry
	}

	var entries []*pb.AddrBookRecord_AddrEntry
	for _, incoming := range addrs {
		existingEntry := updateExisting(incoming)

		if existingEntry == nil {
			// 	if signed {
			// 		entries = append(entries, existingEntry)
			// 	}
			// } else {
			// new addr, add & broadcast
			entry := &pb.AddrBookRecord_AddrEntry{
				Addr:   incoming.Bytes(),
				Ttl:    int64(ttl),
				Expiry: newExp,
			}
			entries = append(entries, entry)

			// note: there's a minor chance that writing the record will fail, in which case we would've broadcast
			// the addresses without persisting them. This is very unlikely and not much of an issue.
			ab.subsManager.BroadcastAddr(p, incoming)
		}
	}

	// if signed {
	// 	// when adding signed addrs, we want to keep _only_ the incoming addrs
	// 	pr.Addrs = entries
	// } else {
	pr.Addrs = append(pr.Addrs, entries...)
	// }

	pr.dirty = true
	pr.clean(ab.clock.Now())
	return pr.flush(ab.ds)
}

// deletes addresses in place, avoiding copies until we encounter the first deletion.
// does not preserve order, but entries are re-sorted before flushing to disk anyway.
func deleteInPlace(s []*pb.AddrBookRecord_AddrEntry, addrs []ma.Multiaddr) []*pb.AddrBookRecord_AddrEntry {
	if s == nil || len(addrs) == 0 {
		return s
	}
	survived := len(s)
Outer:
	for i, addr := range s {
		for _, del := range addrs {
			if !bytes.Equal(del.Bytes(), addr.Addr) {
				continue
			}
			survived--
			// if there are no survivors, bail out
			if survived == 0 {
				break Outer
			}
			s[i] = s[survived]
			// we've already dealt with s[i], move to the next
			continue Outer
		}
	}
	return s[:survived]
}

func (ab *dsAddrBook) deleteAddrs(p peer.ID, addrs []ma.Multiaddr) (err error) {
	pr, err := ab.loadRecord(p, false, false)
	if err != nil {
		return fmt.Errorf("failed to load peerstore entry for peer %v while deleting addrs, err: %v", p, err)
	}

	pr.Lock()
	defer pr.Unlock()

	if pr.Addrs == nil {
		return nil
	}

	pr.Addrs = deleteInPlace(pr.Addrs, addrs)

	pr.dirty = true
	pr.clean(ab.clock.Now())
	return pr.flush(ab.ds)
}

func cleanAddrs(addrs []ma.Multiaddr, pid peer.ID) []ma.Multiaddr {
	clean := make([]ma.Multiaddr, 0, len(addrs))
	for _, addr := range addrs {
		// Remove suffix of /p2p/peer-id from address
		addr, addrPid := peer.SplitAddr(addr)
		if addr == nil {
			log.Warnw("Was passed a nil multiaddr", "peer", pid)
			continue
		}
		if addrPid != "" && addrPid != pid {
			log.Warnf("Was passed p2p address with a different peerId. found: %s, expected: %s", addrPid, pid)
			continue
		}
		clean = append(clean, addr)
	}
	return clean
}
