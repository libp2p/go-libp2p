package webrtc_w3c

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/transport/webrtc_w3c/pb"
	"github.com/libp2p/go-msgio/pbio"
	"github.com/pion/webrtc/v3"
)

const maxMessageSize = 4096

var (
	errExpectedOffer    = errors.New("expected an SDP offer")
	errEmptyData        = errors.New("empty data")
	errConnectionFailed = errors.New("peerconnection failed to connect")
	errExpectedAnswer   = errors.New("expected an SDP answer")
)

func handleIncoming(ctx context.Context, config webrtc.Configuration, stream network.Stream) (*webrtc.PeerConnection, error) {
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		log.Warn("error creating a peer connection: %v", err)
		return nil, err
	}
	// handshake deadline
	if deadline, ok := ctx.Deadline(); ok {
		err = stream.SetDeadline(deadline)
		if err != nil {
			log.Warn("failed to set stream deadline: %v", err)
			return nil, err
		}
	}

	reader := pbio.NewDelimitedReader(stream, maxMessageSize)
	writer := pbio.NewDelimitedWriter(stream)

	// set up callback for when peerconnection moves to the connected
	// state. If the connection succeeds, this will be overwritten by
	// the webrtc.connection's callback.
	connectedChan := make(chan error, 1)
	closeRead := make(chan struct{})
	var connectedOnce sync.Once
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			connectedOnce.Do(func() {
				close(connectedChan)
				close(closeRead)
			})
		case webrtc.PeerConnectionStateFailed:
			fallthrough
		case webrtc.PeerConnectionStateClosed:
			connectedOnce.Do(func() {
				// safe since the channel is only written to once
				connectedChan <- errConnectionFailed
				close(connectedChan)
				close(closeRead)
			})
		default:
			// do nothing
		}
	})

	// setup the ICE candidate callback
	pc.OnICECandidate(func(candiate *webrtc.ICECandidate) {
		data := ""
		if candiate != nil {
			b, err := json.Marshal(candiate.ToJSON())
			if err != nil {
				log.Warn("failed to marshal candidate to JSON")
				return
			}
			data = string(b)
		}

		msg := &pb.Message{
			Type: pb.Message_ICE_CANDIDATE.Enum(),
			Data: &data,
		}
		// TODO: Do something with this error
		_ = writer.WriteMsg(msg)

	})

	defer func() {
		// de-register candidate callback
		pc.OnICECandidate(func(_ *webrtc.ICECandidate) {})
	}()

	// read an incoming offer
	var msg pb.Message
	if err := reader.ReadMsg(&msg); err != nil {
		log.Warn("failed to read SDP offer: %v", err)
		return nil, err
	}
	if msg.Type == nil || msg.GetType() != pb.Message_SDP_OFFER {
		log.Warn("expected SDP offer, instead got: %v", msg.GetType())
		return nil, errExpectedOffer
	}
	if msg.Data == nil {
		log.Warn("message is empty")
		return nil, errEmptyData
	}
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  *msg.Data,
	}
	if err := pc.SetRemoteDescription(offer); err != nil {
		log.Warn("failed to set remote description: %v", err)
		return nil, err
	}

	// create and write an answer
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		log.Warn("failed to create answer: %v", err)
		return nil, err
	}

	answerMessage := &pb.Message{
		Type: pb.Message_SDP_ANSWER.Enum(),
		Data: &answer.SDP,
	}
	if err := writer.WriteMsg(answerMessage); err != nil {
		log.Warn("failed to send answer: %v", err)
		return nil, err
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		log.Warn("failed to set local description: %v", err)
		return nil, err
	}

	done := ctx.Done()
	readErr := make(chan error)
	// start a goroutine to read candidates
	go func() {
		for {
			select {
			case <-done:
				return
			case <-closeRead:
				return
			default:
			}

			var msg pb.Message
			if err := reader.ReadMsg(&msg); err != nil {
				readErr <- err
				return
			}
			if msg.Type == nil || msg.GetType() != pb.Message_ICE_CANDIDATE {
				readErr <- errors.New("got non-candidate message")
				return
			}
			if msg.Data == nil {
				readErr <- errEmptyData
				return
			}
			if *msg.Data == "" {
				return
			}

			// unmarshal IceCandidateInit
			var init webrtc.ICECandidateInit
			if err := json.Unmarshal([]byte(*msg.Data), &init); err != nil {
				log.Debugf("could not unmarshal candidate: %v, %s", err, *msg.Data)
				readErr <- err
				return
			}
			if err := pc.AddICECandidate(init); err != nil {
				log.Debugf("bad candidate: %v", err)
				readErr <- err
				return
			}
		}
	}()

	select {
	case <-done:
		_ = pc.Close()
		return nil, ctx.Err()
	case err := <-connectedChan:
		if err != nil {
			_ = pc.Close()
			return nil, err
		}
		break
	case err := <-readErr:
		if err == nil {
			log.Error("err: %v", err)
			panic("nil error should never be written to this channel %v")
		}
		_ = pc.Close()
		return nil, err
	}
	return pc, nil
}

func connect(ctx context.Context, config webrtc.Configuration, stream network.Stream) (*webrtc.PeerConnection, error) {
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		log.Warn("error creating a peer connection: %v", err)
		return nil, err
	}
	// handshake deadline
	if deadline, ok := ctx.Deadline(); ok {
		err = stream.SetDeadline(deadline)
		if err != nil {
			log.Warn("failed to set stream deadline: %v", err)
			return nil, err
		}
	}

	reader := pbio.NewDelimitedReader(stream, maxMessageSize)
	writer := pbio.NewDelimitedWriter(stream)

	// set up callback for when peerconnection moves to the connected
	// state. If the connection succeeds, this will be overwritten by
	// the webrtc.connection's callback.
	connectedChan := make(chan error, 1)
	closeRead := make(chan struct{})
	var connectedOnce sync.Once
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			connectedOnce.Do(func() {
				close(connectedChan)
				close(closeRead)
			})
		case webrtc.PeerConnectionStateFailed:
			fallthrough
		case webrtc.PeerConnectionStateClosed:
			connectedOnce.Do(func() {
				// safe since the channel is only written to once
				connectedChan <- errConnectionFailed
				close(connectedChan)
				close(closeRead)
			})
		default:
			// do nothing
		}
	})

	// setup the ICE candidate callback
	pc.OnICECandidate(func(candiate *webrtc.ICECandidate) {
		data := ""
		if candiate != nil {
			b, err := json.Marshal(candiate.ToJSON())
			if err != nil {
				log.Warn("failed to marshal candidate to JSON")
				return
			}
			data = string(b)
		}

		msg := &pb.Message{
			Type: pb.Message_ICE_CANDIDATE.Enum(),
			Data: &data,
		}
		// TODO: Do something with this error
		_ = writer.WriteMsg(msg)

	})

	defer func() {
		// de-register candidate callback
		pc.OnICECandidate(func(_ *webrtc.ICECandidate) {})
	}()

	// we initialize a datachannel so that we have an ICE component for which to collect candidates
	if _, err := pc.CreateDataChannel("init", nil); err != nil {
		log.Warn("could not initialize peerconnection")
		return nil, err
	}

	// create and write an offer
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Warn("failed to create offer: %v", err)
		return nil, err
	}

	offerMessage := &pb.Message{
		Type: pb.Message_SDP_OFFER.Enum(),
		Data: &offer.SDP,
	}
	if err := writer.WriteMsg(offerMessage); err != nil {
		log.Warn("failed to send offer: %v", err)
		return nil, err
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		log.Warn("failed to set local description: %v", err)
		return nil, err
	}

	// read an incoming answer
	var msg pb.Message
	if err := reader.ReadMsg(&msg); err != nil {
		log.Warn("failed to read SDP answer: %v", err)
		return nil, err
	}
	if msg.Type == nil || msg.GetType() != pb.Message_SDP_ANSWER {
		return nil, errExpectedAnswer
	}
	if msg.Data == nil {
		return nil, errEmptyData
	}
	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  *msg.Data,
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		log.Warn("failed to set remote description: %v", err)
		return nil, err
	}

	done := ctx.Done()
	readErr := make(chan error)
	// start a goroutine to read candidates
	go func() {
		for {
			select {
			case <-done:
				return
			case <-closeRead:
				return
			default:
			}

			var msg pb.Message
			if err := reader.ReadMsg(&msg); err != nil {
				readErr <- err
				return
			}
			if msg.Type == nil || msg.GetType() != pb.Message_ICE_CANDIDATE {
				readErr <- errors.New("got non-candidate message")
				return
			}
			if msg.Data == nil {
				readErr <- errEmptyData
				return
			}

			// unmarshal IceCandidateInit
			var init webrtc.ICECandidateInit
			if *msg.Data == "" {
				return
			}
			if err := json.Unmarshal([]byte(*msg.Data), &init); err != nil {
				readErr <- err
				return
			}
			if err := pc.AddICECandidate(init); err != nil {
				readErr <- err
				return
			}
		}
	}()

	select {
	case <-done:
		_ = pc.Close()
		return nil, ctx.Err()
	case err := <-connectedChan:
		if err != nil {
			_ = pc.Close()
			return nil, err
		}
		break
	case err := <-readErr:
		if err == nil {
			panic("nil error should never be written to this channel")
		}
		log.Warn("error: %v", err)
		_ = pc.Close()
		return nil, err
	}
	return pc, nil
}
