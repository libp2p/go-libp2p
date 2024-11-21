package tcpreuse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/net/reuseport"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const acceptQueueSize = 64 // It is fine to read 3 bytes from 64 connections in parallel.

// How long we wait for a connection to be accepted before dropping it.
const acceptTimeout = 30 * time.Second

var log = logging.Logger("tcp-demultiplex")

// ConnMgr enables you to share the same listen address between TCP and WebSocket transports.
type ConnMgr struct {
	enableReuseport bool
	reuse           reuseport.Transport
	connGater       connmgr.ConnectionGater
	rcmgr           network.ResourceManager

	mx        sync.Mutex
	listeners map[string]*multiplexedListener
}

func NewConnMgr(enableReuseport bool, gater connmgr.ConnectionGater, rcmgr network.ResourceManager) *ConnMgr {
	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}
	return &ConnMgr{
		enableReuseport: enableReuseport,
		reuse:           reuseport.Transport{},
		connGater:       gater,
		rcmgr:           rcmgr,
		listeners:       make(map[string]*multiplexedListener),
	}
}

func (t *ConnMgr) maListen(listenAddr ma.Multiaddr) (manet.Listener, error) {
	if t.useReuseport() {
		return t.reuse.Listen(listenAddr)
	} else {
		return manet.Listen(listenAddr)
	}
}

func (t *ConnMgr) useReuseport() bool {
	return t.enableReuseport && ReuseportIsAvailable()
}

func getTCPAddr(listenAddr ma.Multiaddr) (ma.Multiaddr, error) {
	haveTCP := false
	addr, _ := ma.SplitFunc(listenAddr, func(c ma.Component) bool {
		if haveTCP {
			return true
		}
		if c.Protocol().Code == ma.P_TCP {
			haveTCP = true
		}
		return false
	})
	if !haveTCP {
		return nil, fmt.Errorf("invalid listen addr %s, need tcp address", listenAddr)
	}
	return addr, nil
}

// DemultiplexedListen returns a listener for laddr listening for `connType` connections. The connections
// accepted from returned listeners need to be upgraded with a `transport.Upgrader`.
// NOTE: All listeners for port 0 share the same underlying socket, so they have the same specific port.
func (t *ConnMgr) DemultiplexedListen(laddr ma.Multiaddr, connType DemultiplexedConnType) (manet.Listener, error) {
	if !connType.IsKnown() {
		return nil, fmt.Errorf("unknown connection type: %s", connType)
	}
	laddr, err := getTCPAddr(laddr)
	if err != nil {
		return nil, err
	}

	t.mx.Lock()
	defer t.mx.Unlock()
	ml, ok := t.listeners[laddr.String()]
	if ok {
		dl, err := ml.DemultiplexedListen(connType)
		if err != nil {
			return nil, err
		}
		return dl, nil
	}

	l, err := t.maListen(laddr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelFunc := func() error {
		cancel()
		t.mx.Lock()
		defer t.mx.Unlock()
		delete(t.listeners, laddr.String())
		delete(t.listeners, l.Multiaddr().String())
		return l.Close()
	}
	ml = &multiplexedListener{
		Listener:  l,
		listeners: make(map[DemultiplexedConnType]*demultiplexedListener),
		ctx:       ctx,
		closeFn:   cancelFunc,
		connGater: t.connGater,
		rcmgr:     t.rcmgr,
	}
	t.listeners[laddr.String()] = ml
	t.listeners[l.Multiaddr().String()] = ml

	dl, err := ml.DemultiplexedListen(connType)
	if err != nil {
		cerr := ml.Close()
		return nil, errors.Join(err, cerr)
	}

	ml.wg.Add(1)
	go ml.run()

	return dl, nil
}

var _ manet.Listener = &demultiplexedListener{}

type multiplexedListener struct {
	manet.Listener
	listeners map[DemultiplexedConnType]*demultiplexedListener
	mx        sync.RWMutex

	connGater connmgr.ConnectionGater
	rcmgr     network.ResourceManager
	ctx       context.Context
	closeFn   func() error
	wg        sync.WaitGroup
}

var ErrListenerExists = errors.New("listener already exists for this conn type on this address")

func (m *multiplexedListener) DemultiplexedListen(connType DemultiplexedConnType) (manet.Listener, error) {
	if !connType.IsKnown() {
		return nil, fmt.Errorf("unknown connection type: %s", connType)
	}

	m.mx.Lock()
	defer m.mx.Unlock()
	if _, ok := m.listeners[connType]; ok {
		return nil, ErrListenerExists
	}

	ctx, cancel := context.WithCancel(m.ctx)
	l := &demultiplexedListener{
		buffer:     make(chan manet.Conn),
		inner:      m.Listener,
		ctx:        ctx,
		cancelFunc: cancel,
		closeFn:    func() error { m.removeDemultiplexedListener(connType); return nil },
	}

	m.listeners[connType] = l

	return l, nil
}

func (m *multiplexedListener) run() error {
	defer m.Close()
	defer m.wg.Done()
	acceptQueue := make(chan struct{}, acceptQueueSize)
	for {
		c, err := m.Listener.Accept()
		if err != nil {
			return err
		}

		// Gate and resource limit the connection here.
		// If done after sampling the connection, we'll be vulnerable to DOS attacks by a single peer
		// which clogs up our entire connection queue.
		// This duplicates the responsibility of gating and resource limiting between here and the upgrader. The
		// alternative without duplication requires moving the process of upgrading the connection here, which forces
		// us to establish the websocket connection here. That is more duplication, or a significant breaking change.
		//
		// Bugs around multiple calls to OpenConnection or InterceptAccept are prevented by the transport
		// integration tests.
		if m.connGater != nil && !m.connGater.InterceptAccept(c) {
			log.Debugf("gater blocked incoming connection on local addr %s from %s",
				c.LocalMultiaddr(), c.RemoteMultiaddr())
			if err := c.Close(); err != nil {
				log.Warnf("failed to close incoming connection rejected by gater: %s", err)
			}
			continue
		}
		connScope, err := m.rcmgr.OpenConnection(network.DirInbound, true, c.RemoteMultiaddr())
		if err != nil {
			log.Debugw("resource manager blocked accept of new connection", "error", err)
			if err := c.Close(); err != nil {
				log.Warnf("failed to open incoming connection. Rejected by resource manager: %s", err)
			}
			continue
		}

		select {
		case acceptQueue <- struct{}{}:
		// NOTE: We can drop the connection, but this is similar to the behaviour in the upgrader.
		case <-m.ctx.Done():
			c.Close()
			log.Debugf("accept queue full, dropping connection: %s", c.RemoteMultiaddr())
		}

		m.wg.Add(1)
		go func() {
			defer func() { <-acceptQueue }()
			defer m.wg.Done()
			ctx, cancelCtx := context.WithTimeout(m.ctx, acceptTimeout)
			defer cancelCtx()
			t, c, err := identifyConnType(c)
			if err != nil {
				connScope.Done()
				log.Debugf("error demultiplexing connection: %s", err.Error())
				return
			}

			connWithScope, err := manetConnWithScope(c, connScope)
			if err != nil {
				connScope.Done()
				closeErr := c.Close()
				err = errors.Join(err, closeErr)
				log.Debugf("error wrapping connection with scope: %s", err.Error())
				return
			}

			m.mx.RLock()
			demux, ok := m.listeners[t]
			m.mx.RUnlock()
			if !ok {
				closeErr := connWithScope.Close()
				if closeErr != nil {
					log.Debugf("no registered listener for demultiplex connection %s. Error closing the connection %s", t, closeErr.Error())
				} else {
					log.Debugf("no registered listener for demultiplex connection %s", t)
				}
				return
			}

			select {
			case demux.buffer <- connWithScope:
			case <-ctx.Done():
				connWithScope.Close()
			}
		}()
	}
}

func (m *multiplexedListener) Close() error {
	m.mx.Lock()
	for _, l := range m.listeners {
		l.cancelFunc()
	}
	err := m.closeListener()
	m.mx.Unlock()
	m.wg.Wait()
	return err
}

func (m *multiplexedListener) closeListener() error {
	lerr := m.Listener.Close()
	cerr := m.closeFn()
	return errors.Join(lerr, cerr)
}

func (m *multiplexedListener) removeDemultiplexedListener(c DemultiplexedConnType) {
	m.mx.Lock()
	defer m.mx.Unlock()

	delete(m.listeners, c)
	if len(m.listeners) == 0 {
		m.closeListener()
		m.mx.Unlock()
		m.wg.Wait()
		m.mx.Lock()
	}
}

type demultiplexedListener struct {
	buffer     chan manet.Conn
	inner      manet.Listener
	ctx        context.Context
	cancelFunc context.CancelFunc
	closeFn    func() error
}

func (m *demultiplexedListener) Accept() (manet.Conn, error) {
	select {
	case c := <-m.buffer:
		return c, nil
	case <-m.ctx.Done():
		return nil, transport.ErrListenerClosed
	}
}

func (m *demultiplexedListener) Close() error {
	m.cancelFunc()
	return m.closeFn()
}

func (m *demultiplexedListener) Multiaddr() ma.Multiaddr {
	return m.inner.Multiaddr()
}

func (m *demultiplexedListener) Addr() net.Addr {
	return m.inner.Addr()
}
