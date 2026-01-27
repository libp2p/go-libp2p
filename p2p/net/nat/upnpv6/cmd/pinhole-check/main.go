package main

import (
	"context"
	"flag"
	"log"
	"net"
	"time"

	"github.com/libp2p/go-libp2p/p2p/net/nat/upnpv6"
)

func main() {
	ipStr := flag.String("ip", "", "internal IPv6 address")
	port := flag.Uint("port", 0, "internal TCP/UDP port")
	proto := flag.String("proto", "tcp", "protocol: tcp or udp")
	lease := flag.Duration("lease", 2*time.Minute, "pinhole lease duration")
	hold := flag.Duration("hold", 10*time.Minute, "how long to keep the pinhole open (0 = forever)")
	flag.Parse()

	if *ipStr == "" || *port == 0 {
		log.Fatal("-ip and -port are required")
	}

	ip := net.ParseIP(*ipStr)
	if ip == nil {
		log.Fatal("invalid IPv6 address")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	client, err := upnpv6.Discover(ctx)
	cancel()
	if err != nil {
		log.Fatalf("discover failed: %v", err)
	}

	mgr := upnpv6.NewManager(client)
	ctxOpen, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	id, err := mgr.OpenPinhole(ctxOpen, ip, uint16(*port), *proto, *lease)
	cancel()
	if err != nil {
		log.Fatalf("open pinhole failed: %v", err)
	}
	log.Printf("pinhole id: %d", id)
	log.Printf("testing address: %s:%d (%s)", ip, *port, *proto)

	if *hold > 0 {
		log.Printf("holding for %s", hold.String())
		time.Sleep(*hold)
	} else {
		select {}
	}

	ctxClose, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = mgr.ClosePinhole(ctxClose, id)
	cancel()
}
