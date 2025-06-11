package autonatv2

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"math/rand/v2"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"
	"github.com/libp2p/go-msgio/pbio"
	ma "github.com/multiformats/go-multiaddr"
)

// client implements the client for making dial requests for AutoNAT v2. It verifies successful
// dials and provides an option to send data for dial requests.
type client struct {
	host               host.Host
	dialData           []byte
	normalizeMultiaddr func(ma.Multiaddr) ma.Multiaddr
	metricsTracer      MetricsTracer

	mu sync.Mutex
	// dialBackQueues maps nonce to the channel for providing the local multiaddr of the connection
	// the nonce was received on
	dialBackQueues map[uint64]chan ma.Multiaddr
}

type normalizeMultiaddrer interface {
	NormalizeMultiaddr(ma.Multiaddr) ma.Multiaddr
}

func newClient(s *autoNATSettings) *client {
	return &client{
		dialData:       make([]byte, 4000),
		dialBackQueues: make(map[uint64]chan ma.Multiaddr),
		metricsTracer:  s.metricsTracer,
	}
}

func (ac *client) Start(h host.Host) {
	normalizeMultiaddr := func(a ma.Multiaddr) ma.Multiaddr { return a }
	if hn, ok := h.(normalizeMultiaddrer); ok {
		normalizeMultiaddr = hn.NormalizeMultiaddr
	}
	ac.host = h
	ac.normalizeMultiaddr = normalizeMultiaddr
	ac.host.SetStreamHandler(DialBackProtocol, ac.handleDialBack)
}

func (ac *client) Close() {
	ac.host.RemoveStreamHandler(DialBackProtocol)
}

// GetReachability verifies address reachability with a AutoNAT v2 server p.
func (ac *client) GetReachability(ctx context.Context, p peer.ID, reqs []Request) (Result, error) {
	result, err := ac.getReachability(ctx, p, reqs)

	// Track metrics
	if ac.metricsTracer != nil {
		ac.metricsTracer.ClientCompletedRequest(reqs, result, err)
	}

	return result, err
}

func (ac *client) getReachability(ctx context.Context, p peer.ID, reqs []Request) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, streamTimeout)
	defer cancel()

	s, err := ac.host.NewStream(ctx, p, DialProtocol)
	if err != nil {
		return Result{}, fmt.Errorf("open %s stream failed: %w", DialProtocol, err)
	}

	if err := s.Scope().SetService(ServiceName); err != nil {
		s.Reset()
		return Result{}, fmt.Errorf("attach stream %s to service %s failed: %w", DialProtocol, ServiceName, err)
	}

	if err := s.Scope().ReserveMemory(maxMsgSize, network.ReservationPriorityAlways); err != nil {
		s.Reset()
		return Result{}, fmt.Errorf("failed to reserve memory for stream %s: %w", DialProtocol, err)
	}
	defer s.Scope().ReleaseMemory(maxMsgSize)

	s.SetDeadline(time.Now().Add(streamTimeout))
	defer s.Close()

	nonce := rand.Uint64()
	ch := make(chan ma.Multiaddr, 1)
	ac.mu.Lock()
	ac.dialBackQueues[nonce] = ch
	ac.mu.Unlock()
	defer func() {
		ac.mu.Lock()
		delete(ac.dialBackQueues, nonce)
		ac.mu.Unlock()
	}()

	msg := newDialRequest(reqs, nonce)
	w := pbio.NewDelimitedWriter(s)
	if err := w.WriteMsg(&msg); err != nil {
		s.Reset()
		return Result{}, fmt.Errorf("dial request write failed: %w", err)
	}

	r := pbio.NewDelimitedReader(s, maxMsgSize)
	if err := r.ReadMsg(&msg); err != nil {
		s.Reset()
		return Result{}, fmt.Errorf("dial msg read failed: %w", err)
	}

	switch {
	case msg.GetDialResponse() != nil:
		break
	// provide dial data if appropriate
	case msg.GetDialDataRequest() != nil:
		if err := validateDialDataRequest(reqs, &msg); err != nil {
			s.Reset()
			return Result{}, fmt.Errorf("invalid dial data request: %s %w", s.Conn().RemoteMultiaddr(), err)
		}
		// dial data request is valid and we want to send data
		if err := sendDialData(ac.dialData, int(msg.GetDialDataRequest().GetNumBytes()), w, &msg); err != nil {
			s.Reset()
			return Result{}, fmt.Errorf("dial data send failed: %w", err)
		}
		if err := r.ReadMsg(&msg); err != nil {
			s.Reset()
			return Result{}, fmt.Errorf("dial response read failed: %w", err)
		}
		if msg.GetDialResponse() == nil {
			s.Reset()
			return Result{}, fmt.Errorf("invalid response type: %T", msg.Msg)
		}
	default:
		s.Reset()
		return Result{}, fmt.Errorf("invalid msg type: %T", msg.Msg)
	}

	resp := msg.GetDialResponse()
	if resp.GetStatus() != pb.DialResponse_OK {
		// E_DIAL_REFUSED has implication for deciding future address verificiation priorities
		// wrap a distinct error for convenient errors.Is usage
		if resp.GetStatus() == pb.DialResponse_E_DIAL_REFUSED {
			return Result{AllAddrsRefused: true}, nil
		}
		return Result{}, fmt.Errorf("dial request failed: response status %d %s", resp.GetStatus(),
			pb.DialResponse_ResponseStatus_name[int32(resp.GetStatus())])
	}
	if resp.GetDialStatus() == pb.DialStatus_UNUSED {
		return Result{}, fmt.Errorf("invalid response: invalid dial status UNUSED")
	}
	if int(resp.AddrIdx) >= len(reqs) {
		return Result{}, fmt.Errorf("invalid response: addr index out of range: %d [0-%d)", resp.AddrIdx, len(reqs))
	}
	// wait for nonce from the server
	var dialBackAddr ma.Multiaddr
	if resp.GetDialStatus() == pb.DialStatus_OK {
		timer := time.NewTimer(dialBackStreamTimeout)
		select {
		case at := <-ch:
			dialBackAddr = at
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
	return ac.newResult(resp, reqs, dialBackAddr)
}

func validateDialDataRequest(reqs []Request, msg *pb.Message) error {
	idx := int(msg.GetDialDataRequest().AddrIdx)
	if idx >= len(reqs) { // invalid address index
		return fmt.Errorf("addr index out of range: %d [0-%d)", idx, len(reqs))
	}
	if msg.GetDialDataRequest().NumBytes > maxHandshakeSizeBytes { // data request is too high
		return fmt.Errorf("requested data too high: %d", msg.GetDialDataRequest().NumBytes)
	}
	if !reqs[idx].SendDialData { // low priority addr
		return fmt.Errorf("low priority addr: %s index %d", reqs[idx].Addr, idx)
	}
	return nil
}

func (ac *client) newResult(resp *pb.DialResponse, reqs []Request, dialBackAddr ma.Multiaddr) (Result, error) {
	idx := int(resp.AddrIdx)
	if idx >= len(reqs) {
		// This should have been validated by this point, but checking this is cheap.
		return Result{}, fmt.Errorf("addrs index(%d) greater than len(reqs)(%d)", idx, len(reqs))
	}
	addr := reqs[idx].Addr

	rch := network.ReachabilityUnknown //nolint:ineffassign
	switch resp.DialStatus {
	case pb.DialStatus_OK:
		if !ac.areAddrsConsistent(dialBackAddr, addr) {
			// the server is misinforming us about the address it successfully dialed
			// either we received no dialback or the address on the dialback is inconsistent with
			// what the server is telling us
			return Result{}, fmt.Errorf("invalid response: dialBackAddr: %s, respAddr: %s", dialBackAddr, addr)
		}
		rch = network.ReachabilityPublic
	case pb.DialStatus_E_DIAL_BACK_ERROR:
		if !ac.areAddrsConsistent(dialBackAddr, addr) {
			return Result{}, fmt.Errorf("dial-back stream error: dialBackAddr: %s, respAddr: %s", dialBackAddr, addr)
		}
		// We received the dial back but the server claims the dial back errored.
		// As long as we received the correct nonce in dial back it is safe to assume
		// that we are public.
		rch = network.ReachabilityPublic
	case pb.DialStatus_E_DIAL_ERROR:
		rch = network.ReachabilityPrivate
	default:
		// Unexpected response code. Discard the response and fail.
		log.Warnf("invalid status code received in response for addr %s: %d", addr, resp.DialStatus)
		return Result{}, fmt.Errorf("invalid response: invalid status code for addr %s: %d", addr, resp.DialStatus)
	}

	return Result{
		Addr:         addr,
		Idx:          idx,
		Reachability: rch,
	}, nil
}

func sendDialData(dialData []byte, numBytes int, w pbio.Writer, msg *pb.Message) (err error) {
	ddResp := &pb.DialDataResponse{Data: dialData}
	*msg = pb.Message{
		Msg: &pb.Message_DialDataResponse{
			DialDataResponse: ddResp,
		},
	}
	for remain := numBytes; remain > 0; {
		if remain < len(ddResp.Data) {
			ddResp.Data = ddResp.Data[:remain]
		}
		if err := w.WriteMsg(msg); err != nil {
			return fmt.Errorf("write failed: %w", err)
		}
		remain -= len(dialData)
	}
	return nil
}

func newDialRequest(reqs []Request, nonce uint64) pb.Message {
	addrbs := make([][]byte, len(reqs))
	for i, r := range reqs {
		addrbs[i] = r.Addr.Bytes()
	}
	return pb.Message{
		Msg: &pb.Message_DialRequest{
			DialRequest: &pb.DialRequest{
				Addrs: addrbs,
				Nonce: nonce,
			},
		},
	}
}

// handleDialBack receives the nonce on the dial-back stream
func (ac *client) handleDialBack(s network.Stream) {
	defer func() {
		if rerr := recover(); rerr != nil {
			fmt.Fprintf(os.Stderr, "caught panic: %s\n%s\n", rerr, debug.Stack())
		}
		s.Reset()
	}()

	if err := s.Scope().SetService(ServiceName); err != nil {
		log.Debugf("failed to attach stream to service %s: %w", ServiceName, err)
		s.Reset()
		return
	}

	if err := s.Scope().ReserveMemory(dialBackMaxMsgSize, network.ReservationPriorityAlways); err != nil {
		log.Debugf("failed to reserve memory for stream %s: %w", DialBackProtocol, err)
		s.Reset()
		return
	}
	defer s.Scope().ReleaseMemory(dialBackMaxMsgSize)

	s.SetDeadline(time.Now().Add(dialBackStreamTimeout))
	defer s.Close()

	r := pbio.NewDelimitedReader(s, dialBackMaxMsgSize)
	var msg pb.DialBack
	if err := r.ReadMsg(&msg); err != nil {
		log.Debugf("failed to read dialback msg from %s: %s", s.Conn().RemotePeer(), err)
		s.Reset()
		return
	}
	nonce := msg.GetNonce()

	ac.mu.Lock()
	ch := ac.dialBackQueues[nonce]
	ac.mu.Unlock()
	if ch == nil {
		log.Debugf("dialback received with invalid nonce: localAdds: %s peer: %s nonce: %d", s.Conn().LocalMultiaddr(), s.Conn().RemotePeer(), nonce)
		s.Reset()
		return
	}
	select {
	case ch <- s.Conn().LocalMultiaddr():
	default:
		log.Debugf("multiple dialbacks received: localAddr: %s peer: %s", s.Conn().LocalMultiaddr(), s.Conn().RemotePeer())
		s.Reset()
		return
	}
	w := pbio.NewDelimitedWriter(s)
	res := pb.DialBackResponse{}
	if err := w.WriteMsg(&res); err != nil {
		log.Debugf("failed to write dialback response: %s", err)
		s.Reset()
	}
}

func (ac *client) areAddrsConsistent(connLocalAddr, dialedAddr ma.Multiaddr) bool {
	if len(connLocalAddr) == 0 || len(dialedAddr) == 0 {
		return false
	}
	connLocalAddr = ac.normalizeMultiaddr(connLocalAddr)
	dialedAddr = ac.normalizeMultiaddr(dialedAddr)

	localProtos := connLocalAddr.Protocols()
	externalProtos := dialedAddr.Protocols()
	if len(localProtos) != len(externalProtos) {
		return false
	}
	for i, lp := range localProtos {
		ep := externalProtos[i]
		if i == 0 {
			switch ep.Code {
			case ma.P_DNS, ma.P_DNSADDR:
				if lp.Code == ma.P_IP4 || lp.Code == ma.P_IP6 {
					continue
				}
				return false
			case ma.P_DNS4:
				if lp.Code == ma.P_IP4 {
					continue
				}
				return false
			case ma.P_DNS6:
				if lp.Code == ma.P_IP6 {
					continue
				}
				return false
			}
			if lp.Code != ep.Code {
				return false
			}
		} else if lp.Code != ep.Code {
			return false
		}
	}
	return true
}
