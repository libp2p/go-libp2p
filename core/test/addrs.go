package test

import (
	"fmt"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
)

// GenerateTestAddrs generates a slice of ma.Multiaddr
func GenerateTestAddrs(n int) []ma.Multiaddr {
	out := make([]ma.Multiaddr, n)
	for i := 0; i < n; i++ {
		a, err := ma.NewMultiaddr(fmt.Sprintf("/ip4/1.2.3.4/tcp/%d", i))
		if err != nil {
			continue
		}
		out[i] = a
	}
	return out
}

// AssertAddressesEqual checks that two slices of ma.Multiaddr are same length
func AssertAddressesEqual(t *testing.T, exp, act []ma.Multiaddr) {
	t.Helper()
	if len(exp) != len(act) {
		t.Fatalf("lengths not the same. expected %d, got %d\n", len(exp), len(act))
	}

	for _, a := range exp {
		found := false

		for _, b := range act {
			if a.Equal(b) {
				found = true
				break
			}
		}

		if !found {
			t.Fatalf("expected address %s not found", a)
		}
	}
}
