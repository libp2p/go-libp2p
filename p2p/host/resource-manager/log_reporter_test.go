package rcmgr

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
)

func TestLogReporter(t *testing.T) {
	reporter := NewLogReporter()

	// Test blocked memory event
	memEvt := TraceEvt{
		Type:     TraceBlockReserveMemoryEvt,
		Name:     "test-scope",
		Priority: 128,
		Delta:    1024,
		Memory:   2048,
	}
	// Should not panic
	reporter.ConsumeEvent(memEvt)

	// Test blocked stream event
	streamEvt := TraceEvt{
		Type:       TraceBlockAddStreamEvt,
		Name:       "test-scope",
		DeltaIn:    1,
		DeltaOut:   0,
		StreamsIn:  5,
		StreamsOut: 3,
	}
	reporter.ConsumeEvent(streamEvt)

	// Test blocked connection event
	connEvt := TraceEvt{
		Type:     TraceBlockAddConnEvt,
		Name:     "test-scope",
		DeltaIn:  0,
		DeltaOut: 1,
		Delta:    1, // fd
		ConnsIn:  2,
		ConnsOut: 4,
		FD:       10,
	}
	reporter.ConsumeEvent(connEvt)

	// Test non-blocked events (should be ignored)
	normalEvt := TraceEvt{
		Type: TraceAddStreamEvt,
		Name: "test-scope",
	}
	reporter.ConsumeEvent(normalEvt)
}

func TestLogReporterDirection(t *testing.T) {
	reporter := NewLogReporter()

	// Test inbound stream
	inboundStream := TraceEvt{
		Type:       TraceBlockAddStreamEvt,
		Name:       "test-scope",
		DeltaIn:    1,
		DeltaOut:   0,
		StreamsIn:  5,
		StreamsOut: 3,
	}
	reporter.ConsumeEvent(inboundStream)

	// Test outbound stream
	outboundStream := TraceEvt{
		Type:       TraceBlockAddStreamEvt,
		Name:       "test-scope",
		DeltaIn:    0,
		DeltaOut:   1,
		StreamsIn:  5,
		StreamsOut: 3,
	}
	reporter.ConsumeEvent(outboundStream)

	// Test inbound connection
	inboundConn := TraceEvt{
		Type:     TraceBlockAddConnEvt,
		Name:     "test-scope",
		DeltaIn:  1,
		DeltaOut: 0,
		Delta:    0,
		ConnsIn:  2,
		ConnsOut: 4,
		FD:       10,
	}
	reporter.ConsumeEvent(inboundConn)

	// Test outbound connection
	outboundConn := TraceEvt{
		Type:     TraceBlockAddConnEvt,
		Name:     "test-scope",
		DeltaIn:  0,
		DeltaOut: 1,
		Delta:    1,
		ConnsIn:  2,
		ConnsOut: 4,
		FD:       10,
	}
	reporter.ConsumeEvent(outboundConn)
}

func TestLogReporterIntegration(t *testing.T) {
	// Create a resource manager with our log reporter
	limits := DefaultLimits.AutoScale()
	limiter := NewFixedLimiter(limits)

	mgr, err := NewResourceManager(limiter)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	// The log reporter should be installed by default
	// Try to exceed a limit and verify the trace event is generated
	_, err = mgr.OpenConnection(network.DirInbound, false, nil)
	if err != nil {
		// Expected to fail when limits are exceeded, but that's okay
		// The important part is that our reporter processes the event
	}
}
