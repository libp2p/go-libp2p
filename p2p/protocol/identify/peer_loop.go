package identify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p-core/helpers"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/libp2p/go-libp2p-core/record"

	pb "github.com/libp2p/go-libp2p/p2p/protocol/identify/pb"

	ggio "github.com/gogo/protobuf/io"
	ma "github.com/multiformats/go-multiaddr"
)

var errProtocolNotSupported = errors.New("protocol not supported")

type identifySnapshot struct {
	protocols []string
	addrs     []ma.Multiaddr
	record    *record.Envelope
}

type peerHandler struct {
	ids *IDService

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	pid peer.ID

	snapshotMu sync.RWMutex
	snapshot   *identifySnapshot

	pushCh  chan struct{}
	deltaCh chan struct{}
}

func newPeerHandler(pid peer.ID, ids *IDService) *peerHandler {
	ph := &peerHandler{
		ids: ids,
		pid: pid,

		snapshot: ids.getSnapshot(),

		pushCh:  make(chan struct{}, 1),
		deltaCh: make(chan struct{}, 1),
	}

	return ph
}

func (ph *peerHandler) start() {
	ctx, cancel := context.WithCancel(context.Background())
	ph.ctx = ctx
	ph.cancel = cancel

	ph.wg.Add(1)
	go ph.loop()
}

func (ph *peerHandler) close() error {
	ph.cancel()
	ph.wg.Wait()
	return nil
}

// per peer loop for pushing updates
func (ph *peerHandler) loop() {
	defer ph.wg.Done()

	for {
		select {
		// our listen addresses have changed, send an IDPush.
		case <-ph.pushCh:
			if err := ph.sendPush(); err != nil {
				log.Warnw("failed to send Identify Push", "peer", ph.pid, "error", err)
			}

		case <-ph.deltaCh:
			if err := ph.sendDelta(); err != nil {
				log.Warnw("failed to send Identify Delta", "peer", ph.pid, "error", err)
			}

		case <-ph.ctx.Done():
			return
		}
	}
}

func (ph *peerHandler) sendDelta() error {
	// send a push if the peer does not support the Delta protocol.
	if !ph.peerSupportsProtos([]string{IDDelta}) {
		log.Debugw("will send push as peer does not support delta", "peer", ph.pid)
		if err := ph.sendPush(); err != nil {
			return fmt.Errorf("failed to send push on delta message: %w", err)
		}
		return nil
	}

	// extract a delta message, updating the last state.
	mes := ph.nextDelta()
	if mes == nil || (len(mes.AddedProtocols) == 0 && len(mes.RmProtocols) == 0) {
		return nil
	}

	ds, err := ph.openStream([]string{IDDelta})
	if err != nil {
		return fmt.Errorf("failed to open delta stream: %w", err)
	}

	if err := ph.sendMessage(ds, &pb.Identify{Delta: mes}); err != nil {
		return fmt.Errorf("failed to send delta message, %w", err)
	}
	return nil
}

func (ph *peerHandler) sendPush() error {
	dp, err := ph.openStream([]string{IDPush})
	if err == errProtocolNotSupported {
		log.Debugw("not sending push as peer does not support protocol", "peer", ph.pid)
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to open push stream: %w", err)
	}

	conn := dp.Conn()
	mes := &pb.Identify{}

	snapshot := ph.ids.getSnapshot()
	ph.snapshotMu.Lock()
	ph.snapshot = snapshot
	ph.snapshotMu.Unlock()

	ph.ids.populateMessage(mes, conn, snapshot)

	if err := ph.sendMessage(dp, mes); err != nil {
		return fmt.Errorf("failed to send push message: %w", err)
	}
	return nil
}

func (ph *peerHandler) openStream(protos []string) (network.Stream, error) {
	// wait for the other peer to send us an Identify response on "all" connections we have with it
	// so we can look at it's supported protocols and avoid a multistream-select roundtrip to negotiate the protocol
	// if we know for a fact that it dosen't support the protocol.
	conns := ph.ids.Host.Network().ConnsToPeer(ph.pid)
	for _, c := range conns {
		select {
		case <-ph.ids.IdentifyWait(c):
		case <-ph.ctx.Done():
			return nil, ph.ctx.Err()
		}
	}

	if !ph.peerSupportsProtos(protos) {
		return nil, errProtocolNotSupported
	}

	// negotiate a stream without opening a new connection as we "should" already have a connection.
	ctx, cancel := context.WithTimeout(ph.ctx, 30*time.Second)
	defer cancel()
	ctx = network.WithNoDial(ctx, "should already have connection")

	// newstream will open a stream on the first protocol the remote peer supports from the among
	// the list of protocols passed to it.
	s, err := ph.ids.Host.NewStream(ctx, ph.pid, protocol.ConvertFromStrings(protos)...)
	if err != nil {
		return nil, err
	}

	return s, err
}

// returns true if the peer supports atleast one of the given protocols
func (ph *peerHandler) peerSupportsProtos(protos []string) bool {
	conns := ph.ids.Host.Network().ConnsToPeer(ph.pid)
	for _, c := range conns {
		select {
		case <-ph.ids.IdentifyWait(c):
		case <-ph.ctx.Done():
			return false
		}
	}

	pstore := ph.ids.Host.Peerstore()

	if sup, err := pstore.SupportsProtocols(ph.pid, protos...); err == nil && len(sup) == 0 {
		return false
	}
	return true
}

func (ph *peerHandler) nextDelta() *pb.Delta {
	curr := ph.ids.Host.Mux().Protocols()

	// Extract the old protocol list and replace the old snapshot with an
	// updated one.
	ph.snapshotMu.Lock()
	snapshot := *ph.snapshot
	old := snapshot.protocols
	snapshot.protocols = curr
	ph.snapshot = &snapshot
	ph.snapshotMu.Unlock()

	oldProtos := make(map[string]struct{}, len(old))
	currProtos := make(map[string]struct{}, len(curr))

	for _, proto := range old {
		oldProtos[proto] = struct{}{}
	}

	for _, proto := range curr {
		currProtos[proto] = struct{}{}
	}

	var added []string
	var removed []string

	// has it been added ?
	for p := range currProtos {
		if _, ok := oldProtos[p]; !ok {
			added = append(added, p)
		}
	}

	// has it been removed ?
	for p := range oldProtos {
		if _, ok := currProtos[p]; !ok {
			removed = append(removed, p)
		}
	}

	return &pb.Delta{
		AddedProtocols: added,
		RmProtocols:    removed,
	}
}

func (ph *peerHandler) sendMessage(s network.Stream, mes *pb.Identify) error {
	defer helpers.FullClose(s)
	c := s.Conn()
	if err := ggio.NewDelimitedWriter(s).WriteMsg(mes); err != nil {
		return err

	}
	log.Debugw("sent identify update", "protocol", s.Protocol(), "peer", c.RemotePeer(),
		"peer address", c.RemoteMultiaddr())
	return nil
}
