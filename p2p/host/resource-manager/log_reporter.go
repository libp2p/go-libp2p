package rcmgr

import (
	"github.com/libp2p/go-libp2p/core/network"
)

// LogReporter is a trace reporter that logs blocked resource events.
// It consumes trace events and produces debug logs, ensuring consistency
// between logs and metrics by using the same trace event source.
type LogReporter struct{}

var _ TraceReporter = (*LogReporter)(nil)

// NewLogReporter creates a new LogReporter instance.
func NewLogReporter() LogReporter {
	return LogReporter{}
}

// ConsumeEvent implements TraceReporter by logging blocked resource events.
func (r LogReporter) ConsumeEvent(evt TraceEvt) {
	switch evt.Type {
	case TraceBlockReserveMemoryEvt:
		r.logBlockedMemory(evt)
	case TraceBlockAddStreamEvt:
		r.logBlockedStream(evt)
	case TraceBlockAddConnEvt:
		r.logBlockedConn(evt)
	}
}

func (r LogReporter) logBlockedMemory(evt TraceEvt) {
	logValues := make([]interface{}, 0, 2*6)
	logValues = append(logValues,
		"scope", evt.Name,
		"priority", evt.Priority,
		"current", evt.Memory,
		"attempted", evt.Delta,
	)

	log.Debug("blocked memory reservation", logValues...)
}

func (r LogReporter) logBlockedStream(evt TraceEvt) {
	logValues := make([]interface{}, 0, 2*6)

	var dir network.Direction
	if evt.DeltaIn != 0 {
		dir = network.DirInbound
	} else {
		dir = network.DirOutbound
	}

	logValues = append(logValues,
		"scope", evt.Name,
		"direction", dir,
		"inbound", evt.StreamsIn,
		"outbound", evt.StreamsOut,
	)

	log.Debug("blocked stream", logValues...)
}

func (r LogReporter) logBlockedConn(evt TraceEvt) {
	logValues := make([]interface{}, 0, 2*7)

	var dir network.Direction
	if evt.DeltaIn != 0 {
		dir = network.DirInbound
	} else {
		dir = network.DirOutbound
	}

	usefd := evt.Delta != 0

	logValues = append(logValues,
		"scope", evt.Name,
		"direction", dir,
		"usefd", usefd,
		"inbound", evt.ConnsIn,
		"outbound", evt.ConnsOut,
		"fd", evt.FD,
	)

	log.Debug("blocked connection", logValues...)
}
