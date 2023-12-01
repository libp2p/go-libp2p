package crypto

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
)

// NetworkCookie provides a way for preventing peers from different
// networks from connecting to each other
type NetworkCookie []byte

// Empty returns true if the network cookie is empty
func (nc NetworkCookie) Empty() bool {
	return len(nc) == 0
}

// String returns string representation of the NetworkCookie, which is
// a hex string
func (nc NetworkCookie) String() string {
	return hex.EncodeToString(nc)
}

// Equal returns true if this cookie is the same as the other cookie
func (nc NetworkCookie) Equal(other NetworkCookie) bool {
	return bytes.Equal(nc, other)
}

// ParseNetworkCookie parses a hex string into a NetworkCookie
func ParseNetworkCookie(s string) (NetworkCookie, error) {
	if s == "" {
		return nil, nil
	}
	parsed, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("error decoding network cookie hex string: %w", err)
	}
	if len(parsed) > 255 {
		return nil, errors.New("network cookie string too long")
	}
	return parsed, nil
}

type privKeyWithCookie struct {
	PrivKey
	nc NetworkCookie
}

// AddCookieToPrivKey adds network cookie to private key
// If nc is an empty NetworkCookie, the function just returns pk
func AddNetworkCookieToPrivKey(pk PrivKey, nc NetworkCookie) PrivKey {
	if nc.Empty() {
		return pk
	}

	return &privKeyWithCookie{
		PrivKey: pk,
		nc:      nc,
	}
}

// StripNetworkCookieFromPrivKey removes network cookie from the
// private key, if it's present
func StripNetworkCookieFromPrivKey(pk PrivKey) PrivKey {
	if pkc, ok := pk.(*privKeyWithCookie); ok {
		return pkc.PrivKey
	}

	return pk
}

// NetworkCookieFromPrivKey extracts network cookie from PrivKey,
// if it's present there. If it's not, the function returns an empty
// NetworkCookie
func NetworkCookieFromPrivKey(pk PrivKey) NetworkCookie {
	if pkc, ok := pk.(*privKeyWithCookie); ok {
		return pkc.nc
	}

	return nil
}
