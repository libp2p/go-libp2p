package tcpreuse

import (
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse/internal/sampledconn"
	manet "github.com/multiformats/go-multiaddr/net"
)

// This is reading the first 3 bytes of the first packet after the handshake.
// It's set to the default TCP connect timeout in the TCP Transport.
//
// A var so we can change it in tests.
var identifyConnTimeout = 5 * time.Second

type DemultiplexedConnType int

const (
	DemultiplexedConnType_Unknown DemultiplexedConnType = iota
	DemultiplexedConnType_MultistreamSelect
	DemultiplexedConnType_HTTP
	DemultiplexedConnType_TLS
)

func (t DemultiplexedConnType) String() string {
	switch t {
	case DemultiplexedConnType_MultistreamSelect:
		return "MultistreamSelect"
	case DemultiplexedConnType_HTTP:
		return "HTTP"
	case DemultiplexedConnType_TLS:
		return "TLS"
	default:
		return fmt.Sprintf("Unknown(%d)", int(t))
	}
}

func (t DemultiplexedConnType) IsKnown() bool {
	return t >= 1 || t <= 3
}

// identifyConnType attempts to identify the connection type by peeking at the
// first few bytes.
// Its Callers must not use the passed in Conn after this function returns.
// If an error is returned, the connection will be closed.
func identifyConnType(c manet.Conn) (DemultiplexedConnType, manet.Conn, error) {
	if err := c.SetReadDeadline(time.Now().Add(identifyConnTimeout)); err != nil {
		closeErr := c.Close()
		return 0, nil, errors.Join(err, closeErr)
	}

	s, peekedConn, err := sampledconn.PeekBytes(c)
	if err != nil {
		closeErr := c.Close()
		return 0, nil, errors.Join(err, closeErr)
	}

	if err := peekedConn.SetReadDeadline(time.Time{}); err != nil {
		closeErr := peekedConn.Close()
		return 0, nil, errors.Join(err, closeErr)
	}

	if IsMultistreamSelect(s) {
		return DemultiplexedConnType_MultistreamSelect, peekedConn, nil
	}
	if IsTLS(s) {
		return DemultiplexedConnType_TLS, peekedConn, nil
	}
	if IsHTTP(s) {
		return DemultiplexedConnType_HTTP, peekedConn, nil
	}
	return DemultiplexedConnType_Unknown, peekedConn, nil
}

// Matchers are implemented here instead of in the transports so we can easily fuzz them together.
type Prefix = [3]byte

func IsMultistreamSelect(s Prefix) bool {
	return string(s[:]) == "\x13/m"
}

func IsHTTP(s Prefix) bool {
	switch string(s[:]) {
	case "GET", "HEA", "POS", "PUT", "DEL", "CON", "OPT", "TRA", "PAT":
		return true
	default:
		return false
	}
}

func IsTLS(s Prefix) bool {
	switch string(s[:]) {
	case "\x16\x03\x01", "\x16\x03\x02", "\x16\x03\x03":
		return true
	default:
		return false
	}
}
