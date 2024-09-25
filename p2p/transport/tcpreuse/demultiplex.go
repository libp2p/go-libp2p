package tcpreuse

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

type peekAble interface {
	// Peek returns the next n bytes without advancing the reader. The bytes stop
	// being valid at the next read call. If Peek returns fewer than n bytes, it
	// also returns an error explaining why the read is short. The error is
	// [ErrBufferFull] if n is larger than b's buffer size.
	Peek(n int) ([]byte, error)
}

var _ peekAble = (*bufio.Reader)(nil)

type DemultiplexedConnType int

const (
	Unknown DemultiplexedConnType = iota
	MultistreamSelect
	HTTP
	TLS
)

func (t DemultiplexedConnType) String() string {
	switch t {
	case MultistreamSelect:
		return "MultistreamSelect"
	case HTTP:
		return "HTTP"
	case TLS:
		return "TLS"
	default:
		return fmt.Sprintf("Unknown(%d)", int(t))
	}
}

func (t DemultiplexedConnType) IsKnown() bool {
	return t >= 1 || t <= 3
}

func ConnTypeFromConn(c net.Conn) (DemultiplexedConnType, manet.Conn, error) {
	if err := c.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		closeErr := c.Close()
		return 0, nil, errors.Join(err, closeErr)
	}

	s, sc, err := ReadSampleFromConn(c)
	if err != nil {
		closeErr := c.Close()
		return 0, nil, errors.Join(err, closeErr)
	}

	if err := c.SetReadDeadline(time.Time{}); err != nil {
		closeErr := c.Close()
		return 0, nil, errors.Join(err, closeErr)
	}

	if IsMultistreamSelect(s) {
		return MultistreamSelect, sc, nil
	}
	if IsTLS(s) {
		return TLS, sc, nil
	}
	if IsHTTP(s) {
		return HTTP, sc, nil
	}
	return Unknown, sc, nil
}

// ReadSampleFromConn read the sample and returns a reader which still include the sample, so it can be kept undamaged.
// If an error occurs it only return the error.
func ReadSampleFromConn(c net.Conn) (Sample, manet.Conn, error) {
	if peekAble, ok := c.(peekAble); ok {
		b, err := peekAble.Peek(len(Sample{}))
		switch {
		case err == nil:
			mac, err := manet.WrapNetConn(c)
			if err != nil {
				return Sample{}, nil, err
			}

			return Sample(b), mac, nil
		case errors.Is(err, bufio.ErrBufferFull):
			// fallback to sampledConn
		default:
			return Sample{}, nil, err
		}
	}

	tcpConnLike, ok := c.(tcpConnInterface)
	if !ok {
		return Sample{}, nil, fmt.Errorf("expected tcp-like connection")
	}

	laddr, err := manet.FromNetAddr(c.LocalAddr())
	if err != nil {
		return Sample{}, nil, fmt.Errorf("failed to convert nconn.LocalAddr: %s", err)
	}

	raddr, err := manet.FromNetAddr(c.RemoteAddr())
	if err != nil {
		return Sample{}, nil, fmt.Errorf("failed to convert nconn.RemoteAddr: %s", err)
	}

	sc := &sampledConn{tcpConnInterface: tcpConnLike, maEndpoints: maEndpoints{laddr: laddr, raddr: raddr}}
	_, err = io.ReadFull(c, sc.s[:])
	if err != nil {
		return Sample{}, nil, err
	}

	return sc.s, sc, nil
}

// Try out best to mimic a TCPConn's functions
// Note: Skipping `SyscallConn() (syscall.RawConn, error)` since it can be misused given we've read a few bytes from the connection
// If this is an issue here we can revisit the options.
type tcpConnInterface interface {
	net.Conn

	CloseRead() error
	CloseWrite() error

	SetLinger(sec int) error
	SetKeepAlive(keepalive bool) error
	SetKeepAlivePeriod(d time.Duration) error
	SetNoDelay(noDelay bool) error
	MultipathTCP() (bool, error)

	io.ReaderFrom
	io.WriterTo
}

type maEndpoints struct {
	laddr ma.Multiaddr
	raddr ma.Multiaddr
}

// LocalMultiaddr returns the local address associated with
// this connection
func (c *maEndpoints) LocalMultiaddr() ma.Multiaddr {
	return c.laddr
}

// RemoteMultiaddr returns the remote address associated with
// this connection
func (c *maEndpoints) RemoteMultiaddr() ma.Multiaddr {
	return c.raddr
}

type sampledConn struct {
	tcpConnInterface
	maEndpoints

	s              Sample
	readFromSample uint8
}

var _ = [math.MaxUint8]struct{}{}[len(Sample{})] // compiletime assert sampledConn.readFromSample wont overflow
var _ io.ReaderFrom = (*sampledConn)(nil)
var _ io.WriterTo = (*sampledConn)(nil)

func (sc *sampledConn) Read(b []byte) (int, error) {
	if int(sc.readFromSample) != len(sc.s) {
		red := copy(b, sc.s[sc.readFromSample:])
		sc.readFromSample += uint8(red)
		return red, nil
	}

	return sc.tcpConnInterface.Read(b)
}

// forward optimizations
func (sc *sampledConn) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(sc.tcpConnInterface, r)
}

// forward optimizations
func (sc *sampledConn) WriteTo(w io.Writer) (total int64, err error) {
	if int(sc.readFromSample) != len(sc.s) {
		b := sc.s[sc.readFromSample:]
		written, err := w.Write(b)
		if written < 0 || len(b) < written {
			// buggy writer, harden against this
			sc.readFromSample = uint8(len(sc.s))
			total = int64(len(sc.s))
		} else {
			sc.readFromSample += uint8(written)
			total += int64(written)
		}
		if err != nil {
			return total, err
		}
	}

	written, err := io.Copy(w, sc.tcpConnInterface)
	total += written
	return total, err
}

type Matcher interface {
	Match(s Sample) bool
}

// Sample might evolve over time.
type Sample [3]byte

// Matchers are implemented here instead of in the transports so we can easily fuzz them together.

func IsMultistreamSelect(s Sample) bool {
	return string(s[:]) == "\x13/m"
}

func IsHTTP(s Sample) bool {
	switch string(s[:]) {
	case "GET", "HEA", "POS", "PUT", "DEL", "CON", "OPT", "TRA", "PAT":
		return true
	default:
		return false
	}
}

func IsTLS(s Sample) bool {
	switch string(s[:]) {
	case "\x16\x03\x01", "\x16\x03\x02", "\x16\x03\x03", "\x16\x03\x04":
		return true
	default:
		return false
	}
}
