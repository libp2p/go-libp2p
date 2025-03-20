// The udpmux package contains the logic for multiplexing multiple WebRTC (ICE)
// connections over a single UDP socket.
package udpmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	logging "github.com/ipfs/go-log/v2"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/pion/ice/v4"
	"github.com/pion/stun"
)

var log = logging.Logger("webrtc-udpmux")

// ReceiveBufSize is the size of the buffer used to receive packets from the PacketConn.
// It is fine for this number to be higher than the actual path MTU as this value is not
// used to decide the packet size on the write path.
const ReceiveBufSize = 1500

type Candidate struct {
	Ufrag string
	Addr  *net.UDPAddr
}

// UDPMux multiplexes multiple ICE connections over a single net.PacketConn,
// generally a UDP socket.
//
// The connections are indexed by (ufrag, IP address family) and by remote
// address from which the connection has received valid STUN/RTC packets.
//
// When a new packet is received on the underlying net.PacketConn, we
// first check the address map to see if there is a connection associated with the
// remote address:
// If found, we pass the packet to that connection.
// Otherwise, we check to see if the packet is a STUN packet.
// If it is, we read the ufrag from the STUN packet and use it to check if there
// is a connection associated with the (ufrag, IP address family) pair.
// If found we add the association to the address map.
type UDPMux struct {
	socket net.PacketConn

	queue chan Candidate

	mx sync.Mutex
	// ufragMap allows us to multiplex incoming STUN packets based on ufrag
	ufragMap map[ufragConnKey]*muxedConnection
	// addrMap allows us to correctly direct incoming packets after the connection
	// is established and ufrag isn't available on all packets
	addrMap map[string]*muxedConnection
	// ufragAddrMap allows cleaning up all addresses from the addrMap once the connection is closed
	// During the ICE connectivity checks, the same ufrag might be used on multiple addresses.
	ufragAddrMap map[ufragConnKey][]net.Addr

	// the context controls the lifecycle of the mux
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

var _ ice.UDPMux = &UDPMux{}

func NewUDPMux(socket net.PacketConn) *UDPMux {
	ctx, cancel := context.WithCancel(context.Background())
	mux := &UDPMux{
		ctx:          ctx,
		cancel:       cancel,
		socket:       socket,
		ufragMap:     make(map[ufragConnKey]*muxedConnection),
		addrMap:      make(map[string]*muxedConnection),
		ufragAddrMap: make(map[ufragConnKey][]net.Addr),
		queue:        make(chan Candidate, 32),
	}

	return mux
}

func (mux *UDPMux) Start() {
	mux.wg.Add(1)
	go func() {
		defer mux.wg.Done()
		mux.readLoop()
	}()
}

// GetListenAddresses implements ice.UDPMux
func (mux *UDPMux) GetListenAddresses() []net.Addr {
	return []net.Addr{mux.socket.LocalAddr()}
}

// GetConn implements ice.UDPMux
// It creates a net.PacketConn for a given ufrag if an existing one cannot be found.
// We differentiate IPv4 and IPv6 addresses, since a remote is can be reachable at multiple different
// UDP addresses of the same IP address family (eg. server-reflexive addresses and peer-reflexive addresses).
func (mux *UDPMux) GetConn(ufrag string, addr net.Addr) (net.PacketConn, error) {
	a, ok := addr.(*net.UDPAddr)
	if !ok {
		return nil, fmt.Errorf("unexpected address type: %T", addr)
	}
	select {
	case <-mux.ctx.Done():
		return nil, io.ErrClosedPipe
	default:
		isIPv6 := ok && a.IP.To4() == nil
		_, conn := mux.getOrCreateConn(ufrag, isIPv6, mux, addr)
		return conn, nil
	}
}

// Close implements ice.UDPMux
func (mux *UDPMux) Close() error {
	select {
	case <-mux.ctx.Done():
		return nil
	default:
	}
	mux.cancel()
	mux.socket.Close()
	mux.wg.Wait()
	return nil
}

// writeTo writes a packet to the underlying net.PacketConn
func (mux *UDPMux) writeTo(buf []byte, addr net.Addr) (int, error) {
	return mux.socket.WriteTo(buf, addr)
}

func (mux *UDPMux) readLoop() {
	for {
		select {
		case <-mux.ctx.Done():
			return
		default:
		}

		buf := pool.Get(ReceiveBufSize)

		n, addr, err := mux.socket.ReadFrom(buf)
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") || errors.Is(err, context.Canceled) {
				log.Debugf("readLoop exiting: socket %s closed", mux.socket.LocalAddr())
			} else {
				log.Errorf("error reading from socket %s: %v", mux.socket.LocalAddr(), err)
			}
			pool.Put(buf)
			return
		}
		buf = buf[:n]

		if processed := mux.processPacket(buf, addr); !processed {
			pool.Put(buf)
		}
	}
}

func (mux *UDPMux) processPacket(buf []byte, addr net.Addr) (processed bool) {
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		log.Errorf("received a non-UDP address: %s", addr)
		return false
	}
	isIPv6 := udpAddr.IP.To4() == nil

	// Connections are indexed by remote address. We first
	// check if the remote address has a connection associated
	// with it. If yes, we push the received packet to the connection
	mux.mx.Lock()
	conn, ok := mux.addrMap[addr.String()]
	mux.mx.Unlock()
	if ok {
		if err := conn.Push(buf, addr); err != nil {
			log.Debugf("could not push packet: %v", err)
			return false
		}
		return true
	}

	if !stun.IsMessage(buf) {
		log.Debug("incoming message is not a STUN message")
		return false
	}

	msg := &stun.Message{Raw: buf}
	if err := msg.Decode(); err != nil {
		log.Debugf("failed to decode STUN message: %s", err)
		return false
	}
	if msg.Type != stun.BindingRequest {
		log.Debugf("incoming message should be a STUN binding request, got %s", msg.Type)
		return false
	}

	ufrag, err := ufragFromSTUNMessage(msg)
	if err != nil {
		log.Debugf("could not find STUN username: %s", err)
		return false
	}

	connCreated, conn := mux.getOrCreateConn(ufrag, isIPv6, mux, udpAddr)
	if connCreated {
		select {
		case mux.queue <- Candidate{Addr: udpAddr, Ufrag: ufrag}:
		default:
			log.Debugw("queue full, dropping incoming candidate", "ufrag", ufrag, "addr", udpAddr)
			conn.Close()
			return false
		}
	}

	if err := conn.Push(buf, addr); err != nil {
		log.Debugf("could not push packet: %v", err)
		return false
	}
	return true
}

func (mux *UDPMux) Accept(ctx context.Context) (Candidate, error) {
	select {
	case c := <-mux.queue:
		return c, nil
	case <-ctx.Done():
		return Candidate{}, ctx.Err()
	case <-mux.ctx.Done():
		return Candidate{}, mux.ctx.Err()
	}
}

type ufragConnKey struct {
	ufrag  string
	isIPv6 bool
}

// ufragFromSTUNMessage returns the local or ufrag
// from the STUN username attribute. Local ufrag is the ufrag of the
// peer which initiated the connectivity check, e.g in a connectivity
// check from A to B, the username attribute will be B_ufrag:A_ufrag
// with the local ufrag value being A_ufrag. In case of ice-lite, the
// localUfrag value will always be the remote peer's ufrag since ICE-lite
// implementations do not generate connectivity checks. In our specific
// case, since the local and remote ufrag is equal, we can return
// either value.
func ufragFromSTUNMessage(msg *stun.Message) (string, error) {
	attr, err := msg.Get(stun.AttrUsername)
	if err != nil {
		return "", err
	}
	index := bytes.Index(attr, []byte{':'})
	if index == -1 {
		return "", fmt.Errorf("invalid STUN username attribute")
	}
	return string(attr[index+1:]), nil
}

// RemoveConnByUfrag removes the connection associated with the ufrag and all the
// addresses associated with that connection. This method is called by pion when
// a peerconnection is closed.
func (mux *UDPMux) RemoveConnByUfrag(ufrag string) {
	if ufrag == "" {
		return
	}

	mux.mx.Lock()
	defer mux.mx.Unlock()

	for _, isIPv6 := range [...]bool{true, false} {
		key := ufragConnKey{ufrag: ufrag, isIPv6: isIPv6}
		if conn, ok := mux.ufragMap[key]; ok {
			delete(mux.ufragMap, key)
			for _, addr := range mux.ufragAddrMap[key] {
				delete(mux.addrMap, addr.String())
			}
			delete(mux.ufragAddrMap, key)
			conn.close()
		}
	}
}

func (mux *UDPMux) getOrCreateConn(ufrag string, isIPv6 bool, _ *UDPMux, addr net.Addr) (created bool, _ *muxedConnection) {
	key := ufragConnKey{ufrag: ufrag, isIPv6: isIPv6}

	mux.mx.Lock()
	defer mux.mx.Unlock()

	if conn, ok := mux.ufragMap[key]; ok {
		mux.addrMap[addr.String()] = conn
		mux.ufragAddrMap[key] = append(mux.ufragAddrMap[key], addr)
		return false, conn
	}

	conn := newMuxedConnection(mux, ufrag)
	mux.ufragMap[key] = conn
	mux.addrMap[addr.String()] = conn
	mux.ufragAddrMap[key] = append(mux.ufragAddrMap[key], addr)
	return true, conn
}
