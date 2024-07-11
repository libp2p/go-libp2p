package cmdlib

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/quic-go/quic-go"
)

func RunClient(raddr string, p string) error {
	peerID, err := peer.Decode(p)
	if err != nil {
		return err
	}
	addr, err := ma.NewMultiaddr(raddr)
	if err != nil {
		return err
	}
	priv, _, err := ic.GenerateECDSAKeyPair(rand.Reader)
	if err != nil {
		return err
	}

	reuse, err := quicreuse.NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{})
	if err != nil {
		return err
	}
	t, err := libp2pquic.NewTransport(priv, reuse, nil, nil, nil)
	if err != nil {
		return err
	}

	log.Printf("Dialing %s\n", addr.String())
	conn, err := t.Dial(context.Background(), addr, peerID)
	if err != nil {
		return err
	}
	defer conn.Close()
	str, err := conn.OpenStream(context.Background())
	if err != nil {
		return err
	}
	defer str.Close()
	const msg = "Hello world!"
	log.Printf("Sending: %s\n", msg)
	if _, err := str.Write([]byte(msg)); err != nil {
		return err
	}
	if err := str.CloseWrite(); err != nil {
		return err
	}
	data, err := io.ReadAll(str)
	if err != nil {
		return err
	}
	log.Printf("Received: %s\n", data)
	return nil
}

func RunServer(port string, location chan peer.AddrInfo) error {
	addr, err := ma.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/udp/%s/quic-v1", port))
	if err != nil {
		return err
	}
	priv, _, err := ic.GenerateECDSAKeyPair(rand.Reader)
	if err != nil {
		return err
	}
	peerID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return err
	}

	reuse, err := quicreuse.NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{})
	if err != nil {
		return err
	}
	t, err := libp2pquic.NewTransport(priv, reuse, nil, nil, nil)
	if err != nil {
		return err
	}

	ln, err := t.Listen(addr)
	if err != nil {
		return err
	}
	fmt.Printf("Listening. Now run: go run cmd/client/main.go %s %s\n", ln.Multiaddr(), peerID)
	if location != nil {
		location <- peer.AddrInfo{ID: peerID, Addrs: []ma.Multiaddr{ln.Multiaddr()}}
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		log.Printf("Accepted new connection from %s (%s)\n", conn.RemotePeer(), conn.RemoteMultiaddr())
		go func() {
			if err := handleConn(conn); err != nil {
				log.Printf("handling conn failed: %s", err.Error())
			}
		}()
	}
}

func handleConn(conn tpt.CapableConn) error {
	str, err := conn.AcceptStream()
	if err != nil {
		return err
	}
	data, err := io.ReadAll(str)
	if err != nil {
		return err
	}
	log.Printf("Received: %s\n", data)
	if _, err := str.Write(data); err != nil {
		return err
	}
	return str.Close()
}
