package libp2pfx

import (
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"go.uber.org/fx"
)

func IdentifyService(opts ...identify.Option) fx.Option {
	return fx.Provide(func(l fx.Lifecycle, h host.Host) (identify.IDService, error) {
		s, err := identify.NewIDService(h, opts...)
		if err != nil {
			return nil, err
		}
		l.Append(fx.StartStopHook(s.Start, s.Close))
		return s, nil
	})
}

var PingService = fx.Provide(func(l fx.Lifecycle, h host.Host) *ping.PingService {
	s := &ping.PingService{Host: h}
	l.Append(fx.StartStopHook(s.Start, s.Stop))
	return s
})
