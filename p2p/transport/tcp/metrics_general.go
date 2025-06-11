//go:build !linux && !darwin && !windows && !riscv64 && !loong64

package tcp

import "github.com/mikioh/tcpinfo"

const (
	hasSegmentCounter = false
	hasByteCounter    = false
)

func getSegmentsSent(_ *tcpinfo.Info) uint64 { return 0 }
func getSegmentsRcvd(_ *tcpinfo.Info) uint64 { return 0 }
func getBytesSent(_ *tcpinfo.Info) uint64    { return 0 }
func getBytesRcvd(_ *tcpinfo.Info) uint64    { return 0 }
