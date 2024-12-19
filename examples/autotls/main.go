package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/ipfs/go-log/v2"

	p2pforge "github.com/ipshipyard/p2p-forge/client"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"
)

var logger = log.Logger("autotls")

func main() {
	// Create a background context
	ctx := context.Background()

	log.SetLogLevel("*", "error")
	log.SetLogLevel("basichost", "info")  // Set the log level for the basichost package to info
	log.SetLogLevel("autotls", "debug")   // Set the log level for the autotls-example package to debug
	log.SetLogLevel("p2p-forge", "debug") // Set the log level for the p2pforge package to debug
	log.SetLogLevel("nat", "debug")       // Set the log level for the p2pforge package to debug

	certLoaded := make(chan bool, 1) // Create a channel to signal when the cert is loaded

	// p2pforge is the AutoTLS client library.
	// The cert manager handles the creation and management of certificate
	certManager, err := p2pforge.NewP2PForgeCertMgr(
		p2pforge.WithCAEndpoint(p2pforge.DefaultCAEndpoint), // Let's Encrypt production CA. You can also use the staging CA (p2pforge.DefaultCATestEndpoint) to avoid rate limiting.
		p2pforge.WithOnCertLoaded(func() {
			certLoaded <- true
		}),
		p2pforge.WithUserAgent("go-libp2p/example/autotls"),
	)

	if err != nil {
		panic(err)
	}

	// Start the cert manager
	certManager.Start()
	defer certManager.Stop()

	opts := []libp2p.Option{
		libp2p.DisableRelay(), // Disable relay, since we need a public IP address
		libp2p.NATPortMap(),   // Attempt to open ports using UPnP for NATed hosts.

		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/5500",         // regular tcp connections
			"/ip4/0.0.0.0/udp/5500/quic-v1", // a UDP endpoint for the QUIC transport

			// AutoTLS will automatically generate a certificate for this host
			// and use the forge domain (`libp2p.direct`) as the SNI hostname.
			fmt.Sprintf("/ip4/0.0.0.0/tcp/5500/tls/sni/*.%s/ws", p2pforge.DefaultForgeDomain),
			fmt.Sprintf("/ip6/::/tcp/5500/tls/sni/*.%s/ws", p2pforge.DefaultForgeDomain),
		),

		// Configure the TCP transport
		libp2p.Transport(tcp.NewTCPTransport),

		// Configure the QUIC transport
		libp2p.Transport(quic.NewTransport),

		// Configure the WS transport with the AutoTLS cert manager
		libp2p.Transport(ws.New, ws.WithTLSConfig(certManager.TLSConfig())),

		libp2p.UserAgent("go-libp2p/example/autotls"),
		// AddrsFactory takes the multiaddrs we're listening on and sets the multiaddrs to advertise to the network.
		// We use the AutoTLS address factory so that the `*` in the AutoTLS address string is replaced with the
		// actual IP address of the host once detected
		libp2p.AddrsFactory(certManager.AddressFactory()),
	}
	h, err := libp2p.New(opts...)
	if err != nil {
		panic(err)
	}

	logger.Info("Host created: ", h.ID())

	// Bootstrap the DHT to verify our public IPs address with AutoNAT
	dhtOpts := []dht.Option{
		dht.Mode(dht.ModeClient),
		dht.BootstrapPeers(dht.GetDefaultBootstrapPeerAddrInfos()...),
	}
	dht, err := dht.New(ctx, h, dhtOpts...)
	if err != nil {
		panic(err)
	}

	go dht.Bootstrap(ctx)

	time.Sleep(5 * time.Second)

	logger.Info("Addresses: ", h.Addrs())

	certManager.ProvideHost(h)

	select {
	case <-certLoaded:
		logger.Info("TLS certificate loaded ")
		logger.Info("Addresses: ", h.Addrs())
	case <-ctx.Done():
		logger.Info("Context done")
	}
	// Wait for interrupt signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}
