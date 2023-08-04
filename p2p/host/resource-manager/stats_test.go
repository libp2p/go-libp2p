package rcmgr

import (
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var registerOnce sync.Once

func TestTraceReporterStartAndClose(t *testing.T) {
	rcmgr, err := NewResourceManager(NewFixedLimiter(DefaultLimits.AutoScale()), WithTraceReporter(StatsTraceReporter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer rcmgr.Close()
}

func TestConsumeEvent(t *testing.T) {
	evt := TraceEvt{
		Type:     TraceBlockAddStreamEvt,
		Name:     "conn-1",
		DeltaOut: 1,
		Time:     time.Now().Format(time.RFC3339Nano),
	}

	registerOnce.Do(func() {
		MustRegisterWith(prometheus.DefaultRegisterer)
	})

	str, err := NewStatsTraceReporter()
	if err != nil {
		t.Fatal(err)
	}

	str.ConsumeEvent(evt)
}
