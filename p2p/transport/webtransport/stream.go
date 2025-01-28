package libp2pwebtransport

import (
	"errors"
	"net"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/quic-go/webtransport-go"
)

const (
	reset webtransport.StreamErrorCode = 0
)

type webtransportStream struct {
	webtransport.Stream
	wsess *webtransport.Session
}

var _ net.Conn = &webtransportStream{}

func (s *webtransportStream) LocalAddr() net.Addr {
	return s.wsess.LocalAddr()
}

func (s *webtransportStream) RemoteAddr() net.Addr {
	return s.wsess.RemoteAddr()
}

type stream struct {
	webtransport.Stream
}

var _ network.MuxedStream = &stream{}

func (s *stream) Read(b []byte) (n int, err error) {
	n, err = s.Stream.Read(b)
	if err != nil && errors.Is(err, &webtransport.StreamError{}) {
		err = network.ErrReset
	}
	return n, err
}

func (s *stream) Write(b []byte) (n int, err error) {
	n, err = s.Stream.Write(b)
	if err != nil && errors.Is(err, &webtransport.StreamError{}) {
		err = network.ErrReset
	}
	return n, err
}

func (s *stream) Reset() error {
	s.Stream.CancelRead(reset)
	s.Stream.CancelWrite(reset)
	return nil
}

// ResetWithError resets the stream ignoring the error code. Error codes aren't
// specified for WebTransport as the current implementation of WebTransport in
// browsers(https://www.ietf.org/archive/id/draft-kinnear-webtransport-http2-02.html)
// only supports 1 byte error codes. For more details, see
// https://github.com/libp2p/specs/blob/4eca305185c7aef219e936bef76c48b1ab0a8b43/error-codes/README.md?plain=1#L84
func (s *stream) ResetWithError(_ network.StreamErrorCode) error {
	s.Stream.CancelRead(reset)
	s.Stream.CancelWrite(reset)
	return nil
}

func (s *stream) Close() error {
	s.Stream.CancelRead(reset)
	return s.Stream.Close()
}

func (s *stream) CloseRead() error {
	s.Stream.CancelRead(reset)
	return nil
}

func (s *stream) CloseWrite() error {
	return s.Stream.Close()
}
