package nat

import (
	"context"
	"net"
	"time"

	natpmp "github.com/jackpal/go-nat-pmp"
)

var (
	_ NAT = (*natpmpNAT)(nil)
)

func discoverNATPMP(ctx context.Context) (NAT, error) {
	ip, err := getDefaultGateway()
	if err != nil {
		return nil, err
	}

	clientCh := make(chan *natpmp.Client, 1)
	errCh := make(chan error, 1)

	// We can't cancel the natpmp library, but we can at least still return
	// on context cancellation by putting this in a goroutine
	go func() {
		client, err := discoverNATPMPWithAddr(ctx, ip)
		if err != nil {
			errCh <- err
			return
		}
		clientCh <- client
	}()

	select {
	case client := <-clientCh:
		return &natpmpNAT{client, ip, make(map[int]int)}, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func discoverNATPMPWithAddr(ctx context.Context, ip net.IP) (*natpmp.Client, error) {
	var client *natpmp.Client
	if deadline, ok := ctx.Deadline(); ok {
		client = natpmp.NewClientWithTimeout(ip, time.Until(deadline))
	} else {
		client = natpmp.NewClient(ip)
	}
	_, err := client.GetExternalAddress()
	if err != nil {
		return nil, err
	}
	return client, nil
}

type natpmpNAT struct {
	c       *natpmp.Client
	gateway net.IP
	ports   map[int]int
}

func (n *natpmpNAT) GetDeviceAddress() (addr net.IP, err error) {
	return n.gateway, nil
}

func (n *natpmpNAT) GetInternalAddress() (addr net.IP, err error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}

		for _, addr := range addrs {
			switch x := addr.(type) {
			case *net.IPNet:
				if x.Contains(n.gateway) {
					return x.IP, nil
				}
			}
		}
	}

	return nil, ErrNoInternalAddress
}

func (n *natpmpNAT) GetExternalAddress() (addr net.IP, err error) {
	res, err := n.c.GetExternalAddress()
	if err != nil {
		return nil, err
	}

	d := res.ExternalIPAddress
	return net.IPv4(d[0], d[1], d[2], d[3]), nil
}

func (n *natpmpNAT) AddPortMapping(_ context.Context, protocol string, internalPort int, _ string, timeout time.Duration) (int, error) {
	var (
		err error
	)

	timeoutInSeconds := int(timeout / time.Second)

	if externalPort := n.ports[internalPort]; externalPort > 0 {
		_, err = n.c.AddPortMapping(protocol, internalPort, externalPort, timeoutInSeconds)
		if err == nil {
			n.ports[internalPort] = externalPort
			return externalPort, nil
		}
	}

	for i := 0; i < 3; i++ {
		externalPort := randomPort()
		_, err = n.c.AddPortMapping(protocol, internalPort, externalPort, timeoutInSeconds)
		if err == nil {
			n.ports[internalPort] = externalPort
			return externalPort, nil
		}
	}

	return 0, err
}

func (n *natpmpNAT) DeletePortMapping(_ context.Context, _ string, internalPort int) (err error) {
	delete(n.ports, internalPort)
	return nil
}

func (n *natpmpNAT) Type() string {
	return "NAT-PMP"
}
