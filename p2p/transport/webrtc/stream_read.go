package libp2pwebrtc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	pb "github.com/libp2p/go-libp2p/p2p/transport/webrtc/pb"
)

// Read from the underlying datachannel. This also
// process sctp control messages such as DCEP, which is
// handled internally by pion, and stream closure which
// is signaled by `Read` on the datachannel returning
// io.EOF.
func (s *webRTCStream) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}

	var (
		readErr error
		read    int
	)
	for read == 0 && readErr == nil {
		if s.isClosed() {
			if s.stateHandler.IsReset() {
				return 0, network.ErrReset
			}
			return 0, io.EOF
		}
		read, readErr = s.readMessage(b)
	}
	if errors.Is(readErr, os.ErrDeadlineExceeded) {
		return read, ErrTimeout
	}
	return read, readErr
}

func (s *webRTCStream) readMessage(b []byte) (int, error) {
	read := copy(b, s.readBuffer)
	s.readBuffer = s.readBuffer[read:]
	remaining := len(s.readBuffer)

	if remaining == 0 && !s.stateHandler.AllowRead() {
		log.Debug("[2] stream has no more data to read")
		if read != 0 {
			return read, nil
		}
		return read, io.EOF
	}

	if read > 0 {
		return read, nil
	}

	// read from datachannel
	var msg pb.Message
	err := s.readMessageFromDataChannel(&msg)
	if err != nil {
		// This case occurs when the remote node goes away
		// without writing a FIN message
		if errors.Is(err, io.EOF) {
			// if the channel was properly closed, return EOF
			if !s.stateHandler.AllowRead() && !s.stateHandler.IsReset() {
				return 0, io.EOF
			}
			return 0, network.ErrReset
		}

		if errors.Is(err, os.ErrDeadlineExceeded) && s.stateHandler.IsReset() {
			return 0, network.ErrReset
		}
		return read, err
	}

	// append incoming data to read readBuffer
	if s.stateHandler.AllowRead() && msg.Message != nil {
		s.readBuffer = append(s.readBuffer, msg.GetMessage()...)
	}

	// process any flags on the message
	if msg.Flag != nil {
		s.processIncomingFlag(msg.GetFlag())
	}

	return read, nil
}

func (s *webRTCStream) readMessageFromDataChannel(msg *pb.Message) error {
	s.readerMux.Lock()
	defer s.readerMux.Unlock()
	return s.reader.ReadMsg(msg)
}

func (s *webRTCStream) SetReadDeadline(t time.Time) error {
	return s.dataChannel.SetReadDeadline(t)
}

func (s *webRTCStream) CloseRead() error {
	if s.isClosed() {
		return nil
	}
	var err error
	s.closeOnce.Do(func() {
		err = s.writeMessageToWriter(&pb.Message{Flag: pb.Message_STOP_SENDING.Enum()})
		if err != nil {
			log.Debug("could not write STOP_SENDING message")
			err = fmt.Errorf("could not close stream for reading: %w", err)
			return
		}
		if s.stateHandler.CloseRead() == stateClosed {
			s.close(false, true)
		}
	})
	return err
}
