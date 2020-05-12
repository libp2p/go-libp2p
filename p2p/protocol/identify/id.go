package identify

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	ic "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/helpers"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/libp2p/go-libp2p-core/record"

	"github.com/libp2p/go-eventbus"
	pb "github.com/libp2p/go-libp2p/p2p/protocol/identify/pb"

	ggio "github.com/gogo/protobuf/io"
	logging "github.com/ipfs/go-log"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr-net"
	msmux "github.com/multiformats/go-multistream"
)

var log = logging.Logger("net/identify")

// ID is the protocol.ID of the Identify Service.
const ID = "/p2p/id/1.1.0"

// LegacyID is the protocol.ID of version 1.0.0 of the identify
// service, which does not support signed peer records.
const LegacyID = "/ipfs/id/1.0.0"

// LibP2PVersion holds the current protocol version for a client running this code
// TODO(jbenet): fix the versioning mess.
// XXX: Don't change this till 2020. You'll break all go-ipfs versions prior to
// 0.4.17 which asserted an exact version match.
const LibP2PVersion = "ipfs/0.1.0"

// ClientVersion is the default user agent.
//
// Deprecated: Set this with the UserAgent option.
var ClientVersion = "github.com/libp2p/go-libp2p"

func init() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	version := bi.Main.Version
	if version == "(devel)" {
		ClientVersion = bi.Main.Path
	} else {
		ClientVersion = fmt.Sprintf("%s@%s", bi.Main.Path, bi.Main.Version)
	}
}

// transientTTL is a short ttl for invalidated previously connected addrs
const transientTTL = 10 * time.Second

// IDService is a structure that implements ProtocolIdentify.
// It is a trivial service that gives the other peer some
// useful information about the local peer. A sort of hello.
//
// The IDService sends:
//  * Our IPFS Protocol Version
//  * Our IPFS Agent Version
//  * Our public Listen Addresses
type IDService struct {
	Host      host.Host
	UserAgent string

	ctx       context.Context
	ctxCancel context.CancelFunc
	// ensure we shutdown ONLY once
	closeSync sync.Once
	// track resources that need to be shut down before we shut down
	refCount sync.WaitGroup

	// Identified connections (finished and in progress).
	connsMu sync.RWMutex
	conns   map[network.Conn]chan struct{}

	addrMu sync.Mutex

	peerrec   *record.Envelope
	peerrecMu sync.RWMutex

	// our own observed addresses.
	observedAddrs *ObservedAddrManager

	emitters struct {
		evtPeerProtocolsUpdated        event.Emitter
		evtPeerIdentificationCompleted event.Emitter
		evtPeerIdentificationFailed    event.Emitter
	}
}

// NewIDService constructs a new *IDService and activates it by
// attaching its stream handler to the given host.Host.
func NewIDService(h host.Host, opts ...Option) *IDService {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}

	userAgent := ClientVersion
	if cfg.userAgent != "" {
		userAgent = cfg.userAgent
	}

	hostCtx, cancel := context.WithCancel(context.Background())
	s := &IDService{
		Host:      h,
		UserAgent: userAgent,

		ctx:           hostCtx,
		ctxCancel:     cancel,
		conns:         make(map[network.Conn]chan struct{}),
		observedAddrs: NewObservedAddrManager(hostCtx, h),
	}

	// handle local protocol handler updates, and push deltas to peers.
	var err error

	s.refCount.Add(1)
	go s.handleEvents()

	s.emitters.evtPeerProtocolsUpdated, err = h.EventBus().Emitter(&event.EvtPeerProtocolsUpdated{})
	if err != nil {
		log.Warnf("identify service not emitting peer protocol updates; err: %s", err)
	}
	s.emitters.evtPeerIdentificationCompleted, err = h.EventBus().Emitter(&event.EvtPeerIdentificationCompleted{})
	if err != nil {
		log.Warnf("identify service not emitting identification completed events; err: %s", err)
	}
	s.emitters.evtPeerIdentificationFailed, err = h.EventBus().Emitter(&event.EvtPeerIdentificationFailed{})
	if err != nil {
		log.Warnf("identify service not emitting identification failed events; err: %s", err)
	}

	// register protocols that do not depend on peer records.
	h.SetStreamHandler(IDDelta, s.deltaHandler)
	h.SetStreamHandler(LegacyID, s.requestHandler)
	h.SetStreamHandler(LegacyIDPush, s.pushHandler)

	// register protocols that depend on peer records.
	h.SetStreamHandler(ID, s.requestHandler)
	h.SetStreamHandler(IDPush, s.pushHandler)

	h.Network().Notify((*netNotifiee)(s))
	return s
}

func (ids *IDService) handleEvents() {
	defer ids.refCount.Done()

	sub, err := ids.Host.EventBus().Subscribe([]interface{}{&event.EvtLocalProtocolsUpdated{},
		&event.EvtLocalAddressesUpdated{}}, eventbus.BufSize(256))
	if err != nil {
		log.Errorf("failed to subscribe to events on the bus, err=%s", err)
		return
	}

	defer sub.Close()

	for {
		select {
		case e, more := <-sub.Out():
			if !more {
				return
			}
			switch evt := e.(type) {
			case event.EvtLocalAddressesUpdated:
				ids.handleLocalAddrsUpdated(evt)
			case event.EvtLocalProtocolsUpdated:
				ids.handleProtosChanged(evt)
			}

		case <-ids.ctx.Done():
			return
		}
	}
}

// Close shuts down the IDService
func (ids *IDService) Close() error {
	ids.closeSync.Do(func() {
		ids.ctxCancel()
		ids.refCount.Wait()
	})
	return nil
}

func (ids *IDService) handleProtosChanged(evt event.EvtLocalProtocolsUpdated) {
	ids.fireProtocolDelta(evt)
}

func (ids *IDService) handleLocalAddrsUpdated(evt event.EvtLocalAddressesUpdated) {
	ids.peerrecMu.Lock()
	rec := evt.SignedPeerRecord
	ids.peerrec = rec
	ids.peerrecMu.Unlock()

	log.Debug("triggering push based on updated local PeerRecord")
	ids.Push()
}

// OwnObservedAddrs returns the addresses peers have reported we've dialed from
func (ids *IDService) OwnObservedAddrs() []ma.Multiaddr {
	return ids.observedAddrs.Addrs()
}

func (ids *IDService) ObservedAddrsFor(local ma.Multiaddr) []ma.Multiaddr {
	return ids.observedAddrs.AddrsFor(local)
}

// IdentifyConn synchronously triggers an identify request on the connection and
// waits for it to complete. If the connection is being identified by another
// caller, this call will wait. If the connection has already been identified,
// it will return immediately.
func (ids *IDService) IdentifyConn(c network.Conn) {
	<-ids.IdentifyWait(c)
}

// IdentifyWait triggers an identify (if the connection has not already been
// identified) and returns a channel that is closed when the identify protocol
// completes.
func (ids *IDService) IdentifyWait(c network.Conn) <-chan struct{} {
	ids.connsMu.RLock()
	wait, found := ids.conns[c]
	ids.connsMu.RUnlock()

	if found {
		return wait
	}

	ids.connsMu.Lock()
	defer ids.connsMu.Unlock()

	wait, found = ids.conns[c]

	if !found {
		wait = make(chan struct{})
		ids.conns[c] = wait

		// Spawn an identify. The connection may actually be closed
		// already, but that doesn't really matter. We'll fail to open a
		// stream then forget the connection.
		go ids.identifyConn(c, wait)
	}

	return wait
}

func (ids *IDService) removeConn(c network.Conn) {
	ids.connsMu.Lock()
	delete(ids.conns, c)
	ids.connsMu.Unlock()
}

func (ids *IDService) identifyConn(c network.Conn, signal chan struct{}) {
	var (
		s   network.Stream
		err error
	)

	defer func() {
		close(signal)

		// emit the appropriate event.
		if p := c.RemotePeer(); err == nil {
			ids.emitters.evtPeerIdentificationCompleted.Emit(event.EvtPeerIdentificationCompleted{Peer: p})
		} else {
			ids.emitters.evtPeerIdentificationFailed.Emit(event.EvtPeerIdentificationFailed{Peer: p, Reason: err})
		}
	}()

	s, err = c.NewStream()
	if err != nil {
		log.Debugw("error opening identify stream", "error", err)
		// the connection is probably already closed if we hit this.
		// TODO: Remove this?
		c.Close()

		// We usually do this on disconnect, but we may have already
		// processed the disconnect event.
		ids.removeConn(c)
		return
	}

	protocolIDs := []string{ID, LegacyID}
	// ok give the response to our handler.
	var selectedProto string
	if selectedProto, err = msmux.SelectOneOf(protocolIDs, s); err != nil {
		log.Event(context.TODO(), "IdentifyOpenFailed", c.RemotePeer(), logging.Metadata{"error": err})
		s.Reset()
		return
	}
	s.SetProtocol(protocol.ID(selectedProto))
	ids.responseHandler(s)
}

func protoSupportsPeerRecords(proto protocol.ID) bool {
	return proto == ID || proto == IDPush
}

func (ids *IDService) requestHandler(s network.Stream) {
	defer helpers.FullClose(s)
	c := s.Conn()

	w := ggio.NewDelimitedWriter(s)
	mes := pb.Identify{}
	ids.populateMessage(&mes, s.Conn(), protoSupportsPeerRecords(s.Protocol()))
	w.WriteMsg(&mes)

	log.Debugf("%s sent message to %s %s", ID, c.RemotePeer(), c.RemoteMultiaddr())
}

func (ids *IDService) responseHandler(s network.Stream) {
	c := s.Conn()

	r := ggio.NewDelimitedReader(s, 2048)
	mes := pb.Identify{}
	if err := r.ReadMsg(&mes); err != nil {
		log.Warning("error reading identify message: ", err)
		s.Reset()
		return
	}

	defer func() { go helpers.FullClose(s) }()

	log.Debugf("%s received message from %s %s", s.Protocol(), c.RemotePeer(), c.RemoteMultiaddr())
	ids.consumeMessage(&mes, c, protoSupportsPeerRecords(s.Protocol()))
}

func (ids *IDService) broadcast(protos []protocol.ID, payloadWriter func(s network.Stream)) {
	var wg sync.WaitGroup

	protoStrs := protocol.ConvertToStrings(protos)
	ctx, cancel := context.WithTimeout(ids.ctx, 30*time.Second)
	ctx = network.WithNoDial(ctx, protoStrs[0])

	pstore := ids.Host.Peerstore()
	for _, p := range ids.Host.Network().Peers() {
		wg.Add(1)

		go func(p peer.ID, conns []network.Conn) {
			defer wg.Done()

			// Wait till identify completes so we can check the
			// supported protocols.
			for _, c := range conns {
				select {
				case <-ids.IdentifyWait(c):
				case <-ctx.Done():
					return
				}
			}

			// avoid the unnecessary stream if the peer does not support the protocol.
			if sup, err := pstore.SupportsProtocols(p, protoStrs...); err != nil && len(sup) == 0 {
				// the peer does not support the required protocol.
				return
			}
			// if the peerstore query errors, we go ahead anyway.

			s, err := ids.Host.NewStream(ctx, p, protos...)
			if err != nil {
				log.Debugf("error opening push stream to %s: %s", p, err.Error())
				return
			}

			rch := make(chan struct{}, 1)
			go func() {
				payloadWriter(s)
				rch <- struct{}{}
			}()

			select {
			case <-rch:
			case <-ctx.Done():
				// this is taking too long, abort!
				s.Reset()
			}
		}(p, ids.Host.Network().ConnsToPeer(p))
	}

	// this supervisory goroutine is necessary to cancel the context
	go func() {
		wg.Wait()
		cancel()
	}()
}

func (ids *IDService) populateMessage(mes *pb.Identify, c network.Conn, usePeerRecords bool) {
	// set protocols this node is currently handling
	protos := ids.Host.Mux().Protocols()
	mes.Protocols = make([]string, len(protos))
	for i, p := range protos {
		mes.Protocols[i] = p
	}

	// observed address so other side is informed of their
	// "public" address, at least in relation to us.
	mes.ObservedAddr = c.RemoteMultiaddr().Bytes()

	if usePeerRecords {
		ids.peerrecMu.RLock()
		rec := ids.peerrec
		ids.peerrecMu.RUnlock()

		if rec == nil {
			log.Errorf("latest peer record does not exist. identify message incomplete!")
		} else {
			recBytes, err := rec.Marshal()
			if err != nil {
				log.Errorf("error marshaling peer record: %v", err)
			} else {
				mes.SignedPeerRecord = recBytes
				log.Debugf("%s sent peer record to %s", c.LocalPeer(), c.RemotePeer())
			}
		}
	} else {
		// set listen addrs, get our latest addrs from Host.
		laddrs := ids.Host.Addrs()
		// Note: LocalMultiaddr is sometimes 0.0.0.0
		viaLoopback := manet.IsIPLoopback(c.LocalMultiaddr()) || manet.IsIPLoopback(c.RemoteMultiaddr())
		mes.ListenAddrs = make([][]byte, 0, len(laddrs))
		for _, addr := range laddrs {
			if !viaLoopback && manet.IsIPLoopback(addr) {
				continue
			}
			mes.ListenAddrs = append(mes.ListenAddrs, addr.Bytes())
		}
	}

	// set our public key
	ownKey := ids.Host.Peerstore().PubKey(ids.Host.ID())

	// check if we even have a public key.
	if ownKey == nil {
		// public key is nil. We are either using insecure transport or something erratic happened.
		// check if we're even operating in "secure mode"
		if ids.Host.Peerstore().PrivKey(ids.Host.ID()) != nil {
			// private key is present. But NO public key. Something bad happened.
			log.Errorf("did not have own public key in Peerstore")
		}
		// if neither of the key is present it is safe to assume that we are using an insecure transport.
	} else {
		// public key is present. Safe to proceed.
		if kb, err := ownKey.Bytes(); err != nil {
			log.Errorf("failed to convert key to bytes")
		} else {
			mes.PublicKey = kb
		}
	}

	// set protocol versions
	pv := LibP2PVersion
	av := ids.UserAgent
	mes.ProtocolVersion = &pv
	mes.AgentVersion = &av
}

func (ids *IDService) consumeMessage(mes *pb.Identify, c network.Conn, usePeerRecords bool) {
	p := c.RemotePeer()

	// mes.Protocols
	ids.Host.Peerstore().SetProtocols(p, mes.Protocols...)

	// mes.ObservedAddr
	ids.consumeObservedAddress(mes.GetObservedAddr(), c)

	// mes.ListenAddrs
	laddrs := mes.GetListenAddrs()
	lmaddrs := make([]ma.Multiaddr, 0, len(laddrs))
	for _, addr := range laddrs {
		maddr, err := ma.NewMultiaddrBytes(addr)
		if err != nil {
			log.Debugf("%s failed to parse multiaddr from %s %s", ID,
				p, c.RemoteMultiaddr())
			continue
		}
		lmaddrs = append(lmaddrs, maddr)
	}

	// NOTE: Do not add `c.RemoteMultiaddr()` to the peerstore if the remote
	// peer doesn't tell us to do so. Otherwise, we'll advertise it.
	//
	// This can cause an "addr-splosion" issue where the network will slowly
	// gossip and collect observed but unadvertised addresses. Given a NAT
	// that picks random source ports, this can cause DHT nodes to collect
	// many undialable addresses for other peers.

	// add certified addresses for the peer, if they sent us a signed peer record
	var signedPeerRecord *record.Envelope
	if usePeerRecords {
		var err error
		signedPeerRecord, err = signedPeerRecordFromMessage(mes)
		if err != nil {
			log.Errorf("error getting peer record from Identify message: %v", err)
		}
	}

	// Extend the TTLs on the known (probably) good addresses.
	// Taking the lock ensures that we don't concurrently process a disconnect.
	ids.addrMu.Lock()
	ttl := peerstore.RecentlyConnectedAddrTTL
	if ids.Host.Network().Connectedness(p) == network.Connected {
		ttl = peerstore.ConnectedAddrTTL
	}

	// invalidate previous addrs -- we use a transient ttl instead of 0 to ensure there
	// is no period of having no good addrs whatsoever
	ids.Host.Peerstore().UpdateAddrs(p, peerstore.ConnectedAddrTTL, transientTTL)

	// add signed addrs if we have them and the peerstore supports them
	cab, ok := peerstore.GetCertifiedAddrBook(ids.Host.Peerstore())
	if ok && signedPeerRecord != nil {
		_, addErr := cab.ConsumePeerRecord(signedPeerRecord, ttl)
		if addErr != nil {
			log.Debugf("error adding signed addrs to peerstore: %v", addErr)
		}
	} else {
		ids.Host.Peerstore().AddAddrs(p, lmaddrs, ttl)
	}
	ids.addrMu.Unlock()

	log.Debugf("%s received listen addrs for %s: %s", c.LocalPeer(), c.RemotePeer(), lmaddrs)

	// get protocol versions
	pv := mes.GetProtocolVersion()
	av := mes.GetAgentVersion()

	ids.Host.Peerstore().Put(p, "ProtocolVersion", pv)
	ids.Host.Peerstore().Put(p, "AgentVersion", av)

	// get the key from the other side. we may not have it (no-auth transport)
	ids.consumeReceivedPubKey(c, mes.PublicKey)
}

func (ids *IDService) consumeReceivedPubKey(c network.Conn, kb []byte) {
	lp := c.LocalPeer()
	rp := c.RemotePeer()

	if kb == nil {
		log.Debugf("%s did not receive public key for remote peer: %s", lp, rp)
		return
	}

	newKey, err := ic.UnmarshalPublicKey(kb)
	if err != nil {
		log.Warningf("%s cannot unmarshal key from remote peer: %s, %s", lp, rp, err)
		return
	}

	// verify key matches peer.ID
	np, err := peer.IDFromPublicKey(newKey)
	if err != nil {
		log.Debugf("%s cannot get peer.ID from key of remote peer: %s, %s", lp, rp, err)
		return
	}

	if np != rp {
		// if the newKey's peer.ID does not match known peer.ID...

		if rp == "" && np != "" {
			// if local peerid is empty, then use the new, sent key.
			err := ids.Host.Peerstore().AddPubKey(rp, newKey)
			if err != nil {
				log.Debugf("%s could not add key for %s to peerstore: %s", lp, rp, err)
			}

		} else {
			// we have a local peer.ID and it does not match the sent key... error.
			log.Errorf("%s received key for remote peer %s mismatch: %s", lp, rp, np)
		}
		return
	}

	currKey := ids.Host.Peerstore().PubKey(rp)
	if currKey == nil {
		// no key? no auth transport. set this one.
		err := ids.Host.Peerstore().AddPubKey(rp, newKey)
		if err != nil {
			log.Debugf("%s could not add key for %s to peerstore: %s", lp, rp, err)
		}
		return
	}

	// ok, we have a local key, we should verify they match.
	if currKey.Equals(newKey) {
		return // ok great. we're done.
	}

	// weird, got a different key... but the different key MATCHES the peer.ID.
	// this odd. let's log error and investigate. this should basically never happen
	// and it means we have something funky going on and possibly a bug.
	log.Errorf("%s identify got a different key for: %s", lp, rp)

	// okay... does ours NOT match the remote peer.ID?
	cp, err := peer.IDFromPublicKey(currKey)
	if err != nil {
		log.Errorf("%s cannot get peer.ID from local key of remote peer: %s, %s", lp, rp, err)
		return
	}
	if cp != rp {
		log.Errorf("%s local key for remote peer %s yields different peer.ID: %s", lp, rp, cp)
		return
	}

	// okay... curr key DOES NOT match new key. both match peer.ID. wat?
	log.Errorf("%s local key and received key for %s do not match, but match peer.ID", lp, rp)
}

// HasConsistentTransport returns true if the address 'a' shares a
// protocol set with any address in the green set. This is used
// to check if a given address might be one of the addresses a peer is
// listening on.
func HasConsistentTransport(a ma.Multiaddr, green []ma.Multiaddr) bool {
	protosMatch := func(a, b []ma.Protocol) bool {
		if len(a) != len(b) {
			return false
		}

		for i, p := range a {
			if b[i].Code != p.Code {
				return false
			}
		}
		return true
	}

	protos := a.Protocols()

	for _, ga := range green {
		if protosMatch(protos, ga.Protocols()) {
			return true
		}
	}

	return false
}

func (ids *IDService) consumeObservedAddress(observed []byte, c network.Conn) {
	if observed == nil {
		return
	}

	maddr, err := ma.NewMultiaddrBytes(observed)
	if err != nil {
		log.Debugf("error parsing received observed addr for %s: %s", c, err)
		return
	}

	ids.observedAddrs.Record(c, maddr)
}

func addrInAddrs(a ma.Multiaddr, as []ma.Multiaddr) bool {
	for _, b := range as {
		if a.Equal(b) {
			return true
		}
	}
	return false
}

func signedPeerRecordFromMessage(msg *pb.Identify) (*record.Envelope, error) {
	if msg.SignedPeerRecord == nil || len(msg.SignedPeerRecord) == 0 {
		return nil, nil
	}
	env, _, err := record.ConsumeEnvelope(msg.SignedPeerRecord, peer.PeerRecordEnvelopeDomain)
	return env, err
}

// netNotifiee defines methods to be used with the IpfsDHT
type netNotifiee IDService

func (nn *netNotifiee) IDService() *IDService {
	return (*IDService)(nn)
}

func (nn *netNotifiee) Connected(n network.Network, v network.Conn) {
	nn.IDService().IdentifyWait(v)
}

func (nn *netNotifiee) Disconnected(n network.Network, v network.Conn) {
	ids := nn.IDService()

	// Stop tracking the connection.
	ids.removeConn(v)

	// undo the setting of addresses to peer.ConnectedAddrTTL we did
	ids.addrMu.Lock()
	defer ids.addrMu.Unlock()

	if ids.Host.Network().Connectedness(v.RemotePeer()) != network.Connected {
		// Last disconnect.
		ps := ids.Host.Peerstore()
		ps.UpdateAddrs(v.RemotePeer(), peerstore.ConnectedAddrTTL, peerstore.RecentlyConnectedAddrTTL)
	}
}

func (nn *netNotifiee) OpenedStream(n network.Network, v network.Stream) {}
func (nn *netNotifiee) ClosedStream(n network.Network, v network.Stream) {}
func (nn *netNotifiee) Listen(n network.Network, a ma.Multiaddr)         {}
func (nn *netNotifiee) ListenClose(n network.Network, a ma.Multiaddr)    {}
