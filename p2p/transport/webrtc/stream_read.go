package libp2pwebrtc

import (
	"errors"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/pb"
)

func (s *stream) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}

	s.mx.Lock()
	defer s.mx.Unlock()

	if s.closeErr != nil {
		return 0, s.closeErr
	}
	switch s.receiveState {
	case receiveStateDataRead:
		return 0, io.EOF
	case receiveStateReset:
		return 0, network.ErrReset
	}

	var read int
	for {
		if s.nextMessage == nil {
			// load the next message
			s.mx.Unlock()
			var msg pb.Message
			if err := s.reader.ReadMsg(&msg); err != nil {
				s.mx.Lock()
				if err == io.EOF {
					// if the channel was properly closed, return EOF
					if s.receiveState == receiveStateDataRead {
						return 0, io.EOF
					}
					// This case occurs when the remote node closes the stream without writing a FIN message
					// There's little we can do here
					return 0, errors.New("didn't receive final state for stream")
				}
				if s.receiveState == receiveStateReset {
					return 0, network.ErrReset
				}
				return 0, err
			}
			s.mx.Lock()
			s.nextMessage = &msg
		}

		if len(s.nextMessage.Message) > 0 {
			n := copy(b, s.nextMessage.Message)
			read += n
			s.nextMessage.Message = s.nextMessage.Message[n:]
			return read, nil
		}

		// process flags on the message after reading all the data
		s.processIncomingFlag(s.nextMessage.Flag)
		s.nextMessage = nil
		if s.closeErr != nil {
			return read, s.closeErr
		}
		switch s.receiveState {
		case receiveStateDataRead:
			return read, io.EOF
		case receiveStateReset:
			s.dataChannel.SetReadDeadline(time.Time{})
			return read, network.ErrReset
		}
	}
}

func (s *stream) SetReadDeadline(t time.Time) error { return s.dataChannel.SetReadDeadline(t) }

func (s *stream) CloseRead() error {
	s.mx.Lock()
	defer s.mx.Unlock()

	if s.nextMessage != nil {
		s.processIncomingFlag(s.nextMessage.Flag)
		s.nextMessage = nil
	}
	var err error
	if s.receiveState == receiveStateReceiving && s.closeErr == nil {
		err = s.sendControlMessage(&pb.Message{Flag: pb.Message_STOP_SENDING.Enum()})
	}
	s.receiveState = receiveStateReset
	s.maybeDeclareStreamDone()

	// make any calls to Read blocking on ReadMsg return immediately
	s.dataChannel.SetReadDeadline(time.Now())

	return err
}
