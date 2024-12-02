package autonatv2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync"
	"time"

	pool "github.com/libp2p/go-buffer-pool"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"
	"github.com/libp2p/go-msgio/pbio"

	"math/rand"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var (
	errResourceLimitExceeded = errors.New("resource limit exceeded")
	errBadRequest            = errors.New("bad request")
	errDialDataRefused       = errors.New("dial data refused")
)

type dataRequestPolicyFunc = func(s network.Stream, dialAddr ma.Multiaddr) bool

type EventDialRequestCompleted struct {
	Error            error
	ResponseStatus   pb.DialResponse_ResponseStatus
	DialStatus       pb.DialStatus
	DialDataRequired bool
	DialedAddr       ma.Multiaddr
}

// server implements the AutoNATv2 server.
// It can ask client to provide dial data before attempting the requested dial.
// It rate limits requests on a global level, per peer level and on whether the request requires dial data.
type server struct {
	host       host.Host
	dialerHost host.Host
	limiter    *rateLimiter

	// dialDataRequestPolicy is used to determine whether dialing the address requires receiving
	// dial data. It is set to amplification attack prevention by default.
	dialDataRequestPolicy                dataRequestPolicyFunc
	amplificatonAttackPreventionDialWait time.Duration
	metricsTracer                        MetricsTracer

	// for tests
	now               func() time.Time
	allowPrivateAddrs bool
}

func newServer(host, dialer host.Host, s *autoNATSettings) *server {
	return &server{
		dialerHost:                           dialer,
		host:                                 host,
		dialDataRequestPolicy:                s.dataRequestPolicy,
		amplificatonAttackPreventionDialWait: s.amplificatonAttackPreventionDialWait,
		allowPrivateAddrs:                    s.allowPrivateAddrs,
		limiter: &rateLimiter{
			RPM:         s.serverRPM,
			PerPeerRPM:  s.serverPerPeerRPM,
			DialDataRPM: s.serverDialDataRPM,
			now:         s.now,
		},
		now:           s.now,
		metricsTracer: s.metricsTracer,
	}
}

// Enable attaches the stream handler to the host.
func (as *server) Start() {
	as.host.SetStreamHandler(DialProtocol, as.handleDialRequest)
}

func (as *server) Close() {
	as.host.RemoveStreamHandler(DialProtocol)
	as.dialerHost.Close()
	as.limiter.Close()
}

// handleDialRequest is the dial-request protocol stream handler
func (as *server) handleDialRequest(s network.Stream) {
	defer func() {
		if rerr := recover(); rerr != nil {
			fmt.Fprintf(os.Stderr, "caught panic: %s\n%s\n", rerr, debug.Stack())
			s.Reset()
		}
	}()

	log.Debugf("received dial-request from: %s, addr: %s", s.Conn().RemotePeer(), s.Conn().RemoteMultiaddr())
	evt := as.serveDialRequest(s)
	log.Debugf("completed dial-request from %s, response status: %s, dial status: %s, err: %s",
		s.Conn().RemotePeer(), evt.ResponseStatus, evt.DialStatus, evt.Error)
	if as.metricsTracer != nil {
		as.metricsTracer.CompletedRequest(evt)
	}
}

func (as *server) serveDialRequest(s network.Stream) EventDialRequestCompleted {
	if err := s.Scope().SetService(ServiceName); err != nil {
		s.Reset()
		log.Debugf("failed to attach stream to %s service: %w", ServiceName, err)
		return EventDialRequestCompleted{
			Error: errors.New("failed to attach stream to autonat-v2"),
		}
	}

	if err := s.Scope().ReserveMemory(maxMsgSize, network.ReservationPriorityAlways); err != nil {
		s.Reset()
		log.Debugf("failed to reserve memory for stream %s: %w", DialProtocol, err)
		return EventDialRequestCompleted{Error: errResourceLimitExceeded}
	}
	defer s.Scope().ReleaseMemory(maxMsgSize)

	deadline := as.now().Add(streamTimeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	s.SetDeadline(as.now().Add(streamTimeout))
	defer s.Close()

	p := s.Conn().RemotePeer()

	var msg pb.Message
	w := pbio.NewDelimitedWriter(s)
	// Check for rate limit before parsing the request
	if !as.limiter.Accept(p) {
		msg = pb.Message{
			Msg: &pb.Message_DialResponse{
				DialResponse: &pb.DialResponse{
					Status: pb.DialResponse_E_REQUEST_REJECTED,
				},
			},
		}
		if err := w.WriteMsg(&msg); err != nil {
			s.Reset()
			log.Debugf("failed to write request rejected response to %s: %s", p, err)
			return EventDialRequestCompleted{
				ResponseStatus: pb.DialResponse_E_REQUEST_REJECTED,
				Error:          fmt.Errorf("write failed: %w", err),
			}
		}
		log.Debugf("rejected request from %s: rate limit exceeded", p)
		return EventDialRequestCompleted{ResponseStatus: pb.DialResponse_E_REQUEST_REJECTED}
	}
	defer as.limiter.CompleteRequest(p)

	r := pbio.NewDelimitedReader(s, maxMsgSize)
	if err := r.ReadMsg(&msg); err != nil {
		s.Reset()
		log.Debugf("failed to read request from %s: %s", p, err)
		return EventDialRequestCompleted{Error: fmt.Errorf("read failed: %w", err)}
	}
	if msg.GetDialRequest() == nil {
		s.Reset()
		log.Debugf("invalid message type from %s: %T expected: DialRequest", p, msg.Msg)
		return EventDialRequestCompleted{Error: errBadRequest}
	}

	// parse peer's addresses
	var dialAddr ma.Multiaddr
	var addrIdx int
	for i, ab := range msg.GetDialRequest().GetAddrs() {
		if i >= maxPeerAddresses {
			break
		}
		a, err := ma.NewMultiaddrBytes(ab)
		if err != nil {
			continue
		}
		if !as.allowPrivateAddrs && !manet.IsPublicAddr(a) {
			continue
		}
		if !as.dialerHost.Network().CanDial(p, a) {
			continue
		}
		dialAddr = a
		addrIdx = i
		break
	}
	// No dialable address
	if dialAddr == nil {
		msg = pb.Message{
			Msg: &pb.Message_DialResponse{
				DialResponse: &pb.DialResponse{
					Status: pb.DialResponse_E_DIAL_REFUSED,
				},
			},
		}
		if err := w.WriteMsg(&msg); err != nil {
			s.Reset()
			log.Debugf("failed to write dial refused response to %s: %s", p, err)
			return EventDialRequestCompleted{
				ResponseStatus: pb.DialResponse_E_DIAL_REFUSED,
				Error:          fmt.Errorf("write failed: %w", err),
			}
		}
		return EventDialRequestCompleted{
			ResponseStatus: pb.DialResponse_E_DIAL_REFUSED,
		}
	}

	nonce := msg.GetDialRequest().Nonce

	isDialDataRequired := as.dialDataRequestPolicy(s, dialAddr)
	if isDialDataRequired && !as.limiter.AcceptDialDataRequest(p) {
		msg = pb.Message{
			Msg: &pb.Message_DialResponse{
				DialResponse: &pb.DialResponse{
					Status: pb.DialResponse_E_REQUEST_REJECTED,
				},
			},
		}
		if err := w.WriteMsg(&msg); err != nil {
			s.Reset()
			log.Debugf("failed to write request rejected response to %s: %s", p, err)
			return EventDialRequestCompleted{
				ResponseStatus:   pb.DialResponse_E_REQUEST_REJECTED,
				Error:            fmt.Errorf("write failed: %w", err),
				DialDataRequired: true,
			}
		}
		log.Debugf("rejected request from %s: rate limit exceeded", p)
		return EventDialRequestCompleted{
			ResponseStatus:   pb.DialResponse_E_REQUEST_REJECTED,
			DialDataRequired: true,
		}
	}

	if isDialDataRequired {
		if err := getDialData(w, s, &msg, addrIdx); err != nil {
			s.Reset()
			log.Debugf("%s refused dial data request: %s", p, err)
			return EventDialRequestCompleted{
				Error:            errDialDataRefused,
				DialDataRequired: true,
				DialedAddr:       dialAddr,
			}
		}
		// wait for a bit to prevent thundering herd style attacks on a victim
		waitTime := time.Duration(rand.Intn(int(as.amplificatonAttackPreventionDialWait) + 1)) // the range is [0, n)
		t := time.NewTimer(waitTime)
		defer t.Stop()
		select {
		case <-ctx.Done():
			s.Reset()
			log.Debugf("rejecting request without dialing: %s %p ", p, ctx.Err())
			return EventDialRequestCompleted{Error: ctx.Err(), DialDataRequired: true, DialedAddr: dialAddr}
		case <-t.C:
		}
	}

	dialStatus := as.dialBack(ctx, s.Conn().RemotePeer(), dialAddr, nonce)
	msg = pb.Message{
		Msg: &pb.Message_DialResponse{
			DialResponse: &pb.DialResponse{
				Status:     pb.DialResponse_OK,
				DialStatus: dialStatus,
				AddrIdx:    uint32(addrIdx),
			},
		},
	}
	if err := w.WriteMsg(&msg); err != nil {
		s.Reset()
		log.Debugf("failed to write response to %s: %s", p, err)
		return EventDialRequestCompleted{
			ResponseStatus:   pb.DialResponse_OK,
			DialStatus:       dialStatus,
			Error:            fmt.Errorf("write failed: %w", err),
			DialDataRequired: isDialDataRequired,
			DialedAddr:       dialAddr,
		}
	}
	return EventDialRequestCompleted{
		ResponseStatus:   pb.DialResponse_OK,
		DialStatus:       dialStatus,
		Error:            nil,
		DialDataRequired: isDialDataRequired,
		DialedAddr:       dialAddr,
	}
}

// getDialData gets data from the client for dialing the address
func getDialData(w pbio.Writer, s network.Stream, msg *pb.Message, addrIdx int) error {
	numBytes := minHandshakeSizeBytes + rand.Intn(maxHandshakeSizeBytes-minHandshakeSizeBytes)
	*msg = pb.Message{
		Msg: &pb.Message_DialDataRequest{
			DialDataRequest: &pb.DialDataRequest{
				AddrIdx:  uint32(addrIdx),
				NumBytes: uint64(numBytes),
			},
		},
	}
	if err := w.WriteMsg(msg); err != nil {
		return fmt.Errorf("dial data write: %w", err)
	}
	// pbio.Reader that we used so far on this stream is buffered. But at this point
	// there is nothing unread on the stream. So it is safe to use the raw stream to
	// read, reducing allocations.
	return readDialData(numBytes, s)
}

func readDialData(numBytes int, r io.Reader) error {
	mr := &msgReader{R: r, Buf: pool.Get(maxMsgSize)}
	defer pool.Put(mr.Buf)
	for remain := numBytes; remain > 0; {
		msg, err := mr.ReadMsg()
		if err != nil {
			return fmt.Errorf("dial data read: %w", err)
		}
		// protobuf format is:
		// (oneof dialDataResponse:<fieldTag><len varint>)(dial data:<fieldTag><len varint><bytes>)
		bytesLen := len(msg)
		bytesLen -= 2 // fieldTag + varint first byte
		if bytesLen > 127 {
			bytesLen -= 1 // varint second byte
		}
		bytesLen -= 2 // second fieldTag + varint first byte
		if bytesLen > 127 {
			bytesLen -= 1 // varint second byte
		}
		if bytesLen > 0 {
			remain -= bytesLen
		}
		// Check if the peer is not sending too little data forcing us to just do a lot of compute
		if bytesLen < 100 && remain > 0 {
			return fmt.Errorf("dial data msg too small: %d", bytesLen)
		}
	}
	return nil
}

func (as *server) dialBack(ctx context.Context, p peer.ID, addr ma.Multiaddr, nonce uint64) pb.DialStatus {
	ctx, cancel := context.WithTimeout(ctx, dialBackDialTimeout)
	ctx = network.WithForceDirectDial(ctx, "autonatv2")
	as.dialerHost.Peerstore().AddAddr(p, addr, peerstore.TempAddrTTL)
	defer func() {
		cancel()
		as.dialerHost.Network().ClosePeer(p)
		as.dialerHost.Peerstore().ClearAddrs(p)
		as.dialerHost.Peerstore().RemovePeer(p)
	}()

	err := as.dialerHost.Connect(ctx, peer.AddrInfo{ID: p})
	if err != nil {
		return pb.DialStatus_E_DIAL_ERROR
	}

	s, err := as.dialerHost.NewStream(ctx, p, DialBackProtocol)
	if err != nil {
		return pb.DialStatus_E_DIAL_BACK_ERROR
	}

	defer s.Close()
	s.SetDeadline(as.now().Add(dialBackStreamTimeout))

	w := pbio.NewDelimitedWriter(s)
	if err := w.WriteMsg(&pb.DialBack{Nonce: nonce}); err != nil {
		s.Reset()
		return pb.DialStatus_E_DIAL_BACK_ERROR
	}

	// Since the underlying connection is on a separate dialer, it'll be closed after this
	// function returns. Connection close will drop all the queued writes. To ensure message
	// delivery, do a CloseWrite and read a byte from the stream. The peer actually sends a
	// response of type DialBackResponse but we only care about the fact that the DialBack
	// message has reached the peer. So we ignore that message on the read side.
	s.CloseWrite()
	s.SetDeadline(as.now().Add(5 * time.Second)) // 5 is a magic number
	b := make([]byte, 1)                         // Read 1 byte here because 0 len reads are free to return (0, nil) immediately
	s.Read(b)

	return pb.DialStatus_OK
}

// rateLimiter implements a sliding window rate limit of requests per minute. It allows 1 concurrent request
// per peer. It rate limits requests globally, at a peer level and depending on whether it requires dial data.
type rateLimiter struct {
	// PerPeerRPM is the rate limit per peer
	PerPeerRPM int
	// RPM is the global rate limit
	RPM int
	// DialDataRPM is the rate limit for requests that require dial data
	DialDataRPM int

	mu           sync.Mutex
	closed       bool
	reqs         []entry
	peerReqs     map[peer.ID][]time.Time
	dialDataReqs []time.Time
	// ongoingReqs tracks in progress requests. This is used to disallow multiple concurrent requests by the
	// same peer
	// TODO: Should we allow a few concurrent requests per peer?
	ongoingReqs map[peer.ID]struct{}

	now func() time.Time // for tests
}

type entry struct {
	PeerID peer.ID
	Time   time.Time
}

func (r *rateLimiter) Accept(p peer.ID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	if r.peerReqs == nil {
		r.peerReqs = make(map[peer.ID][]time.Time)
		r.ongoingReqs = make(map[peer.ID]struct{})
	}

	nw := r.now()
	r.cleanup(nw)

	if _, ok := r.ongoingReqs[p]; ok {
		return false
	}
	if len(r.reqs) >= r.RPM || len(r.peerReqs[p]) >= r.PerPeerRPM {
		return false
	}

	r.ongoingReqs[p] = struct{}{}
	r.reqs = append(r.reqs, entry{PeerID: p, Time: nw})
	r.peerReqs[p] = append(r.peerReqs[p], nw)
	return true
}

func (r *rateLimiter) AcceptDialDataRequest(p peer.ID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	if r.peerReqs == nil {
		r.peerReqs = make(map[peer.ID][]time.Time)
		r.ongoingReqs = make(map[peer.ID]struct{})
	}
	nw := r.now()
	r.cleanup(nw)
	if len(r.dialDataReqs) >= r.DialDataRPM {
		return false
	}
	r.dialDataReqs = append(r.dialDataReqs, nw)
	return true
}

// cleanup removes stale requests.
//
// This is fast enough in rate limited cases and the state is small enough to
// clean up quickly when blocking requests.
func (r *rateLimiter) cleanup(now time.Time) {
	idx := len(r.reqs)
	for i, e := range r.reqs {
		if now.Sub(e.Time) >= time.Minute {
			pi := len(r.peerReqs[e.PeerID])
			for j, t := range r.peerReqs[e.PeerID] {
				if now.Sub(t) < time.Minute {
					pi = j
					break
				}
			}
			r.peerReqs[e.PeerID] = r.peerReqs[e.PeerID][pi:]
			if len(r.peerReqs[e.PeerID]) == 0 {
				delete(r.peerReqs, e.PeerID)
			}
		} else {
			idx = i
			break
		}
	}
	r.reqs = r.reqs[idx:]

	idx = len(r.dialDataReqs)
	for i, t := range r.dialDataReqs {
		if now.Sub(t) < time.Minute {
			idx = i
			break
		}
	}
	r.dialDataReqs = r.dialDataReqs[idx:]
}

func (r *rateLimiter) CompleteRequest(p peer.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.ongoingReqs, p)
}

func (r *rateLimiter) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.peerReqs = nil
	r.ongoingReqs = nil
	r.dialDataReqs = nil
}

// amplificationAttackPrevention is a dialDataRequestPolicy which requests data when the peer's observed
// IP address is different from the dial back IP address
func amplificationAttackPrevention(s network.Stream, dialAddr ma.Multiaddr) bool {
	connIP, err := manet.ToIP(s.Conn().RemoteMultiaddr())
	if err != nil {
		return true
	}
	dialIP, _ := manet.ToIP(s.Conn().LocalMultiaddr()) // must be an IP multiaddr
	return !connIP.Equal(dialIP)
}
