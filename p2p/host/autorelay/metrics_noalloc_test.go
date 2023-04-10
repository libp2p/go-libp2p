//go:build nocover

package autorelay

import (
	"math/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	pbv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
)

func getRandScheduledWork() scheduledWorkTimes {
	randTime := func() time.Time {
		return time.Now().Add(time.Duration(rand.Intn(10)) * time.Second)
	}
	return scheduledWorkTimes{
		leastFrequentInterval:       0,
		nextRefresh:                 randTime(),
		nextBackoff:                 randTime(),
		nextOldCandidateCheck:       randTime(),
		nextAllowedCallToPeerSource: randTime(),
	}
}

func TestMetricsNoAllocNoCover(t *testing.T) {
	scheduledWork := []scheduledWorkTimes{}
	for i := 0; i < 10; i++ {
		scheduledWork = append(scheduledWork, getRandScheduledWork())
	}
	errs := []error{
		client.ReservationError{Status: pbv2.Status_MALFORMED_MESSAGE},
		client.ReservationError{Status: pbv2.Status_MALFORMED_MESSAGE},
		nil,
	}
	tr := NewMetricsTracer()
	tests := map[string]func(){
		"RelayFinderStatus":          func() { tr.RelayFinderStatus(rand.Intn(2) == 1) },
		"ReservationEnded":           func() { tr.ReservationEnded(rand.Intn(10)) },
		"ReservationRequestFinished": func() { tr.ReservationRequestFinished(rand.Intn(2) == 1, errs[rand.Intn(len(errs))]) },
		"RelayAddressCount":          func() { tr.RelayAddressCount(rand.Intn(10)) },
		"RelayAddressUpdated":        func() { tr.RelayAddressUpdated() },
		"ReservationOpened":          func() { tr.ReservationOpened(rand.Intn(10)) },
		"CandidateChecked":           func() { tr.CandidateChecked(rand.Intn(2) == 1) },
		"CandidateAdded":             func() { tr.CandidateAdded(rand.Intn(10)) },
		"CandidateRemoved":           func() { tr.CandidateRemoved(rand.Intn(10)) },
		"ScheduledWorkUpdated":       func() { tr.ScheduledWorkUpdated(&scheduledWork[rand.Intn(len(scheduledWork))]) },
		"DesiredReservations":        func() { tr.DesiredReservations(rand.Intn(10)) },
		"CandidateLoopState":         func() { tr.CandidateLoopState(candidateLoopState(rand.Intn(10))) },
	}
	for method, f := range tests {
		allocs := testing.AllocsPerRun(1000, f)

		if allocs > 0 {
			t.Fatalf("Alloc Test: %s, got: %0.2f, expected: 0 allocs", method, allocs)
		}
	}
}
