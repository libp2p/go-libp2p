package negotiation

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	tls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	"github.com/stretchr/testify/require"
)

var (
	noiseOpt = libp2p.Security("/noise", noise.New)
	tlsOpt   = libp2p.Security("/tls", tls.New)
)

func TestSecurityNegotiation(t *testing.T) {
	testcases := []testcase{
		{
			Name:             "server and client have the same preference",
			ServerPreference: []libp2p.Option{tlsOpt, noiseOpt},
			ClientPreference: []libp2p.Option{tlsOpt, noiseOpt},
			Expected:         "/tls",
		},
		{
			Name:             "client only supports one security",
			ServerPreference: []libp2p.Option{tlsOpt, noiseOpt},
			ClientPreference: []libp2p.Option{noiseOpt},
			Expected:         "/noise",
		},
		{
			Name:             "server only supports one security",
			ServerPreference: []libp2p.Option{noiseOpt},
			ClientPreference: []libp2p.Option{tlsOpt, noiseOpt},
			Expected:         "/noise",
		},
		{
			Name:             "no  overlap",
			ServerPreference: []libp2p.Option{noiseOpt},
			ClientPreference: []libp2p.Option{tlsOpt},
			Error:            "failed to negotiate security protocol: protocols not supported",
		},
	}

	clientID, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	serverID, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)

	for _, tc := range testcases {

		t.Run(tc.Name, func(t *testing.T) {
			server, err := libp2p.New(
				libp2p.Identity(serverID),
				libp2p.ChainOptions(tc.ServerPreference...),
				libp2p.Transport(tcp.NewTCPTransport),
				libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
			)
			require.NoError(t, err)

			client, err := libp2p.New(
				libp2p.Identity(clientID),
				libp2p.ChainOptions(tc.ClientPreference...),
				libp2p.Transport(tcp.NewTCPTransport),
				libp2p.NoListenAddrs,
			)
			require.NoError(t, err)

			err = client.Connect(context.Background(), peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()})
			if tc.Error != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.Error)
				return
			}

			require.NoError(t, err)
			conns := client.Network().ConnsToPeer(server.ID())
			require.Len(t, conns, 1, "expected exactly one connection")
			require.Equal(t, tc.Expected, conns[0].ConnState().Security)
		})
	}
}
