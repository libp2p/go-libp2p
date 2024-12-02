package tcpreuse

import (
	"context"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// DialContext is like Dial but takes a context.
func (t *ConnMgr) DialContext(ctx context.Context, raddr ma.Multiaddr) (manet.Conn, error) {
	if t.useReuseport() {
		return t.reuse.DialContext(ctx, raddr)
	}
	var d manet.Dialer
	return d.DialContext(ctx, raddr)
}
