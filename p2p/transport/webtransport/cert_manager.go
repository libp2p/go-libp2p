package libp2pwebtransport

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	pb "github.com/libp2p/go-libp2p/p2p/transport/webtransport/pb"

	"github.com/benbjohnson/clock"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multihash"
)

// Allow for a bit of clock skew.
// When we generate a certificate, the NotBefore time is set to clockSkewAllowance before the current time.
// Similarly, we stop using a certificate one clockSkewAllowance before its expiry time.
const clockSkewAllowance = time.Hour

type certConfig struct {
	tlsConf *tls.Config
	sha256  [32]byte // cached from the tlsConf
}

func (c *certConfig) Start() time.Time { return c.tlsConf.Certificates[0].Leaf.NotBefore }
func (c *certConfig) End() time.Time   { return c.tlsConf.Certificates[0].Leaf.NotAfter }

func newCertConfig(start, end time.Time) (*certConfig, error) {
	conf, err := getTLSConf(start, end)
	if err != nil {
		return nil, err
	}
	return &certConfig{
		tlsConf: conf,
		sha256:  sha256.Sum256(conf.Certificates[0].Leaf.Raw),
	}, nil
}

// Certificate renewal logic:
//  1. On startup, we generate one cert that is valid from now (-1h, to allow for clock skew), and another
//     cert that is valid from the expiry date of the first certificate (again, with allowance for clock skew).
//  2. Once we reach 1h before expiry of the first certificate, we switch over to the second certificate.
//     At the same time, we stop advertising the certhash of the first cert and generate the next cert.
type certManager struct {
	clock     clock.Clock
	ctx       context.Context
	ctxCancel context.CancelFunc
	refCount  sync.WaitGroup

	mx            sync.RWMutex
	lastConfig    *certConfig // initially nil
	currentConfig *certConfig
	nextConfig    *certConfig // nil until we have passed half the certValidity of the current config
	addrComp      ma.Multiaddr

	protobuf []byte
}

func newCertManager(clock clock.Clock) (*certManager, error) {
	m := &certManager{clock: clock}
	m.ctx, m.ctxCancel = context.WithCancel(context.Background())
	if err := m.init(); err != nil {
		return nil, err
	}

	m.background()
	return m, nil
}

func (m *certManager) init() error {
	start := m.clock.Now().Add(-clockSkewAllowance)
	var err error
	m.nextConfig, err = newCertConfig(start, start.Add(certValidity))
	if err != nil {
		return err
	}
	return m.rollConfig()
}

func (m *certManager) rollConfig() error {
	// We stop using the current certificate clockSkewAllowance before its expiry time.
	// At this point, the next certificate needs to be valid for one clockSkewAllowance.
	nextStart := m.nextConfig.End().Add(-2 * clockSkewAllowance)
	c, err := newCertConfig(nextStart, nextStart.Add(certValidity))
	if err != nil {
		return err
	}
	m.lastConfig = m.currentConfig
	m.currentConfig = m.nextConfig
	m.nextConfig = c
	if err := m.cacheProtobuf(); err != nil {
		return err
	}
	return m.cacheAddrComponent()
}

func (m *certManager) background() {
	d := m.currentConfig.End().Add(-clockSkewAllowance).Sub(m.clock.Now())
	log.Debugw("setting timer", "duration", d.String())
	t := m.clock.Timer(d)
	m.refCount.Add(1)

	go func() {
		defer m.refCount.Done()
		defer t.Stop()

		for {
			select {
			case <-m.ctx.Done():
				return
			case now := <-t.C:
				m.mx.Lock()
				if err := m.rollConfig(); err != nil {
					log.Errorw("rolling config failed", "error", err)
				}
				d := m.currentConfig.End().Add(-clockSkewAllowance).Sub(now)
				log.Debugw("rolling certificates", "next", d.String())
				t.Reset(d)
				m.mx.Unlock()
			}
		}
	}()
}

func (m *certManager) GetConfig() *tls.Config {
	m.mx.RLock()
	defer m.mx.RUnlock()
	return m.currentConfig.tlsConf
}

func (m *certManager) AddrComponent() ma.Multiaddr {
	m.mx.RLock()
	defer m.mx.RUnlock()
	return m.addrComp
}

func (m *certManager) Protobuf() []byte {
	return m.protobuf
}

func (m *certManager) cacheProtobuf() error {
	hashes := make([][32]byte, 0, 3)
	if m.lastConfig != nil {
		hashes = append(hashes, m.lastConfig.sha256)
	}
	hashes = append(hashes, m.currentConfig.sha256)
	if m.nextConfig != nil {
		hashes = append(hashes, m.nextConfig.sha256)
	}

	msg := pb.WebTransport{CertHashes: make([][]byte, 0, len(hashes))}
	for _, certHash := range hashes {
		h, err := multihash.Encode(certHash[:], multihash.SHA2_256)
		if err != nil {
			return fmt.Errorf("failed to encode certificate hash: %w", err)
		}
		msg.CertHashes = append(msg.CertHashes, h)
	}
	msgBytes, err := msg.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal WebTransport protobuf: %w", err)
	}
	m.protobuf = msgBytes
	return nil
}

func (m *certManager) cacheAddrComponent() error {
	addr, err := addrComponentForCert(m.currentConfig.sha256[:])
	if err != nil {
		return err
	}
	if m.nextConfig != nil {
		comp, err := addrComponentForCert(m.nextConfig.sha256[:])
		if err != nil {
			return err
		}
		addr = addr.Encapsulate(comp)
	}
	m.addrComp = addr
	return nil
}

func (m *certManager) Close() error {
	m.ctxCancel()
	m.refCount.Wait()
	return nil
}
