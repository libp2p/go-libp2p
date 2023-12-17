package libp2pwebrtc

import (
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/pb"
)

func (s *stream) Read(b []byte) (int, error) {
	s.readerSem <- struct{}{}
	defer func() { <-s.readerSem }()

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

	if len(b) == 0 {
		return 0, nil
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
					// This case occurs when remote closes the datachannel without writing a FIN
					// message. Some implementations discard the buffered data on closing the
					// datachannel. For these implementations a stream reset will be observed as an
					// abrupt closing of the datachannel.
					s.receiveState = receiveStateReset
					return 0, network.ErrReset
				}
				if s.receiveState == receiveStateReset {
					return 0, network.ErrReset
				}
				if s.receiveState == receiveStateDataRead {
					return 0, io.EOF
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
			return read, network.ErrReset
		}
	}
}

func (s *stream) SetReadDeadline(t time.Time) error { return s.dataChannel.SetReadDeadline(t) }

func (s *stream) CloseRead() error {
	s.mx.Lock()
	defer s.mx.Unlock()
	var err error
	if s.receiveState == receiveStateReceiving && s.closeErr == nil {
		err = s.writer.WriteMsg(&pb.Message{Flag: pb.Message_STOP_SENDING.Enum()})
		s.receiveState = receiveStateReset
	}
	s.controlMessageReaderOnce.Do(s.spawnControlMessageReader)
	return err
}
