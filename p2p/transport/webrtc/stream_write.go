package libp2pwebrtc

import (
	"errors"
	"os"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/pb"
)

var errWriteAfterClose = errors.New("write after close")

// If we have less space than minMessageSize, we don't put a new message on the data channel.
// Instead, we wait until more space opens up.
const minMessageSize = 1 << 10

func (s *stream) Write(b []byte) (int, error) {
	s.mx.Lock()
	defer s.mx.Unlock()

	if s.closeForShutdownErr != nil {
		return 0, s.closeForShutdownErr
	}
	switch s.sendState {
	case sendStateReset:
		return 0, network.ErrReset
	case sendStateDataSent, sendStateDataReceived:
		return 0, errWriteAfterClose
	}

	if !s.writeDeadline.IsZero() && time.Now().After(s.writeDeadline) {
		return 0, os.ErrDeadlineExceeded
	}

	var writeDeadlineTimer *time.Timer
	defer func() {
		if writeDeadlineTimer != nil {
			writeDeadlineTimer.Stop()
		}
	}()

	var n int
	var msg pb.Message
	for len(b) > 0 {
		if s.closeForShutdownErr != nil {
			return n, s.closeForShutdownErr
		}
		switch s.sendState {
		case sendStateReset:
			return n, network.ErrReset
		case sendStateDataSent, sendStateDataReceived:
			return n, errWriteAfterClose
		}

		writeDeadline := s.writeDeadline
		// deadline deleted, stop and remove the timer
		if writeDeadline.IsZero() && writeDeadlineTimer != nil {
			writeDeadlineTimer.Stop()
			writeDeadlineTimer = nil
		}
		var writeDeadlineChan <-chan time.Time
		if !writeDeadline.IsZero() {
			if writeDeadlineTimer == nil {
				writeDeadlineTimer = time.NewTimer(time.Until(writeDeadline))
			} else {
				if !writeDeadlineTimer.Stop() {
					<-writeDeadlineTimer.C
				}
				writeDeadlineTimer.Reset(time.Until(writeDeadline))
			}
			writeDeadlineChan = writeDeadlineTimer.C
		}

		availableSpace := s.availableSendSpace()
		if availableSpace < minMessageSize {
			s.mx.Unlock()
			select {
			case <-writeDeadlineChan:
				s.mx.Lock()
				return n, os.ErrDeadlineExceeded
			case <-s.writeStateChanged:
			}
			s.mx.Lock()
			continue
		}
		end := maxMessageSize
		if end > availableSpace {
			end = availableSpace
		}
		end -= protoOverhead + varintOverhead
		if end > len(b) {
			end = len(b)
		}
		msg = pb.Message{Message: b[:end]}
		if err := s.writer.WriteMsg(&msg); err != nil {
			return n, err
		}
		n += end
		b = b[end:]
	}
	return n, nil
}

func (s *stream) SetWriteDeadline(t time.Time) error {
	s.mx.Lock()
	defer s.mx.Unlock()
	s.writeDeadline = t
	s.notifyWriteStateChanged()
	return nil
}

func (s *stream) availableSendSpace() int {
	buffered := int(s.dataChannel.BufferedAmount())
	availableSpace := maxSendBuffer - buffered
	if availableSpace+maxTotalControlMessagesSize < 0 { // this should never happen, but better check
		log.Errorw("data channel buffered more data than the maximum amount", "max", maxSendBuffer, "buffered", buffered)
	}
	return availableSpace
}

func (s *stream) cancelWrite() error {
	s.mx.Lock()
	defer s.mx.Unlock()

	// There's no need to reset the write half if the write half has been closed
	// successfully or has been reset previously
	if s.sendState == sendStateDataReceived || s.sendState == sendStateReset {
		return nil
	}
	s.sendState = sendStateReset
	// Remove reference to this stream from data channel
	s.dataChannel.OnBufferedAmountLow(nil)
	s.notifyWriteStateChanged()
	return s.writer.WriteMsg(&pb.Message{Flag: pb.Message_RESET.Enum()})
}

func (s *stream) CloseWrite() error {
	s.mx.Lock()
	defer s.mx.Unlock()

	if s.sendState != sendStateSending {
		return nil
	}
	s.sendState = sendStateDataSent
	// Remove reference to this stream from data channel
	s.dataChannel.OnBufferedAmountLow(nil)
	s.notifyWriteStateChanged()
	return s.writer.WriteMsg(&pb.Message{Flag: pb.Message_FIN.Enum()})
}

func (s *stream) notifyWriteStateChanged() {
	select {
	case s.writeStateChanged <- struct{}{}:
	default:
	}
}
