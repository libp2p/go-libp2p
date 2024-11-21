package metricsserver_test

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/exp/metricsserver"
	libp2phttp "github.com/libp2p/go-libp2p/p2p/http"
)

func newClient() (*http.Client, peer.ID, func(), error) {
	streamHost, err := libp2p.New(libp2p.NoListenAddrs, libp2p.DefaultTransports)
	if err != nil {
		return nil, "", nil, err
	}

	rt := &libp2phttp.Host{
		StreamHost: streamHost,
	}

	return &http.Client{Transport: rt}, streamHost.ID(), func() {
		streamHost.Close()
	}, nil
}

func ExampleWithMetricsServer() {
	client, clientID, close, err := newClient()
	if err != nil {
		fmt.Println("Error creating client:", err)
		return
	}
	defer close()

	var opts []libp2p.Option
	opts = metricsserver.WithMetricsServer(opts, []peer.ID{clientID})

	h, err := libp2p.New(opts...)
	if err != nil {
		fmt.Println("Error creating libp2p host:", err)
		return
	}
	defer h.Close()

	url := fmt.Sprintf("multiaddr:%s/p2p/%s/http-path/%s", h.Addrs()[0], h.ID(), url.QueryEscape(metricsserver.ProtocolID))
	resp, err := client.Get(url)
	if err != nil {
		fmt.Println("Error getting metrics:", err)
		return
	}

	metricsResponseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading metrics response body:", err)
		return
	}

	foundMetrics := strings.Contains(string(metricsResponseBody), "go_gc_duration_seconds")
	fmt.Println("foundMetrics:", foundMetrics)
	// Output: foundMetrics: true
}
