package tlsdiag

import (
	"context"
	"flag"
	"fmt"
	"net"
	"time"

	ci "github.com/libp2p/go-libp2p/core/crypto"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"

	"github.com/libp2p/go-libp2p/core/peer"
)

func StartServer() error {
	port := flag.Int("p", 5533, "port")
	keyType := flag.String("key", "ecdsa", "rsa, ecdsa, ed25519 or secp256k1")
	netCookieString := flag.String("netCookie", "", "network cookie (hex string)")
	flag.Parse()

	priv, err := generateKey(*keyType)
	if err != nil {
		return err
	}

	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return err
	}
	fmt.Printf(" Peer ID: %s", id)
	fmt.Printf(" Peer ID: %s", id)
	cookieOpt := ""
	if *netCookieString != "" {
		nc, err := ci.ParseNetworkCookie(*netCookieString)
		if err != nil {
			return err
		}
		fmt.Printf(" Network cookie: %s\n", nc)
		priv = ci.AddNetworkCookieToPrivKey(priv, nc)
		cookieOpt = fmt.Sprintf(" -netCookie %s", nc)
	} else {
		fmt.Println()
	}
	tp, err := libp2ptls.New(libp2ptls.ID, priv, nil)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", *port))
	if err != nil {
		return err
	}
	fmt.Printf("Listening for new connections on %s\n", ln.Addr())
	fmt.Printf("Now run the following command in a separate terminal:\n")
	fmt.Printf("\tgo run cmd/tlsdiag.go client -p %d -id %s%s\n", *port, id, cookieOpt)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		fmt.Printf("Accepted raw connection from %s\n", conn.RemoteAddr())
		go func() {
			if err := handleConn(tp, conn); err != nil {
				fmt.Printf("Error handling connection from %s: %s\n", conn.RemoteAddr(), err)
			}
		}()
	}
}

func handleConn(tp *libp2ptls.Transport, conn net.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sconn, err := tp.SecureInbound(ctx, conn, "")
	if err != nil {
		return err
	}
	fmt.Printf("Authenticated client: %s\n", sconn.RemotePeer())
	fmt.Fprintf(sconn, "Hello client!")
	fmt.Printf("Closing connection to %s\n", conn.RemoteAddr())
	return sconn.Close()
}
