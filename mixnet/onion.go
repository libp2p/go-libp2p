package mixnet

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/mixnet/circuit"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	frameVersionFullOnion  byte = 0x01
	frameVersionHeaderOnly byte = 0x02
)

func encryptOnion(payload []byte, c *circuit.Circuit, dest peer.ID, hopKeys [][]byte) ([]byte, error) {
	if c == nil || len(c.Peers) == 0 {
		return nil, fmt.Errorf("empty circuit")
	}
	if len(hopKeys) != len(c.Peers) {
		return nil, fmt.Errorf("hop key count mismatch")
	}

	current := payload
	for i := len(c.Peers) - 1; i >= 0; i-- {
		isFinal := byte(0)
		nextHop := ""
		if i == len(c.Peers)-1 {
			isFinal = 1
			nextHop = dest.String()
		} else {
			nextHop = c.Peers[i+1].String()
		}
		plain, err := buildHopPayload(isFinal, nextHop, current)
		if err != nil {
			return nil, err
		}
		enc, err := encryptHopPayload(hopKeys[i], plain)
		if err != nil {
			return nil, err
		}
		current = enc
	}
	return current, nil
}

func buildHopPayload(isFinal byte, nextHop string, payload []byte) ([]byte, error) {
	if len(nextHop) > 65535 {
		return nil, fmt.Errorf("next hop too long")
	}
	header := make([]byte, 1+2+len(nextHop))
	header[0] = isFinal
	binary.LittleEndian.PutUint16(header[1:3], uint16(len(nextHop)))
	copy(header[3:], []byte(nextHop))
	return append(header, payload...), nil
}

func encryptHopPayload(key []byte, payload []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid hop key length")
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := aead.Seal(nil, nonce, payload, nil)
	return append(nonce, ciphertext...), nil
}

func encodeEncryptedFrame(circuitID string, payload []byte) ([]byte, error) {
	return encodeEncryptedFrameWithVersion(circuitID, frameVersionFullOnion, payload)
}

func encodeEncryptedFrameWithVersion(circuitID string, version byte, payload []byte) ([]byte, error) {
	if len(circuitID) == 0 || len(circuitID) > 255 {
		return nil, fmt.Errorf("invalid circuit id")
	}
	header := make([]byte, 1+len(circuitID)+1+4)
	header[0] = byte(len(circuitID))
	copy(header[1:], []byte(circuitID))
	header[1+len(circuitID)] = version
	binary.LittleEndian.PutUint32(header[1+len(circuitID)+1:], uint32(len(payload)))
	return append(header, payload...), nil
}
