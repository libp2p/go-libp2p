package nat

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/huin/goupnp"
	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/huin/goupnp/dcps/internetgateway2"

	"github.com/koron/go-ssdp"
)

var _ NAT = (*upnp_NAT)(nil)

func discoverUPNP_IG1(ctx context.Context) ([]NAT, []error) {
	return discoverSearchTarget(ctx, internetgateway1.URN_WANConnectionDevice_1)
}

func discoverUPNP_IG2(ctx context.Context) ([]NAT, []error) {
	return discoverSearchTarget(ctx, internetgateway2.URN_WANConnectionDevice_2)
}

func discoverSearchTarget(ctx context.Context, target string) (nats []NAT, errs []error) {
	// find devices
	devs, err := goupnp.DiscoverDevicesCtx(ctx, target)
	if err != nil {
		errs = append(errs, err)
		return
	}

	for _, dev := range devs {
		if dev.Err != nil {
			errs = append(errs, dev.Err)
			continue
		}
		dev.Root.Device.VisitServices(serviceVisitor(ctx, dev.Root, &nats, &errs))
	}
	return
}

// discoverUPNP_GenIGDev is a fallback for routers that fail to respond to our
// targetted SSDP queries. It will query all devices and try to find any
// InternetGatewayDevice.
func discoverUPNP_GenIGDev(ctx context.Context) (nats []NAT, errs []error) {
	DeviceList, err := ssdp.Search(ssdp.All, 5, "")
	if err != nil {
		errs = append(errs, err)
		return
	}

	// Limit the number of InternetGateways we'll query. Normally we'd only
	// expect 1 or 2, but in case of a weird network we also don't want to do
	// too much work.
	const maxIGDevs = 3
	foundIGDevs := 0
	for _, Service := range DeviceList {
		if !strings.Contains(Service.Type, "InternetGatewayDevice") {
			continue
		}
		if foundIGDevs >= maxIGDevs {
			log.Debug("found more than maxIGDevs UPnP devices, stopping search")
			break
		}
		foundIGDevs++

		DeviceURL, err := url.Parse(Service.Location)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		RootDevice, err := goupnp.DeviceByURLCtx(ctx, DeviceURL)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		RootDevice.Device.VisitServices(serviceVisitor(ctx, RootDevice, &nats, &errs))
	}
	return
}

// serviceVisitor is a vistor function that visits all services of a root
// device and collects NATs.
//
// It works on InternetGateway V1 and V2 devices. For V1 devices, V2 services should not be encountered, and the visitor will collect an error in that case.
func serviceVisitor(ctx context.Context, rootDevice *goupnp.RootDevice, outNats *[]NAT, outErrs *[]error) func(srv *goupnp.Service) {
	return func(srv *goupnp.Service) {
		if ctx.Err() != nil {
			return
		}
		switch srv.ServiceType {
		case internetgateway2.URN_WANIPConnection_1:
			client := &internetgateway2.WANIPConnection1{ServiceClient: goupnp.ServiceClient{
				SOAPClient: srv.NewSOAPClient(),
				RootDevice: rootDevice,
				Service:    srv,
			}}
			_, isNat, err := client.GetNATRSIPStatusCtx(ctx)
			if err != nil {
				*outErrs = append(*outErrs, err)
			} else if isNat {
				*outNats = append(*outNats, &upnp_NAT{client, make(map[int]int), "UPNP (IP1)", rootDevice})
			}

		case internetgateway2.URN_WANIPConnection_2:
			if rootDevice.Device.DeviceType == internetgateway2.URN_WANConnectionDevice_1 {
				*outErrs = append(*outErrs, fmt.Errorf("found V2 service on V1 device"))
				return
			}
			client := &internetgateway2.WANIPConnection2{ServiceClient: goupnp.ServiceClient{
				SOAPClient: srv.NewSOAPClient(),
				RootDevice: rootDevice,
				Service:    srv,
			}}
			_, isNat, err := client.GetNATRSIPStatusCtx(ctx)
			if err != nil {
				*outErrs = append(*outErrs, err)
			} else if isNat {
				*outNats = append(*outNats, &upnp_NAT{client, make(map[int]int), "UPNP (IP2)", rootDevice})
			}

		case internetgateway2.URN_WANPPPConnection_1:
			client := &internetgateway2.WANPPPConnection1{ServiceClient: goupnp.ServiceClient{
				SOAPClient: srv.NewSOAPClient(),
				RootDevice: rootDevice,
				Service:    srv,
			}}
			_, isNat, err := client.GetNATRSIPStatusCtx(ctx)
			if err != nil {
				*outErrs = append(*outErrs, err)
			} else if isNat {
				*outNats = append(*outNats, &upnp_NAT{client, make(map[int]int), "UPNP (PPP1)", rootDevice})
			}
		}
	}
}

type upnp_NAT_Client interface {
	GetExternalIPAddress() (string, error)
	AddPortMappingCtx(context.Context, string, uint16, string, uint16, string, bool, string, uint32) error
	DeletePortMappingCtx(context.Context, string, uint16, string) error
}

type upnp_NAT struct {
	c          upnp_NAT_Client
	ports      map[int]int
	typ        string
	rootDevice *goupnp.RootDevice
}

func (u *upnp_NAT) GetExternalAddress() (addr net.IP, err error) {
	ipString, err := u.c.GetExternalIPAddress()
	if err != nil {
		return nil, err
	}

	ip := net.ParseIP(ipString)
	if ip == nil {
		return nil, ErrNoExternalAddress
	}

	return ip, nil
}

func mapProtocol(s string) string {
	switch s {
	case "udp":
		return "UDP"
	case "tcp":
		return "TCP"
	default:
		panic("invalid protocol: " + s)
	}
}

func (u *upnp_NAT) AddPortMapping(ctx context.Context, protocol string, internalPort int, description string, timeout time.Duration) (int, error) {
	ip, err := u.GetInternalAddress()
	if err != nil {
		return 0, nil
	}

	timeoutInSeconds := uint32(timeout / time.Second)

	if externalPort := u.ports[internalPort]; externalPort > 0 {
		err = u.c.AddPortMappingCtx(ctx, "", uint16(externalPort), mapProtocol(protocol), uint16(internalPort), ip.String(), true, description, timeoutInSeconds)
		if err == nil {
			return externalPort, nil
		}
	}

	for i := 0; i < 3; i++ {
		externalPort := randomPort()
		err = u.c.AddPortMappingCtx(ctx, "", uint16(externalPort), mapProtocol(protocol), uint16(internalPort), ip.String(), true, description, timeoutInSeconds)
		if err == nil {
			u.ports[internalPort] = externalPort
			return externalPort, nil
		}
	}

	return 0, err
}

func (u *upnp_NAT) DeletePortMapping(ctx context.Context, protocol string, internalPort int) error {
	if externalPort := u.ports[internalPort]; externalPort > 0 {
		delete(u.ports, internalPort)
		return u.c.DeletePortMappingCtx(ctx, "", uint16(externalPort), mapProtocol(protocol))
	}

	return nil
}

func (u *upnp_NAT) GetDeviceAddress() (net.IP, error) {
	addr, err := net.ResolveUDPAddr("udp4", u.rootDevice.URLBase.Host)
	if err != nil {
		return nil, err
	}

	return addr.IP, nil
}

func (u *upnp_NAT) GetInternalAddress() (net.IP, error) {
	devAddr, err := u.GetDeviceAddress()
	if err != nil {
		return nil, err
	}

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
				if x.Contains(devAddr) {
					return x.IP, nil
				}
			}
		}
	}

	return nil, ErrNoInternalAddress
}

func (n *upnp_NAT) Type() string { return n.typ }
