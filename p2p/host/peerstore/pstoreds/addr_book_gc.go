package pstoreds

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoreds/pb"
	"google.golang.org/protobuf/proto"

	ds "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/query"
	b32 "github.com/multiformats/go-base32"
)

var (
	// GC lookahead entries are stored in key pattern:
	// /peers/gc/addrs/<unix timestamp of next visit>/<peer ID b32> => nil
	// in databases with lexicographical key order, this time-indexing allows us to visit
	// only the timeslice we are interested in.
	gcLookaheadBase = ds.NewKey("/peers/gc/addrs")

	// queries
	purgeLookaheadQuery = query.Query{
		Prefix:   gcLookaheadBase.String(),
		Orders:   []query.Order{query.OrderByFunction(orderByTimestampInKey)},
		KeysOnly: true,
	}

	purgeStoreQuery = query.Query{
		Prefix:   addrBookBase.String(),
		Orders:   []query.Order{query.OrderByKey{}},
		KeysOnly: false,
	}

	populateLookaheadQuery = query.Query{
		Prefix:   addrBookBase.String(),
		Orders:   []query.Order{query.OrderByKey{}},
		KeysOnly: true,
	}
)

// dsAddrBookGc is responsible for garbage collection in a datastore-backed address book.
type dsAddrBookGc struct {
	ctx              context.Context
	ab               *dsAddrBook
	running          chan struct{}
	lookaheadEnabled bool
	purgeFunc        func()
	currWindowEnd    int64
}

func newAddressBookGc(ctx context.Context, ab *dsAddrBook) (*dsAddrBookGc, error) {
	if ab.opts.GCPurgeInterval < 0 {
		return nil, fmt.Errorf("negative GC purge interval provided: %s", ab.opts.GCPurgeInterval)
	}
	if ab.opts.GCLookaheadInterval < 0 {
		return nil, fmt.Errorf("negative GC lookahead interval provided: %s", ab.opts.GCLookaheadInterval)
	}
	if ab.opts.GCInitialDelay < 0 {
		return nil, fmt.Errorf("negative GC initial delay provided: %s", ab.opts.GCInitialDelay)
	}
	if ab.opts.GCLookaheadInterval > 0 && ab.opts.GCLookaheadInterval < ab.opts.GCPurgeInterval {
		return nil, fmt.Errorf("lookahead interval must be larger than purge interval, respectively: %s, %s",
			ab.opts.GCLookaheadInterval, ab.opts.GCPurgeInterval)
	}

	lookaheadEnabled := ab.opts.GCLookaheadInterval > 0
	gc := &dsAddrBookGc{
		ctx:              ctx,
		ab:               ab,
		running:          make(chan struct{}, 1),
		lookaheadEnabled: lookaheadEnabled,
	}

	if lookaheadEnabled {
		gc.purgeFunc = gc.purgeLookahead
	} else {
		gc.purgeFunc = gc.purgeStore
	}

	// do not start GC timers if purge is disabled; this GC can only be triggered manually.
	if ab.opts.GCPurgeInterval > 0 {
		gc.ab.childrenDone.Add(1)
		go gc.background()
	}

	return gc, nil
}

// gc prunes expired addresses from the datastore at regular intervals. It should be spawned as a goroutine.
func (gc *dsAddrBookGc) background() {
	defer gc.ab.childrenDone.Done()

	select {
	case <-gc.ab.clock.After(gc.ab.opts.GCInitialDelay):
	case <-gc.ab.ctx.Done():
		// yield if we have been cancelled/closed before the delay elapses.
		return
	}

	purgeTimer := time.NewTicker(gc.ab.opts.GCPurgeInterval)
	defer purgeTimer.Stop()

	var lookaheadCh <-chan time.Time
	if gc.lookaheadEnabled {
		lookaheadTimer := time.NewTicker(gc.ab.opts.GCLookaheadInterval)
		lookaheadCh = lookaheadTimer.C
		gc.populateLookahead() // do a lookahead now
		defer lookaheadTimer.Stop()
	}

	for {
		select {
		case <-purgeTimer.C:
			gc.purgeFunc()

		case <-lookaheadCh:
			// will never trigger if lookahead is disabled (nil Duration).
			gc.populateLookahead()

		case <-gc.ctx.Done():
			return
		}
	}
}

// purgeCycle runs a single GC purge cycle. It operates within the lookahead window if lookahead is enabled; else it
// visits all entries in the datastore, deleting the addresses that have expired.
func (gc *dsAddrBookGc) purgeLookahead() {
	select {
	case gc.running <- struct{}{}:
		defer func() { <-gc.running }()
	default:
		// yield if lookahead is running.
		return
	}

	var id peer.ID
	record := &addrsRecord{AddrBookRecord: &pb.AddrBookRecord{}} // empty record to reuse and avoid allocs.
	batch, err := newCyclicBatch(gc.ab.ds, defaultOpsPerCyclicBatch)
	if err != nil {
		log.Warnf("failed while creating batch to purge GC entries: %v", err)
	}

	// This function drops an unparseable GC entry; this is for safety. It is an escape hatch in case
	// we modify the format of keys going forward. If a user runs a new version against an old DB,
	// if we don't clean up unparseable entries we'll end up accumulating garbage.
	dropInError := func(key ds.Key, err error, msg string) {
		if err != nil {
			log.Warnf("failed while %s record with GC key: %v, err: %v; deleting", msg, key, err)
		}
		if err = batch.Delete(context.TODO(), key); err != nil {
			log.Warnf("failed to delete corrupt GC lookahead entry: %v, err: %v", key, err)
		}
	}

	// This function drops a GC key if the entry is cleaned correctly. It may reschedule another visit
	// if the next earliest expiry falls within the current window again.
	dropOrReschedule := func(key ds.Key, ar *addrsRecord) {
		if err := batch.Delete(context.TODO(), key); err != nil {
			log.Warnf("failed to delete lookahead entry: %v, err: %v", key, err)
		}

		// re-add the record if it needs to be visited again in this window.
		if len(ar.Addrs) != 0 && ar.Addrs[0].Expiry <= gc.currWindowEnd {
			gcKey := gcLookaheadBase.ChildString(fmt.Sprintf("%d/%s", ar.Addrs[0].Expiry, key.Name()))
			if err := batch.Put(context.TODO(), gcKey, []byte{}); err != nil {
				log.Warnf("failed to add new GC key: %v, err: %v", gcKey, err)
			}
		}
	}

	results, err := gc.ab.ds.Query(context.TODO(), purgeLookaheadQuery)
	if err != nil {
		log.Warnf("failed while fetching entries to purge: %v", err)
		return
	}
	defer results.Close()

	now := gc.ab.clock.Now().Unix()

	// keys: 	/peers/gc/addrs/<unix timestamp of next visit>/<peer ID b32>
	// values: 	nil
	for result := range results.Next() {
		gcKey := ds.RawKey(result.Key)
		ts, err := strconv.ParseInt(gcKey.Parent().Name(), 10, 64)
		if err != nil {
			dropInError(gcKey, err, "parsing timestamp")
			log.Warnf("failed while parsing timestamp from key: %v, err: %v", result.Key, err)
			continue
		} else if ts > now {
			// this is an ordered cursor; when we hit an entry with a timestamp beyond now, we can break.
			break
		}

		idb32, err := b32.RawStdEncoding.DecodeString(gcKey.Name())
		if err != nil {
			dropInError(gcKey, err, "parsing peer ID")
			log.Warnf("failed while parsing b32 peer ID from key: %v, err: %v", result.Key, err)
			continue
		}

		id, err = peer.IDFromBytes(idb32)
		if err != nil {
			dropInError(gcKey, err, "decoding peer ID")
			log.Warnf("failed while decoding peer ID from key: %v, err: %v", result.Key, err)
			continue
		}

		// if the record is in cache, we clean it and flush it if necessary.
		if cached, ok := gc.ab.cache.Peek(id); ok {
			cached.Lock()
			if cached.clean(gc.ab.clock.Now()) {
				if err = cached.flush(batch); err != nil {
					log.Warnf("failed to flush entry modified by GC for peer: &v, err: %v", id.Pretty(), err)
				}
			}
			dropOrReschedule(gcKey, cached)
			cached.Unlock()
			continue
		}

		record.Reset()

		// otherwise, fetch it from the store, clean it and flush it.
		entryKey := addrBookBase.ChildString(gcKey.Name())
		val, err := gc.ab.ds.Get(context.TODO(), entryKey)
		if err != nil {
			// captures all errors, including ErrNotFound.
			dropInError(gcKey, err, "fetching entry")
			continue
		}
		err = proto.Unmarshal(val, record)
		if err != nil {
			dropInError(gcKey, err, "unmarshalling entry")
			continue
		}
		if record.clean(gc.ab.clock.Now()) {
			err = record.flush(batch)
			if err != nil {
				log.Warnf("failed to flush entry modified by GC for peer: &v, err: %v", id.Pretty(), err)
			}
		}
		dropOrReschedule(gcKey, record)
	}

	if err = batch.Commit(context.TODO()); err != nil {
		log.Warnf("failed to commit GC purge batch: %v", err)
	}
}

func (gc *dsAddrBookGc) purgeStore() {
	select {
	case gc.running <- struct{}{}:
		defer func() { <-gc.running }()
	default:
		// yield if lookahead is running.
		return
	}

	record := &addrsRecord{AddrBookRecord: &pb.AddrBookRecord{}} // empty record to reuse and avoid allocs.
	batch, err := newCyclicBatch(gc.ab.ds, defaultOpsPerCyclicBatch)
	if err != nil {
		log.Warnf("failed while creating batch to purge GC entries: %v", err)
	}

	results, err := gc.ab.ds.Query(context.TODO(), purgeStoreQuery)
	if err != nil {
		log.Warnf("failed while opening iterator: %v", err)
		return
	}
	defer results.Close()

	// keys: 	/peers/addrs/<peer ID b32>
	for result := range results.Next() {
		record.Reset()
		if err = proto.Unmarshal(result.Value, record); err != nil {
			// TODO log
			continue
		}

		id := record.Id
		if !record.clean(gc.ab.clock.Now()) {
			continue
		}

		if err := record.flush(batch); err != nil {
			log.Warnf("failed to flush entry modified by GC for peer: &v, err: %v", id, err)
		}
		gc.ab.cache.Remove(peer.ID(id))
	}

	if err = batch.Commit(context.TODO()); err != nil {
		log.Warnf("failed to commit GC purge batch: %v", err)
	}
}

// populateLookahead populates the lookahead window by scanning the entire store and picking entries whose earliest
// expiration falls within the window period.
//
// Those entries are stored in the lookahead region in the store, indexed by the timestamp when they need to be
// visited, to facilitate temporal range scans.
func (gc *dsAddrBookGc) populateLookahead() {
	if gc.ab.opts.GCLookaheadInterval == 0 {
		return
	}

	select {
	case gc.running <- struct{}{}:
		defer func() { <-gc.running }()
	default:
		// yield if something's running.
		return
	}

	until := gc.ab.clock.Now().Add(gc.ab.opts.GCLookaheadInterval).Unix()

	var id peer.ID
	record := &addrsRecord{AddrBookRecord: &pb.AddrBookRecord{}}
	results, err := gc.ab.ds.Query(context.TODO(), populateLookaheadQuery)
	if err != nil {
		log.Warnf("failed while querying to populate lookahead GC window: %v", err)
		return
	}
	defer results.Close()

	batch, err := newCyclicBatch(gc.ab.ds, defaultOpsPerCyclicBatch)
	if err != nil {
		log.Warnf("failed while creating batch to populate lookahead GC window: %v", err)
		return
	}

	for result := range results.Next() {
		idb32 := ds.RawKey(result.Key).Name()
		k, err := b32.RawStdEncoding.DecodeString(idb32)
		if err != nil {
			log.Warnf("failed while decoding peer ID from key: %v, err: %v", result.Key, err)
			continue
		}
		if id, err = peer.IDFromBytes(k); err != nil {
			log.Warnf("failed while decoding peer ID from key: %v, err: %v", result.Key, err)
		}

		// if the record is in cache, use the cached version.
		if cached, ok := gc.ab.cache.Peek(id); ok {
			cached.RLock()
			if len(cached.Addrs) == 0 || cached.Addrs[0].Expiry > until {
				cached.RUnlock()
				continue
			}
			gcKey := gcLookaheadBase.ChildString(fmt.Sprintf("%d/%s", cached.Addrs[0].Expiry, idb32))
			if err = batch.Put(context.TODO(), gcKey, []byte{}); err != nil {
				log.Warnf("failed while inserting GC entry for peer: %v, err: %v", id.Pretty(), err)
			}
			cached.RUnlock()
			continue
		}

		record.Reset()

		val, err := gc.ab.ds.Get(context.TODO(), ds.RawKey(result.Key))
		if err != nil {
			log.Warnf("failed which getting record from store for peer: %v, err: %v", id.Pretty(), err)
			continue
		}
		if err := proto.Unmarshal(val, record); err != nil {
			log.Warnf("failed while unmarshalling record from store for peer: %v, err: %v", id.Pretty(), err)
			continue
		}
		if len(record.Addrs) > 0 && record.Addrs[0].Expiry <= until {
			gcKey := gcLookaheadBase.ChildString(fmt.Sprintf("%d/%s", record.Addrs[0].Expiry, idb32))
			if err = batch.Put(context.TODO(), gcKey, []byte{}); err != nil {
				log.Warnf("failed while inserting GC entry for peer: %v, err: %v", id.Pretty(), err)
			}
		}
	}

	if err = batch.Commit(context.TODO()); err != nil {
		log.Warnf("failed to commit GC lookahead batch: %v", err)
	}

	gc.currWindowEnd = until
}

// orderByTimestampInKey orders the results by comparing the timestamp in the
// key. A lexiographic sort by itself is wrong since "10" is less than "2", but
// as an int 2 is obviously less than 10.
func orderByTimestampInKey(a, b query.Entry) int {
	aKey := ds.RawKey(a.Key)
	aInt, err := strconv.ParseInt(aKey.Parent().Name(), 10, 64)
	if err != nil {
		return -1
	}
	bKey := ds.RawKey(b.Key)
	bInt, err := strconv.ParseInt(bKey.Parent().Name(), 10, 64)
	if err != nil {
		return -1
	}
	if aInt < bInt {
		return -1
	} else if aInt == bInt {
		return 0
	}
	return 1
}
