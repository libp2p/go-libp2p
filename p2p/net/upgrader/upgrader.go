package upgrader

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ipnet "github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/net/pnet"
	"github.com/libp2p/go-libp2p/p2p/transport/magiselect"

	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
	manet "github.com/multiformats/go-multiaddr/net"
	mss "github.com/multiformats/go-multistream"
)

// ErrNilPeer is returned when attempting to upgrade an outbound connection
// without specifying a peer ID.
var ErrNilPeer = errors.New("nil peer")

// AcceptQueueLength is the number of connections to fully setup before not accepting any new connections
var AcceptQueueLength = 16

const (
	defaultAcceptTimeout    = 15 * time.Second
	defaultNegotiateTimeout = 60 * time.Second
)

type Option func(*upgrader) error

func WithAcceptTimeout(t time.Duration) Option {
	return func(u *upgrader) error {
		u.acceptTimeout = t
		return nil
	}
}

type StreamMuxer struct {
	ID    protocol.ID
	Muxer network.Multiplexer
}

// Upgrader is a multistream upgrader that can upgrade an underlying connection
// to a full transport connection (secure and multiplexed).
type upgrader struct {
	psk       ipnet.PSK
	connGater connmgr.ConnectionGater
	rcmgr     network.ResourceManager

	muxerMuxer *mss.MultistreamMuxer[protocol.ID]
	muxers     []StreamMuxer
	muxerIDs   []protocol.ID

	security      []sec.SecureTransport
	securityMuxer *mss.MultistreamMuxer[protocol.ID]
	securityIDs   []protocol.ID

	straightSecurity []sec.StraightableSecureTransport
	straightMatcher  mafmt.Pattern

	// AcceptTimeout is the maximum duration an Accept is allowed to take.
	// This includes the time between accepting the raw network connection,
	// protocol selection as well as the handshake, if applicable.
	//
	// If unset, the default value (15s) is used.
	acceptTimeout time.Duration
}

var _ transport.Upgrader = &upgrader{}

func New(security []sec.SecureTransport, muxers []StreamMuxer, psk ipnet.PSK, rcmgr network.ResourceManager, connGater connmgr.ConnectionGater, opts ...Option) (transport.Upgrader, error) {
	u := &upgrader{
		acceptTimeout: defaultAcceptTimeout,
		rcmgr:         rcmgr,
		connGater:     connGater,
		psk:           psk,
		muxerMuxer:    mss.NewMultistreamMuxer[protocol.ID](),
		muxers:        muxers,
		security:      security,
		securityMuxer: mss.NewMultistreamMuxer[protocol.ID](),
	}
	for _, opt := range opts {
		if err := opt(u); err != nil {
			return nil, err
		}
	}
	if u.rcmgr == nil {
		u.rcmgr = &network.NullResourceManager{}
	}
	u.muxerIDs = make([]protocol.ID, 0, len(muxers))
	for _, m := range muxers {
		u.muxerMuxer.AddHandler(m.ID, nil)
		u.muxerIDs = append(u.muxerIDs, m.ID)
	}
	u.securityIDs = make([]protocol.ID, 0, len(security))
	u.straightSecurity = make([]sec.StraightableSecureTransport, 0, len(security))
	suffixes := []mafmt.Pattern{mafmt.Nothing}
	for _, s := range security {
		u.securityMuxer.AddHandler(s.ID(), nil)
		u.securityIDs = append(u.securityIDs, s.ID())
		if straight, ok := s.(sec.StraightableSecureTransport); ok {
			u.straightSecurity = append(u.straightSecurity, straight)
			if straight.Suffix() == nil {
				return nil, fmt.Errorf("StraightSecureTransport %q returned an empty suffix", straight.ID())
			}
			suffixes = append(suffixes, straight.SuffixMatcher())
		}
	}
	u.straightMatcher = mafmt.Or(suffixes...)
	return u, nil
}

// UpgradeListener upgrades the passed multiaddr-net listener into a full libp2p-transport listener.
func (u *upgrader) UpgradeListener(t transport.Transport, list manet.Listener) transport.ListenerFromUpgrader {
	ctx, cancel := context.WithCancel(context.Background())
	l := &listener{
		Listener:  list,
		upgrader:  u,
		transport: t,
		rcmgr:     u.rcmgr,
		threshold: newThreshold(AcceptQueueLength),
		incoming:  make(chan transport.CapableConn),
		cancel:    cancel,
		ctx:       ctx,
	}
	go l.handleIncoming()
	return l
}

func (u *upgrader) UpgradeOutbound(ctx context.Context, t transport.Transport, maconn manet.Conn, suffix ma.Multiaddr, p peer.ID, scope network.ConnManagementScope) (transport.CapableConn, error) {
	return u.upgrade(ctx, t, maconn, network.DirOutbound, suffix, p, scope)
}
func (u *upgrader) UpgradeInbound(ctx context.Context, t transport.Transport, maconn manet.Conn, p peer.ID, scope network.ConnManagementScope) (transport.CapableConn, error) {
	return u.upgrade(ctx, t, maconn, network.DirInbound, nil, p, scope)
}

// suffix is only used for Outbound connections
func (u *upgrader) upgrade(ctx context.Context, t transport.Transport, maconn manet.Conn, dir network.Direction, suffix ma.Multiaddr, p peer.ID, connScope network.ConnManagementScope) (_ transport.CapableConn, err error) {
	defer func() {
		if err != nil {
			connScope.Done()
		}
	}()
	if dir == network.DirOutbound && p == "" {
		return nil, ErrNilPeer
	}
	var stat network.ConnStats
	if cs, ok := maconn.(network.ConnStat); ok {
		stat = cs.Stat()
	}

	var conn net.Conn = maconn
	if u.psk != nil {
		pconn, err := pnet.NewProtectedConn(u.psk, conn)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to setup private network protector: %w", err)
		}
		conn = pconn
	} else if ipnet.ForcePrivateNetwork {
		log.Error("tried to dial with no Private Network Protector but usage of Private Networks is forced by the environment")
		return nil, ipnet.ErrNotInPrivateNetwork
	}

	isServer := dir == network.DirInbound
	sconn, security, err := u.setupSecurity(ctx, conn, suffix, p, isServer)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to negotiate security protocol: %w", err)
	}

	// call the connection gater, if one is registered.
	if u.connGater != nil && !u.connGater.InterceptSecured(dir, sconn.RemotePeer(), maconn) {
		if err := maconn.Close(); err != nil {
			log.Errorw("failed to close connection", "peer", p, "addr", maconn.RemoteMultiaddr(), "error", err)
		}
		return nil, fmt.Errorf("gater rejected connection with peer %s and addr %s with direction %d",
			sconn.RemotePeer(), maconn.RemoteMultiaddr(), dir)
	}
	// Only call SetPeer if it hasn't already been set -- this can happen when we don't know
	// the peer in advance and in some bug scenarios.
	if connScope.PeerScope() == nil {
		if err := connScope.SetPeer(sconn.RemotePeer()); err != nil {
			log.Debugw("resource manager blocked connection for peer", "peer", sconn.RemotePeer(), "addr", conn.RemoteAddr(), "error", err)
			if err := maconn.Close(); err != nil {
				log.Errorw("failed to close connection", "peer", p, "addr", maconn.RemoteMultiaddr(), "error", err)
			}
			return nil, fmt.Errorf("resource manager connection with peer %s and addr %s with direction %d",
				sconn.RemotePeer(), maconn.RemoteMultiaddr(), dir)
		}
	}

	muxer, smconn, err := u.setupMuxer(ctx, sconn, isServer, connScope.PeerScope())
	if err != nil {
		sconn.Close()
		return nil, fmt.Errorf("failed to negotiate stream multiplexer: %w", err)
	}

	tc := &transportConn{
		MuxedConn:                 smconn,
		ConnMultiaddrs:            maconn,
		ConnSecurity:              sconn,
		transport:                 t,
		stat:                      stat,
		scope:                     connScope,
		muxer:                     muxer,
		security:                  security,
		usedEarlyMuxerNegotiation: sconn.ConnState().UsedEarlyMuxerNegotiation,
	}
	return tc, nil
}

func (u *upgrader) setupSecurity(ctx context.Context, conn net.Conn, suffix ma.Multiaddr, p peer.ID, isServer bool) (_ sec.SecureConn, _ protocol.ID, err error) {
	st, conn, err := u.negotiateSecurity(ctx, conn, suffix, isServer)
	if err != nil {
		return nil, "", err
	}
	if isServer {
		sconn, err := st.SecureInbound(ctx, conn, p)
		return sconn, st.ID(), err
	}
	sconn, err := st.SecureOutbound(ctx, conn, p)
	return sconn, st.ID(), err
}

func (u *upgrader) negotiateMuxer(nc net.Conn, isServer bool) (*StreamMuxer, error) {
	if err := nc.SetDeadline(time.Now().Add(defaultNegotiateTimeout)); err != nil {
		return nil, err
	}

	var proto protocol.ID
	if isServer {
		selected, _, err := u.muxerMuxer.Negotiate(nc)
		if err != nil {
			return nil, err
		}
		proto = selected
	} else {
		selected, err := mss.SelectOneOf(u.muxerIDs, nc)
		if err != nil {
			return nil, err
		}
		proto = selected
	}

	if err := nc.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}

	if m := u.getMuxerByID(proto); m != nil {
		return m, nil
	}
	return nil, fmt.Errorf("selected protocol we don't have a transport for")
}

func (u *upgrader) getMuxerByID(id protocol.ID) *StreamMuxer {
	for _, m := range u.muxers {
		if m.ID == id {
			return &m
		}
	}
	return nil
}

func (u *upgrader) setupMuxer(ctx context.Context, conn sec.SecureConn, server bool, scope network.PeerScope) (protocol.ID, network.MuxedConn, error) {
	muxerSelected := conn.ConnState().StreamMultiplexer
	// Use muxer selected from security handshake if available. Otherwise fall back to multistream-selection.
	if len(muxerSelected) > 0 {
		m := u.getMuxerByID(muxerSelected)
		if m == nil {
			return "", nil, fmt.Errorf("selected a muxer we don't know: %s", muxerSelected)
		}
		c, err := m.Muxer.NewConn(conn, server, scope)
		if err != nil {
			return "", nil, err
		}
		return muxerSelected, c, nil
	}

	type result struct {
		smconn  network.MuxedConn
		muxerID protocol.ID
		err     error
	}

	done := make(chan result, 1)
	// TODO: The muxer should take a context.
	go func() {
		m, err := u.negotiateMuxer(conn, server)
		if err != nil {
			done <- result{err: err}
			return
		}
		smconn, err := m.Muxer.NewConn(conn, server, scope)
		done <- result{smconn: smconn, muxerID: m.ID, err: err}
	}()

	select {
	case r := <-done:
		return r.muxerID, r.smconn, r.err
	case <-ctx.Done():
		// interrupt this process
		conn.Close()
		// wait to finish
		<-done
		return "", nil, ctx.Err()
	}
}

func (u *upgrader) getSecurityByID(id protocol.ID) sec.SecureTransport {
	for _, s := range u.security {
		if s.ID() == id {
			return s
		}
	}
	return nil
}

var ErrNoMagiselectMatch = errors.New("no magiselect match")

func (u *upgrader) negotiateSecurity(ctx context.Context, insecure net.Conn, suffix ma.Multiaddr, server bool) (sec.SecureTransport, net.Conn, error) {
	if suffix != nil {
		for _, straight := range u.straightSecurity {
			if straight.SuffixMatcher().Matches(suffix) {
				return straight, insecure, nil
			}
		}
		return nil, nil, fmt.Errorf("suffix was provided but does not match anything %q %q", insecure, suffix) // buggy transport
	}

	type result struct {
		proto protocol.ID
		st    sec.SecureTransport
		err   error
	}

	done := make(chan result, 1)
	go func() {
		var r result
		r.proto, r.st, r.err = func() (protocol.ID, sec.SecureTransport, error) {
			if server {
				var s magiselect.Sample
				var err error
				s, insecure, err = magiselect.ReadSampleFromConn(insecure)
				if err != nil {
					return "", nil, err
				}

				if magiselect.IsMultistreamSelect(s) {
					proto, _, err := u.securityMuxer.Negotiate(insecure)
					return proto, nil, err
				}

				for _, ss := range u.straightSecurity {
					if ss.Match(s) {
						return "", ss, nil
					}
				}

				return "", nil, ErrNoMagiselectMatch
			}

			proto, err := mss.SelectOneOf(u.securityIDs, insecure)
			return proto, nil, err
		}()
		done <- r
	}()

	select {
	case r := <-done:
		if r.err != nil {
			return nil, nil, r.err
		}
		if r.st != nil {
			return r.st, insecure, nil
		}
		if s := u.getSecurityByID(r.proto); s != nil {
			return s, insecure, nil
		}
		return nil, nil, fmt.Errorf("selected unknown security transport: %s", r.proto)
	case <-ctx.Done():
		// We *must* do this. We have outstanding work on the connection, and it's no longer safe to use.
		insecure.Close()
		<-done // wait to stop using the connection.
		return nil, nil, ctx.Err()
	}
}

func (u *upgrader) Suffixes() []ma.Multiaddr {
	r := make([]ma.Multiaddr, len(u.straightSecurity)+1) // +1 for nil indicating multistream-select
	for i, s := range u.straightSecurity {
		r[i] = s.Suffix()
	}
	return r
}

func (u *upgrader) SuffixesProtocols() []int {
	r := make([]int, len(u.straightSecurity))
	for i, s := range u.straightSecurity {
		r[i] = s.SuffixProtocol()
	}
	return r
}

func (u *upgrader) SuffixMatcher() mafmt.Pattern {
	return u.straightMatcher
}
