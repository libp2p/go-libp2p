package tcpreuse

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse/internal/sampledconn"
	manet "github.com/multiformats/go-multiaddr/net"
)

type connWithScope struct {
	sampledconn.ManetTCPConnInterface
	ConnScope network.ConnManagementScope
}

func (c *connWithScope) Close() error {
	defer c.ConnScope.Done()
	return c.ManetTCPConnInterface.Close()
}

func manetConnWithScope(c manet.Conn, scope network.ConnManagementScope) (*connWithScope, error) {
	if tcpconn, ok := c.(sampledconn.ManetTCPConnInterface); ok {
		return &connWithScope{tcpconn, scope}, nil
	}

	return nil, fmt.Errorf("manet.Conn is not a TCP Conn")
}
