package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"os"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/quic-go/quic-go"
)

func main() {
	if len(os.Args) < 3 || len(os.Args) > 4 {
		fmt.Printf("Usage: %s <multiaddr> <peer id> [<netcookie>]", os.Args[0])
		return
	}
	nc := ""
	if len(os.Args) == 4 {
		nc = os.Args[3]
	}
	if err := run(os.Args[1], os.Args[2], nc); err != nil {
		log.Fatalf(err.Error())
	}
}

func run(raddr, p, ncStr string) error {
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
	nc, err := ic.ParseNetworkCookie(ncStr)
	if err != nil {
		return err
	}

	reuse, err := quicreuse.NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{})
	if err != nil {
		return err
	}
	priv = ic.AddNetworkCookieToPrivKey(priv, nc)
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
