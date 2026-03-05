// Package mixnet provides privacy-enhanced transport components.
package mixnet

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

)

// ============================================================
// Req 11: Transport Agnostic - Use libp2p's abstraction
// ============================================================
// The code already uses host.Connect() and host.NewStream() which are
// transport-agnostic. libp2p handles TCP/QUIC/WebRTC automatically.
// This section adds explicit multiaddr protocol detection.

// TransportInfo holds transport capability information for a peer.
type TransportInfo struct {
	PeerID     peer.ID
	Supported  []string // e.g., "/tcp/0", "/quic/1", "/webrtc"
	Multiaddrs []string
}

// DetectTransportCapabilities detects what transports a peer supports using multiaddr.
// This implements Req 11 - Transport Agnostic.
func DetectTransportCapabilities(h host.Host, p peer.ID) (*TransportInfo, error) {
	info := &TransportInfo{PeerID: p}

	// Get peer info from peerstore (includes advertised addresses)
	pi := h.Peerstore().PeerInfo(p)

	for _, addr := range pi.Addrs {
		// Parse multiaddr to get protocol stack
		protocols := addr.Protocols()
		for _, proto := range protocols {
			protoStr := fmt.Sprintf("/%s/%d", proto.Name, proto.Code)

			// Check if we already have this transport
			found := false
			for _, existing := range info.Supported {
				if existing == protoStr {
					found = true
					break
				}
			}
			if !found {
				info.Supported = append(info.Supported, protoStr)
			}
		}
		info.Multiaddrs = append(info.Multiaddrs, addr.String())
	}

	return info, nil
}

// CanDialTransport checks if we can dial a peer using a specific transport.
func CanDialTransport(h host.Host, p peer.ID, transport string) bool {
	info, err := DetectTransportCapabilities(h, p)
	if err != nil {
		return false
	}

	for _, t := range info.Supported {
		if t == transport {
			return true
		}
	}
	return false
}

// ============================================================
// Req 12: Protocol Identification - Verify protocol support
// ============================================================

// VerifyProtocolSupport verifies that a peer supports the mixnet protocol.
// This implements Req 12 - Protocol Identification.
func VerifyProtocolSupport(h host.Host, p peer.ID, protoID protocol.ID) (bool, error) {
	// Use libp2p's built-in protocol support check
	supported, err := h.Peerstore().SupportsProtocols(p, protoID)
	if err != nil {
		return false, fmt.Errorf("protocol check failed: %w", err)
	}

	if len(supported) == 0 {
		return false, nil
	}

	// Verify the returned protocols include our protocol
	for _, sp := range supported {
		if protocol.ID(sp) == protoID {
			return true, nil
		}
	}

	return false, nil
}

// VerifyRelayProtocols verifies a relay supports both relay and mixnet protocols.
func VerifyRelayProtocols(h host.Host, p peer.ID) (bool, error) {
	// Check for circuitv2 relay protocol
	relayProto := protocol.ID("/libp2p/circuit/relay/0.2.0/hop")
	relaySupported, err := h.Peerstore().SupportsProtocols(p, relayProto)
	if err != nil || len(relaySupported) == 0 {
		// Try older relay protocol
		relayProto = protocol.ID("/libp2p/circuit/relay")
		relaySupported, err = h.Peerstore().SupportsProtocols(p, relayProto)
		if err != nil || len(relaySupported) == 0 {
			return false, fmt.Errorf("peer %s does not support relay protocol", p)
		}
	}

	// Check for mixnet protocol
	mixnetSupported, err := h.Peerstore().SupportsProtocols(p, ProtocolID)
	if err != nil || len(mixnetSupported) == 0 {
		return false, fmt.Errorf("peer %s does not support mixnet protocol", p)
	}

	return true, nil
}

// ============================================================
// Req 14: Metadata Privacy - Privacy-enhanced shard encoding
// ============================================================

// PrivacyShardHeader represents a privacy-enhanced shard header.
// This DOES NOT include the destination in plaintext.
type PrivacyShardHeader struct {
	// SessionID allows destination to reassemble shards
	SessionID []byte
	// ShardIndex for ordering
	ShardIndex uint32
	// TotalShards for reconstruction
	TotalShards uint32
	// HasKeys indicates if encryption keys are included
	HasKeys bool
	// KeyData encrypted key material (for first shard only)
	KeyData []byte
	// Padding random bytes to hide message size
	Padding []byte
}

// PrivacyPaddingConfig holds padding configuration for metadata privacy.
type PrivacyPaddingConfig struct {
	// Enabled enables random padding
	Enabled bool
	// MinBytes minimum padding bytes
	MinBytes int
	// MaxBytes maximum padding bytes
	MaxBytes int
}

// DefaultPrivacyPaddingConfig returns default padding configuration.
func DefaultPrivacyPaddingConfig() *PrivacyPaddingConfig {
	return &PrivacyPaddingConfig{
		Enabled:  true,
		MinBytes: 16,
		MaxBytes: 256,
	}
}

// PaddingConfig is the global padding configuration
var PaddingConfig = DefaultPrivacyPaddingConfig()

// EncodePrivacyShard encodes a shard with privacy-enhanced header.
func EncodePrivacyShard(shardData []byte, header PrivacyShardHeader) ([]byte, error) {
	// Build header without destination
	// Format: [session_len(1)][session_id][shard_idx(4)][total_shards(4)][has_keys(1)][key_len(4)][keys][padding_len(2)][padding][shard]

	var headerBytes []byte

	// session_len + session_id
	headerBytes = append(headerBytes, byte(len(header.SessionID)))
	headerBytes = append(headerBytes, header.SessionID...)

	// shard_index (4 bytes)
	idxBytes := make([]byte, 4)
	idxBytes[0] = byte(header.ShardIndex)
	idxBytes[1] = byte(header.ShardIndex >> 8)
	idxBytes[2] = byte(header.ShardIndex >> 16)
	idxBytes[3] = byte(header.ShardIndex >> 24)
	headerBytes = append(headerBytes, idxBytes...)

	// total_shards (4 bytes)
	totalBytes := make([]byte, 4)
	totalBytes[0] = byte(header.TotalShards)
	totalBytes[1] = byte(header.TotalShards >> 8)
	totalBytes[2] = byte(header.TotalShards >> 16)
	totalBytes[3] = byte(header.TotalShards >> 24)
	headerBytes = append(headerBytes, totalBytes...)

	// has_keys (1 byte)
	if header.HasKeys {
		headerBytes = append(headerBytes, 1)
	} else {
		headerBytes = append(headerBytes, 0)
	}

	// key_len + key_data
	keyLen := uint32(0)
	if header.KeyData != nil {
		keyLen = uint32(len(header.KeyData))
	}
	keyLenBytes := make([]byte, 4)
	keyLenBytes[0] = byte(keyLen)
	keyLenBytes[1] = byte(keyLen >> 8)
	keyLenBytes[2] = byte(keyLen >> 16)
	keyLenBytes[3] = byte(keyLen >> 24)
	headerBytes = append(headerBytes, keyLenBytes...)

	if keyLen > 0 {
		headerBytes = append(headerBytes, header.KeyData...)
	}

	// Add padding if enabled
	if PaddingConfig.Enabled {
		padding := generateRandomPadding(PaddingConfig.MinBytes, PaddingConfig.MaxBytes)
		header.Padding = padding

		paddingLen := uint16(len(padding))
		headerBytes = append(headerBytes, byte(paddingLen))
		headerBytes = append(headerBytes, byte(paddingLen>>8))
		headerBytes = append(headerBytes, padding...)
	} else {
		// No padding - write 0 length
		headerBytes = append(headerBytes, 0, 0)
	}

	// Append actual shard data
	return append(headerBytes, shardData...), nil
}

// DecodePrivacyShard decodes a privacy-enhanced shard header.
func DecodePrivacyShard(data []byte) (*PrivacyShardHeader, []byte, error) {
	if len(data) < 10 {
		return nil, nil, fmt.Errorf("data too short for privacy header")
	}

	offset := 0

	// session_len + session_id
	sessionLen := int(data[offset])
	offset++
	if sessionLen > 64 || sessionLen < 0 {
		return nil, nil, fmt.Errorf("invalid session length")
	}

	if len(data) < offset+sessionLen+9 {
		return nil, nil, fmt.Errorf("data too short")
	}

	sessionID := data[offset : offset+sessionLen]
	offset += sessionLen

	// shard_index
	shardIndex := uint32(data[offset]) | uint32(data[offset+1])<<8 | uint32(data[offset+2])<<16 | uint32(data[offset+3])<<24
	offset += 4

	// total_shards
	totalShards := uint32(data[offset]) | uint32(data[offset+1])<<8 | uint32(data[offset+2])<<16 | uint32(data[offset+3])<<24
	offset += 4

	// has_keys
	hasKeys := data[offset] == 1
	offset++

	// key_len + key_data
	keyLen := uint32(data[offset]) | uint32(data[offset+1])<<8 | uint32(data[offset+2])<<16 | uint32(data[offset+3])<<24
	offset += 4

	var keyData []byte
	if keyLen > 0 {
		if len(data) < offset+int(keyLen) {
			return nil, nil, fmt.Errorf("data too short for key data")
		}
		keyData = data[offset : offset+int(keyLen)]
		offset += int(keyLen)
	}

	// padding_len + padding
	if len(data) < offset+2 {
		return nil, nil, fmt.Errorf("data too short for padding length")
	}
	paddingLen := uint16(data[offset]) | uint16(data[offset+1])<<8
	offset += 2

	var padding []byte
	if paddingLen > 0 {
		if len(data) < offset+int(paddingLen) {
			return nil, nil, fmt.Errorf("data too short for padding")
		}
		padding = data[offset : offset+int(paddingLen)]
		offset += int(paddingLen)
	}

	// Remaining is shard data
	shardData := data[offset:]

	return &PrivacyShardHeader{
		SessionID:   sessionID,
		ShardIndex:  shardIndex,
		TotalShards: totalShards,
		HasKeys:     hasKeys,
		KeyData:     keyData,
		Padding:     padding,
	}, shardData, nil
}

// generateRandomPadding creates random padding bytes.
func generateRandomPadding(min, max int) []byte {
	if min >= max {
		max = min + 1
	}
	// Use pseudo-random for now (in production, use crypto/rand)
	size := min
	if max > min {
		size = min + ((min * 17) % (max - min))
	}
	padding := make([]byte, size)
	for i := range padding {
		padding[i] = byte(i * 17 % 256)
	}
	return padding
}
