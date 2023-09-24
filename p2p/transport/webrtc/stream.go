package libp2pwebrtc

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/pb"
	"github.com/libp2p/go-msgio/pbio"

	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v3"
)

const (
	// maxMessageSize is the maximum message size of the Protobuf message we send / receive.
	maxMessageSize = 16384
	// Pion SCTP association has an internal receive buffer of 1MB (roughly, 1MB per connection).
	// We can change this value in the SettingEngine before creating the peerconnection.
	// https://github.com/pion/webrtc/blob/v3.1.49/sctptransport.go#L341
	maxBufferedAmount = 2 * maxMessageSize
	// bufferedAmountLowThreshold and maxBufferedAmount are bound
	// to a stream but congestion control is done on the whole
	// SCTP association. This means that a single stream can monopolize
	// the complete congestion control window (cwnd) if it does not
	// read stream data and it's remote continues to send. We can
	// add messages to the send buffer once there is space for 1 full
	// sized message.
	bufferedAmountLowThreshold = maxBufferedAmount / 2

	// Proto overhead assumption is 5 bytes
	protoOverhead = 5
	// Varint overhead is assumed to be 2 bytes. This is safe since
	// 1. This is only used and when writing message, and
	// 2. We only send messages in chunks of `maxMessageSize - varintOverhead`
	// which includes the data and the protobuf header. Since `maxMessageSize`
	// is less than or equal to 2 ^ 14, the varint will not be more than
	// 2 bytes in length.
	varintOverhead = 2
)

type receiveState uint8

const (
	receiveStateReceiving receiveState = iota
	receiveStateDataRead               // received and read the FIN
	receiveStateReset                  // either by calling CloseRead locally, or by receiving
)

type sendState uint8

const (
	sendStateSending sendState = iota
	sendStateDataSent
	sendStateReset
)

// Package pion detached data channel into a net.Conn
// and then a network.MuxedStream
type stream struct {
	mx sync.Mutex
	// pbio.Reader is not thread safe,
	// and while our Read is not promised to be thread safe,
	// we ourselves internally read from multiple routines...
	reader pbio.Reader
	// this buffer is limited up to a single message. Reason we need it
	// is because a reader might read a message midway, and so we need a
	// wait to buffer that for as long as the remaining part is not (yet) read
	nextMessage  *pb.Message
	receiveState receiveState

	// The public Write API is not promised to be thread safe,
	// but we need to be able to write control messages.
	writer               pbio.Writer
	sendStateChanged     chan struct{}
	sendState            sendState
	controlMsgQueue      []*pb.Message
	writeDeadline        time.Time
	writeDeadlineUpdated chan struct{}
	writeAvailable       chan struct{}

	readLoopOnce sync.Once

	onDone      func()
	id          uint16 // for logging purposes
	dataChannel *datachannel.DataChannel
	closeErr    error
}

var _ network.MuxedStream = &stream{}

func newStream(
	channel *webrtc.DataChannel,
	rwc datachannel.ReadWriteCloser,
	onDone func(),
) *stream {
	s := &stream{
		reader: pbio.NewDelimitedReader(rwc, maxMessageSize),
		writer: pbio.NewDelimitedWriter(rwc),

		sendStateChanged:     make(chan struct{}, 1),
		writeDeadlineUpdated: make(chan struct{}, 1),
		writeAvailable:       make(chan struct{}, 1),

		id:          *channel.ID(),
		dataChannel: rwc.(*datachannel.DataChannel),
		onDone:      onDone,
	}

	channel.SetBufferedAmountLowThreshold(bufferedAmountLowThreshold)
	channel.OnBufferedAmountLow(func() {
		s.mx.Lock()
		defer s.mx.Unlock()
		// first send out queued control messages
		for len(s.controlMsgQueue) > 0 {
			msg := s.controlMsgQueue[0]
			available := s.availableSendSpace()
			if controlMsgSize < available {
				s.writer.WriteMsg(msg) // TODO: handle error
				s.controlMsgQueue = s.controlMsgQueue[1:]
			} else {
				return
			}
		}

		if s.isDone() {
			// onDone removes the stream from the connection and requires the connection lock.
			// This callback(onBufferedAmountLow) is executing in the sctp readLoop goroutine.
			// If Connection.Close is called concurrently, the closing goroutine will acquire
			// the connection lock and wait for sctp readLoop to exit, the sctp readLoop will
			// wait for the connection lock before exiting, causing a deadlock.
			// Run this in a different goroutine to avoid the deadlock.
			go func() {
				s.mx.Lock()
				defer s.mx.Unlock()
				// TODO: we should be closing the underlying datachannel, but this resets the stream
				// See https://github.com/libp2p/specs/issues/575 for details.
				// _ = s.dataChannel.Close()
				// TODO: write for the spawned reader to return
				s.onDone()
			}()
		}

		select {
		case s.writeAvailable <- struct{}{}:
		default:
		}
	})
	return s
}

func (s *stream) Close() error {
	closeWriteErr := s.CloseWrite()
	closeReadErr := s.CloseRead()
	if closeWriteErr != nil {
		return closeWriteErr
	}
	return closeReadErr
}

func (s *stream) Reset() error {
	cancelWriteErr := s.cancelWrite()
	closeReadErr := s.CloseRead()
	if cancelWriteErr != nil {
		return cancelWriteErr
	}
	return closeReadErr
}

func (s *stream) SetDeadline(t time.Time) error {
	_ = s.SetReadDeadline(t)
	return s.SetWriteDeadline(t)
}

// processIncomingFlag process the flag on an incoming message
// It needs to be called with msg.Flag, not msg.GetFlag(),
// otherwise we'd misinterpret the default value.
// It needs to be called while the mutex is locked.
func (s *stream) processIncomingFlag(flag *pb.Message_Flag) {
	if flag == nil {
		return
	}

	switch *flag {
	case pb.Message_FIN:
		if s.receiveState == receiveStateReceiving {
			s.receiveState = receiveStateDataRead
		}
	case pb.Message_STOP_SENDING:
		if s.sendState == sendStateSending {
			s.sendState = sendStateReset
		}
		select {
		case s.sendStateChanged <- struct{}{}:
		default:
		}
	case pb.Message_RESET:
		if s.receiveState == receiveStateReceiving {
			s.receiveState = receiveStateReset
		}
	}
	s.maybeDeclareStreamDone()
}

// maybeDeclareStreamDone is used to force reset a stream. It should be called with
// the stream lock acquired. It calls stream.onDone which requires the connection lock.
func (s *stream) maybeDeclareStreamDone() {
	if s.isDone() {
		_ = s.SetReadDeadline(time.Now().Add(-1 * time.Hour)) // pion ignores zero times
		// TODO: we should be closing the underlying datachannel, but this resets the stream
		// See https://github.com/libp2p/specs/issues/575 for details.
		// _ = s.dataChannel.Close()
		// TODO: write for the spawned reader to return
		s.onDone()
	}
}

// isDone indicates whether the stream is completed and all the control messages have also been
// flushed. It must be called with the stream lock acquired.
func (s *stream) isDone() bool {
	return (s.sendState == sendStateReset || s.sendState == sendStateDataSent) &&
		(s.receiveState == receiveStateReset || s.receiveState == receiveStateDataRead) &&
		len(s.controlMsgQueue) == 0
}

func (s *stream) setCloseError(e error) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.closeErr = e
}
