//go:build js

package libp2pwebtransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"syscall/js"

	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/sec"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/security/noise/pb"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/multiformats/go-multihash"
	"go.uber.org/multierr"
)

type transport struct {
	common
}

type Option func(*transport) error

func (t *transport) verifyChallengeOnOutboundConnection(ctx context.Context, conn net.Conn, p peer.ID, certHashes []multihash.DecodedMultihash) (sec.SecureConn, error) {
	// Now run a Noise handshake (using early data) and get all the certificate hashes from the server.
	// We will verify that the certhashes we used to dial is a subset of the certhashes we received from the server.
	var verified bool
	n, err := t.noise.WithSessionOptions(noise.EarlyData(newEarlyDataReceiver(func(b *pb.NoiseExtensions) error {
		decodedCertHashes, err := decodeCertHashesFromProtobuf(b.WebtransportCerthashes)
		if err != nil {
			return err
		}
		for _, sent := range certHashes {
			var found bool
			for _, rcvd := range decodedCertHashes {
				if sent.Code == rcvd.Code && bytes.Equal(sent.Digest, rcvd.Digest) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("missing cert hash: %v", sent)
			}
		}
		verified = true
		return nil
	}), nil))
	if err != nil {
		return nil, fmt.Errorf("failed to create Noise transport: %w", err)
	}
	c, err := n.SecureOutbound(ctx, conn, p)
	if err != nil {
		return nil, err
	}
	if err = c.Close(); err != nil {
		return nil, err
	}
	// The Noise handshake _should_ guarantee that our verification callback is called.
	// Double-check just in case.
	if !verified {
		return nil, errors.New("didn't verify")
	}

	return c, nil
}

func New(key ic.PrivKey, psk pnet.PSK, connManager *quicreuse.ConnManager, gater connmgr.ConnectionGater, rcmgr network.ResourceManager, opts ...Option) (tpt.Transport, error) {
	if len(psk) > 0 {
		return nil, errors.New("WebTransport doesn't support private networks yet")
	}
	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}
	pid, err := peer.IDFromPrivateKey(key)
	if err != nil {
		return nil, err
	}

	noiseTransport, err := noise.New(noise.ID, key, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize noise transport: %w", err)
	}

	t := &transport{
		common: common{
			privKey: key,
			pid:     pid,
			rcmgr:   rcmgr,
			gater:   gater,
			noise:   noiseTransport,
		},
	}

	// Apply any additional options
	for _, opt := range opts {
		if err := opt(t); err != nil {
			return nil, err
		}
	}

	return t, nil
}

func (t *transport) Protocols() []int {
	return []int{ma.P_WEBTRANSPORT}
}

func (t *transport) Proxy() bool {
	return false
}

func (t *transport) Listen(laddr ma.Multiaddr) (tpt.Listener, error) {
	return nil, errors.New("Listen is not supported in WASM for WebTransport")
}

func (t *transport) CanDial(addr ma.Multiaddr) bool {
	ok, _ := IsWebtransportMultiaddr(addr)
	return ok
}

// await tries to await a piece of code, it will leave the promise in an undefined
// state if the context is canceled or expires.
func await(ctx context.Context, v js.Value) (success []js.Value, err error) {
	// This does not look very efficient but I don't care about performance right
	// now and this makes the code WAY more readable than callback hell.
	c := make(chan struct{}, 1)
	var s, f js.Func
	s = js.FuncOf(func(_ js.Value, args []js.Value) any {
		success = args
		c <- struct{}{}
		s.Release()
		f.Release()
		return nil
	})
	f = js.FuncOf(func(_ js.Value, args []js.Value) any {
		errs := make([]error, len(args))
		for i, v := range args {
			errs[i] = errors.New(v.String())
		}
		err = fmt.Errorf("JS catch: %w", multierr.Combine(errs...))
		c <- struct{}{}
		s.Release()
		f.Release()
		return nil
	})

	// Here we create an adhoc promise that we will race against the real one.
	// This allows us to callback into s which will Release s and f. Removing
	// references to v and hopefully allowing the JS GC to cancel the promise.
	var resolve js.Value
	capture := js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve = args[0]
		return nil
	})
	promises := js.Global().Get("Promise")
	stopper := promises.New(capture)
	capture.Release()
	promises.Call("race", []any{v, stopper}).Call("then", s, f)
	select {
	case <-ctx.Done():
		resolve.Invoke() // This will trigger s and cleanup in a thread safe manner.
		return nil, ctx.Err()
	case <-c:
		return
	}
}

func byteSliceToJS(buf []byte) js.Value {
	uint8Array := js.Global().Get("Uint8Array").New(len(buf))
	if js.CopyBytesToJS(uint8Array, buf) != len(buf) {
		panic("expected to copy all bytes")
	}
	return uint8Array
}

func (t *transport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (tpt.CapableConn, error) {
	// Open a connection scope with the resource manager
	scope, err := t.rcmgr.OpenConnection(network.DirOutbound, false, raddr)
	if err != nil {
		log.Debugw("resource manager blocked outgoing connection", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}

	// Call the dialWithScope method to handle the actual dialing process
	c, err := t.dialWithScope(ctx, raddr, p, scope)
	if err != nil {
		scope.Done()
		return nil, err
	}

	return c, nil
}

func (t *transport) dialWithScope(ctx context.Context, raddr ma.Multiaddr, p peer.ID, scope network.ConnManagementScope) (tpt.CapableConn, error) {
	certHashes, err := extractCertHashes(raddr)
	if err != nil {
		return nil, err
	}

	if len(certHashes) == 0 {
		return nil, errors.New("can't dial webtransport without certhashes")
	}

	sni, _ := extractSNI(raddr)

	if err := scope.SetPeer(p); err != nil {
		log.Debugw("resource manager blocked outgoing connection for peer", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}

	maddr, _ := ma.SplitFunc(raddr, func(c ma.Component) bool { return c.Protocol().Code == ma.P_WEBTRANSPORT })
	return t.dial(ctx, maddr, p, sni, certHashes, scope)
}

func (t *transport) dial(ctx context.Context, tgt ma.Multiaddr, p peer.ID, sni string, certHashes []multihash.DecodedMultihash, scope network.ConnManagementScope) (tpt.CapableConn, error) {
	webtransport := js.Global().Get("WebTransport")
	if webtransport.IsUndefined() {
		return nil, fmt.Errorf("WebTransport is not supported in your browser")
	}

	var raddr string
	if sni != "" {
		raddr = sni
	} else {
		var err error
		_, raddr, err = manet.DialArgs(tgt)
		if err != nil {
			return nil, err
		}
	}

	url := fmt.Sprintf("https://%s%s?type=noise", raddr, webtransportHTTPEndpoint)

	ch := make([]any, len(certHashes))
	for i, h := range certHashes {
		if h.Code != multihash.SHA2_256 {
			// https://developer.mozilla.org/en-US/docs/Web/API/WebTransport/WebTransport#parameters
			// At time of writing, SHA-256 is the only hash algorithm listed in the specification.
			continue
		}

		ch[i] = map[string]any{
			"algorithm": "sha-256",
			"value":     byteSliceToJS(h.Digest),
		}
	}

	wt := webtransport.New(url, map[string]any{"serverCertificateHashes": ch})
	_, err := await(ctx, wt.Get("ready"))
	if err != nil {
		return nil, fmt.Errorf("initial connection: %w", err)
	}

	c := newConn(scope, t, wt, tgt, p, addr{url})

	s, err := c.openStream(ctx)
	if err != nil {
		c.Close()
		return nil, err
	}
	defer s.Close()

	verified, err := t.verifyChallengeOnOutboundConnection(ctx, s, p, certHashes)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("verifying challenge: %w", err)
	}
	c.rpk = verified.RemotePublicKey()

	return c, nil
}
