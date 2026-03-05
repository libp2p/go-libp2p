package mixnet

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/flynn/noise"
)

// KeyExchangeProtocolID is the protocol used to negotiate per-circuit hop keys.
const KeyExchangeProtocolID = "/lib-mix/key/1.0.0"

var keyExchangeCipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

func (m *Mixnet) exchangeHopKey(ctx context.Context, relay peer.ID, circuitID string) ([]byte, error) {
	stream, err := m.host.NewStream(ctx, relay, KeyExchangeProtocolID)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}

	payload, err := encodeKeyExchangePayload(circuitID, key)
	if err != nil {
		return nil, err
	}

	if err := runNoiseXXInitiator(ctx, stream, payload); err != nil {
		return nil, err
	}

	return key, nil
}

func runNoiseXXInitiator(ctx context.Context, stream network.Stream, payload []byte) error {
	kp, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return fmt.Errorf("keypair: %w", err)
	}

	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   keyExchangeCipherSuite,
		Pattern:       noise.HandshakeXX,
		Initiator:     true,
		StaticKeypair: kp,
	})
	if err != nil {
		return fmt.Errorf("handshake init: %w", err)
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	} else {
		_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	}

	msg, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return fmt.Errorf("handshake write: %w", err)
	}
	if err := writeFrame(stream, msg); err != nil {
		return err
	}

	resp, err := readFrame(stream)
	if err != nil {
		return err
	}
	if _, _, _, err := hs.ReadMessage(nil, resp); err != nil {
		return fmt.Errorf("handshake read: %w", err)
	}

	msg, cs1, _, err := hs.WriteMessage(nil, payload)
	if err != nil {
		return fmt.Errorf("handshake write final: %w", err)
	}
	if err := writeFrame(stream, msg); err != nil {
		return err
	}

	if cs1 == nil {
		return fmt.Errorf("missing cipher state after handshake")
	}

	return nil
}

func encodeKeyExchangePayload(circuitID string, key []byte) ([]byte, error) {
	if len(circuitID) > 255 {
		return nil, fmt.Errorf("circuit ID too long")
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid hop key length")
	}
	out := make([]byte, 0, 1+len(circuitID)+2+len(key))
	out = append(out, byte(len(circuitID)))
	out = append(out, []byte(circuitID)...)
	keyLen := make([]byte, 2)
	binary.LittleEndian.PutUint16(keyLen, uint16(len(key)))
	out = append(out, keyLen...)
	out = append(out, key...)
	return out, nil
}

func writeFrame(w io.Writer, msg []byte) error {
	if len(msg) > 65535 {
		return fmt.Errorf("frame too large")
	}
	lenBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(lenBuf, uint16(len(msg)))
	if _, err := w.Write(lenBuf); err != nil {
		return err
	}
	_, err := w.Write(msg)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	lenBuf := make([]byte, 2)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(lenBuf))
	if n <= 0 {
		return nil, fmt.Errorf("invalid frame length")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
