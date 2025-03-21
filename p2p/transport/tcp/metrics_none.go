// riscv64 see: https://github.com/marten-seemann/tcp/pull/1

//go:build windows || riscv64 || loong64

package tcp

import (
	"github.com/libp2p/go-libp2p/core/transport"
	manet "github.com/multiformats/go-multiaddr/net"
)

type aggregatingCollector struct{}

func newTracingConn(c manet.Conn, collector *aggregatingCollector, isClient bool) (manet.Conn, error) {
	return c, nil
}
func newTracingListener(l transport.GatedMaListener, collector *aggregatingCollector) transport.GatedMaListener {
	return l
}
