package simconn

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var ErrDeadlineExceeded = errors.New("deadline exceeded")

type Router interface {
	SendPacket(deadline time.Time, p Packet) error
}

type Packet struct {
	To   net.Addr
	From net.Addr
	buf  []byte
}

type SimConn struct {
	mu     sync.Mutex
	closed bool

	packetsSent atomic.Uint64
	packetsRcvd atomic.Uint64
	bytesSent   atomic.Int64
	bytesRcvd   atomic.Int64

	router Router

	myAddr        *net.UDPAddr
	myLocalAddr   net.Addr
	packetsToRead chan Packet

	// In case we don't fully read the packet in ReadFrom, we need to store the current packet
	currentPacketToRead *Packet

	readDeadline  time.Time
	writeDeadline time.Time
}

// NewSimConn creates a new simulated connection with the specified parameters
func NewSimConn(addr *net.UDPAddr, rtr Router) *SimConn {
	return &SimConn{
		router:        rtr,
		myAddr:        addr,
		packetsToRead: make(chan Packet, 512), // buffered channel to prevent blocking
		closed:        false,
	}
}

type ConnStats struct {
	BytesSent   int
	BytesRcvd   int
	PacketsSent int
	PacketsRcvd int
}

func (c *SimConn) Stats() ConnStats {
	return ConnStats{
		BytesSent:   int(c.bytesSent.Load()),
		BytesRcvd:   int(c.bytesRcvd.Load()),
		PacketsSent: int(c.packetsSent.Load()),
		PacketsRcvd: int(c.packetsRcvd.Load()),
	}
}

// SetLocalAddr only changes what `.LocalAddr()` returns.
// Packets will still come From the initially configured addr.
func (c *SimConn) SetLocalAddr(addr net.Addr) {
	c.myLocalAddr = addr
}

func (c *SimConn) RecvPacket(p Packet) {
	c.packetsRcvd.Add(1)
	c.bytesRcvd.Add(int64(len(p.buf)))
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	select {
	case c.packetsToRead <- p:
	default:
		// drop the packet if the channel is full
	}
}

var _ net.PacketConn = &SimConn{}

// Close implements net.PacketConn
func (c *SimConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	close(c.packetsToRead)
	return nil
}

// ReadFrom implements net.PacketConn
func (c *SimConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, nil, net.ErrClosed
	}
	deadline := c.readDeadline
	c.mu.Unlock()

	if !deadline.IsZero() && time.Now().After(deadline) {
		return 0, nil, ErrDeadlineExceeded
	}

	var pkt Packet
	if c.currentPacketToRead != nil {
		pkt = *c.currentPacketToRead
		c.currentPacketToRead = nil
	} else {
		if !deadline.IsZero() {
			select {
			case pkt = <-c.packetsToRead:
			case <-time.After(time.Until(deadline)):
				return 0, nil, ErrDeadlineExceeded
			}
		} else {
			pkt = <-c.packetsToRead
		}
	}

	n = copy(p, pkt.buf)
	if n < len(pkt.buf) {
		// Store remaining data for next read
		c.currentPacketToRead = &Packet{
			From: pkt.From,
			buf:  pkt.buf[n:],
		}
	}
	return n, pkt.From, nil
}

// WriteTo implements net.PacketConn
func (c *SimConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, net.ErrClosed
	}
	deadline := c.writeDeadline
	c.mu.Unlock()

	if !deadline.IsZero() && time.Now().After(deadline) {
		return 0, ErrDeadlineExceeded
	}

	c.packetsSent.Add(1)
	c.bytesSent.Add(int64(len(p)))

	pkt := Packet{
		From: c.myAddr,
		To:   addr,
		buf:  append([]byte(nil), p...),
	}
	return len(p), c.router.SendPacket(deadline, pkt)
}

func (c *SimConn) UnicastAddr() net.Addr {
	return c.myAddr
}

// LocalAddr implements net.PacketConn
func (c *SimConn) LocalAddr() net.Addr {
	if c.myLocalAddr != nil {
		return c.myLocalAddr
	}
	return c.myAddr
}

// SetDeadline implements net.PacketConn
func (c *SimConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	c.writeDeadline = t
	return nil
}

// SetReadDeadline implements net.PacketConn
func (c *SimConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	return nil
}

// SetWriteDeadline implements net.PacketConn
func (c *SimConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = t
	return nil
}

func IntToPublicIPv4(n int) net.IP {
	n += 1
	// Avoid private IP ranges
	b := make([]byte, 4)
	b[0] = byte((n>>24)&0xFF | 1)
	b[1] = byte((n >> 16) & 0xFF)
	b[2] = byte((n >> 8) & 0xFF)
	b[3] = byte(n & 0xFF)

	// Check and modify if it's in private ranges
	if (b[0] == 10) || // 10.0.0.0/8
		(b[0] == 172 && b[1] >= 16 && b[1] <= 31) || // 172.16.0.0/12
		(b[0] == 192 && b[1] == 168) { // 192.168.0.0/16
		b[0] = 203 // Use 203.x.x.x as public range
	}

	return net.IPv4(b[0], b[1], b[2], b[3])
}
