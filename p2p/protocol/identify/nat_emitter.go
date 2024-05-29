package identify

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
)

type natEmitter struct {
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	reachabilitySub event.Subscription
	reachability    network.Reachability
	eventInterval   time.Duration

	currentUDPNATDeviceType  network.NATDeviceType
	currentTCPNATDeviceType  network.NATDeviceType
	emitNATDeviceTypeChanged event.Emitter

	observedAddrMgr *ObservedAddrManager
}

func newNATEmitter(h host.Host, o *ObservedAddrManager, eventInterval time.Duration) (*natEmitter, error) {
	ctx, cancel := context.WithCancel(context.Background())
	n := &natEmitter{
		observedAddrMgr: o,
		ctx:             ctx,
		cancel:          cancel,
		eventInterval:   eventInterval,
	}
	reachabilitySub, err := h.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged), eventbus.Name("identify (nat emitter)"))
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to reachability event: %s", err)
	}
	n.reachabilitySub = reachabilitySub

	emitter, err := h.EventBus().Emitter(new(event.EvtNATDeviceTypeChanged), eventbus.Stateful)
	if err != nil {
		return nil, fmt.Errorf("failed to create emitter for NATDeviceType: %s", err)
	}
	n.emitNATDeviceTypeChanged = emitter

	n.wg.Add(1)
	go n.worker()
	return n, nil
}

func (n *natEmitter) worker() {
	defer n.wg.Done()
	subCh := n.reachabilitySub.Out()
	ticker := time.NewTicker(n.eventInterval)
	pendingUpdate := false
	enoughTimeSinceLastUpdate := true
	for {
		select {
		case evt, ok := <-subCh:
			if !ok {
				subCh = nil
				continue
			}
			ev, ok := evt.(event.EvtLocalReachabilityChanged)
			if !ok {
				log.Error("invalid event: %v", evt)
				continue
			}
			n.reachability = ev.Reachability
		case <-ticker.C:
			enoughTimeSinceLastUpdate = true
			if pendingUpdate {
				n.maybeNotify()
				pendingUpdate = false
				enoughTimeSinceLastUpdate = false
			}
		case <-n.observedAddrMgr.addrRecordedNotif:
			pendingUpdate = true
			if enoughTimeSinceLastUpdate {
				n.maybeNotify()
				pendingUpdate = false
				enoughTimeSinceLastUpdate = false
			}
		case <-n.ctx.Done():
			return
		}
	}
}

func (n *natEmitter) maybeNotify() {
	if n.reachability == network.ReachabilityPrivate {
		tcpNATType, udpNATType := n.observedAddrMgr.getNATType()
		if tcpNATType != n.currentTCPNATDeviceType {
			n.currentTCPNATDeviceType = tcpNATType
			n.emitNATDeviceTypeChanged.Emit(event.EvtNATDeviceTypeChanged{
				TransportProtocol: network.NATTransportTCP,
				NatDeviceType:     n.currentTCPNATDeviceType,
			})
		}
		if udpNATType != n.currentUDPNATDeviceType {
			n.currentUDPNATDeviceType = udpNATType
			n.emitNATDeviceTypeChanged.Emit(event.EvtNATDeviceTypeChanged{
				TransportProtocol: network.NATTransportUDP,
				NatDeviceType:     n.currentUDPNATDeviceType,
			})
		}
	}
}

func (n *natEmitter) Close() {
	n.cancel()
	n.wg.Wait()
	n.reachabilitySub.Close()
	n.emitNATDeviceTypeChanged.Close()
}
