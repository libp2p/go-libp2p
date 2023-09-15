package libp2pwebrtc

// The code in this file is adapted from the Go standard library's hex package.
// As found in https://cs.opensource.google/go/go/+/refs/tags/go1.20.2:src/encoding/hex/hex.go
//
// The reason we adapted the original code is to allow us to deal with interspersed requirements
// while at the same time hex encoding/decoding, without having to do so in two passes.

import (
	"encoding/hex"
	"errors"
)

// encodeInterspersedHex encodes a byte slice into a string of hex characters,
// separating each encoded byte with a colon (':').
//
// Example: { 0x01, 0x02, 0x03 } -> "01:02:03"
func encodeInterspersedHex(src []byte) string {
	if len(src) == 0 {
		return ""
	}
	s := hex.EncodeToString(src)
	n := len(s)
	// Determine number of colons
	colons := n / 2
	if n%2 == 0 {
		colons--
	}
	buffer := make([]byte, n+colons)

	for i, j := 0, 0; i < n; i, j = i+2, j+3 {
		copy(buffer[j:j+2], s[i:i+2])
		if j+3 < len(buffer) {
			buffer[j+2] = ':'
		}
	}
	return string(buffer)
}

var errUnexpectedIntersperseHexChar = errors.New("unexpected character in interspersed hex string")

// decodeInterspersedHexFromASCIIString decodes an ASCII string of hex characters into a byte slice,
// where the hex characters are expected to be separated by a colon (':').
//
// NOTE that this function returns an error in case the input string contains non-ASCII characters.
//
// Example: "01:02:03" -> { 0x01, 0x02, 0x03 }
func decodeInterspersedHexFromASCIIString(s string) ([]byte, error) {
	n := len(s)
	buffer := make([]byte, n/3*2+n%3)
	j := 0
	for i := 0; i < n; i++ {
		if i%3 == 2 {
			if s[i] != ':' {
				return nil, errUnexpectedIntersperseHexChar
			}
		} else {
			if s[i] == ':' {
				return nil, errUnexpectedIntersperseHexChar
			}
			buffer[j] = s[i]
			j++
		}
	}
	dst := make([]byte, hex.DecodedLen(len(buffer)))
	if _, err := hex.Decode(dst, buffer); err != nil {
		return nil, err
	}
	return dst, nil
}
