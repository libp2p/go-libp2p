package libp2pwebrtc

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	pb "github.com/libp2p/go-libp2p/p2p/transport/webrtc/pb"
	"google.golang.org/protobuf/proto"
)

func (s *webRTCStream) Write(b []byte) (int, error) {
	if !s.stateHandler.AllowWrite() {
		return 0, io.ErrClosedPipe
	}

	// Check if there is any message on the wire. This is used for control
	// messages only when the read side of the stream is closed
	if s.stateHandler.State() == stateReadClosed {
		s.readLoopOnce.Do(s.spawnControlMessageReader)
	}

	const chunkSize = maxMessageSize - protoOverhead - varintOverhead

	var n int

	for len(b) > 0 {
		end := len(b)
		if chunkSize < end {
			end = chunkSize
		}

		err := s.writeMessage(&pb.Message{Message: b[:end]})
		n += end
		b = b[end:]
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				err = ErrTimeout
			}
			return n, err
		}
	}

	return n, nil
}

// used for reading control messages while writing, in case the reader is closed,
// as to ensure we do still get control messages. This is important as according to the spec
// our data and control channels are intermixed on the same conn.
func (s *webRTCStream) spawnControlMessageReader() {
	go func() {
		// zero the read deadline, so read call only returns
		// when the underlying datachannel closes or there is
		// a message on the channel
		s.dataChannel.SetReadDeadline(time.Time{})
		var msg pb.Message
		for {
			if s.stateHandler.Closed() {
				return
			}
			err := s.readMessageFromDataChannel(&msg)
			if err != nil {
				if errors.Is(err, io.EOF) {
					s.close(true, true)
				}
				return
			}
			if msg.Flag != nil {
				state, reset := s.stateHandler.HandleInboundFlag(msg.GetFlag())
				if state == stateClosed {
					log.Debug("closing: after handle inbound flag")
					s.close(reset, true)
				}
			}
		}
	}()
}

func (s *webRTCStream) writeMessage(msg *pb.Message) error {
	var writeDeadlineTimer *time.Timer
	defer func() {
		if writeDeadlineTimer != nil {
			writeDeadlineTimer.Stop()
		}
	}()

	for {
		if !s.stateHandler.AllowWrite() {
			return io.ErrClosedPipe
		}

		writeDeadline, hasWriteDeadline := s.getWriteDeadline()
		if !hasWriteDeadline {
			// writeDeadline = time.Unix(999999999999999999, 0)
			// Does this cause the timer to overflow ? https://cs.opensource.google/go/go/+/master:src/time/sleep.go;l=32?q=runtimeTimer&ss=go%2Fgo
			// this could be causing an overflow above https://cs.opensource.google/go/go/+/master:src/time/time.go;l=1123?q=unixTim&ss=go%2Fgo
			// Go adds 62135596800 to the seconds parameter of `time.Unix`
			writeDeadline = time.Unix(math.MaxInt64-62135596801, 0)
		}
		if writeDeadlineTimer == nil {
			writeDeadlineTimer = time.NewTimer(time.Until(writeDeadline))
		} else {
			if !writeDeadlineTimer.Stop() {
				<-writeDeadlineTimer.C
			}
			writeDeadlineTimer.Reset(time.Until(writeDeadline))
		}

		bufferedAmount := int(s.dataChannel.BufferedAmount())
		addedBuffer := bufferedAmount + varintOverhead + proto.Size(msg)
		if addedBuffer > maxBufferedAmount {
			select {
			case <-writeDeadlineTimer.C:
				return os.ErrDeadlineExceeded
			case <-s.writeAvailable:
				return s.writeMessageToWriter(msg)
			case <-s.ctx.Done():
				if s.stateHandler.IsReset() {
					return network.ErrReset
				}
				return io.ErrClosedPipe
			case <-s.writerDeadlineUpdated:
			}
		} else {
			return s.writeMessageToWriter(msg)
		}
	}
}

func (s *webRTCStream) writeMessageToWriter(msg *pb.Message) error {
	s.writerMux.Lock()
	defer s.writerMux.Unlock()
	return s.writer.WriteMsg(msg)
}

func (s *webRTCStream) SetWriteDeadline(t time.Time) error {
	s.writerDeadlineMux.Lock()
	defer s.writerDeadlineMux.Unlock()
	s.writerDeadline = t
	select {
	case s.writerDeadlineUpdated <- struct{}{}:
	default:
	}
	return nil
}

func (s *webRTCStream) getWriteDeadline() (time.Time, bool) {
	s.writerDeadlineMux.Lock()
	defer s.writerDeadlineMux.Unlock()
	return s.writerDeadline, !s.writerDeadline.IsZero()
}

func (s *webRTCStream) CloseWrite() error {
	if s.isClosed() {
		return nil
	}
	var err error
	s.closeOnce.Do(func() {
		err = s.writeMessage(&pb.Message{Flag: pb.Message_FIN.Enum()})
		if err != nil {
			log.Debug("could not write FIN message")
			err = fmt.Errorf("close stream for writing: %w", err)
			return
		}
		// if successfully written, process the outgoing flag
		state := s.stateHandler.CloseWrite()
		// unblock and fail any ongoing writes
		select {
		case s.writeAvailable <- struct{}{}:
		default:
		}
		// check if closure required
		if state == stateClosed {
			s.close(false, true)
		}
	})
	return err
}
