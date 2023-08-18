package libp2phttp_test

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2phttp "github.com/libp2p/go-libp2p/p2p/http"
	ma "github.com/multiformats/go-multiaddr"
)

type IPFSGatewayHandler struct{}

func (IPFSGatewayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func ExampleHTTPHost_withAStockGoHTTPClient() {
	server := libp2phttp.HTTPHost{
		ServeInsecureHTTP: true, // For our example, we'll allow insecure HTTP
		ListenAddrs:       []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/0/http")},
	}

	// A server with a simple echo protocol
	server.SetHTTPHandler("/echo/1.0.0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "application/octet-stream")
		io.Copy(w, r.Body)
	}))
	go server.Serve()
	defer server.Close()

	var serverHTTPPort string
	var err error
	for _, a := range server.Addrs() {
		serverHTTPPort, err = a.ValueForProtocol(ma.P_TCP)
		if err == nil {
			break
		}
	}
	if err != nil {
		log.Fatal(err)
	}

	// Make an HTTP request using the Go standard library.
	resp, err := http.Post("http://127.0.0.1:"+serverHTTPPort+"/echo/1.0.0/", "application/octet-stream", strings.NewReader("Hello HTTP"))
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(body))

	// Output: Hello HTTP
}

func ExampleHTTPHost_ListenOnHTTPTransportAndStreams() {
	serverStreamHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/50124/quic-v1"))
	if err != nil {
		log.Fatal(err)
	}
	server := libp2phttp.HTTPHost{
		ServeInsecureHTTP: true, // For our example, we'll allow insecure HTTP
		ListenAddrs:       []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/50124/http")},
		StreamHost:        serverStreamHost,
	}
	go server.Serve()
	defer server.Close()

	fmt.Println("Server listening on:", server.Addrs())
	// Output: Server listening on: [/ip4/127.0.0.1/udp/50124/quic-v1 /ip4/127.0.0.1/tcp/50124/http]
}

func ExampleHTTPHost_overLibp2pStreams() {
	serverStreamHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"))
	if err != nil {
		log.Fatal(err)
	}

	server := libp2phttp.HTTPHost{
		StreamHost: serverStreamHost,
	}

	// A server with a simple echo protocol
	server.SetHTTPHandler("/echo/1.0.0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "application/octet-stream")
		io.Copy(w, r.Body)
	}))
	go server.Serve()
	defer server.Close()

	clientStreamHost, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		log.Fatal(err)
	}

	client := libp2phttp.HTTPHost{StreamHost: clientStreamHost}

	// Make an HTTP request using the Go standard library, but over libp2p
	// streams. If the server were listening on an HTTP transport, this could
	// also make the request over the HTTP transport.
	httpClient, err := client.NamespacedClient("/echo/1.0.0", peer.AddrInfo{ID: server.PeerID(), Addrs: server.Addrs()})

	// Only need to Post to "/" because this client is namespaced to the "/echo/1.0.0" protocol.
	resp, err := httpClient.Post("/", "application/octet-stream", strings.NewReader("Hello HTTP"))
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(body))

	// Output: Hello HTTP
}
