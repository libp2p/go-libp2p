package network

// NATDeviceType indicates the type of the NAT device.
type NATDeviceType int

const (
	// NATDeviceTypeUnknown indicates that the type of the NAT device is unknown.
	NATDeviceTypeUnknown NATDeviceType = iota

	// NATDeviceTypeEndpointIndependent is a NAT device that maps addresses
	// independent of the destination address. An EndpointIndependent NAT is
	// a NAT where all outgoing connections from the same source IP address
	// and port are mapped by the NAT device to the same IP address and port
	// irrespective of the destination endpoint.
	//
	// NAT traversal with hole punching is possible with an
	// EndpointIndependent NAT ONLY if the remote peer is ALSO behind an
	// EndpointIndependent NAT. If the remote peer is behind an
	// EndpointDependent NAT, hole punching will fail.
	NATDeviceTypeEndpointIndependent

	// NATDeviceTypeEndpointDependent is a NAT device that maps addresses
	// depending on the destination address. An EndpointDependent NAT maps
	// outgoing connections with different destination addresses to
	// different IP addresses and ports, even if they originate from the
	// same source IP address and port.
	//
	// NAT traversal with hole-punching is currently NOT possible in libp2p
	// with EndpointDependent NATs irrespective of the remote peer's NAT
	// type.
	NATDeviceTypeEndpointDependent
)

const (
	// NATDeviceTypeCone is the same as endpoint independent
	//
	// Deprecated: Use NATDeviceTypeEndpointIndependent
	NATDeviceTypeCone = NATDeviceTypeEndpointIndependent
	// NATDeviceTypeSymmetric is the same as endpoint dependent
	//
	// Deprecated: Use NATDeviceTypeEndpointDependent
	NATDeviceTypeSymmetric = NATDeviceTypeEndpointDependent
)

func (r NATDeviceType) String() string {
	switch r {
	case 0:
		return "Unknown"
	case 1:
		return "Endpoint Independent"
	case 2:
		return "Endpoint Dependent"
	default:
		return "unrecognized"
	}
}

// NATTransportProtocol is the transport protocol for which the NAT Device Type has been determined.
type NATTransportProtocol int

const (
	// NATTransportUDP means that the NAT Device Type has been determined for the UDP Protocol.
	NATTransportUDP NATTransportProtocol = iota
	// NATTransportTCP means that the NAT Device Type has been determined for the TCP Protocol.
	NATTransportTCP
)

func (n NATTransportProtocol) String() string {
	switch n {
	case 0:
		return "UDP"
	case 1:
		return "TCP"
	default:
		return "unrecognized"
	}
}
