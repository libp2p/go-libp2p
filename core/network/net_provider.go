package network

import "net"

type NetProvider interface {
	Interfaces() ([]net.Interface, error)
	InterfaceAddrs() ([]net.Addr, error)
}

type defaultNetProvider struct{}

func (defaultNetProvider) Interfaces() ([]net.Interface, error) {
	return net.Interfaces()
}

func (defaultNetProvider) InterfaceAddrs() ([]net.Addr, error) {
	return net.InterfaceAddrs()
}

var netProvider NetProvider = defaultNetProvider{}

func Interfaces() ([]net.Interface, error) {
	return netProvider.Interfaces()
}

func InterfaceAddrs() ([]net.Addr, error) {
	return netProvider.InterfaceAddrs()
}

func SetNetProvider(provider NetProvider) {
	netProvider = provider
}
