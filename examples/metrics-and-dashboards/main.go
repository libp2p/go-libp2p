package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	rcmgrObs "github.com/libp2p/go-libp2p/p2p/host/resource-manager/obs"
)

func main() {
	http.Handle("/metrics", promhttp.Handler())
	go func() {
		http.ListenAndServe(":2112", nil)
	}()

	rcmgrObs.MustRegisterWith(prometheus.DefaultRegisterer)

	str, err := rcmgrObs.NewStatsTraceReporter()
	if err != nil {
		log.Fatal(err)
	}

	rmgr, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(rcmgr.DefaultLimits.AutoScale()), rcmgr.WithTraceReporter(str))
	if err != nil {
		log.Fatal(err)
	}
	server, err := libp2p.New(libp2p.ResourceManager(rmgr))
	if err != nil {
		log.Fatal(err)
	}

	// Make a bunch of clients that all ping the server at various times
	wg := sync.WaitGroup{}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			time.Sleep(time.Duration(i%100) * 100 * time.Millisecond)
			newClient(peer.AddrInfo{
				ID:    server.ID(),
				Addrs: server.Addrs(),
			}, i)
		}(i)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	// Listen for ctrl c
	ctrlC := make(chan os.Signal, 1)
	signal.Notify(ctrlC, os.Interrupt)

	select {
	case <-done:
	case <-ctrlC:
	}

}

func newClient(serverInfo peer.AddrInfo, pings int) {
	client, err := libp2p.New(
		// We just want metrics from h2
		libp2p.DisableMetrics(),
		libp2p.NoListenAddrs,
	)
	if err != nil {
		log.Fatal(err)
	}

	client.Connect(context.Background(), serverInfo)

	p := ping.Ping(context.Background(), client, serverInfo.ID)

	pingSoFar := 0

	for pingSoFar < pings {
		res := <-p
		pingSoFar++
		if res.Error != nil {
			log.Fatal(res.Error)
		}
		time.Sleep(time.Second)
	}

}
