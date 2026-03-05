package mixnet

import (
	"testing"
)

func TestMixnetStream_OpenStream(t *testing.T) {
	if DefaultConfig() == nil {
		t.Fatal("expected default config")
	}
}
