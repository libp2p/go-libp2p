package simconn

import (
	"math"
	"sync"
	"time"
)

// A rateLimiter is used by a link to determine how long to wait before sending
// data given a bandwidth cap.
type rateLimiter struct {
	lock         sync.Mutex
	refillRate   float64   // bytes per nanosecond
	allowance    float64   // in bytes
	maxAllowance float64   // in bytes
	lastUpdate   time.Time // when allowance was updated last
}

// Creates a new RateLimiter with the following parameters:
// bandwidth (in bits/sec).
// burstSize is in Bytes
func newRateLimiter(bandwidth int, burstSize int) *rateLimiter {
	//  convert bandwidth to bytes per nanosecond
	refillRate := float64(bandwidth) / float64(time.Second*8)
	maxAllowance := float64(burstSize)
	return &rateLimiter{
		refillRate:   refillRate,
		allowance:    maxAllowance,
		maxAllowance: maxAllowance,
		lastUpdate:   time.Now(),
	}
}

// Returns how long to wait before sending data with length 'dataSize' bytes
func (r *rateLimiter) Limit(dataSize int) time.Duration {
	r.lock.Lock()
	defer r.lock.Unlock()
	//  update time
	var duration time.Duration = time.Duration(0)
	if r.refillRate == 0 {
		return duration
	}
	current := time.Now()
	elapsedTime := current.Sub(r.lastUpdate)
	r.lastUpdate = current

	allowance := r.allowance + float64(elapsedTime)*r.refillRate
	//  allowance can't exceed bandwidth
	if allowance > r.maxAllowance {
		allowance = r.maxAllowance
	}

	allowance -= float64(dataSize)
	if allowance < 0 {
		//  sleep until allowance is back to 0
		duration = time.Duration(-allowance / r.refillRate)
	}

	r.allowance = allowance
	return duration
}

type LinkSettings struct {
	BitsPerSecond int
	Latency       time.Duration
	MTU           int
}

type packetWithDeliveryTime struct {
	Packet
	DeliveryTime time.Time
}

type PacketRecieverBlocking interface {
	RecvPacketBlocking(p Packet)
}

type latencyLink struct {
	Out func(p Packet)
	In  chan *packetWithDeliveryTime
	// q is technically unbounded here, but in practice is bounded by the bandwidth-delay product
	q []*packetWithDeliveryTime
}

func newLatencyLink(out func(p Packet)) *latencyLink {
	return &latencyLink{
		In:  make(chan *packetWithDeliveryTime),
		Out: out,
	}
}

func (l *latencyLink) Start(wg *sync.WaitGroup) {
	defer wg.Done()
	nextEvent := time.NewTimer(time.Second)
	nextEvent.Stop()

	for {
		select {
		case p, ok := <-l.In:
			if !ok {
				return
			}
			if !time.Now().Before(p.DeliveryTime) {
				l.Out(p.Packet)
				continue
			}
			l.q = append(l.q, p)
			if len(l.q) == 1 {
				nextEvent.Reset(time.Until(l.q[0].DeliveryTime))
			}
		case <-nextEvent.C:
			if len(l.q) == 0 {
				continue
			}
			nextPacket := l.q[0]
			if nextPacket.DeliveryTime.After(time.Now()) {
				nextEvent.Reset(time.Until(nextPacket.DeliveryTime))
				continue
			}
			l.Out(nextPacket.Packet)
			l.q = l.q[1:]
			if len(l.q) > 0 {
				nextEvent.Reset(time.Until(l.q[0].DeliveryTime))
			}
		}
	}
}

type SimulatedLink struct {
	// Internal state
	closed chan struct{}
	wg     sync.WaitGroup

	downstream *packetQueue
	upstream   *packetQueue

	upLimiter   *rateLimiter
	downLimiter *rateLimiter

	upLatency   *latencyLink
	downLatency *latencyLink

	UplinkSettings   LinkSettings
	DownlinkSettings LinkSettings

	UploadPacket   Router
	DownloadPacket PacketRecieverBlocking
}

func delayPacketHandling(limiter *rateLimiter, p packetWithDeliveryTime) {
	delay := limiter.Limit(len(p.buf))
	if delay > 0 {
		time.Sleep(delay)
	}
}

func (l *SimulatedLink) backgroundDownlink() {
	defer l.wg.Done()
	defer close(l.downLatency.In)
	for {
		p, ok := l.downstream.Pop()
		if !ok {
			return
		}
		delayPacketHandling(l.downLimiter, p)
		l.downLatency.In <- &p
	}
}

func (l *SimulatedLink) backgroundUplink() {
	defer l.wg.Done()
	defer close(l.upLatency.In)
	for {
		p, ok := l.upstream.Pop()
		if !ok {
			return
		}
		delayPacketHandling(l.upLimiter, p)
		l.upLatency.In <- &p
	}
}

func calculateBDP(mtu, bandwidth int, latency time.Duration) int {
	bdpBytes := (float64(bandwidth) / 8) * float64(latency.Seconds())
	// If we straddle the packet boundary, round up to the nearest MTU
	mtusWorth := int(math.Ceil(bdpBytes / float64(mtu)))
	return mtusWorth * mtu
}

func (l *SimulatedLink) Start() {
	l.closed = make(chan struct{})

	// Sane defaults
	if l.DownlinkSettings.MTU == 0 {
		l.DownlinkSettings.MTU = 1400
	}
	if l.UplinkSettings.MTU == 0 {
		l.UplinkSettings.MTU = 1400
	}

	downBDP := calculateBDP(l.DownlinkSettings.MTU, l.DownlinkSettings.BitsPerSecond, l.DownlinkSettings.Latency)
	upBDP := calculateBDP(l.UplinkSettings.MTU, l.UplinkSettings.BitsPerSecond, l.UplinkSettings.Latency)
	l.downstream = newPacketQ(downBDP)
	l.upstream = newPacketQ(upBDP)

	const burstSizeInPackets = 16
	l.upLimiter = newRateLimiter(l.UplinkSettings.BitsPerSecond, l.UplinkSettings.MTU*burstSizeInPackets)
	l.downLimiter = newRateLimiter(l.DownlinkSettings.BitsPerSecond, l.DownlinkSettings.MTU*burstSizeInPackets)

	l.upLatency = newLatencyLink(func(p Packet) { _ = l.UploadPacket.SendPacket(p) })
	l.downLatency = newLatencyLink(func(p Packet) { l.DownloadPacket.RecvPacketBlocking(p) })

	l.wg.Add(4)
	// TODO: Can we coalesce these into a single goroutine? Is it worth it?
	go l.upLatency.Start(&l.wg)
	go l.downLatency.Start(&l.wg)
	go l.backgroundDownlink()
	go l.backgroundUplink()
}

func (l *SimulatedLink) Close() error {
	close(l.closed)
	l.downstream.Close()
	l.upstream.Close()
	l.wg.Wait()
	return nil
}

func (l *SimulatedLink) SendPacket(p Packet) error {
	if len(p.buf) > l.UplinkSettings.MTU {
		// Dropping packet if it's too large for the link
		return nil
	}
	l.upstream.Push(packetWithDeliveryTime{Packet: p, DeliveryTime: time.Now().Add(l.UplinkSettings.Latency)})
	return nil
}

func (l *SimulatedLink) RecvPacket(p Packet) {
	if len(p.buf) > l.DownlinkSettings.MTU {
		// Dropping packet if it's too large for the link
		return
	}
	l.downstream.Push(packetWithDeliveryTime{Packet: p, DeliveryTime: time.Now().Add(l.DownlinkSettings.Latency)})
}
