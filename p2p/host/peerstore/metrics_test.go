package peerstore

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/test"
)

func TestLatencyEWMAFun(t *testing.T) {
	t.Skip("run it for fun")

	m := NewMetrics()
	id, err := test.RandPeerID()
	if err != nil {
		t.Fatal(err)
	}

	mu := 100.0
	sig := 10.0
	next := func() time.Duration {
		mu = (rand.NormFloat64() * sig) + mu
		return time.Duration(mu)
	}

	print := func() {
		fmt.Printf("%3.f %3.f --> %d\n", sig, mu, m.LatencyEWMA(id))
	}

	for {
		time.Sleep(200 * time.Millisecond)
		m.RecordLatency(id, next())
		print()
	}
}

func TestLatencyEWMA(t *testing.T) {
	m := NewMetrics()
	id, err := test.RandPeerID()
	if err != nil {
		t.Fatal(err)
	}

	const exp = 100
	const mu = exp
	const sig = 10
	next := func() time.Duration { return time.Duration(rand.Intn(20) - 10 + mu) }

	for range 10 {
		m.RecordLatency(id, next())
	}

	lat := m.LatencyEWMA(id)
	diff := exp - lat
	if diff < 0 {
		diff = -diff
	}
	if diff > sig {
		t.Fatalf("latency outside of expected range. expected %d ± %d, got %d", exp, sig, lat)
	}
}
