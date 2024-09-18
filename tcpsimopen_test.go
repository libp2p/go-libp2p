package libp2p_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	tls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/stretchr/testify/require"
)

func TestSimOpen(t *testing.T) {
	tls := libp2p.Security(tls.ID, tls.New)
	noise := libp2p.Security(noise.ID, noise.New)

	type testCase struct {
		name            string
		securityOptions []libp2p.Option
	}

	testCasesPerHost := []testCase{
		{"TLS,Noise", []libp2p.Option{tls, noise}},
		{"Noise,TLS", []libp2p.Option{noise, tls}},
		{"TLS", []libp2p.Option{tls}},
		{"Noise", []libp2p.Option{noise}},
	}

	noIntersection := func(a, b []libp2p.Option) bool {
		for _, aOpt := range a {
			for _, bOpt := range b {
				if reflect.ValueOf(aOpt).Pointer() == reflect.ValueOf(bOpt).Pointer() {
					return false
				}
			}
		}
		return true
	}

	for _, tc1 := range testCasesPerHost {
		for _, tc2 := range testCasesPerHost {
			t.Run(fmt.Sprintf("h1(%s)<->h2(%s)", tc1.name, tc2.name), func(t *testing.T) {
				newHosts := func() (host.Host, host.Host) {
					h1, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.ChainOptions(tc1.securityOptions...))
					require.NoError(t, err)
					h2, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.ChainOptions(tc2.securityOptions...))
					require.NoError(t, err)
					return h1, h2
				}
				closeHosts := func(hs ...host.Host) {
					for _, h := range hs {
						require.NoError(t, h.Close())
					}
				}

				simConnect := func(ctx context.Context, h1, h2 host.Host) error {
					errs := make(chan error, 2)
					go func() {
						errs <- h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
					}()
					go func() {
						errs <- h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()})
					}()
					return errors.Join(<-errs, <-errs)
				}
				closeConns := func(hs ...host.Host) {
					for _, h := range hs {
						for _, c := range h.Network().Conns() {
							require.NoError(t, c.Close())
						}
					}
				}

				if noIntersection(tc1.securityOptions, tc2.securityOptions) {
					// This is going to fail because there is no common security protocol
					for i := 0; i < 3; i++ {
						h1, h2 := newHosts()
						ctx, cancel := context.WithCancel(context.Background())
						require.Error(t, simConnect(ctx, h1, h2), "iteration %d", i)
						cancel()
						closeHosts(h1, h2)
					}
				} else {
					for i := 0; i < 100; i++ {
						h1, h2 := newHosts()
						ctx, cancel := context.WithCancel(context.Background())
						require.NoError(t, simConnect(ctx, h1, h2), "iteration %d", i)
						cancel()
						closeConns(h1, h2)
						closeHosts(h1, h2)
					}
				}

			})
		}
	}
}
