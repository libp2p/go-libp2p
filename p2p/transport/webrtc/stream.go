package libp2pwebrtc

import (
	"errors"
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
	// maxFINACKWait is the maximum amount of time a stream will wait to read
	// FIN_ACK before closing the data channel
	maxFINACKWait = 10 * time.Second
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
	// controlMessageReaderEndTime is the end time for reading FIN_ACK from the control
	// message reader. We cannot rely on SetReadDeadline to do this since that is prone to
	// race condition where a previous deadline timer fires after the latest call to
	// SetReadDeadline
	// See: https://github.com/pion/sctp/pull/290
	controlMessageReaderEndTime time.Time
	controlMessageReaderDone    sync.WaitGroup

	onDone              func()
	id                  uint16 // for logging purposes
	dataChannel         *datachannel.DataChannel
	closeForShutdownErr error
}

var _ network.MuxedStream = &stream{}

func newStream(
	channel *webrtc.DataChannel,
	rwc datachannel.ReadWriteCloser,
	onDone func(),
) *stream {
	s := &stream{
		reader:            pbio.NewDelimitedReader(rwc, maxMessageSize),
		writer:            pbio.NewDelimitedWriter(rwc),
		writeStateChanged: make(chan struct{}, 1),
		id:                *channel.ID(),
		dataChannel:       rwc.(*datachannel.DataChannel),
		onDone:            onDone,
	}
	// released when the controlMessageReader goroutine exits
	s.controlMessageReaderDone.Add(1)
	s.dataChannel.SetBufferedAmountLowThreshold(bufferedAmountLowThreshold)
	s.dataChannel.OnBufferedAmountLow(func() {
		s.notifyWriteStateChanged()

	})
	return s
}

func (s *stream) Close() error {
	s.mx.Lock()
	isClosed := s.closeForShutdownErr != nil
	s.mx.Unlock()
	if isClosed {
		return nil
	}

	closeWriteErr := s.CloseWrite()
	closeReadErr := s.CloseRead()
	if closeWriteErr != nil || closeReadErr != nil {
		s.Reset()
		return errors.Join(closeWriteErr, closeReadErr)
	}

	s.mx.Lock()
	if s.controlMessageReaderEndTime.IsZero() {
		s.controlMessageReaderEndTime = time.Now().Add(maxFINACKWait)
		s.setDataChannelReadDeadline(time.Now().Add(-1 * time.Hour))
		go func() {
			s.controlMessageReaderDone.Wait()
			s.cleanup()
		}()
	}
	s.mx.Unlock()
	return nil
}

func (s *stream) Reset() error {
	s.mx.Lock()
	isClosed := s.closeForShutdownErr != nil
	s.mx.Unlock()
	if isClosed {
		return nil
	}

	defer s.cleanup()
	cancelWriteErr := s.cancelWrite()
	closeReadErr := s.CloseRead()
	s.setDataChannelReadDeadline(time.Now().Add(-1 * time.Hour))
	return errors.Join(closeReadErr, cancelWriteErr)
}

func (s *stream) closeForShutdown(closeErr error) {
	defer s.cleanup()

	s.mx.Lock()
	defer s.mx.Unlock()

	s.closeForShutdownErr = closeErr
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
func (s *stream) spawnControlMessageReader() {
	s.controlMessageReaderOnce.Do(func() {
		// Spawn a goroutine to ensure that we're not holding any locks
		go func() {
			defer s.controlMessageReaderDone.Done()
			// cleanup the sctp deadline timer goroutine
			defer s.setDataChannelReadDeadline(time.Time{})

			setDeadline := func() bool {
				if s.controlMessageReaderEndTime.IsZero() || time.Now().Before(s.controlMessageReaderEndTime) {
					s.setDataChannelReadDeadline(s.controlMessageReaderEndTime)
					return true
				}
				return false
			}

			// Unblock any Read call waiting on reader.ReadMsg
			s.setDataChannelReadDeadline(time.Now().Add(-1 * time.Hour))

			s.readerMx.Lock()
			// We have the lock: any readers blocked on reader.ReadMsg have exited.
			s.mx.Lock()
			defer s.mx.Unlock()
			// From this point onwards only this goroutine will do reader.ReadMsg.
			// We just wanted to ensure any exising readers have exited.
			// Read calls from this point onwards will exit immediately on checking
			// s.readState
			s.readerMx.Unlock()

			if s.nextMessage != nil {
				s.processIncomingFlag(s.nextMessage.Flag)
				s.nextMessage = nil
			}
			for s.closeForShutdownErr == nil &&
				s.sendState != sendStateDataReceived && s.sendState != sendStateReset {
				var msg pb.Message
				if !setDeadline() {
					return
				}
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
					return
				}
				s.processIncomingFlag(msg.Flag)
			}
		}()
	})
}

func (s *stream) cleanup() {
	// Even if we close the datachannel pion keeps a reference to the datachannel around.
	// Remove the onBufferedAmountLow callback to ensure that we at least garbage collect
	// memory we allocated for this stream.
	s.dataChannel.OnBufferedAmountLow(nil)
	s.dataChannel.Close()
	if s.onDone != nil {
		s.onDone()
	}
}
