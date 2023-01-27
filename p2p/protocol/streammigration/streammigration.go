package streammigration

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	streammigration_pb "github.com/libp2p/go-libp2p/p2p/protocol/streammigration/pb"
	"github.com/libp2p/go-msgio/protoio"
)

// TODOS PoC:
// - Track existing streams
// - responder should hook up new stream to migratable stream in handler

// TODOS:
// - return a wrapped stream that can be migrated.
// - handle cases where both ends don't support stream migration.
// - Test early eof semantics
// - Test stream resets.
// - I think there's a timing issue (?) if the responder sends an EOF before it acks the migration.
// - handle stream resets during migration, before migration, after migration.
// - mirror the original stream state on the new state (half-closed)

var log = logging.Logger("p2p-holepunch")

const ID = "/libp2p/stream-migration/"

const maxMsgSize = 4 * 2 // 2 32-bit ints

type migratableStreamState int

const (
	noMigration migratableStreamState = iota
	initiatorStartingMigration
	afterAckForMigration
)

type MigratableStream struct {
	network.Stream
	mu                        sync.Mutex
	newStream                 *network.Stream
	originalStreamId          uint64
	newStreamId               uint64
	isInitiator               bool
	state                     migratableStreamState
	readEOFOnOriginalStream   bool
	originalStreamClosed      bool
	originalStreamWriteClosed bool

	onMigrationFinished func(originalStreamID uint64, ms *MigratableStream)
}

func NewMigratableStream(stream network.Stream, streamID uint64, isInitiator bool) *MigratableStream {
	return &MigratableStream{
		Stream:           stream,
		originalStreamId: streamID,
		isInitiator:      isInitiator,
	}
}

func (ms *MigratableStream) Read(p []byte) (n int, err error) {
	defer func() {
		// log.Debugf("Read initiator=%v err: %v.  ", ms.isInitiator, err)
		if err == io.EOF {
			ms.mu.Lock()
			defer ms.mu.Unlock()
			ms.readEOFOnOriginalStream = true

			if !ms.isInitiator {
				ms.CloseWrite()
			}

			if ms.state != noMigration {
				// We're in a migration, so we eat this error since it may be part of the migration.
				// If the endpoint meant to close this, they will close the new stream
				// as well after the migration.
				err = nil
			}
		}
	}()

	switch ms.state {
	case noMigration:
		fallthrough
	case initiatorStartingMigration:
		return ms.Stream.Read(p)
	case afterAckForMigration:
		if ms.tryFinishMigration() {
			return ms.Stream.Read(p)
		}

		if ms.readEOFOnOriginalStream {
			return (*ms.newStream).Read(p)
		} else {
			// More to read on the original stream
			return ms.Stream.Read(p)
		}
	default:
		panic("unreachable")
	}
}

// checkMigrationFinished checks if we can throw away the original stream.
// If we can it will set the state to `noMigration` and replace the Stream value
// with the new stream (and forget the original one). Returns true if we
// finished migration.
func (ms *MigratableStream) tryFinishMigration() bool {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if ms.state == afterAckForMigration && (ms.originalStreamClosed || ms.originalStreamWriteClosed) && ms.readEOFOnOriginalStream {
		if ms.originalStreamWriteClosed && !ms.originalStreamClosed {
			ms.Stream.Close()
		}

		ms.state = noMigration
		ms.Stream = *ms.newStream
		originalStreamId := ms.originalStreamId
		ms.originalStreamId = ms.newStreamId

		ms.newStream = nil

		ms.originalStreamClosed = false
		ms.originalStreamWriteClosed = false
		ms.readEOFOnOriginalStream = false

		if ms.onMigrationFinished != nil {
			ms.onMigrationFinished(originalStreamId, ms)
			ms.onMigrationFinished = nil
		}

		log.Info("Migration finished")
		return true
	}
	return false
}

func (ms *MigratableStream) Write(p []byte) (n int, err error) {
	defer func() {
		// log.Debugf("Write initiator=%v err: %v.  ", ms.isInitiator, err)
	}()
	switch ms.state {
	case noMigration:
		fallthrough
	case initiatorStartingMigration:
		return ms.Stream.Write(p)
	case afterAckForMigration:
		if ms.tryFinishMigration() {
			return ms.Stream.Write(p)
		}

		ms.mu.Lock()
		closedForWrites := ms.originalStreamClosed || ms.originalStreamWriteClosed
		ms.mu.Unlock()
		if closedForWrites {
			// Check if we're fully done with the original stream
			return (*ms.newStream).Write(p)
		} else {
			return ms.Stream.Write(p)
		}
	default:
		panic("unreachable")
	}
}

func (ms *MigratableStream) Close() error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	// TODO what happens when stream is closed during migration?
	ms.originalStreamClosed = true
	return ms.Stream.Close()
}

func (ms *MigratableStream) CloseWrite() error {
	// TODO what happens when stream is closed during migration?
	ms.originalStreamWriteClosed = true
	return ms.Stream.CloseWrite()
}

func (ms *MigratableStream) migrateTo(new network.Stream, newStreamID uint64) error {
	var originalStreamId uint64
	ms.mu.Lock()
	ms.newStream = &new
	ms.newStreamId = newStreamID
	ms.state = initiatorStartingMigration

	originalStreamId = ms.originalStreamId
	ms.mu.Unlock()

	wr := protoio.NewDelimitedWriter(new)
	err := wr.WriteMsg(&streammigration_pb.StreamMigration{
		Type: &streammigration_pb.StreamMigration_Migrate{Migrate: &streammigration_pb.Migrate{
			Id:   &newStreamID,
			From: &originalStreamId,
		}},
	})

	if err != nil {
		return err
	}

	rdr := protoio.NewDelimitedReader(new, maxMsgSize)
	var msg streammigration_pb.AckMigrate
	rdr.ReadMsg(&msg)
	if msg.GetDenyMigrate() {
		log.Errorf("unexpected message from peer: %v", msg)
		// TODO handle a deny
		return nil
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.state = afterAckForMigration

	return ms.CloseWrite()
}

type StreamMigrator struct {
	protocol.Switch
	nextStreamID uint64 // mutated with atomic Add
	mu           sync.Mutex
	// TODO maybe a sync.Map ?
	remoteinitiatedStreams map[peer.ID]map[uint64]*MigratableStream
}

func New(sw protocol.Switch) *StreamMigrator {
	return &StreamMigrator{
		Switch:                 sw,
		remoteinitiatedStreams: make(map[peer.ID]map[uint64]*MigratableStream),
	}
}

func (sm *StreamMigrator) Handle(s network.Stream) {
	rdr := protoio.NewDelimitedReader(s, maxMsgSize)
	var msg streammigration_pb.StreamMigration
	err := rdr.ReadMsg(&msg)
	if err != nil {
		log.Error("Error reading msg: ", err)
		return
	}

	if labelMsg := msg.GetLabel(); labelMsg != nil {
		id := labelMsg.GetId()
		log.Debug("This stream is labelled", id)

		s := &MigratableStream{
			Stream:           s,
			originalStreamId: id,
			isInitiator:      false,
		}

		remotePeerID := s.Conn().RemotePeer()

		sm.mu.Lock()
		if sm.remoteinitiatedStreams[remotePeerID] == nil {
			sm.remoteinitiatedStreams[remotePeerID] = make(map[uint64]*MigratableStream)
		}
		sm.remoteinitiatedStreams[remotePeerID][id] = s
		sm.mu.Unlock()

		sm.Switch.Handle(s)

	} else if migrateMsg := msg.GetMigrate(); migrateMsg != nil {

		id := migrateMsg.GetId()
		from := migrateMsg.GetFrom()
		log.Debug("This stream is migrating from", from, "to", id)

		remotePeerID := s.Conn().RemotePeer()

		sm.mu.Lock()
		defer sm.mu.Unlock()
		log.Debugf("Remote peer is %v", remotePeerID)
		log.Debugf("Looking for stream %d. %v. %v", from, len(sm.remoteinitiatedStreams[remotePeerID]), sm.remoteinitiatedStreams[remotePeerID])
		if sm.remoteinitiatedStreams[remotePeerID] == nil || sm.remoteinitiatedStreams[remotePeerID][from] == nil {
			log.Error("Stream not found")
			return
		}

		ms := sm.remoteinitiatedStreams[remotePeerID][from]

		wr := protoio.NewDelimitedWriter(s)
		err := wr.WriteMsg(&streammigration_pb.AckMigrate{})
		if err != nil {
			log.Error("Failed to ack migrate.")
		}

		ms.mu.Lock()
		ms.newStreamId = id
		ms.newStream = &s
		ms.state = afterAckForMigration
		ms.onMigrationFinished = func(originalStreamID uint64, ms *MigratableStream) {
			sm.mu.Lock()
			defer sm.mu.Unlock()

			remotePeerID := s.Conn().RemotePeer()
			delete(sm.remoteinitiatedStreams[remotePeerID], originalStreamID)
			sm.remoteinitiatedStreams[remotePeerID][ms.originalStreamId] = ms
		}
		ms.mu.Unlock()
	}
}

// LabelStream will send the label message over the wire and return this stream's streamID.
func (sm *StreamMigrator) LabelStream(s network.Stream) (uint64, error) {
	streamID := atomic.AddUint64(&sm.nextStreamID, 1)
	wr := protoio.NewDelimitedWriter(s)
	err := wr.WriteMsg(&streammigration_pb.StreamMigration{
		Type: &streammigration_pb.StreamMigration_Label{Label: &streammigration_pb.Label{Id: &streamID}},
	})
	if err != nil {
		return 0, fmt.Errorf("failed to write label stream message: %s", err)
	}

	return streamID, nil
}

// Migrate will start migrating the migratable stream s from  the original
// network stream to the new network stream.
func (sm *StreamMigrator) Migrate(s *MigratableStream, new network.Stream) {
	streamID := atomic.AddUint64(&sm.nextStreamID, 1)
	s.migrateTo(new, streamID)
}
