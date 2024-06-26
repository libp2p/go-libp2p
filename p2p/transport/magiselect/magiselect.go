package magiselect

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net"
)

type peekAble interface {
	// Peek returns the next n bytes without advancing the reader. The bytes stop
	// being valid at the next read call. If Peek returns fewer than n bytes, it
	// also returns an error explaining why the read is short. The error is
	// [ErrBufferFull] if n is larger than b's buffer size.
	Peek(n int) ([]byte, error)
}

var _ peekAble = (*bufio.Reader)(nil)

// ReadSample read the sample and returns a reader which still include the sample, so it can be kept undamaged.
// If an error occurs it only return the error.
func ReadSampleFromConn(c net.Conn) (Sample, net.Conn, error) {
	if peekAble, ok := c.(peekAble); ok {
		b, err := peekAble.Peek(len(Sample{}))
		switch {
		case err == nil:
			return Sample(b), c, nil
		case errors.Is(err, bufio.ErrBufferFull):
			// fallback to sampledConn
		default:
			return Sample{}, nil, err
		}
	}

	sc := &sampledConn{Conn: c}
	_, err := io.ReadFull(c, sc.s[:])
	if err != nil {
		return Sample{}, nil, err
	}
	return sc.s, sc, nil
}

type sampledConn struct {
	net.Conn

	s             Sample
	redFromSample uint8
}

var _ = [math.MaxUint8]struct{}{}[len(Sample{})] // compiletime assert sampledConn.redFromSample wont overflow
var _ io.ReaderFrom = (*sampledConn)(nil)
var _ io.WriterTo = (*sampledConn)(nil)

func (sc *sampledConn) Read(b []byte) (int, error) {
	if int(sc.redFromSample) != len(sc.s) {
		red := copy(b, sc.s[sc.redFromSample:])
		sc.redFromSample += uint8(red)
		return red, nil
	}

	return sc.Conn.Read(b)
}

// forward optimizations
func (sc *sampledConn) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(sc.Conn, r)
}

// forward optimizations
func (sc *sampledConn) WriteTo(w io.Writer) (total int64, err error) {
	if int(sc.redFromSample) != len(sc.s) {
		b := sc.s[sc.redFromSample:]
		written, err := w.Write(b)
		if written < 0 || len(b) < written {
			// buggy writter, harden against this
			sc.redFromSample = uint8(len(sc.s))
			total = int64(len(sc.s))
		} else {
			sc.redFromSample += uint8(written)
			total += int64(written)
		}
		if err != nil {
			return total, err
		}
	}

	written, err := io.Copy(w, sc.Conn)
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

func IsNoise(s Sample) bool {
	length := binary.BigEndian.Uint16(s[:])
	if length < 2 {
		return false
	}

	b := s[2]
	typ := b & 0b111
	field := (b & 0b11111000) >> 3
	switch field {
	case 1, 2, 3:
		return typ == 2
	case 4:
		return typ == 2 || typ == 3
	default:
		return false
	}
}
