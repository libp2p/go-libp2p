package simconn

import (
	"errors"
	"fmt"
	"net"
)

type SimpleSimNet struct {
	router PerfectRouter
	links  []*SimulatedLink
}

type NodeBiDiLinkSettings struct {
	Downlink LinkSettings
	Uplink   LinkSettings
}

func (n *SimpleSimNet) Start() error {
	for _, link := range n.links {
		link.Start()
	}
	return nil
}

func (n *SimpleSimNet) Close() error {
	var errs error
	for _, link := range n.links {
		err := link.Close()
		if err != nil {
			errs = errors.Join(errs, err)
		}
	}
	if errs != nil {
		return fmt.Errorf("failed to close some links: %w", errs)
	}
	return nil
}

func (n *SimpleSimNet) AddNode(addr net.Addr, conn *SimConn, linkSettings NodeBiDiLinkSettings) {
	link := &SimulatedLink{
		DownlinkSettings: linkSettings.Downlink,
		UplinkSettings:   linkSettings.Uplink,
		UploadPacket:     &n.router,
		DownloadPacket:   conn,
	}
	conn.router = link
	n.links = append(n.links, link)
	n.router.AddNode(addr, link)
}

func (n *SimpleSimNet) RemoveNode(addr net.Addr) {
	n.router.RemoveNode(addr)
}

func (n *SimpleSimNet) SendPacket(p Packet) error {
	return n.router.SendPacket(p)
}
