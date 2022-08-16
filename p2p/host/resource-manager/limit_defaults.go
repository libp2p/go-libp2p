package rcmgr

import (
	"encoding/json"
	"math"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"

	"github.com/pbnjay/memory"
)

type baseLimitConfig struct {
	BaseLimit         BaseLimit
	BaseLimitIncrease BaseLimitIncrease
}

// ScalingLimitConfig is a struct for configuring default limits.
// {}BaseLimit is the limits that Apply for a minimal node (128 MB of memory for libp2p) and 256 file descriptors.
// {}LimitIncrease is the additional limit granted for every additional 1 GB of RAM.
type ScalingLimitConfig struct {
	SystemBaseLimit     BaseLimit
	SystemLimitIncrease BaseLimitIncrease

	TransientBaseLimit     BaseLimit
	TransientLimitIncrease BaseLimitIncrease

	AllowlistedSystemBaseLimit     BaseLimit
	AllowlistedSystemLimitIncrease BaseLimitIncrease

	AllowlistedTransientBaseLimit     BaseLimit
	AllowlistedTransientLimitIncrease BaseLimitIncrease

	ServiceBaseLimit     BaseLimit
	ServiceLimitIncrease BaseLimitIncrease
	ServiceLimits        map[string]baseLimitConfig // use AddServiceLimit to modify

	ServicePeerBaseLimit     BaseLimit
	ServicePeerLimitIncrease BaseLimitIncrease
	ServicePeerLimits        map[string]baseLimitConfig // use AddServicePeerLimit to modify

	ProtocolBaseLimit     BaseLimit
	ProtocolLimitIncrease BaseLimitIncrease
	ProtocolLimits        map[protocol.ID]baseLimitConfig // use AddProtocolLimit to modify

	ProtocolPeerBaseLimit     BaseLimit
	ProtocolPeerLimitIncrease BaseLimitIncrease
	ProtocolPeerLimits        map[protocol.ID]baseLimitConfig // use AddProtocolPeerLimit to modify

	PeerBaseLimit     BaseLimit
	PeerLimitIncrease BaseLimitIncrease
	PeerLimits        map[peer.ID]baseLimitConfig // use AddPeerLimit to modify

	ConnBaseLimit     BaseLimit
	ConnLimitIncrease BaseLimitIncrease

	StreamBaseLimit     BaseLimit
	StreamLimitIncrease BaseLimitIncrease
}

func (cfg *ScalingLimitConfig) AddServiceLimit(svc string, base BaseLimit, inc BaseLimitIncrease) {
	if cfg.ServiceLimits == nil {
		cfg.ServiceLimits = make(map[string]baseLimitConfig)
	}
	cfg.ServiceLimits[svc] = baseLimitConfig{
		BaseLimit:         base,
		BaseLimitIncrease: inc,
	}
}

func (cfg *ScalingLimitConfig) AddProtocolLimit(proto protocol.ID, base BaseLimit, inc BaseLimitIncrease) {
	if cfg.ProtocolLimits == nil {
		cfg.ProtocolLimits = make(map[protocol.ID]baseLimitConfig)
	}
	cfg.ProtocolLimits[proto] = baseLimitConfig{
		BaseLimit:         base,
		BaseLimitIncrease: inc,
	}
}

func (cfg *ScalingLimitConfig) AddPeerLimit(p peer.ID, base BaseLimit, inc BaseLimitIncrease) {
	if cfg.PeerLimits == nil {
		cfg.PeerLimits = make(map[peer.ID]baseLimitConfig)
	}
	cfg.PeerLimits[p] = baseLimitConfig{
		BaseLimit:         base,
		BaseLimitIncrease: inc,
	}
}

func (cfg *ScalingLimitConfig) AddServicePeerLimit(svc string, base BaseLimit, inc BaseLimitIncrease) {
	if cfg.ServicePeerLimits == nil {
		cfg.ServicePeerLimits = make(map[string]baseLimitConfig)
	}
	cfg.ServicePeerLimits[svc] = baseLimitConfig{
		BaseLimit:         base,
		BaseLimitIncrease: inc,
	}
}

func (cfg *ScalingLimitConfig) AddProtocolPeerLimit(proto protocol.ID, base BaseLimit, inc BaseLimitIncrease) {
	if cfg.ProtocolPeerLimits == nil {
		cfg.ProtocolPeerLimits = make(map[protocol.ID]baseLimitConfig)
	}
	cfg.ProtocolPeerLimits[proto] = baseLimitConfig{
		BaseLimit:         base,
		BaseLimitIncrease: inc,
	}
}

type LimitConfig struct {
	System    BaseLimit `json:",omitempty"`
	Transient BaseLimit `json:",omitempty"`

	// Limits that are applied to resources with an allowlisted multiaddr.
	// These will only be used if the normal System & Transient limits are
	// reached.
	AllowlistedSystem    BaseLimit `json:",omitempty"`
	AllowlistedTransient BaseLimit `json:",omitempty"`

	ServiceDefault BaseLimit            `json:",omitempty"`
	Service        map[string]BaseLimit `json:",omitempty"`

	ServicePeerDefault BaseLimit            `json:",omitempty"`
	ServicePeer        map[string]BaseLimit `json:",omitempty"`

	ProtocolDefault BaseLimit                 `json:",omitempty"`
	Protocol        map[protocol.ID]BaseLimit `json:",omitempty"`

	ProtocolPeerDefault BaseLimit                 `json:",omitempty"`
	ProtocolPeer        map[protocol.ID]BaseLimit `json:",omitempty"`

	PeerDefault BaseLimit             `json:",omitempty"`
	Peer        map[peer.ID]BaseLimit `json:",omitempty"`

	Conn   BaseLimit `json:",omitempty"`
	Stream BaseLimit `json:",omitempty"`
}

func (cfg *LimitConfig) MarshalJSON() ([]byte, error) {
	// we want to marshal the encoded peer id
	encodedPeerMap := make(map[string]BaseLimit, len(cfg.Peer))
	for p, v := range cfg.Peer {
		encodedPeerMap[peer.Encode(p)] = v
	}

	type Alias LimitConfig
	return json.Marshal(&struct {
		*Alias
		Peer map[string]BaseLimit `json:",omitempty"`
	}{
		Alias: (*Alias)(cfg),
		Peer:  encodedPeerMap,
	})
}

func (cfg *LimitConfig) Apply(c LimitConfig) {
	cfg.System.Apply(c.System)
	cfg.Transient.Apply(c.Transient)
	cfg.AllowlistedSystem.Apply(c.AllowlistedSystem)
	cfg.AllowlistedTransient.Apply(c.AllowlistedTransient)
	cfg.ServiceDefault.Apply(c.ServiceDefault)
	cfg.ProtocolDefault.Apply(c.ProtocolDefault)
	cfg.ProtocolPeerDefault.Apply(c.ProtocolPeerDefault)
	cfg.PeerDefault.Apply(c.PeerDefault)
	cfg.Conn.Apply(c.Conn)
	cfg.Stream.Apply(c.Stream)

	// TODO: the following could be solved a lot nicer, if only we could use generics
	for s, l := range cfg.Service {
		r := cfg.ServiceDefault
		if l2, ok := c.Service[s]; ok {
			r = l2
		}
		l.Apply(r)
		cfg.Service[s] = l
	}
	if c.Service != nil && cfg.Service == nil {
		cfg.Service = make(map[string]BaseLimit)
	}
	for s, l := range c.Service {
		if _, ok := cfg.Service[s]; !ok {
			cfg.Service[s] = l
		}
	}

	for s, l := range cfg.ServicePeer {
		r := cfg.ServicePeerDefault
		if l2, ok := c.ServicePeer[s]; ok {
			r = l2
		}
		l.Apply(r)
		cfg.ServicePeer[s] = l
	}
	if c.ServicePeer != nil && cfg.ServicePeer == nil {
		cfg.ServicePeer = make(map[string]BaseLimit)
	}
	for s, l := range c.ServicePeer {
		if _, ok := cfg.ServicePeer[s]; !ok {
			cfg.ServicePeer[s] = l
		}
	}

	for s, l := range cfg.Protocol {
		r := cfg.ProtocolDefault
		if l2, ok := c.Protocol[s]; ok {
			r = l2
		}
		l.Apply(r)
		cfg.Protocol[s] = l
	}
	if c.Protocol != nil && cfg.Protocol == nil {
		cfg.Protocol = make(map[protocol.ID]BaseLimit)
	}
	for s, l := range c.Protocol {
		if _, ok := cfg.Protocol[s]; !ok {
			cfg.Protocol[s] = l
		}
	}

	for s, l := range cfg.ProtocolPeer {
		r := cfg.ProtocolPeerDefault
		if l2, ok := c.ProtocolPeer[s]; ok {
			r = l2
		}
		l.Apply(r)
		cfg.ProtocolPeer[s] = l
	}
	if c.ProtocolPeer != nil && cfg.ProtocolPeer == nil {
		cfg.ProtocolPeer = make(map[protocol.ID]BaseLimit)
	}
	for s, l := range c.ProtocolPeer {
		if _, ok := cfg.ProtocolPeer[s]; !ok {
			cfg.ProtocolPeer[s] = l
		}
	}

	for s, l := range cfg.Peer {
		r := cfg.PeerDefault
		if l2, ok := c.Peer[s]; ok {
			r = l2
		}
		l.Apply(r)
		cfg.Peer[s] = l
	}
	if c.Peer != nil && cfg.Peer == nil {
		cfg.Peer = make(map[peer.ID]BaseLimit)
	}
	for s, l := range c.Peer {
		if _, ok := cfg.Peer[s]; !ok {
			cfg.Peer[s] = l
		}
	}
}

// Scale scales up a limit configuration.
// memory is the amount of memory that the stack is allowed to consume,
// for a full it's recommended to use 1/8 of the installed system memory.
// If memory is smaller than 128 MB, the base configuration will be used.
func (cfg *ScalingLimitConfig) Scale(memory int64, numFD int) LimitConfig {
	var scaleFactor int
	if memory > 128<<20 {
		scaleFactor = int((memory - 128<<20) >> 20)
	}
	lc := LimitConfig{
		System:               scale(cfg.SystemBaseLimit, cfg.SystemLimitIncrease, scaleFactor, numFD),
		Transient:            scale(cfg.TransientBaseLimit, cfg.TransientLimitIncrease, scaleFactor, numFD),
		AllowlistedSystem:    scale(cfg.AllowlistedSystemBaseLimit, cfg.AllowlistedSystemLimitIncrease, scaleFactor, numFD),
		AllowlistedTransient: scale(cfg.AllowlistedTransientBaseLimit, cfg.AllowlistedTransientLimitIncrease, scaleFactor, numFD),
		ServiceDefault:       scale(cfg.ServiceBaseLimit, cfg.ServiceLimitIncrease, scaleFactor, numFD),
		ServicePeerDefault:   scale(cfg.ServicePeerBaseLimit, cfg.ServicePeerLimitIncrease, scaleFactor, numFD),
		ProtocolDefault:      scale(cfg.ProtocolBaseLimit, cfg.ProtocolLimitIncrease, scaleFactor, numFD),
		ProtocolPeerDefault:  scale(cfg.ProtocolPeerBaseLimit, cfg.ProtocolPeerLimitIncrease, scaleFactor, numFD),
		PeerDefault:          scale(cfg.PeerBaseLimit, cfg.PeerLimitIncrease, scaleFactor, numFD),
		Conn:                 scale(cfg.ConnBaseLimit, cfg.ConnLimitIncrease, scaleFactor, numFD),
		Stream:               scale(cfg.StreamBaseLimit, cfg.ConnLimitIncrease, scaleFactor, numFD),
	}
	if cfg.ServiceLimits != nil {
		lc.Service = make(map[string]BaseLimit)
		for svc, l := range cfg.ServiceLimits {
			lc.Service[svc] = scale(l.BaseLimit, l.BaseLimitIncrease, scaleFactor, numFD)
		}
	}
	if cfg.ProtocolLimits != nil {
		lc.Protocol = make(map[protocol.ID]BaseLimit)
		for proto, l := range cfg.ProtocolLimits {
			lc.Protocol[proto] = scale(l.BaseLimit, l.BaseLimitIncrease, scaleFactor, numFD)
		}
	}
	if cfg.PeerLimits != nil {
		lc.Peer = make(map[peer.ID]BaseLimit)
		for p, l := range cfg.PeerLimits {
			lc.Peer[p] = scale(l.BaseLimit, l.BaseLimitIncrease, scaleFactor, numFD)
		}
	}
	if cfg.ServicePeerLimits != nil {
		lc.ServicePeer = make(map[string]BaseLimit)
		for svc, l := range cfg.ServicePeerLimits {
			lc.ServicePeer[svc] = scale(l.BaseLimit, l.BaseLimitIncrease, scaleFactor, numFD)
		}
	}
	if cfg.ProtocolPeerLimits != nil {
		lc.ProtocolPeer = make(map[protocol.ID]BaseLimit)
		for p, l := range cfg.ProtocolPeerLimits {
			lc.ProtocolPeer[p] = scale(l.BaseLimit, l.BaseLimitIncrease, scaleFactor, numFD)
		}
	}
	return lc
}

func (cfg *ScalingLimitConfig) AutoScale() LimitConfig {
	return cfg.Scale(
		int64(memory.TotalMemory())/8,
		getNumFDs()/2,
	)
}

// factor is the number of MBs above the minimum (128 MB)
func scale(base BaseLimit, inc BaseLimitIncrease, factor int, numFD int) BaseLimit {
	l := BaseLimit{
		StreamsInbound:  base.StreamsInbound + (inc.StreamsInbound*factor)>>10,
		StreamsOutbound: base.StreamsOutbound + (inc.StreamsOutbound*factor)>>10,
		Streams:         base.Streams + (inc.Streams*factor)>>10,
		ConnsInbound:    base.ConnsInbound + (inc.ConnsInbound*factor)>>10,
		ConnsOutbound:   base.ConnsOutbound + (inc.ConnsOutbound*factor)>>10,
		Conns:           base.Conns + (inc.Conns*factor)>>10,
		Memory:          base.Memory + (inc.Memory*int64(factor))>>10,
		FD:              base.FD,
	}
	if inc.FDFraction > 0 && numFD > 0 {
		l.FD = int(inc.FDFraction * float64(numFD))
	}
	return l
}

// DefaultLimits are the limits used by the default limiter constructors.
var DefaultLimits = ScalingLimitConfig{
	SystemBaseLimit: BaseLimit{
		ConnsInbound:    64,
		ConnsOutbound:   128,
		Conns:           128,
		StreamsInbound:  64 * 16,
		StreamsOutbound: 128 * 16,
		Streams:         128 * 16,
		Memory:          128 << 20,
		FD:              256,
	},

	SystemLimitIncrease: BaseLimitIncrease{
		ConnsInbound:    64,
		ConnsOutbound:   128,
		Conns:           128,
		StreamsInbound:  64 * 16,
		StreamsOutbound: 128 * 16,
		Streams:         128 * 16,
		Memory:          1 << 30,
		FDFraction:      1,
	},

	TransientBaseLimit: BaseLimit{
		ConnsInbound:    32,
		ConnsOutbound:   64,
		Conns:           64,
		StreamsInbound:  128,
		StreamsOutbound: 256,
		Streams:         256,
		Memory:          32 << 20,
		FD:              64,
	},

	TransientLimitIncrease: BaseLimitIncrease{
		ConnsInbound:    16,
		ConnsOutbound:   32,
		Conns:           32,
		StreamsInbound:  128,
		StreamsOutbound: 256,
		Streams:         256,
		Memory:          128 << 20,
		FDFraction:      0.25,
	},

	// Setting the allowlisted limits to be the same as the normal limits. The
	// allowlist only activates when you reach your normal system/transient
	// limits. So it's okay if these limits err on the side of being too big,
	// since most of the time you won't even use any of these. Tune these down
	// if you want to manage your resources against an allowlisted endpoint.
	AllowlistedSystemBaseLimit: BaseLimit{
		ConnsInbound:    64,
		ConnsOutbound:   128,
		Conns:           128,
		StreamsInbound:  64 * 16,
		StreamsOutbound: 128 * 16,
		Streams:         128 * 16,
		Memory:          128 << 20,
		FD:              256,
	},

	AllowlistedSystemLimitIncrease: BaseLimitIncrease{
		ConnsInbound:    64,
		ConnsOutbound:   128,
		Conns:           128,
		StreamsInbound:  64 * 16,
		StreamsOutbound: 128 * 16,
		Streams:         128 * 16,
		Memory:          1 << 30,
		FDFraction:      1,
	},

	AllowlistedTransientBaseLimit: BaseLimit{
		ConnsInbound:    32,
		ConnsOutbound:   64,
		Conns:           64,
		StreamsInbound:  128,
		StreamsOutbound: 256,
		Streams:         256,
		Memory:          32 << 20,
		FD:              64,
	},

	AllowlistedTransientLimitIncrease: BaseLimitIncrease{
		ConnsInbound:    16,
		ConnsOutbound:   32,
		Conns:           32,
		StreamsInbound:  128,
		StreamsOutbound: 256,
		Streams:         256,
		Memory:          128 << 20,
		FDFraction:      0.25,
	},

	ServiceBaseLimit: BaseLimit{
		StreamsInbound:  1024,
		StreamsOutbound: 4096,
		Streams:         4096,
		Memory:          64 << 20,
	},

	ServiceLimitIncrease: BaseLimitIncrease{
		StreamsInbound:  512,
		StreamsOutbound: 2048,
		Streams:         2048,
		Memory:          128 << 20,
	},

	ServicePeerBaseLimit: BaseLimit{
		StreamsInbound:  128,
		StreamsOutbound: 256,
		Streams:         256,
		Memory:          16 << 20,
	},

	ServicePeerLimitIncrease: BaseLimitIncrease{
		StreamsInbound:  4,
		StreamsOutbound: 8,
		Streams:         8,
		Memory:          4 << 20,
	},

	ProtocolBaseLimit: BaseLimit{
		StreamsInbound:  512,
		StreamsOutbound: 2048,
		Streams:         2048,
		Memory:          64 << 20,
	},

	ProtocolLimitIncrease: BaseLimitIncrease{
		StreamsInbound:  256,
		StreamsOutbound: 512,
		Streams:         512,
		Memory:          164 << 20,
	},

	ProtocolPeerBaseLimit: BaseLimit{
		StreamsInbound:  64,
		StreamsOutbound: 128,
		Streams:         256,
		Memory:          16 << 20,
	},

	ProtocolPeerLimitIncrease: BaseLimitIncrease{
		StreamsInbound:  4,
		StreamsOutbound: 8,
		Streams:         16,
		Memory:          4,
	},

	PeerBaseLimit: BaseLimit{
		ConnsInbound:    4,
		ConnsOutbound:   8,
		Conns:           8,
		StreamsInbound:  256,
		StreamsOutbound: 512,
		Streams:         512,
		Memory:          64 << 20,
		FD:              4,
	},

	PeerLimitIncrease: BaseLimitIncrease{
		StreamsInbound:  128,
		StreamsOutbound: 256,
		Streams:         256,
		Memory:          128 << 20,
		FDFraction:      1.0 / 64,
	},

	ConnBaseLimit: BaseLimit{
		ConnsInbound:  1,
		ConnsOutbound: 1,
		Conns:         1,
		FD:            1,
		Memory:        1 << 20,
	},

	StreamBaseLimit: BaseLimit{
		StreamsInbound:  1,
		StreamsOutbound: 1,
		Streams:         1,
		Memory:          16 << 20,
	},
}

var infiniteBaseLimit = BaseLimit{
	Streams:         math.MaxInt,
	StreamsInbound:  math.MaxInt,
	StreamsOutbound: math.MaxInt,
	Conns:           math.MaxInt,
	ConnsInbound:    math.MaxInt,
	ConnsOutbound:   math.MaxInt,
	FD:              math.MaxInt,
	Memory:          math.MaxInt64,
}

// InfiniteLimits are a limiter configuration that uses infinite limits, thus effectively not limiting anything.
// Keep in mind that the operating system limits the number of file descriptors that an application can use.
var InfiniteLimits = LimitConfig{
	System:               infiniteBaseLimit,
	Transient:            infiniteBaseLimit,
	AllowlistedSystem:    infiniteBaseLimit,
	AllowlistedTransient: infiniteBaseLimit,
	ServiceDefault:       infiniteBaseLimit,
	ServicePeerDefault:   infiniteBaseLimit,
	ProtocolDefault:      infiniteBaseLimit,
	ProtocolPeerDefault:  infiniteBaseLimit,
	PeerDefault:          infiniteBaseLimit,
	Conn:                 infiniteBaseLimit,
	Stream:               infiniteBaseLimit,
}
