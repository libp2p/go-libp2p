package relay

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/flynn/noise"
)

var keyExchangeCipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

func runNoiseXXResponder(ctx context.Context, stream network.Stream) ([]byte, error) {
	kp, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("keypair: %w", err)
	}

	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   keyExchangeCipherSuite,
		Pattern:       noise.HandshakeXX,
		Initiator:     false,
		StaticKeypair: kp,
	})
	if err != nil {
		return nil, fmt.Errorf("handshake init: %w", err)
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	} else {
		_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	}

	msg, err := readFrame(stream)
	if err != nil {
		return nil, err
	}
	if _, _, _, err := hs.ReadMessage(nil, msg); err != nil {
		return nil, fmt.Errorf("handshake read: %w", err)
	}

	reply, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("handshake write: %w", err)
	}
	if err := writeFrame(stream, reply); err != nil {
		return nil, err
	}

	final, err := readFrame(stream)
	if err != nil {
		return nil, err
	}
	plaintext, _, _, err := hs.ReadMessage(nil, final)
	if err != nil {
		return nil, fmt.Errorf("handshake read final: %w", err)
	}

	return plaintext, nil
}

func decodeKeyExchangePayload(data []byte) (string, []byte, error) {
	if len(data) < 1+2 {
		return "", nil, fmt.Errorf("payload too short")
	}
	pos := 0
	idLen := int(data[pos])
	pos++
	if len(data) < pos+idLen+2 {
		return "", nil, fmt.Errorf("payload truncated")
	}
	circuitID := string(data[pos : pos+idLen])
	pos += idLen
	keyLen := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if len(data) < pos+keyLen {
		return "", nil, fmt.Errorf("payload truncated (key)")
	}
	key := make([]byte, keyLen)
	copy(key, data[pos:pos+keyLen])
	return circuitID, key, nil
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
