package libp2pwebrtc

import (
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/pb"
)

func (s *stream) Read(b []byte) (int, error) {
	s.readerMx.Lock()
	defer s.readerMx.Unlock()

	s.mx.Lock()
	defer s.mx.Unlock()

	if s.closeForShutdownErr != nil {
		return 0, s.closeForShutdownErr
	}
	switch s.receiveState {
	case receiveStateDataRead:
		return 0, io.EOF
	case receiveStateReset:
		return 0, s.readError
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
			err := s.reader.ReadMsg(&msg)
			s.mx.Lock()
			if err != nil {
				// connection was closed
				if s.closeForShutdownErr != nil {
					return 0, s.closeForShutdownErr
				}
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
					s.readError = &network.StreamError{Remote: true}
					return 0, s.readError
				}
				if s.receiveState == receiveStateReset {
					return 0, s.readError
				}
				if s.receiveState == receiveStateDataRead {
					return 0, io.EOF
				}
				return 0, err
			}
			s.nextMessage = &msg
		}

		if len(s.nextMessage.Message) > 0 {
			n := copy(b, s.nextMessage.Message)
			read += n
			s.nextMessage.Message = s.nextMessage.Message[n:]
			return read, nil
		}

		// process flags on the message after reading all the data
		s.processIncomingFlag(s.nextMessage)
		s.nextMessage = nil
		if s.closeForShutdownErr != nil {
			return read, s.closeForShutdownErr
		}
		switch s.receiveState {
		case receiveStateDataRead:
			return read, io.EOF
		case receiveStateReset:
			return read, s.readError
		}
	}
}

func (s *stream) SetReadDeadline(t time.Time) error {
	s.mx.Lock()
	defer s.mx.Unlock()
	if s.receiveState == receiveStateReceiving {
		s.setDataChannelReadDeadline(t)
	}
	return nil
}

func (s *stream) setDataChannelReadDeadline(t time.Time) error {
	return s.dataChannel.SetReadDeadline(t)
}

func (s *stream) CloseRead() error {
	return s.closeRead(0, false)
}

func (s *stream) closeRead(errCode network.StreamErrorCode, remote bool) error {
	s.mx.Lock()
	defer s.mx.Unlock()
	var err error
	if s.receiveState == receiveStateReceiving && s.closeForShutdownErr == nil {
		code := uint32(errCode)
		err = s.writer.WriteMsg(&pb.Message{Flag: pb.Message_STOP_SENDING.Enum(), ErrorCode: &code})
		s.receiveState = receiveStateReset
		s.readError = &network.StreamError{Remote: remote, ErrorCode: errCode}
	}
	s.spawnControlMessageReader()
	return err
}
