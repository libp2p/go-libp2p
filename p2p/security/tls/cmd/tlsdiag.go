//go:build !js
// +build !js

package main

import (
	"fmt"
	"os"

	"github.com/libp2p/go-libp2p/p2p/security/tls/cmd/tlsdiag"
)

func main() {
	if len(os.Args) <= 1 {
		fmt.Println("missing argument: client / server")
		return
	}

	role := os.Args[1]
	// remove the role argument from os.Args
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)

	var err error
	switch role {
	case "client":
		err = tlsdiag.StartClient()
	case "server":
		err = tlsdiag.StartServer()
	default:
		fmt.Println("invalid argument. Expected client / server")
		return
	}
	if err != nil {
		panic(err)
	}
}
