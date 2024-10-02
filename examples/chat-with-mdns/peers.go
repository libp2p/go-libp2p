package main

import (
	"bufio"
	"fmt"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
)

type Peers struct {
	peers  map[string]*Peer
	peerMu sync.Mutex
}

func (p *Peers) AddPeer(peer *Peer) {
	p.peerMu.Lock()
	defer p.peerMu.Unlock()

	if _, ok := p.peers[peer.id]; !ok {
		p.peers[peer.id] = peer
	}
}

func (p *Peers) SendAll(msg string) {
	for _, peer := range p.peers {
		peer.write(msg)
	}
}

type Peer struct {
	stream  network.Stream
	stdinCh chan string
	stopCh  chan any
	id      string
}

func (p *Peer) Start() {
	w := bufio.NewWriter(bufio.NewWriter(p.stream))

	p.stopCh = make(chan any, 1)
	p.stdinCh = make(chan string, 10)

	go p.writeData(w, p.stdinCh, p.stopCh)
}

func (p *Peer) writeData(w *bufio.Writer, inCh chan string, stopCh chan any) {
	for {
		select {
		case in := <-inCh:
			_, err := fmt.Fprintf(w, "%s\n", in)
			if err != nil {
				fmt.Printf("Error writing to buffer: %s\n", err.Error())

				return
			}

			err = w.Flush()
			if err != nil {
				fmt.Printf("Error flushing buffer: %s\n", err.Error())

				return
			}

		case <-stopCh:
			return
		}
	}
}

func (p *Peer) Stop() {
	defer close(p.stopCh)
	defer close(p.stdinCh)

	p.stopCh <- struct{}{}
}

func (p *Peer) write(msg string) {
	p.stdinCh <- msg
}
