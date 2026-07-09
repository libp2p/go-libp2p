// The udpmux package contains the logic for multiplexing multiple WebRTC (ICE)
// connections over a single UDP socket.
package udpmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	pool "github.com/libp2p/go-buffer-pool"
	logging "github.com/libp2p/go-libp2p/gologshim"
	"github.com/pion/ice/v4"
	"github.com/pion/stun/v3"
)

var log = logging.Logger("webrtc-udpmux")

// ReceiveBufSize is the size of the buffer used to receive packets from the PacketConn.
// It is fine for this number to be higher than the actual path MTU as this value is not
// used to decide the packet size on the write path.
const ReceiveBufSize = 1500

// maxAddrsPerUfrag bounds the number of remote addresses we will track for a
// single ufrag. ICE typically advertises a handful of candidates per session
// (host + server-reflexive + relay, per IP family); 32 leaves comfortable
// headroom while keeping the per-ufrag bookkeeping bounded.
const maxAddrsPerUfrag = 32

type Candidate struct {
	// LocalUfrag is the local (server) ICE ufrag, parsed from the part of the
	// STUN USERNAME attribute before the ':'. pion retrieves a muxed connection
	// by its local ufrag, so the mux is keyed on this value.
	LocalUfrag string
	// RemoteUfrag is the dialing peer's (client) ICE ufrag, parsed from the part
	// of the STUN USERNAME attribute after the ':'. In WebRTC Direct v1 it is
	// identical to LocalUfrag; in v2 the two differ.
	RemoteUfrag string
	// RemotePwd is the dialing peer's (client) ICE password. The listener needs
	// it to infer the client's SDP offer. In v1 it equals RemoteUfrag; in v2 it
	// is recovered from the server ufrag ("libp2p+webrtc+v2/<remote_pwd>").
	RemotePwd string
	Addr      *net.UDPAddr
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
	// ufragMap allows us to multiplex incoming STUN packets based on the local
	// (server) ufrag, which is the ufrag pion uses to retrieve a muxed connection.
	ufragMap map[ufragConnKey]*muxedConnection
	// addrMap allows us to correctly direct incoming packets after the connection
	// is established and ufrag isn't available on all packets
	addrMap map[string]*muxedConnection
	// ufragAddrMap allows cleaning up all addresses from the addrMap once the connection is closed
	// During the ICE connectivity checks, the same local ufrag might be used on multiple addresses.
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
func (mux *UDPMux) GetConn(localUfrag string, addr net.Addr) (net.PacketConn, error) {
	a, ok := addr.(*net.UDPAddr)
	if !ok {
		return nil, fmt.Errorf("unexpected address type: %T", addr)
	}
	select {
	case <-mux.ctx.Done():
		return nil, io.ErrClosedPipe
	default:
		isIPv6 := ok && a.IP.To4() == nil
		_, conn := mux.getOrCreateConn(localUfrag, isIPv6, mux, addr)
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
				log.Debug("readLoop exiting: socket closed", "local_addr", mux.socket.LocalAddr())
			} else {
				log.Error("error reading from socket", "local_addr", mux.socket.LocalAddr(), "error", err)
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
		log.Error("received a non-UDP address", "addr", addr)
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
			log.Debug("could not push packet", "error", err)
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
		log.Debug("failed to decode STUN message", "error", err)
		return false
	}
	if msg.Type != stun.BindingRequest {
		log.Debug("incoming message should be a STUN binding request", "got_type", msg.Type)
		return false
	}

	localUfrag, remoteUfrag, remotePwd, err := credentialsFromSTUNMessage(msg)
	if err != nil {
		log.Debug("dropping STUN binding request with invalid credentials", "error", err)
		return false
	}

	connCreated, conn := mux.getOrCreateConn(localUfrag, isIPv6, mux, udpAddr)
	if connCreated {
		select {
		case mux.queue <- Candidate{Addr: udpAddr, LocalUfrag: localUfrag, RemoteUfrag: remoteUfrag, RemotePwd: remotePwd}:
		default:
			log.Debug("queue full, dropping incoming candidate", "ufrag", localUfrag, "addr", udpAddr)
			conn.Close()
			return false
		}
	}

	if err := conn.Push(buf, addr); err != nil {
		log.Debug("could not push packet", "error", err)
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
	// localUfrag is the local (server) ICE ufrag. pion retrieves a muxed
	// connection by its local ufrag, so the mux keys on it.
	localUfrag string
	isIPv6     bool
}

// credentialsFromSTUNMessage parses and validates the ICE credentials carried in
// a STUN binding request's USERNAME attribute. For a connectivity check from
// client A to server B the USERNAME is "B_ufrag:A_ufrag" (RFC 8445 section 7.2.2,
// "<remote-ufrag>:<local-ufrag>" from the sender's perspective). B's pion ICE
// agent retrieves a muxed connection by its local (server) ufrag, so the mux
// keys on B_ufrag (the part before the ':'). An ufrag never contains a ':' (it
// is not an ice-char), so splitting on the first ':' is unambiguous.
//
// Both ufrags come from an attacker-controlled STUN packet and are later
// templated into the inferred SDP offer and pion's ICE credentials. They are
// validated here, before the mux allocates any connection state or queues a
// candidate, so a malformed or unsupported request never reaches the listener:
//
//   - both ufrags must be valid ICE username fragments (ice-char, length 4..256,
//     RFC 8839 section 5.4);
//   - the WebRTC Direct version is selected from the server ufrag prefix. It also
//     determines how the client's ICE password (needed to render the client's SDP
//     offer) is recovered: v1 shares one value (client password == client ufrag),
//     v2 encodes the password into the server ufrag as "libp2p+webrtc+v2/<pwd>".
//     An unrecognized version is rejected, never treated as v1.
//
// See https://github.com/libp2p/specs/blob/master/webrtc/webrtc-direct.md.
func credentialsFromSTUNMessage(msg *stun.Message) (localUfrag, remoteUfrag, remotePwd string, err error) {
	attr, err := msg.Get(stun.AttrUsername)
	if err != nil {
		return "", "", "", err
	}
	local, remote, found := strings.Cut(string(attr), ":")
	if !found {
		return "", "", "", fmt.Errorf("invalid STUN username attribute")
	}
	if !isICEUfrag(local) || !isICEUfrag(remote) {
		return "", "", "", fmt.Errorf("invalid ICE ufrag in STUN username")
	}
	switch {
	case strings.HasPrefix(local, UfragPrefixV2):
		pwd := strings.TrimPrefix(local, UfragPrefixV2)
		// The recovered value becomes the inferred offer's ice-pwd, so it must be
		// a valid ICE password (ice-char, length 22..256) per RFC 8839 section 5.4.
		if !isICEPwd(pwd) {
			return "", "", "", fmt.Errorf("invalid v2 ufrag %q: recovered client password is not a valid ICE password", local)
		}
		return local, remote, pwd, nil
	case strings.HasPrefix(local, UfragPrefixV1):
		return local, remote, remote, nil
	default:
		return "", "", "", fmt.Errorf("unsupported WebRTC Direct version in ufrag %q", local)
	}
}

// RemoveConnByUfrag removes the connection associated with the local (server)
// ufrag and all the addresses associated with that connection. This method is
// called by pion when a peerconnection is closed, always with the connection's
// local ufrag, which credentialsFromSTUNMessage has already validated as
// non-empty.
func (mux *UDPMux) RemoveConnByUfrag(localUfrag string) {
	mux.mx.Lock()
	defer mux.mx.Unlock()

	for _, isIPv6 := range [...]bool{true, false} {
		key := ufragConnKey{localUfrag: localUfrag, isIPv6: isIPv6}
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

func (mux *UDPMux) getOrCreateConn(localUfrag string, isIPv6 bool, _ *UDPMux, addr net.Addr) (created bool, _ *muxedConnection) {
	key := ufragConnKey{localUfrag: localUfrag, isIPv6: isIPv6}

	mux.mx.Lock()
	defer mux.mx.Unlock()

	if conn, ok := mux.ufragMap[key]; ok {
		// Cap per-ufrag address tracking. Once we have already associated
		// maxAddrsPerUfrag addresses with this connection, ignore further
		// candidates rather than letting the bookkeeping grow unbounded.
		if len(mux.ufragAddrMap[key]) >= maxAddrsPerUfrag {
			return false, conn
		}
		mux.addrMap[addr.String()] = conn
		mux.ufragAddrMap[key] = append(mux.ufragAddrMap[key], addr)
		return false, conn
	}

	conn := newMuxedConnection(mux, localUfrag)
	mux.ufragMap[key] = conn
	mux.addrMap[addr.String()] = conn
	mux.ufragAddrMap[key] = append(mux.ufragAddrMap[key], addr)
	return true, conn
}
