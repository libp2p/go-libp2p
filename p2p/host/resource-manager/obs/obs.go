// Package obs implements metrics tracing for resource manager
//
// Deprecated: obs is deprecated and the exported types and methods
// are moved to rcmgr package. Use the corresponding identifier in
// the rcmgr package, for example
// obs.NewStatsTraceReporter => rcmgr.NewStatsTraceReporter
package obs

import (
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
)

var MustRegisterWith = rcmgr.MustRegisterWith

// StatsTraceReporter reports stats on the resource manager using its traces.
type StatsTraceReporter = rcmgr.StatsTraceReporter

var NewStatsTraceReporter = rcmgr.NewStatsTraceReporter
