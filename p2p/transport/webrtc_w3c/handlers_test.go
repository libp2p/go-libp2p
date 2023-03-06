package webrtc_w3c

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/require"
)

var _ network.Stream = &stream{}

type stream struct {
	r io.Reader
	w io.Writer
}

// Read implements network.Stream
func (s *stream) Read(p []byte) (n int, err error) {
	return s.r.Read(p)
}

// Write implements network.Stream
func (s *stream) Write(p []byte) (n int, err error) {
	return s.w.Write(p)
}

// Close implements network.Stream
func (s *stream) Close() error {
	return nil
}

// CloseRead implements network.Stream
func (*stream) CloseRead() error {
	return nil
}

// CloseWrite implements network.Stream
func (*stream) CloseWrite() error {
	return nil
}

// Reset implements network.Stream
func (*stream) Reset() error {
	return nil
}

// SetDeadline implements network.Stream
func (*stream) SetDeadline(time.Time) error {
	return nil
}

// SetReadDeadline implements network.Stream
func (*stream) SetReadDeadline(time.Time) error {
	return nil
}

// SetWriteDeadline implements network.Stream
func (*stream) SetWriteDeadline(time.Time) error {
	return nil
}

// Conn implements network.Stream
func (*stream) Conn() network.Conn {
	return nil
}

// ID implements network.Stream
func (*stream) ID() string {
	return ""
}

// Protocol implements network.Stream
func (*stream) Protocol() protocol.ID {
	return ""
}

// Scope implements network.Stream
func (*stream) Scope() network.StreamScope {
	panic("unimplemented")
}

// SetProtocol implements network.Stream
func (*stream) SetProtocol(id protocol.ID) error {
	panic("unimplemented")
}

// Stat implements network.Stream
func (*stream) Stat() network.Stats {
	panic("unimplemented")
}

func makeStreamPair() (network.Stream, network.Stream) {
	ra, wb := io.Pipe()
	rb, wa := io.Pipe()
	return &stream{ra, wa}, &stream{rb, wb}
}

func TestWebrtcW3Handlers_ShouldConnect(t *testing.T) {
	sa, sb := makeStreamPair()
	errC := make(chan error)
	go func() {
		defer close(errC)
		_, err := handleIncoming(context.Background(), webrtc.Configuration{}, sa)
		if err != nil {
			errC <- err
		}
	}()

	_, err := connect(context.Background(), webrtc.Configuration{}, sb)
	require.NoError(t, err)
	require.NoError(t, <-errC)
}
