package test

import (
	"fmt"
	"slices"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
)

func GenerateTestAddrs(n int) []ma.Multiaddr {
	out := make([]ma.Multiaddr, n)
	for i := range n {
		a, err := ma.NewMultiaddr(fmt.Sprintf("/ip4/1.2.3.4/tcp/%d", i))
		if err != nil {
			continue
		}
		out[i] = a
	}
	return out
}

func AssertAddressesEqual(t *testing.T, exp, act []ma.Multiaddr) {
	t.Helper()
	if len(exp) != len(act) {
		t.Fatalf("lengths not the same. expected %d, got %d\n", len(exp), len(act))
	}

	for _, a := range exp {
		found := slices.ContainsFunc(act, a.Equal)

		if !found {
			t.Fatalf("expected address %s not found", a)
		}
	}
}
