//go:build nocover

package rcmgr

import (
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func randomTraceEvt(rng *rand.Rand) TraceEvt {
	// Possibly non-sensical
	typs := []TraceEvtTyp{
		TraceStartEvt,
		TraceCreateScopeEvt,
		TraceDestroyScopeEvt,
		TraceReserveMemoryEvt,
		TraceBlockReserveMemoryEvt,
		TraceReleaseMemoryEvt,
		TraceAddStreamEvt,
		TraceBlockAddStreamEvt,
		TraceRemoveStreamEvt,
		TraceAddConnEvt,
		TraceBlockAddConnEvt,
		TraceRemoveConnEvt,
	}

	names := []string{
		"conn-1",
		"stream-2",
		"peer:abc",
		"system",
		"transient",
		"peer:12D3Koo",
		"protocol:/libp2p/autonat/1.0.0",
		"protocol:/libp2p/autonat/1.0.0.peer:12D3Koo",
		"service:libp2p.autonat",
		"service:libp2p.autonat.peer:12D3Koo",
	}

	return TraceEvt{
		Type:       typs[rng.Intn(len(typs))],
		Name:       names[rng.Intn(len(names))],
		DeltaOut:   rng.Intn(5),
		DeltaIn:    rng.Intn(5),
		Delta:      int64(rng.Intn(5)),
		Memory:     int64(rng.Intn(10000)),
		StreamsIn:  rng.Intn(100),
		StreamsOut: rng.Intn(100),
		ConnsIn:    rng.Intn(100),
		ConnsOut:   rng.Intn(100),
		FD:         rng.Intn(100),
		Time:       time.Now().Format(time.RFC3339Nano),
	}

}

var regOnce sync.Once

func BenchmarkMetricsRecording(b *testing.B) {
	b.ReportAllocs()

	registerOnce.Do(func() {
		MustRegisterWith(prometheus.DefaultRegisterer)
	})

	evtCount := 10000
	evts := make([]TraceEvt, evtCount)
	rng := rand.New(rand.NewSource(int64(b.N)))
	for i := 0; i < evtCount; i++ {
		evts[i] = randomTraceEvt(rng)
	}

	str, err := NewStatsTraceReporter()
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		str.ConsumeEvent(evts[i%len(evts)])
	}
}

func TestNoAllocsNoCover(t *testing.T) {
	str, err := NewStatsTraceReporter()
	require.NoError(t, err)

	evtCount := 10_000
	evts := make([]TraceEvt, 0, evtCount)
	rng := rand.New(rand.NewSource(1))

	for i := 0; i < evtCount; i++ {
		evts = append(evts, randomTraceEvt(rng))
	}

	tagSlice := make([]string, 0, 10)
	allocs := testing.AllocsPerRun(100, func() {
		for i := 0; i < evtCount; i++ {
			str.consumeEventWithLabelSlice(evts[i], &tagSlice)
		}
	})

	if allocs > 10 {
		t.Fatalf("expected less than 10 heap bytes, got %f", allocs)
	}
}
