package autonatv2

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"
	"github.com/libp2p/go-msgio/pbio"
	ma "github.com/multiformats/go-multiaddr"
	"golang.org/x/exp/rand"
)

//go:generate protoc --go_out=. --go_opt=Mpb/autonatv2.proto=./pb pb/autonatv2.proto

// client implements the client for making dial requests for AutoNAT v2. It verifies successful
// dials and provides an option to send data for dial requests.
type client struct {
	host     host.Host
	dialData []byte

	mu sync.Mutex
	// dialBackQueues maps nonce to the channel for providing the local multiaddr of the connection
	// the nonce was received on
	dialBackQueues map[uint64]chan ma.Multiaddr
}

func newClient(h host.Host) *client {
	return &client{host: h, dialData: make([]byte, 8000), dialBackQueues: make(map[uint64]chan ma.Multiaddr)}
}

// RegisterDialBack registers the client to receive DialBack streams initiated by the server to send the nonce.
func (ac *client) RegisterDialBack() {
	ac.host.SetStreamHandler(DialBackProtocol, ac.handleDialBack)
}

// CheckReachability verifies address reachability with a AutoNAT v2 server p.
func (ac *client) CheckReachability(ctx context.Context, p peer.ID, reqs []Request) (Result, error) {
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
		idx := int(msg.GetDialDataRequest().AddrIdx)
		if idx >= len(reqs) { // invalid address index
			s.Reset()
			return Result{}, fmt.Errorf("dial data: addr index out of range: %d [0-%d)", idx, len(reqs))
		}
		if msg.GetDialDataRequest().NumBytes > maxHandshakeSizeBytes { // data request is too high
			s.Reset()
			return Result{}, fmt.Errorf("dial data requested too high: %d", msg.GetDialDataRequest().NumBytes)
		}
		if !reqs[idx].SendDialData { // low priority addr
			s.Reset()
			return Result{}, fmt.Errorf("dial data requested for low priority addr: %s index %d", reqs[idx].Addr, idx)
		}

		// dial data request is valid and we want to send data
		if err := ac.sendDialData(msg.GetDialDataRequest(), w, &msg); err != nil {
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
			return Result{}, fmt.Errorf("dial request failed: %w", ErrDialRefused)
		}
		return Result{}, fmt.Errorf("dial request failed: response status %d %s", resp.GetStatus(),
			pb.DialStatus_name[int32(resp.GetStatus())])
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

func (ac *client) newResult(resp *pb.DialResponse, reqs []Request, dialBackAddr ma.Multiaddr) (Result, error) {
	idx := int(resp.AddrIdx)
	addr := reqs[idx].Addr

	var rch network.Reachability
	switch resp.DialStatus {
	case pb.DialStatus_OK:
		if !areAddrsConsistent(dialBackAddr, addr) {
			// The server reported a successful dial back but we didn't receive the nonce.
			// Discard the response and fail.
			return Result{}, fmt.Errorf("invalid repsonse: no dialback received")
		}
		rch = network.ReachabilityPublic
	case pb.DialStatus_E_DIAL_ERROR:
		rch = network.ReachabilityPrivate
	case pb.DialStatus_E_DIAL_BACK_ERROR:
		rch = network.ReachabilityUnknown
	default:
		// Unexpected response code. Discard the response and fail.
		log.Warnf("invalid status code received in response for addr %s: %d", addr, resp.DialStatus)
		return Result{}, fmt.Errorf("invalid response: invalid status code for addr %s: %d", addr, resp.DialStatus)
	}

	return Result{
		Idx:          idx,
		Addr:         addr,
		Reachability: rch,
		Status:       resp.DialStatus,
	}, nil
}

func (ac *client) sendDialData(req *pb.DialDataRequest, w pbio.Writer, msg *pb.Message) error {
	nb := req.GetNumBytes()
	ddResp := &pb.DialDataResponse{Data: ac.dialData}
	*msg = pb.Message{
		Msg: &pb.Message_DialDataResponse{
			DialDataResponse: ddResp,
		},
	}
	for remain := int(nb); remain > 0; {
		end := remain
		if end > len(ddResp.Data) {
			end = len(ddResp.Data)
		}
		ddResp.Data = ddResp.Data[:end]
		if err := w.WriteMsg(msg); err != nil {
			return err
		}
		remain -= end
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
	if err := s.Scope().SetService(ServiceName); err != nil {
		log.Debugf("failed to attach stream to service %s: %w", ServiceName, err)
		s.Reset()
		return
	}

	if err := s.Scope().ReserveMemory(maxMsgSize, network.ReservationPriorityAlways); err != nil {
		log.Debugf("failed to reserve memory for stream %s: %w", DialBackProtocol, err)
		s.Reset()
		return
	}
	defer s.Scope().ReleaseMemory(maxMsgSize)

	s.SetDeadline(time.Now().Add(dialBackStreamTimeout))
	defer s.Close()

	r := pbio.NewDelimitedReader(s, maxMsgSize)
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
	select {
	case ch <- s.Conn().LocalMultiaddr():
	default:
	}
}

func areAddrsConsistent(local, external ma.Multiaddr) bool {
	if local == nil || external == nil {
		return false
	}

	localProtos := local.Protocols()
	externalProtos := external.Protocols()
	if len(localProtos) != len(externalProtos) {
		return false
	}
	for i := 0; i < len(localProtos); i++ {
		if i == 0 {
			if localProtos[i].Code == externalProtos[i].Code ||
				localProtos[i].Code == ma.P_IP6 && externalProtos[i].Code == ma.P_IP4 /* NAT64 */ {
				continue
			}
			return false
		} else {
			if localProtos[i].Code != externalProtos[i].Code {
				return false
			}
		}
	}
	return true
}
