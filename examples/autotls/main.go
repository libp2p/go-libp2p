package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/ipfs/go-log/v2"

	p2pforge "github.com/ipshipyard/p2p-forge/client"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"
)

var logger = log.Logger("autotls-example")

const userAgent = "go-libp2p/example/autotls"
const identityKeyFile = "identity.key"

func main() {
	// Create a background context
	ctx := context.Background()

	log.SetLogLevel("*", "error")
	log.SetLogLevel("autotls-example", "debug") // Set the log level for the example to debug
	log.SetLogLevel("basichost", "info")        // Set the log level for the basichost package to info
	log.SetLogLevel("autotls", "debug")         // Set the log level for the autotls-example package to debug
	log.SetLogLevel("p2p-forge", "debug")       // Set the log level for the p2pforge package to debug
	log.SetLogLevel("nat", "debug")             // Set the log level for the libp2p nat package to debug

	certLoaded := make(chan bool, 1) // Create a channel to signal when the cert is loaded

	// use dedicated logger for autotls feature
	rawLogger := logger.Desugar()

	// p2pforge is the AutoTLS client library.
	// The cert manager handles the creation and management of certificate
	certManager, err := p2pforge.NewP2PForgeCertMgr(
		// Configure CA ACME endpoint
		// NOTE:
		// This example uses Let's Encrypt staging CA (p2pforge.DefaultCATestEndpoint)
		// which will not work correctly in browser, but is useful for initial testing.
		// Production should use Let's Encrypt production CA (p2pforge.DefaultCAEndpoint).
		p2pforge.WithCAEndpoint(p2pforge.DefaultCATestEndpoint), // test CA endpoint
		// TODO: p2pforge.WithCAEndpoint(p2pforge.DefaultCAEndpoint),  // production CA endpoint

		// Configure where to store certificate
		p2pforge.WithCertificateStorage(&certmagic.FileStorage{Path: "p2p-forge-certs"}),

		// Configure logger to use
		p2pforge.WithLogger(rawLogger.Sugar().Named("autotls")),

		// User-Agent to use during DNS-01 ACME challenge
		p2pforge.WithUserAgent(userAgent),

		// Optional extra delay before the initial registration
		p2pforge.WithRegistrationDelay(10*time.Second),

		// Optional hook called once certificate is ready
		p2pforge.WithOnCertLoaded(func() {
			certLoaded <- true
		}),
	)

	if err != nil {
		panic(err)
	}

	// Start the cert manager
	certManager.Start()
	defer certManager.Stop()

	// Load or generate a persistent peer identity key
	privKey, err := LoadIdentity(identityKeyFile)
	if err != nil {
		panic(err)
	}

	opts := []libp2p.Option{
		libp2p.Identity(privKey), // Use the loaded identity key
		libp2p.DisableRelay(),    // Disable relay, since we need a public IP address
		libp2p.NATPortMap(),      // Attempt to open ports using UPnP for NATed hosts.

		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/5500", // regular TCP IPv4 connections
			"/ip6/::/tcp/5500",      // regular TCP IPv6 connections

			// Configure Secure WebSockets listeners on the same TCP port
			// AutoTLS will automatically generate a certificate for this host
			// and use the forge domain (`libp2p.direct`) as the SNI hostname.
			fmt.Sprintf("/ip4/0.0.0.0/tcp/5500/tls/sni/*.%s/ws", p2pforge.DefaultForgeDomain),
			fmt.Sprintf("/ip6/::/tcp/5500/tls/sni/*.%s/ws", p2pforge.DefaultForgeDomain),
		),

		// Configure the TCP transport
		libp2p.Transport(tcp.NewTCPTransport),

		// Share the same TCP listener between the TCP and WS transports
		libp2p.ShareTCPListener(),

		// Configure the WS transport with the AutoTLS cert manager
		libp2p.Transport(ws.New, ws.WithTLSConfig(certManager.TLSConfig())),

		// Configure user agent for libp2p identify protocol (https://github.com/libp2p/specs/blob/master/identify/README.md)
		libp2p.UserAgent(userAgent),

		// AddrsFactory takes the multiaddrs we're listening on and sets the multiaddrs to advertise to the network.
		// We use the AutoTLS address factory so that the `*` in the AutoTLS address string is replaced with the
		// actual IP address of the host once detected
		libp2p.AddrsFactory(certManager.AddressFactory()),
	}
	h, err := libp2p.New(opts...)
	if err != nil {
		panic(err)
	}

	logger.Info("Host created with PeerID: ", h.ID())

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
