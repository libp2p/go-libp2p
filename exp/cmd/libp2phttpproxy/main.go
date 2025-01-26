package main

import (
	"log"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"

	"github.com/libp2p/go-libp2p"
	libp2phttp "github.com/libp2p/go-libp2p/p2p/http"
	"github.com/multiformats/go-multiaddr"
)

func main() {
	proxyTarget := os.Getenv("PROXY_TARGET")
	if proxyTarget == "" {
		log.Fatal("PROXY_TARGET must be set")
	}

	h, err := libp2p.New(libp2p.ListenAddrStrings(
		"/ip4/0.0.0.0/tcp/0",
		"/ip4/0.0.0.0/tcp/0",
	))
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()

	const multiaddrPrefix = "multiaddr:"
	if !strings.HasPrefix(proxyTarget, multiaddrPrefix) {
		log.Fatalf("PROXY_TARGET must start with %q", multiaddrPrefix)
	}

	targetUrl, _ := url.Parse(proxyTarget)

	httpHost := libp2phttp.Host{
		StreamHost:        h,
		InsecureAllowHTTP: true,
		ListenAddrs:       []multiaddr.Multiaddr{multiaddr.StringCast("/ip4/0.0.0.0/tcp/5005/http")},
	}

	// reverse proxy
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			copiedURL := *targetUrl
			r.Out.URL = &copiedURL
		},
		Transport: &httpHost,
	}

	httpHost.SetHTTPHandlerAtPath("/http-reverse-proxy/0.0.1", "/", proxy)
	go httpHost.Serve()

	log.Println("Listening on:")
	for _, a := range httpHost.Addrs() {
		log.Println(a.Encapsulate(multiaddr.StringCast("/p2p/" + h.ID().String())))
	}

	// Wait for interrupt signal to stop
	intSig := make(chan os.Signal, 1)
	signal.Notify(intSig, os.Interrupt)
	<-intSig
	log.Println("Interrupt signal received, closing host")
}
