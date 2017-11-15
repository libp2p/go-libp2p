package discovery

import (
	"context"
	"math/rand"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p-host"
	"github.com/libp2p/go-libp2p-peer"
	pstore "github.com/libp2p/go-libp2p-peerstore"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr-net"
)

type broadcastService struct {
	host          host.Host
	listenSock    *net.UDPConn   // Socket listening for broadcasts
	sendSocks     []*net.UDPConn // Sockets that send broadcasts
	lk            sync.Mutex
	notifees      []Notifee
	broadcastPort int
	active        bool
}

//
// Bind a socket to send and recv broadcasts
//
func bindBroadcast(ip net.IP, port int) (*net.UDPConn, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: port})
	if err == nil {
		file, err := conn.File()
		if err == nil {
			err = syscall.SetsockoptInt(int(file.Fd()), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
		}
	}
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func NewBroadcastService(ctx context.Context, peerhost host.Host, defaultPort int, interval time.Duration) (Service, error) {

	// Broadcasts can only be received from 0.0.0.0 or 255.255.255.255
	// but they can not be sent from 255.255.255.255.
	// Therefor one listening socket is bound to 255.255.255.255 and a
	// sending socket will be bound to each address that broadcasts
	// should be sent from.
	listenSock, err := bindBroadcast(net.IPv4bcast, defaultPort)
	if err != nil {
		return nil, err
	}

	bs := &broadcastService{
		host:          peerhost,
		listenSock:    listenSock,
		broadcastPort: defaultPort,
		active:        true,
	}

	addrs, err := getDialableListenAddrs(peerhost)
	if err != nil {
		bs.Close()
		return nil, err
	} else {
		for _, a := range addrs {
			if a.IP.IsGlobalUnicast() {
				// binding to the same address and port as the TCP service
				// embeds that address and port into broadcast messages
				otherSock, err := bindBroadcast(a.IP, a.Port)
				if err == nil {
					bs.sendSocks = append(bs.sendSocks, otherSock)
				}
			}
		}
	}

	// fudge the interval to dampen oscillations with other nodes
	margin := int(interval) >> 2
	interval += time.Duration(rand.Intn(margin))
	interval -= time.Duration(rand.Intn(margin))

	go bs.sendBroadcasts(ctx, interval)
	go bs.recvBroadcasts(ctx)

	return bs, nil
}

func (bs *broadcastService) Close() error {
	bs.active = false
	bs.listenSock.Close()
	for _, conn := range bs.sendSocks {
		conn.Close()
	}
	return nil
}

func (bs *broadcastService) handleBroadcast(addr *net.UDPAddr, bpeer peer.ID) {
	// Broadcast messages are UDP, but represent IPv4 TCP sockets
	maddr, err := manet.FromNetAddr(&net.TCPAddr{
		IP:   addr.IP,
		Port: addr.Port,
	})
	if err == nil {
		pi := pstore.PeerInfo{
			ID:    bpeer,
			Addrs: []ma.Multiaddr{maddr},
		}

		bs.lk.Lock()
		defer bs.lk.Unlock()
		for _, n := range bs.notifees {
			if n != nil {
				go n.HandlePeerFound(pi)
			}
		}
	}
}

func (bs *broadcastService) recvBroadcasts(ctx context.Context) {
	var buf = make([]byte, 48)
	for bs.active {
		n, rAddr, err := bs.listenSock.ReadFromUDP(buf)
		if err == nil {
			if n != 0 {
				rPeer, err := peer.IDB58Decode(string(buf[0:n]))
				if err == nil && rPeer != bs.host.ID() {
					bs.handleBroadcast(rAddr, rPeer)
				}
			}
		}
	}
}

func (bs *broadcastService) sendBroadcast() {
	idBuf := []byte(bs.host.ID().Pretty())
	broadcastAddr := net.UDPAddr{
		IP:   net.IPv4bcast,
		Port: bs.broadcastPort,
	}
	for _, conn := range bs.sendSocks {
		conn.WriteToUDP(idBuf, &broadcastAddr)
	}
}

func (bs *broadcastService) sendBroadcasts(ctx context.Context, interval time.Duration) {
	// send an initial broadcast after a quarter interval
	time.Sleep(interval >> 2)
	bs.sendBroadcast()

	// send broadcasts on a periodic timeout
	ticker := time.NewTicker(interval)
	for bs.active {
		select {
		case <-ticker.C:
			bs.sendBroadcast()

		case <-ctx.Done():
			bs.Close()
		}
	}
}

func (m *broadcastService) RegisterNotifee(n Notifee) {
	m.lk.Lock()
	defer m.lk.Unlock()
	for i, v := range m.notifees {
		if v == nil {
			m.notifees[i] = n
			return
		}
	}
	m.notifees = append(m.notifees, n)
}

func (m *broadcastService) UnregisterNotifee(n Notifee) {
	m.lk.Lock()
	defer m.lk.Unlock()
	for i, notif := range m.notifees {
		if notif == n {
			m.notifees[i] = nil
		}
	}
}
