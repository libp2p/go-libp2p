package libp2pquic

import (
	"errors"
	"math"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/quic-go/quic-go"
)

const (
	reset quic.StreamErrorCode = 0
)

type stream struct {
	quic.Stream
}

var _ network.MuxedStream = &stream{}

func parseStreamError(err error) error {
	if err == nil {
		return err
	}
	se := &quic.StreamError{}
	if errors.As(err, &se) {
		code := se.ErrorCode
		if code > math.MaxUint32 {
			// TODO(sukunrt): do we need this?
			code = reset
		}
		err = &network.StreamError{
			ErrorCode:      network.StreamErrorCode(code),
			Remote:         se.Remote,
			TransportError: se,
		}
	}
	ae := &quic.ApplicationError{}
	if errors.As(err, &ae) {
		code := ae.ErrorCode
		err = &network.ConnError{
			ErrorCode:      network.ConnErrorCode(code),
			Remote:         ae.Remote,
			TransportError: ae,
		}
	}
	return err
}

func (s *stream) Read(b []byte) (n int, err error) {
	n, err = s.Stream.Read(b)
	return n, parseStreamError(err)
}

func (s *stream) Write(b []byte) (n int, err error) {
	n, err = s.Stream.Write(b)
	return n, parseStreamError(err)
}

func (s *stream) Reset() error {
	s.Stream.CancelRead(reset)
	s.Stream.CancelWrite(reset)
	return nil
}

func (s *stream) ResetWithError(errCode network.StreamErrorCode) error {
	s.Stream.CancelRead(quic.StreamErrorCode(errCode))
	s.Stream.CancelWrite(quic.StreamErrorCode(errCode))
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
