package quicreuse

import (
	"errors"
	"net"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/quic-go/quic-go"
)

var quicV1MA = ma.StringCast("/quic-v1")

const quicDraft29 quic.VersionNumber = 0xff00001d

var ErrQUICDraft29 = errors.New("QUIC draft-29 has been removed, QUIC (RFC 9000) is accessible with /quic-v1")

func ToQuicMultiaddr(na net.Addr, version quic.VersionNumber) (ma.Multiaddr, error) {
	udpMA, err := manet.FromNetAddr(na)
	if err != nil {
		return nil, err
	}
	switch version {
	case quic.Version1:
		return udpMA.Encapsulate(quicV1MA), nil
	case quicDraft29:
		return nil, ErrQUICDraft29
	default:
		return nil, errors.New("unknown QUIC version")
	}
}

func FromQuicMultiaddr(addr ma.Multiaddr) (*net.UDPAddr, quic.VersionNumber, error) {
	var version quic.VersionNumber
	var partsBeforeQUIC []ma.Multiaddr
	ma.ForEach(addr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_QUIC_V1:
			version = quic.Version1
			return false
		case ma.P_QUIC:
			version = quicDraft29
			return false
		default:
			partsBeforeQUIC = append(partsBeforeQUIC, &c)
			return true
		}
	})
	if len(partsBeforeQUIC) == 0 {
		return nil, version, errors.New("no addr before QUIC component")
	}
	switch version {
	case 0: // not found
		return nil, version, errors.New("unknown QUIC version")
	case quicDraft29:
		return nil, version, ErrQUICDraft29
	}
	netAddr, err := manet.ToNetAddr(ma.Join(partsBeforeQUIC...))
	if err != nil {
		return nil, version, err
	}
	udpAddr, ok := netAddr.(*net.UDPAddr)
	if !ok {
		return nil, 0, errors.New("not a *net.UDPAddr")
	}
	return udpAddr, version, nil
}
