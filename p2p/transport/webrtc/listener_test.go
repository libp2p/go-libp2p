package libp2pwebrtc

import (
	"context"
	"net"
	"testing"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/pion/ice/v4"
	"github.com/pion/stun/v3"
	"github.com/stretchr/testify/require"
)

// iceClientConn is a minimal controlling ICE agent on a single UDP socket. It
// sends connectivity checks to the listener, answers the listener's checks,
// and passes all non-STUN packets through to its reader (the DTLS client).
type iceClientConn struct {
	*net.UDPConn
	raddr   *net.UDPAddr
	ufrag   string // WebRTC Direct v1: the ufrag and password of both sides
	checked bool   // a check sent by us got a success response
}

func (c *iceClientConn) sendCheck(nominate bool) error {
	setters := []stun.Setter{
		stun.BindingRequest, stun.TransactionID,
		stun.NewUsername(c.ufrag + ":" + c.ufrag),
		ice.AttrControlling(1),
		ice.PriorityAttr(1),
	}
	if nominate {
		setters = append(setters, ice.UseCandidate())
	}
	setters = append(setters, stun.NewShortTermIntegrity(c.ufrag), stun.Fingerprint)
	msg, err := stun.Build(setters...)
	if err != nil {
		return err
	}
	_, err = c.WriteTo(msg.Raw, c.raddr)
	return err
}

// readPacket reads one packet. STUN packets are handled here and reported with
// n == 0.
func (c *iceClientConn) readPacket(p []byte) (int, net.Addr, error) {
	n, addr, err := c.UDPConn.ReadFrom(p)
	if err != nil || !stun.IsMessage(p[:n]) {
		return n, addr, err
	}
	m := &stun.Message{Raw: append([]byte(nil), p[:n]...)}
	if err := m.Decode(); err != nil {
		return 0, addr, nil
	}
	switch m.Type {
	case stun.BindingSuccess:
		c.checked = true
	case stun.BindingRequest:
		udpAddr := addr.(*net.UDPAddr)
		resp, err := stun.Build(m, stun.BindingSuccess,
			&stun.XORMappedAddress{IP: udpAddr.IP, Port: udpAddr.Port},
			stun.NewShortTermIntegrity(c.ufrag), stun.Fingerprint,
		)
		if err != nil {
			return 0, addr, err
		}
		if _, err := c.WriteTo(resp.Raw, addr); err != nil {
			return 0, addr, err
		}
	}
	return 0, addr, nil
}

func (c *iceClientConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.readPacket(p)
		if err != nil || n > 0 {
			return n, addr, err
		}
	}
}

// TestListenerHandshakeBeforeNomination dials the listener the way browsers
// sometimes do: the first connectivity check carries no USE-CANDIDATE, the
// DTLS ClientHello follows as soon as that check is answered, and the
// nomination only comes seconds later. The listener must not wait for the
// nomination to run the DTLS handshake.
func TestListenerHandshakeBeforeNomination(t *testing.T) {
	const nominationDelay = 4 * time.Second

	tr, _ := getTransport(t)
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/webrtc-direct"))
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		// This test stops after DTLS, so no connection is ever accepted.
		for {
			if _, err := ln.Accept(); err != nil {
				return
			}
		}
	}()

	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer udpConn.Close()
	conn := &iceClientConn{UDPConn: udpConn, raddr: ln.Addr().(*net.UDPAddr), ufrag: genUfrag()}

	// The first check, without USE-CANDIDATE. Wait for its success response.
	require.NoError(t, conn.sendCheck(false))
	require.NoError(t, udpConn.SetReadDeadline(time.Now().Add(5*time.Second)))
	buf := make([]byte, 1500)
	for !conn.checked {
		_, _, err := conn.readPacket(buf)
		require.NoError(t, err)
	}
	require.NoError(t, udpConn.SetReadDeadline(time.Time{}))

	start := time.Now()
	nominate := time.AfterFunc(nominationDelay, func() { _ = conn.sendCheck(true) })
	defer nominate.Stop()

	cert, err := selfsign.GenerateSelfSigned()
	require.NoError(t, err)
	dtlsConn, err := dtls.ClientWithOptions(conn, conn.raddr,
		dtls.WithCertificates(cert),
		dtls.WithInsecureSkipVerify(true),
		dtls.WithSRTPProtectionProfiles(dtls.SRTP_AEAD_AES_128_GCM, dtls.SRTP_AES128_CM_HMAC_SHA1_80),
	)
	require.NoError(t, err)
	defer dtlsConn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*nominationDelay)
	defer cancel()
	require.NoError(t, dtlsConn.HandshakeContext(ctx))

	took := time.Since(start)
	t.Logf("DTLS handshake done %s after the first successful check", took)
	require.Less(t, took, nominationDelay/2, "the listener waited for the nomination to start DTLS")
}

func TestSelectFirstAuthenticatedPair(t *testing.T) {
	check, err := stun.Build(stun.BindingRequest, stun.TransactionID)
	require.NoError(t, err)
	nomination, err := stun.Build(stun.BindingRequest, stun.TransactionID, ice.UseCandidate())
	require.NoError(t, err)
	a, b := &ice.CandidatePair{}, &ice.CandidatePair{}

	selectPair := newSelectFirstAuthenticatedPair()
	require.True(t, selectPair(check, nil, nil, a), "the first authenticated check selects its pair")
	require.False(t, selectPair(check, nil, nil, a))
	require.False(t, selectPair(check, nil, nil, b), "a check without USE-CANDIDATE does not switch pairs")
	require.False(t, selectPair(nomination, nil, nil, a), "the nominated pair is already selected")
	require.True(t, selectPair(nomination, nil, nil, b), "a nomination switches to its pair")
	require.False(t, selectPair(nomination, nil, nil, b))
	require.True(t, selectPair(nomination, nil, nil, a), "a later nomination wins again")
}
