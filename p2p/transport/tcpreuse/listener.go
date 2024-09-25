package tcpreuse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/net/reuseport"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var log = logging.Logger("tcp-demultiplex")

type ConnMgr struct {
	disableReuseport bool
	reuse            reuseport.Transport
	listeners        map[string]*multiplexedListener
	mx               sync.Mutex
}

func NewConnMgr(disableReuseport bool) *ConnMgr {
	return &ConnMgr{
		disableReuseport: disableReuseport,
		reuse:            reuseport.Transport{},
		listeners:        make(map[string]*multiplexedListener),
	}
}

func (t *ConnMgr) maListen(laddr ma.Multiaddr) (manet.Listener, error) {
	if t.useReuseport() {
		return t.reuse.Listen(laddr)
	} else {
		return manet.Listen(laddr)
	}
}

func (t *ConnMgr) useReuseport() bool {
	return !t.disableReuseport && ReuseportIsAvailable()
}

func (t *ConnMgr) DemultiplexedListen(laddr ma.Multiaddr, connType DemultiplexedConnType) (manet.Listener, error) {
	if !connType.IsKnown() {
		return nil, fmt.Errorf("unknown connection type: %s", connType)
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
		return l.Close()
	}
	ml = &multiplexedListener{
		Listener:  l,
		listeners: make(map[DemultiplexedConnType]*demultiplexedListener),
		buffer:    make(chan manet.Conn, 16), // TODO: how big should this buffer be?
		ctx:       ctx,
		closeFn:   cancelFunc,
	}

	dl, err := ml.DemultiplexedListen(connType)
	if err != nil {
		cerr := ml.Close()
		return nil, errors.Join(err, cerr)
	}

	go func() {
		err = ml.Run()
		if err != nil {
			log.Debugf("Error running multiplexed listener: %s", err.Error())
		}
	}()

	t.listeners[laddr.String()] = ml

	return dl, nil
}

var _ manet.Listener = &demultiplexedListener{}

type multiplexedListener struct {
	manet.Listener
	listeners       map[DemultiplexedConnType]*demultiplexedListener
	mx              sync.Mutex
	listenerCounter int
	buffer          chan manet.Conn

	ctx     context.Context
	closeFn func() error
}

func (m *multiplexedListener) DemultiplexedListen(connType DemultiplexedConnType) (manet.Listener, error) {
	if !connType.IsKnown() {
		return nil, fmt.Errorf("unknown connection type: %s", connType)
	}

	m.mx.Lock()
	defer m.mx.Unlock()
	l, ok := m.listeners[connType]
	if ok {
		return l, nil
	}

	ctx, cancel := context.WithCancel(m.ctx)
	closeFn := func() error {
		cancel()
		m.mx.Lock()
		defer m.mx.Unlock()
		m.listenerCounter--
		if m.listenerCounter == 0 {
			return m.Close()
		}
		return nil
	}

	l = &demultiplexedListener{
		buffer:  make(chan manet.Conn, 16), // TODO: how big should this buffer be?
		inner:   m.Listener,
		ctx:     ctx,
		closeFn: closeFn,
	}

	m.listeners[connType] = l
	m.listenerCounter++

	return l, nil
}

func (m *multiplexedListener) Run() error {
	const numWorkers = 16
	for i := 0; i < numWorkers; i++ {
		go func() {
			m.background()
		}()
	}

	for {
		c, err := m.Listener.Accept()
		if err != nil {
			return err
		}

		select {
		case m.buffer <- c:
		case <-m.ctx.Done():
			return transport.ErrListenerClosed
		}
	}
}

func (m *multiplexedListener) background() {
	// TODO: if/how do we want to handle stalled connections and stop them from clogging up the pipeline?
	// Drop connection because the buffer is full
	for {
		select {
		case c := <-m.buffer:
			t, sampleC, err := ConnTypeFromConn(c)
			if err != nil {
				closeErr := c.Close()
				err = errors.Join(err, closeErr)
				log.Debugf("error demultiplexing connection: %s", err.Error())
				continue
			}

			demux, ok := m.listeners[t]
			if !ok {
				closeErr := c.Close()
				if closeErr != nil {
					log.Debugf("no registered listener for demultiplex connection %s. Error closing the connection %s", t, closeErr.Error())
				} else {
					log.Debugf("no registered listener for demultiplex connection %s", t)
				}
				continue
			}

			select {
			case demux.buffer <- sampleC:
			case <-m.ctx.Done():
				return
			default:
				closeErr := c.Close()
				if closeErr != nil {
					log.Debugf("dropped connection due to full buffer of awaiting connections of type %s. Error closing the connection %s", t, closeErr.Error())
				} else {
					log.Debugf("dropped connection due to full buffer of awaiting connections of type %s", t)
				}
				continue
			}
		case <-m.ctx.Done():
			return
		}
	}
}

func (m *multiplexedListener) Close() error {
	cerr := m.closeFn()
	lerr := m.Listener.Close()
	return errors.Join(lerr, cerr)
}

type demultiplexedListener struct {
	buffer  chan manet.Conn
	inner   manet.Listener
	ctx     context.Context
	closeFn func() error
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
	return m.closeFn()
}

func (m *demultiplexedListener) Multiaddr() ma.Multiaddr {
	// TODO: do we need to add a suffix for the rest of the transport?
	return m.inner.Multiaddr()
}

func (m *demultiplexedListener) Addr() net.Addr {
	return m.inner.Addr()
}
