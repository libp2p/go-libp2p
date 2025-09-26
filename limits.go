package libp2p

import (
	"github.com/libp2p/go-libp2p/internal/limits"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
)

// SetDefaultServiceLimits sets the default limits for bundled libp2p services
func SetDefaultServiceLimits(config *rcmgr.ScalingLimitConfig) {
	limits.SetDefaultServiceLimits(config)
}
