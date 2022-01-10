package blank

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/libp2p/go-libp2p/p2p/host/pstoremanager"

	"github.com/libp2p/go-libp2p-core/connmgr"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/libp2p/go-libp2p-core/record"

	"github.com/libp2p/go-eventbus"

	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
	msmux "github.com/multiformats/go-multistream"
)

var log = logging.Logger("basehost")

type Option func(*BaseHost) error

func WithConnMgr(mgr connmgr.ConnManager) Option {
	return func(h *BaseHost) error {
		h.connmgr = mgr
		return nil
	}
}

func WithMultistreamMuxer(mux *msmux.MultistreamMuxer) Option {
	return func(h *BaseHost) error {
		h.mux = mux
		return nil
	}
}

type BaseHost struct {
	network   network.Network
	psManager *pstoremanager.PeerstoreManager
	eventbus  event.Bus
	mux       *msmux.MultistreamMuxer
	connmgr   connmgr.ConnManager

	closeOnce sync.Once

	emitters struct {
		evtLocalProtocolsUpdated    event.Emitter
		evtPeerConnectednessChanged event.Emitter
	}
}

var _ host.Host = &BaseHost{}

func NewHost(n network.Network, opts ...Option) (*BaseHost, error) {
	eventBus := eventbus.NewBus()
	psManager, err := pstoremanager.NewPeerstoreManager(n.Peerstore(), eventBus)
	if err != nil {
		return nil, err
	}
	h := &BaseHost{
		network:   n,
		eventbus:  eventBus,
		psManager: psManager,
	}
	for _, opt := range opts {
		if err := opt(h); err != nil {
			return nil, err
		}
	}
	if h.mux == nil {
		h.mux = msmux.NewMultistreamMuxer()
	}
	if h.connmgr == nil {
		h.connmgr = &connmgr.NullConnMgr{}
	} else {
		n.Notify(h.connmgr.Notifee())
	}

	if h.emitters.evtLocalProtocolsUpdated, err = h.eventbus.Emitter(&event.EvtLocalProtocolsUpdated{}); err != nil {
		return nil, err
	}
	h.emitters.evtPeerConnectednessChanged, err = h.eventbus.Emitter(&event.EvtPeerConnectednessChanged{})
	if err != nil {
		return nil, err
	}
	n.Notify(newPeerConnectWatcher(h.emitters.evtPeerConnectednessChanged))

	if err := h.initSignedRecord(); err != nil {
		return nil, err
	}
	n.SetStreamHandler(h.newStreamHandler)
	return h, nil
}

func (h *BaseHost) initSignedRecord() error {
	cab, ok := peerstore.GetCertifiedAddrBook(h.network.Peerstore())
	if !ok {
		return errors.New("peerstore does not support signed records")
	}
	rec := peer.PeerRecordFromAddrInfo(peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()})
	ev, err := record.Seal(rec, h.Peerstore().PrivKey(h.ID()))
	if err != nil {
		return fmt.Errorf("failed to create signed record for self: %s", err)
	}
	if _, err := cab.ConsumePeerRecord(ev, peerstore.PermanentAddrTTL); err != nil {
		return fmt.Errorf("failed to persist signed record for self: %s", err)
	}
	return nil
}

func (h *BaseHost) Start() {
	h.psManager.Start()
}

func (h *BaseHost) Addrs() []ma.Multiaddr {
	addrs, err := h.Network().InterfaceListenAddresses()
	if err != nil {
		log.Debug("error retrieving network interface addrs: ", err)
		return nil
	}
	return addrs
}

func (h *BaseHost) Connect(ctx context.Context, ai peer.AddrInfo) error {
	// absorb addresses into peerstore
	h.Peerstore().AddAddrs(ai.ID, ai.Addrs, peerstore.TempAddrTTL)

	cs := h.Network().ConnsToPeer(ai.ID)
	if len(cs) > 0 {
		return nil
	}

	_, err := h.Network().DialPeer(ctx, ai.ID)
	return err
}

func (h *BaseHost) ConnManager() connmgr.ConnManager {
	return h.connmgr
}

func (h *BaseHost) Close() error {
	h.closeOnce.Do(func() {
		h.Network().Close()
		h.connmgr.Close()
		h.emitters.evtLocalProtocolsUpdated.Close()
		h.emitters.evtPeerConnectednessChanged.Close()

		h.psManager.Close()
		if h.Peerstore() != nil {
			h.Peerstore().Close()
		}
	})
	return nil
}

func (h *BaseHost) EventBus() event.Bus {
	return h.eventbus
}

func (h *BaseHost) Peerstore() peerstore.Peerstore {
	return h.Network().Peerstore()
}

// Network returns the Network interface of the Host
func (h *BaseHost) Network() network.Network {
	return h.network
}

// Mux returns the Mux multiplexing incoming streams to protocol handlers
func (h *BaseHost) Mux() protocol.Switch {
	return h.mux
}

func (h *BaseHost) ID() peer.ID {
	return h.Network().LocalPeer()
}

// newStreamHandler is the remote-opened stream handler for network.Network
func (h *BaseHost) newStreamHandler(s network.Stream) {
	protoID, handle, err := h.Mux().Negotiate(s)
	if err != nil {
		log.Infow("protocol negotiation failed", "error", err)
		s.Reset()
		return
	}

	s.SetProtocol(protocol.ID(protoID))
	go handle(protoID, s)
}

func (h *BaseHost) NewStream(ctx context.Context, p peer.ID, protos ...protocol.ID) (network.Stream, error) {
	s, err := h.Network().NewStream(ctx, p)
	if err != nil {
		return nil, err
	}

	// Negotiate the protocol in the background, obeying the context.
	var selected string
	errCh := make(chan error, 1)
	go func() {
		selected, err = msmux.SelectOneOf(protocol.ConvertToStrings(protos), s)
		errCh <- err
	}()
	select {
	case err = <-errCh:
		if err != nil {
			s.Reset()
			return nil, err
		}
	case <-ctx.Done():
		s.Reset()
		// wait for `SelectOneOf` to error out because of resetting the stream.
		<-errCh
		return nil, ctx.Err()
	}

	selpid := protocol.ID(selected)
	s.SetProtocol(selpid)
	h.Peerstore().AddProtocols(p, selected)

	return s, nil
}

// SetStreamHandler sets the protocol handler on the Host's Mux.
// This is equivalent to:
//   host.Mux().SetHandler(proto, handler)
// (Threadsafe)
func (h *BaseHost) SetStreamHandler(pid protocol.ID, handler network.StreamHandler) {
	h.Mux().AddHandler(string(pid), func(p string, rwc io.ReadWriteCloser) error {
		is := rwc.(network.Stream)
		is.SetProtocol(protocol.ID(p))
		handler(is)
		return nil
	})
	h.emitters.evtLocalProtocolsUpdated.Emit(event.EvtLocalProtocolsUpdated{
		Added: []protocol.ID{pid},
	})
}

// SetStreamHandlerMatch sets the protocol handler on the Host's Mux
// using a matching function to do protocol comparisons
func (h *BaseHost) SetStreamHandlerMatch(pid protocol.ID, m func(string) bool, handler network.StreamHandler) {
	h.Mux().AddHandlerWithFunc(string(pid), m, func(p string, rwc io.ReadWriteCloser) error {
		is := rwc.(network.Stream)
		is.SetProtocol(protocol.ID(p))
		handler(is)
		return nil
	})
	h.emitters.evtLocalProtocolsUpdated.Emit(event.EvtLocalProtocolsUpdated{
		Added: []protocol.ID{pid},
	})
}

// RemoveStreamHandler returns ..
func (h *BaseHost) RemoveStreamHandler(pid protocol.ID) {
	h.Mux().RemoveHandler(string(pid))
	h.emitters.evtLocalProtocolsUpdated.Emit(event.EvtLocalProtocolsUpdated{
		Removed: []protocol.ID{pid},
	})
}
