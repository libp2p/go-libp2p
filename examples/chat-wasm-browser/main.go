//go:build js

package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"syscall/js"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
)

const chatProtocol = "/chat/1.0.0"

func main() {
	g := js.Global()
	out := g.Get("output")
	maddr := g.Get("maddr")
	connect := g.Get("connect")
	document := g.Get("document")
	selfp := g.Get("self")
	chats := g.Get("chats")

	h, err := libp2p.New()
	if err != nil {
		out.Set("innerText", "Error starting libp2p, check the console !")
		panic(err)
	}
	self := h.ID().String()
	selfp.Set("innerText", self)

	connect.Set("onclick", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		m := maddr.Get("value").String()
		maddr.Set("value", "")
		out.Set("innerText", "Contacting "+m)
		// Start a new goroutine because are about to do IO and we don't want to block JS's event loop.
		go func() {
			err := func() error {
				info, err := peer.AddrInfoFromString(m)
				if err != nil {
					return fmt.Errorf("parsing maddr: %w", err)
				}

				h.Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.PermanentAddrTTL)
				s, err := h.NewStream(context.Background(), info.ID, chatProtocol)
				if err != nil {
					return fmt.Errorf("NewStream: %w", err)
				}

				anchor := document.Call("createElement", "div")
				peerid := document.Call("createElement", "p")
				them := info.ID.Pretty()
				peerid.Set("innerText", "Connected with: "+them)
				anchor.Call("appendChild", peerid)

				text := document.Call("createElement", "div")
				addLine := func(strs ...string) {
					line := document.Call("createElement", "p")
					var textBuf strings.Builder
					for _, s := range strs {
						textBuf.WriteString(s)
					}
					line.Set("innerText", textBuf.String())
					text.Call("appendChild", line)
				}
				anchor.Call("appendChild", text)

				entry := document.Call("createElement", "input")
				entry.Set("onkeyup", js.FuncOf(func(_ js.Value, args []js.Value) any {
					if len(args) != 1 {
						panic("expected 1 argument callback from onkeyup")
					}
					event := args[0]

					if event.Get("key").String() != "Enter" {
						return nil
					}
					toSend := entry.Get("value").String()
					entry.Set("value", "")
					go func() {
						_, err := strings.NewReader(toSend).WriteTo(s)
						if err != nil {
							addLine("Error sending: ", err.Error())
							return
						}
						_, err = strings.NewReader("\n").WriteTo(s)
						if err != nil {
							addLine("Error sending: ", err.Error())
							return
						}
						addLine(self, "> ", toSend)
					}()
					return nil
				}))
				anchor.Call("appendChild", entry)

				chats.Call("appendChild", anchor)

				scanner := bufio.NewScanner(s)
				for scanner.Scan() {
					message := strings.Trim(scanner.Text(), " \n")
					if message == "" {
						continue
					}
					addLine(them, "> ", message)
				}
				if err := scanner.Err(); err != nil {
					addLine("Error receiving: ", err.Error())
				}

				return nil
			}()
			if err != nil {
				s := "error contacting " + m + ": " + err.Error()
				fmt.Println(s)
				out.Set("innerText", s)
			}
		}()

		return nil
	}))

	select {}
}
