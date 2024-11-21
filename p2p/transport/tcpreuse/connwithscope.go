package tcpreuse

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse/internal/sampledconn"
	manet "github.com/multiformats/go-multiaddr/net"
)

type connWithScope struct {
	sampledconn.ManetTCPConnInterface
	scope network.ConnManagementScope
}

func (c connWithScope) Scope() network.ConnManagementScope {
	return c.scope
}

func manetConnWithScope(c manet.Conn, scope network.ConnManagementScope) (manet.Conn, error) {
	if tcpconn, ok := c.(sampledconn.ManetTCPConnInterface); ok {
		return &connWithScope{tcpconn, scope}, nil
	}

	return nil, fmt.Errorf("manet.Conn is not a TCP Conn")
}
