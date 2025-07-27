package autonat

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/host/autonat/pb"

	"github.com/libp2p/go-msgio/pbio"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/libp2p/go-libp2p/p2p/net/manet"
	"google.golang.org/protobuf/proto"
	"github.com/libp2p/go-msgio/protoio"
)

var streamTimeout = 60 * time.Second

const (
	ServiceName = "libp2p.autonat"

	maxMsgSize = 4096

	maxAddresses = 100
	dialTimeout  = 30 * time.Second
)

// AutoNATService provides NAT autodetection services to other peers
type autoNATService struct {
	instanceLock      sync.Mutex
	instance          context.CancelFunc
	backgroundRunning chan struct{} // closed when background exits

	config *config

	// rate limiter
	mx         sync.Mutex
	reqs       map[peer.ID]int
	globalReqs int
}

// NewAutoNATService creates a new AutoNATService instance attached to a host
func newAutoNATService(c *config) (*autoNATService, error) {
	if c.dialer == nil {
		return nil, errors.New("cannot create NAT service without a network")
	}
	return &autoNATService{
		config: c,
		reqs:   make(map[peer.ID]int),
	}, nil
}

func (s *autoNATService) handleStream(stream network.Stream) {
	defer stream.Close()

	if err := s.handleStreamRequest(stream); err != nil {
		log.Debugf("error handling autonat request: %s", err)
		resp := &pb.Message{
			Type:    pb.Message_DIAL_RESPONSE.Enum(),
			Status:  pb.Message_E_DIAL_ERROR.Enum(),
			Version: proto.Int32(autoNATProtocolVersion),
		}
		s.writeResponse(stream, resp)
	}
}

func (s *autoNATService) handleStreamRequest(stream network.Stream) error {
	rd := protoio.NewDelimitedReader(stream, network.MessageSizeMax)
	wr := protoio.NewDelimitedWriter(stream)

	req := &pb.Message{}
	if err := rd.ReadMsg(req); err != nil {
		return err
	}

	if req.GetType() != pb.Message_DIAL {
		return fmt.Errorf("unknown message type: %d", req.GetType())
	}

	if req.GetVersion() != autoNATProtocolVersion {
		return fmt.Errorf("unknown protocol version: %d", req.GetVersion())
	}

	p := stream.Conn().RemotePeer()
	pi := peer.AddrInfo{ID: p}
	addrs := req.GetAddresses()
	if len(addrs) == 0 {
		return fmt.Errorf("no addresses provided")
	}

	// Limit the number of addresses to check
	if len(addrs) > maxAddresses {
		addrs = addrs[:maxAddresses]
	}

	// Convert addresses to multiaddrs
	maddrs := make([]ma.Multiaddr, 0, len(addrs))
	indices := make([]int32, 0, len(addrs))
	for _, a := range addrs {
		addr, err := ma.NewMultiaddrBytes(a.GetAddress())
		if err != nil {
			continue
		}

		// Check if this is a shared TCP listener address
		protos := addr.Protocols()
		isSharedTCP := false
		if len(protos) > 0 && protos[len(protos)-1].Code == ma.P_TCP {
			for _, comp := range addr.Components() {
				if comp.Protocol().Code == ma.P_TCP {
					isSharedTCP = true
					break
				}
			}
		}

		// Include the address for dialing if it's public or using a shared TCP listener
		if manet.IsPublicAddr(addr) || isSharedTCP {
			maddrs = append(maddrs, addr)
			indices = append(indices, a.GetIdx())
		}
	}

	if len(maddrs) == 0 {
		return fmt.Errorf("no public addresses provided")
	}

	pi.Addrs = maddrs
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()

	// Try to connect to the peer
	if err := s.host.Connect(ctx, pi); err != nil {
		var status pb.Message_ResponseStatus
		if isDialRefused(err) {
			status = pb.Message_E_DIAL_REFUSED
		} else {
			status = pb.Message_E_DIAL_ERROR
		}
		resp := &pb.Message{
			Type:    pb.Message_DIAL_RESPONSE.Enum(),
			Status:  status.Enum(),
			Version: proto.Int32(autoNATProtocolVersion),
		}
		return s.writeResponse(stream, resp)
	}

	// Successfully connected
	addr := maddrs[0]
	idx := indices[0]
	resp := &pb.Message{
		Type:    pb.Message_DIAL_RESPONSE.Enum(),
		Status:  pb.Message_OK.Enum(),
		Version: proto.Int32(autoNATProtocolVersion),
		Address: &pb.Message_Address{
			Address: addr.Bytes(),
			Idx:     proto.Int32(idx),
		},
	}
	return s.writeResponse(stream, resp)
}

func (s *autoNATService) writeResponse(stream network.Stream, resp *pb.Message) error {
	wr := protoio.NewDelimitedWriter(stream)
	return wr.WriteMsg(resp)
}

func (as *autoNATService) handleDial(p peer.ID, obsaddr ma.Multiaddr, mpi *pb.Message_PeerInfo) *pb.Message_DialResponse {
	if mpi == nil {
		return newDialResponseError(pb.Message_E_BAD_REQUEST, "missing peer info")
	}

	mpid := mpi.GetId()
	if mpid != nil {
		mp, err := peer.IDFromBytes(mpid)
		if err != nil {
			return newDialResponseError(pb.Message_E_BAD_REQUEST, "bad peer id")
		}

		if mp != p {
			return newDialResponseError(pb.Message_E_BAD_REQUEST, "peer id mismatch")
		}
	}

	addrs := make([]ma.Multiaddr, 0, as.config.maxPeerAddresses)
	seen := make(map[string]struct{})

	// Don't even try to dial peers with blocked remote addresses. In order to dial a peer, we
	// need to know their public IP address, and it needs to be different from our public IP
	// address.
	if as.config.dialPolicy.skipDial(obsaddr) {
		if as.config.metricsTracer != nil {
			as.config.metricsTracer.OutgoingDialRefused(dial_blocked)
		}
		// Note: versions < v0.20.0 return Message_E_DIAL_ERROR here, thus we can not rely on this error code.
		return newDialResponseError(pb.Message_E_DIAL_REFUSED, "refusing to dial peer with blocked observed address")
	}

	// Determine the peer's IP address.
	hostIP, _ := ma.SplitFirst(obsaddr)
	switch hostIP.Protocol().Code {
	case ma.P_IP4, ma.P_IP6:
	default:
		// This shouldn't be possible as we should skip all addresses that don't include
		// public IP addresses.
		return newDialResponseError(pb.Message_E_INTERNAL_ERROR, "expected an IP address")
	}

	// add observed addr to the list of addresses to dial
	addrs = append(addrs, obsaddr)
	seen[obsaddr.String()] = struct{}{}

	for _, maddr := range mpi.GetAddrs() {
		addr, err := ma.NewMultiaddrBytes(maddr)
		if err != nil {
			log.Debugf("Error parsing multiaddr: %s", err.Error())
			continue
		}

		// For security reasons, we _only_ dial the observed IP address.
		// Replace other IP addresses with the observed one so we can still try the
		// requested ports/transports.
		if ip, rest := ma.SplitFirst(addr); !ip.Equal(hostIP) {
			// Make sure it's an IP address
			switch ip.Protocol().Code {
			case ma.P_IP4, ma.P_IP6:
			default:
				continue
			}
			addr = hostIP.Multiaddr()
			if len(rest) > 0 {
				addr = addr.Encapsulate(rest)
			}
		}

		// Make sure we're willing to dial the rest of the address (e.g., not a circuit
		// address).
		if as.config.dialPolicy.skipDial(addr) {
			continue
		}

		str := addr.String()
		_, ok := seen[str]
		if ok {
			continue
		}

		addrs = append(addrs, addr)
		seen[str] = struct{}{}

		if len(addrs) >= as.config.maxPeerAddresses {
			break
		}
	}

	if len(addrs) == 0 {
		if as.config.metricsTracer != nil {
			as.config.metricsTracer.OutgoingDialRefused(no_valid_address)
		}
		// Note: versions < v0.20.0 return Message_E_DIAL_ERROR here, thus we can not rely on this error code.
		return newDialResponseError(pb.Message_E_DIAL_REFUSED, "no dialable addresses")
	}

	return as.doDial(peer.AddrInfo{ID: p, Addrs: addrs})
}

func (as *autoNATService) doDial(pi peer.AddrInfo) *pb.Message_DialResponse {
	// rate limit check
	as.mx.Lock()
	count := as.reqs[pi.ID]
	if count >= as.config.throttlePeerMax || (as.config.throttleGlobalMax > 0 &&
		as.globalReqs >= as.config.throttleGlobalMax) {
		as.mx.Unlock()
		if as.config.metricsTracer != nil {
			as.config.metricsTracer.OutgoingDialRefused(rate_limited)
		}
		return newDialResponseError(pb.Message_E_DIAL_REFUSED, "too many dials")
	}
	as.reqs[pi.ID] = count + 1
	as.globalReqs++
	as.mx.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), as.config.dialTimeout)
	defer cancel()

	as.config.dialer.Peerstore().ClearAddrs(pi.ID)

	as.config.dialer.Peerstore().AddAddrs(pi.ID, pi.Addrs, peerstore.TempAddrTTL)

	defer func() {
		as.config.dialer.Peerstore().ClearAddrs(pi.ID)
		as.config.dialer.Peerstore().RemovePeer(pi.ID)
	}()

	conn, err := as.config.dialer.DialPeer(ctx, pi.ID)
	if err != nil {
		log.Debugf("error dialing %s: %s", pi.ID, err.Error())
		// wait for the context to timeout to avoid leaking timing information
		// this renders the service ineffective as a port scanner
		<-ctx.Done()
		return newDialResponseError(pb.Message_E_DIAL_ERROR, "dial failed")
	}

	ra := conn.RemoteMultiaddr()
	as.config.dialer.ClosePeer(pi.ID)
	return newDialResponseOK(ra)
}

// Enable the autoNAT service if it is not running.
func (as *autoNATService) Enable() {
	as.instanceLock.Lock()
	defer as.instanceLock.Unlock()
	if as.instance != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	as.instance = cancel
	as.backgroundRunning = make(chan struct{})
	as.config.host.SetStreamHandler(AutoNATProto, as.handleStream)

	go as.background(ctx)
}

// Disable the autoNAT service if it is running.
func (as *autoNATService) Disable() {
	as.instanceLock.Lock()
	defer as.instanceLock.Unlock()
	if as.instance != nil {
		as.config.host.RemoveStreamHandler(AutoNATProto)
		as.instance()
		as.instance = nil
		<-as.backgroundRunning
	}
}

func (as *autoNATService) Close() error {
	as.Disable()
	return as.config.dialer.Close()
}

func (as *autoNATService) background(ctx context.Context) {
	defer close(as.backgroundRunning)

	timer := time.NewTimer(as.config.throttleResetPeriod)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			as.mx.Lock()
			as.reqs = make(map[peer.ID]int)
			as.globalReqs = 0
			as.mx.Unlock()
			jitter := rand.Float32() * float32(as.config.throttleResetJitter)
			timer.Reset(as.config.throttleResetPeriod + time.Duration(int64(jitter)))
		case <-ctx.Done():
			return
		}
	}
}
