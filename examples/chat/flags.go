package main

import (
	"flag"
	"fmt"
	"strings"

	maddr "github.com/multiformats/go-multiaddr"
)

// A newtype we need to create for sake of writing a custom flag parser
type addrList []maddr.Multiaddr

func (al *addrList) String() string {
	strs := make([]string, len(*al))
	for i, addr := range *al {
		strs[i] = addr.String()
	}
	return strings.Join(strs, ",")
}

func (al *addrList) Set(value string) error {
	addr, err := maddr.NewMultiaddr(value)
	if err != nil {
		return err
	}
	*al = append(*al, addr)
	return nil
}

type TorConfig struct {
	KeyPath         string
	Username        string
	Password        string
	ControlAddress  string
	ControlNet      string
	ControlPassword string
	OnlyOnion       bool
	Port            int
}

type Config struct {
	TopicName       string
	BootstrapPeers  addrList
	ListenAddresses addrList
	TorConfig       *TorConfig
}

func ParseFlags() Config {
	config := Config{TorConfig: &TorConfig{}}
	flag.StringVar(&config.TopicName, "topic", "libp2p-demo-chat", "Sets the name of the topic to chat on")
	flag.StringVar(&config.TorConfig.KeyPath, "tor-key-path", "", "Sets the path to the tor hidden service keys")
	flag.StringVar(&config.TorConfig.Password, "tor-password", "", "Sets the password for authenticating the tor proxy and controller")
	flag.StringVar(&config.TorConfig.Username, "tor-username", "", "Sets the username for authenticating the tor proxy")
	flag.StringVar(&config.TorConfig.ControlNet, "tor-control-net", "tcp4", "Sets the network protocol to use for tor")
	flag.StringVar(&config.TorConfig.ControlAddress, "tor-control-addr", "127.0.0.1:9051", "Sets the tor control address")
	flag.StringVar(&config.TorConfig.ControlPassword, "tor-control-password", "", "Sets the tor control password")
	flag.BoolVar(&config.TorConfig.OnlyOnion, "tor-only-onion", false, "Only use tor transport for onion addresses")
	flag.IntVar(&config.TorConfig.Port, "tor-port", 0, "")
	flag.Var(&config.BootstrapPeers, "peer", "Adds a peer multiaddress to the bootstrap list")
	flag.Var(&config.ListenAddresses, "listen", "Adds a multiaddress to the listen list")
	flag.Parse()

	tcfg := config.TorConfig
	if tcfg.KeyPath == "" || tcfg.Password == "" || tcfg.ControlAddress == "" || tcfg.Port < 1 {
		fmt.Println("Must provide all tor configurations in order to use tor transports.")
		config.TorConfig = nil
	}

	return config
}
