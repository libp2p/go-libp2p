package libp2pwebrtc

import (
	"errors"
	"io"
	"os"
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
	// maxTotalControlMessagesSize is the maximum total size of all control messages we will
	// write on this stream.
	// 4 control messages of size 10 bytes + 10 bytes buffer. This number doesn't need to be
	// exact. In the worst case, we enqueue these many bytes more in the webrtc peer connection
	// send queue.
	maxTotalControlMessagesSize = 50
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
	// maxRTT is an estimate of maximum RTT
	// We use this to wait for FIN_ACK and Data Channel Close messages from the peer
	maxRTT = 10 * time.Second
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
	sendStateDataReceived
	sendStateReset
)

// Package pion detached data channel into a net.Conn
// and then a network.MuxedStream
type stream struct {
	mx sync.Mutex

	// readerMx ensures that only a single goroutine reads from the reader. Read is not threadsafe
	// But we may need to read from reader for control messages from a different goroutine.
	readerMx sync.Mutex
	reader   pbio.Reader

	// this buffer is limited up to a single message. Reason we need it
	// is because a reader might read a message midway, and so we need a
	// wait to buffer that for as long as the remaining part is not (yet) read
	nextMessage  *pb.Message
	receiveState receiveState

	writer            pbio.Writer // concurrent writes prevented by mx
	writeStateChanged chan struct{}
	sendState         sendState
	writeDeadline     time.Time

	controlMessageReaderOnce sync.Once

	onCloseOnce         sync.Once
	onClose             func()
	onDataChannelClose  func(remoteClosed bool)
	id                  uint16 // for logging purposes
	dataChannel         *datachannel.DataChannel
	closeForShutdownErr error
	isClosed            bool
	rtt                 time.Duration
}

var _ network.MuxedStream = &stream{}

func newStream(
	channel *webrtc.DataChannel,
	rwc datachannel.ReadWriteCloser,
	rtt time.Duration,
	onClose func(),
	onDataChannelClose func(remoteClosed bool),
) *stream {
	s := &stream{
		reader:             pbio.NewDelimitedReader(rwc, maxMessageSize),
		writer:             pbio.NewDelimitedWriter(rwc),
		writeStateChanged:  make(chan struct{}, 1),
		id:                 *channel.ID(),
		dataChannel:        rwc.(*datachannel.DataChannel),
		onClose:            onClose,
		onDataChannelClose: onDataChannelClose,
		rtt:                rtt,
	}
	s.dataChannel.SetBufferedAmountLowThreshold(bufferedAmountLowThreshold)
	s.dataChannel.OnBufferedAmountLow(func() {
		s.notifyWriteStateChanged()

	})
	return s
}

func (s *stream) Close() error {
	defer s.signalClose()
	s.mx.Lock()
	if s.closeForShutdownErr != nil || s.isClosed {
		s.mx.Unlock()
		return nil
	}
	s.isClosed = true
	closeWriteErr := s.closeWriteUnlocked()
	closeReadErr := s.closeReadUnlocked()
	s.setDataChannelReadDeadline(time.Now().Add(-1 * time.Hour))
	s.mx.Unlock()

	if closeWriteErr != nil || closeReadErr != nil {
		s.Reset()
		return errors.Join(closeWriteErr, closeReadErr)
	}
	return nil
}

func (s *stream) Reset() error {
	defer s.signalClose()
	s.mx.Lock()
	defer s.mx.Unlock()
	if s.closeForShutdownErr != nil {
		return nil
	}
	// reset even if it's closed already
	s.isClosed = true
	cancelWriteErr := s.cancelWriteUnlocked()
	closeReadErr := s.closeReadUnlocked()
	s.setDataChannelReadDeadline(time.Now().Add(-1 * time.Hour))
	return errors.Join(cancelWriteErr, closeReadErr)
}

func (s *stream) closeForShutdown(closeErr error) {
	defer s.signalClose()
	s.mx.Lock()
	defer s.mx.Unlock()
	s.closeForShutdownErr = closeErr
	s.isClosed = true
	s.notifyWriteStateChanged()
}

func (s *stream) SetDeadline(t time.Time) error {
	_ = s.SetReadDeadline(t)
	return s.SetWriteDeadline(t)
}

// processIncomingFlag process the flag on an incoming message
// It needs to be called while the mutex is locked.
func (s *stream) processIncomingFlag(flag *pb.Message_Flag) {
	if flag == nil {
		return
	}

	switch *flag {
	case pb.Message_STOP_SENDING:
		// We must process STOP_SENDING after sending a FIN(sendStateDataSent). Remote peer
		// may not send a FIN_ACK once it has sent a STOP_SENDING
		if s.sendState == sendStateSending || s.sendState == sendStateDataSent {
			s.sendState = sendStateReset
		}
		s.notifyWriteStateChanged()
	case pb.Message_FIN_ACK:
		s.sendState = sendStateDataReceived
		s.notifyWriteStateChanged()
	case pb.Message_FIN:
		if s.receiveState == receiveStateReceiving {
			s.receiveState = receiveStateDataRead
		}
		if err := s.writer.WriteMsg(&pb.Message{Flag: pb.Message_FIN_ACK.Enum()}); err != nil {
			log.Debugf("failed to send FIN_ACK: %s", err)
			// Remote has finished writing all the data It'll stop waiting for the
			// FIN_ACK eventually or will be notified when we close the datachannel
		}
		s.spawnControlMessageReader()
	case pb.Message_RESET:
		if s.receiveState == receiveStateReceiving {
			s.receiveState = receiveStateReset
		}
		s.spawnControlMessageReader()
	}
}

// spawnControlMessageReader is used for processing control messages after the reader is closed.
// It is also responsible for closing the datachannel once the stream is closed
func (s *stream) spawnControlMessageReader() {
	s.controlMessageReaderOnce.Do(func() {
		// Spawn a goroutine to ensure that we're not holding any locks
		go func() {
			// cleanup the sctp deadline timer goroutine
			defer s.setDataChannelReadDeadline(time.Time{})

			// Unblock any Read call waiting on reader.ReadMsg
			s.setDataChannelReadDeadline(time.Now().Add(-1 * time.Hour))

			s.readerMx.Lock()
			// We have the lock any readers blocked on reader.ReadMsg have exited.
			// From this point onwards only this goroutine will do reader.ReadMsg.

			// released after write half is closed
			s.mx.Lock()

			// Read calls after lock release will exit immediately on checking
			// s.readState
			s.readerMx.Unlock()

			if s.nextMessage != nil {
				s.processIncomingFlag(s.nextMessage.Flag)
				s.nextMessage = nil
			}

			var endTime time.Time
			var msg pb.Message
			for {
				// connection closed
				if s.closeForShutdownErr != nil {
					break
				}
				// write half completed
				if s.sendState == sendStateDataReceived || s.sendState == sendStateReset {
					break
				}
				// deadline exceeded
				if !endTime.IsZero() && time.Now().After(endTime) {
					break
				}

				// The stream is closed. Wait for 1RTT before erroring
				if s.isClosed && endTime.IsZero() {
					endTime = time.Now().Add(s.rtt)
				}
				s.setDataChannelReadDeadline(endTime)
				s.mx.Unlock()
				err := s.reader.ReadMsg(&msg)
				s.mx.Lock()
				if err != nil {
					// We have to manually manage deadline exceeded errors since pion/sctp can
					// return deadline exceeded error for cancelled deadlines
					// see: https://github.com/pion/sctp/pull/290/files
					if errors.Is(err, os.ErrDeadlineExceeded) {
						continue
					}
					break
				}
				s.processIncomingFlag(msg.Flag)
			}

			s.mx.Unlock()
			remoteClosed := s.closeDataChannel()
			if s.onDataChannelClose != nil {
				s.onDataChannelClose(remoteClosed)
			}
		}()
	})
}

// closeDataChannel closes the datachannel and waits for 1rtt for remote to close the datachannel
func (s *stream) closeDataChannel() bool {
	s.dataChannel.Close()
	endTime := time.Now().Add(s.rtt)
	var msg pb.Message
	for {
		if time.Now().After(endTime) {
			return false
		}
		s.setDataChannelReadDeadline(endTime)
		err := s.reader.ReadMsg(&msg)
		if err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
			continue
		}
		return err == io.EOF
	}
}

func (s *stream) signalClose() {
	s.onCloseOnce.Do(func() {
		if s.onClose != nil {
			s.onClose()
		}
	})
}
