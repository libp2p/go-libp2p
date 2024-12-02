package quicreuse

import (
	"time"

	"github.com/quic-go/quic-go"
)

var quicConfig = &quic.Config{
	MaxIncomingStreams:         256,
	MaxIncomingUniStreams:      5,              // allow some unidirectional streams, in case we speak WebTransport
	MaxStreamReceiveWindow:     10 * (1 << 20), // 10 MB
	MaxConnectionReceiveWindow: 15 * (1 << 20), // 15 MB
	KeepAlivePeriod:            15 * time.Second,
	Versions:                   []quic.Version{quic.Version1},
	// We don't use datagrams (yet), but this is necessary for WebTransport
	EnableDatagrams: true,
}
