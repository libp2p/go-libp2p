package leaky_test

import (
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p"
)

func TestBadTransportConstructor(t *testing.T) {
	h, err := libp2p.New(libp2p.Transport(func() {}))
	if err == nil {
		h.Close()
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "_test.go") {
		t.Error("expected error to contain debugging info")
	}
}
