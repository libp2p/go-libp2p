// riscv64 see: https://github.com/marten-seemann/tcp/pull/1

//go:build windows || riscv64 || loong64

package tcp

import manet "github.com/multiformats/go-multiaddr/net"

type aggregatingCollector struct{}

func newTracingConn(c manet.Conn, collector *aggregatingCollector, isClient bool) (manet.Conn, error) {
	return c, nil
}
func newTracingListener(l manet.Listener, collector *aggregatingCollector) manet.Listener { return l }
