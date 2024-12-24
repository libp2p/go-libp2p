package blankhost

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/record"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"

	logging "github.com/ipfs/go-log/v2"

	ma "github.com/multiformats/go-multiaddr"
	mstream "github.com/multiformats/go-multistream"
)

var log = logging.Logger("blankhost")

// BlankHost is a thin implementation of the host.Host interface
type BlankHost struct {
	N       network.Network
	M       *mstream.MultistreamMuxer[protocol.ID]
	E       event.Bus
	ConnMgr connmgr.ConnManager
	// SkipInitSignedRecord is a flag to skip the initialization of a signed record for the host
	SkipInitSignedRecord bool
	emitters             struct {
		evtLocalProtocolsUpdated event.Emitter
	}
	onStop []func() error
}

type config struct {
	cmgr                 connmgr.ConnManager
	eventBus             event.Bus
	skipInitSignedRecord bool
}

type Option = func(cfg *config)

func WithConnectionManager(cmgr connmgr.ConnManager) Option {
	return func(cfg *config) {
		cfg.cmgr = cmgr
	}
}

func WithEventBus(eventBus event.Bus) Option {
	return func(cfg *config) {
		cfg.eventBus = eventBus
	}
}

func SkipInitSignedRecord(eventBus event.Bus) Option {
	return func(cfg *config) {
		cfg.skipInitSignedRecord = true
	}
}

func NewBlankHost(n network.Network, options ...Option) *BlankHost {
	cfg := config{
		cmgr: &connmgr.NullConnMgr{},
	}
	for _, opt := range options {
		opt(&cfg)
	}

	bh := &BlankHost{
		N:       n,
		ConnMgr: cfg.cmgr,
		M:       mstream.NewMultistreamMuxer[protocol.ID](),
		E:       cfg.eventBus,

		SkipInitSignedRecord: cfg.skipInitSignedRecord,
	}

	if err := bh.Start(); err != nil {
		log.Errorf("error creating blank host, err=%s", err)
		return nil
	}

	return bh
}

func (bh *BlankHost) Start() error {
	if bh.E == nil {
		bh.E = eventbus.NewBus(eventbus.WithMetricsTracer(eventbus.NewMetricsTracer()))
	}

	// subscribe the connection manager to network notifications (has no effect with NullConnMgr)
	notifee := bh.ConnMgr.Notifee()
	bh.N.Notify(notifee)
	bh.onStop = append(bh.onStop, func() error {
		bh.N.StopNotify(notifee)
		return nil
	})

	var err error
	if bh.emitters.evtLocalProtocolsUpdated, err = bh.E.Emitter(&event.EvtLocalProtocolsUpdated{}); err != nil {
		return err
	}
	bh.onStop = append(bh.onStop, func() error {
		bh.emitters.evtLocalProtocolsUpdated.Close()
		return nil
	})

	bh.N.SetStreamHandler(bh.newStreamHandler)
	bh.onStop = append(bh.onStop, func() error {
		bh.N.SetStreamHandler(func(s network.Stream) { s.Reset() })
		return nil
	})

	// persist a signed peer record for self to the peerstore.
	if !bh.SkipInitSignedRecord {
		if err := bh.initSignedRecord(); err != nil {
			log.Errorf("error creating blank host, err=%s", err)
			return err
		}
	}

	return nil
}

func (bh *BlankHost) Stop() error {
	var err error
	for _, f := range bh.onStop {
		err = errors.Join(err, f())
	}
	bh.onStop = nil
	return err
}

func (bh *BlankHost) initSignedRecord() error {
	cab, ok := peerstore.GetCertifiedAddrBook(bh.N.Peerstore())
	if !ok {
		log.Error("peerstore does not support signed records")
		return errors.New("peerstore does not support signed records")
	}
	rec := peer.PeerRecordFromAddrInfo(peer.AddrInfo{ID: bh.ID(), Addrs: bh.Addrs()})
	ev, err := record.Seal(rec, bh.Peerstore().PrivKey(bh.ID()))
	if err != nil {
		log.Errorf("failed to create signed record for self, err=%s", err)
		return fmt.Errorf("failed to create signed record for self, err=%s", err)
	}
	_, err = cab.ConsumePeerRecord(ev, peerstore.PermanentAddrTTL)
	if err != nil {
		log.Errorf("failed to persist signed record to peerstore,err=%s", err)
		return fmt.Errorf("failed to persist signed record for self, err=%s", err)
	}
	return err
}

var _ host.Host = (*BlankHost)(nil)

func (bh *BlankHost) Addrs() []ma.Multiaddr {
	addrs, err := bh.N.InterfaceListenAddresses()
	if err != nil {
		log.Debug("error retrieving network interface addrs: ", err)
		return nil
	}

	return addrs
}

func (bh *BlankHost) Close() error {
	return bh.N.Close()
}

func (bh *BlankHost) Connect(ctx context.Context, ai peer.AddrInfo) error {
	// absorb addresses into peerstore
	bh.Peerstore().AddAddrs(ai.ID, ai.Addrs, peerstore.TempAddrTTL)

	cs := bh.N.ConnsToPeer(ai.ID)
	if len(cs) > 0 {
		return nil
	}

	_, err := bh.Network().DialPeer(ctx, ai.ID)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}
	return err
}

func (bh *BlankHost) Peerstore() peerstore.Peerstore {
	return bh.N.Peerstore()
}

func (bh *BlankHost) ID() peer.ID {
	return bh.N.LocalPeer()
}

func (bh *BlankHost) NewStream(ctx context.Context, p peer.ID, protos ...protocol.ID) (network.Stream, error) {
	s, err := bh.N.NewStream(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("failed to open stream: %w", err)
	}

	selected, err := mstream.SelectOneOf(protos, s)
	if err != nil {
		s.Reset()
		return nil, fmt.Errorf("failed to negotiate protocol: %w", err)
	}

	s.SetProtocol(selected)
	bh.Peerstore().AddProtocols(p, selected)

	return s, nil
}

func (bh *BlankHost) RemoveStreamHandler(pid protocol.ID) {
	bh.Mux().RemoveHandler(pid)
	bh.emitters.evtLocalProtocolsUpdated.Emit(event.EvtLocalProtocolsUpdated{
		Removed: []protocol.ID{pid},
	})
}

func (bh *BlankHost) SetStreamHandler(pid protocol.ID, handler network.StreamHandler) {
	bh.Mux().AddHandler(pid, func(p protocol.ID, rwc io.ReadWriteCloser) error {
		is := rwc.(network.Stream)
		is.SetProtocol(p)
		handler(is)
		return nil
	})
	bh.emitters.evtLocalProtocolsUpdated.Emit(event.EvtLocalProtocolsUpdated{
		Added: []protocol.ID{pid},
	})
}

func (bh *BlankHost) SetStreamHandlerMatch(pid protocol.ID, m func(protocol.ID) bool, handler network.StreamHandler) {
	bh.Mux().AddHandlerWithFunc(pid, m, func(p protocol.ID, rwc io.ReadWriteCloser) error {
		is := rwc.(network.Stream)
		is.SetProtocol(p)
		handler(is)
		return nil
	})
	bh.emitters.evtLocalProtocolsUpdated.Emit(event.EvtLocalProtocolsUpdated{
		Added: []protocol.ID{pid},
	})
}

// newStreamHandler is the remote-opened stream handler for network.Network
func (bh *BlankHost) newStreamHandler(s network.Stream) {
	protoID, handle, err := bh.Mux().Negotiate(s)
	if err != nil {
		log.Infow("protocol negotiation failed", "error", err)
		s.Reset()
		return
	}

	s.SetProtocol(protoID)

	handle(protoID, s)
}

// TODO: i'm not sure this really needs to be here
func (bh *BlankHost) Mux() protocol.Switch {
	return bh.M
}

// TODO: also not sure this fits... Might be better ways around this (leaky abstractions)
func (bh *BlankHost) Network() network.Network {
	return bh.N
}

func (bh *BlankHost) ConnManager() connmgr.ConnManager {
	return bh.ConnMgr
}

func (bh *BlankHost) EventBus() event.Bus {
	return bh.E
}
