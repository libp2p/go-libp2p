package libp2pwebtransport

import (
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/test"

	"github.com/benbjohnson/clock"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/require"
)

func certificateHashFromTLSConfig(c *tls.Config) [32]byte {
	return sha256.Sum256(c.Certificates[0].Certificate[0])
}

func splitMultiaddr(addr ma.Multiaddr) []ma.Component {
	var components []ma.Component
	ma.ForEach(addr, func(c ma.Component) bool {
		components = append(components, c)
		return true
	})
	return components
}

func certHashFromComponent(t *testing.T, comp ma.Component) []byte {
	t.Helper()
	_, data, err := multibase.Decode(comp.Value())
	require.NoError(t, err)
	mh, err := multihash.Decode(data)
	require.NoError(t, err)
	require.Equal(t, uint64(multihash.SHA2_256), mh.Code)
	return mh.Digest
}

func TestInitialCert(t *testing.T) {
	cl := clock.NewMock()
	cl.Add(1234567 * time.Hour)
	priv, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	require.NoError(t, err)
	m, err := newCertManager(priv, cl)
	require.NoError(t, err)
	defer m.Close()

	conf := m.GetConfig()
	require.Len(t, conf.Certificates, 1)
	cert := conf.Certificates[0]
	require.GreaterOrEqual(t, cl.Now().Add(-clockSkewAllowance), cert.Leaf.NotBefore)
	require.Equal(t, cert.Leaf.NotBefore.Add(certValidity), cert.Leaf.NotAfter)
	addr := m.AddrComponent()
	components := splitMultiaddr(addr)
	require.Len(t, components, 2)
	require.Equal(t, ma.P_CERTHASH, components[0].Protocol().Code)
	hash := certificateHashFromTLSConfig(conf)
	require.Equal(t, hash[:], certHashFromComponent(t, components[0]))
	require.Equal(t, ma.P_CERTHASH, components[1].Protocol().Code)
}

func TestCertRenewal(t *testing.T) {
	cl := clock.NewMock()
	// Add a year to avoid edge cases around the epoch
	cl.Add(time.Hour * 24 * 365)
	priv, _, err := test.SeededTestKeyPair(crypto.Ed25519, 256, 0)
	require.NoError(t, err)
	m, err := newCertManager(priv, cl)
	require.NoError(t, err)
	defer m.Close()

	firstConf := m.GetConfig()
	first := splitMultiaddr(m.AddrComponent())
	require.Len(t, first, 2)
	require.NotEqual(t, first[0].Value(), first[1].Value(), "the hashes should differ")
	// wait for a new certificate to be generated
	cl.Set(m.currentConfig.End().Add(-(clockSkewAllowance + time.Second)))
	require.Never(t, func() bool {
		for i, c := range splitMultiaddr(m.AddrComponent()) {
			if c.Value() != first[i].Value() {
				return true
			}
		}
		return false
	}, 100*time.Millisecond, 10*time.Millisecond)
	cl.Add(2 * time.Second)
	require.Eventually(t, func() bool { return m.GetConfig() != firstConf }, 200*time.Millisecond, 10*time.Millisecond)
	secondConf := m.GetConfig()

	second := splitMultiaddr(m.AddrComponent())
	require.Len(t, second, 2)
	for _, c := range second {
		require.Equal(t, ma.P_CERTHASH, c.Protocol().Code)
	}
	// check that the 2nd certificate from the beginning was rolled over to be the 1st certificate
	require.Equal(t, first[1].Value(), second[0].Value())
	require.NotEqual(t, first[0].Value(), second[1].Value())

	cl.Add(certValidity - 2*clockSkewAllowance + time.Second)
	require.Eventually(t, func() bool { return m.GetConfig() != secondConf }, 200*time.Millisecond, 10*time.Millisecond)
	third := splitMultiaddr(m.AddrComponent())
	require.Len(t, third, 2)
	for _, c := range third {
		require.Equal(t, ma.P_CERTHASH, c.Protocol().Code)
	}
	// check that the 2nd certificate from the beginning was rolled over to be the 1st certificate
	require.Equal(t, second[1].Value(), third[0].Value())
}

func TestDeterministicCertsAcrossReboots(t *testing.T) {
	// Run this test 100 times to make sure it's deterministic
	runs := 100
	for i := range runs {
		t.Run(fmt.Sprintf("Run=%d", i), func(t *testing.T) {
			cl := clock.NewMock()
			priv, _, err := test.SeededTestKeyPair(crypto.Ed25519, 256, 0)
			require.NoError(t, err)
			m, err := newCertManager(priv, cl)
			require.NoError(t, err)
			defer m.Close()

			conf := m.GetConfig()
			require.Len(t, conf.Certificates, 1)
			oldCerts := m.serializedCertHashes

			m.Close()

			cl.Add(time.Hour)
			// reboot
			m, err = newCertManager(priv, cl)
			require.NoError(t, err)
			defer m.Close()

			newCerts := m.serializedCertHashes

			require.Equal(t, oldCerts, newCerts)
		})
	}
}

func TestDeterministicTimeBuckets(t *testing.T) {
	cl := clock.NewMock()
	cl.Add(time.Hour * 24 * 365)
	startA := getCurrentBucketStartTime(cl.Now(), 0)
	startB := getCurrentBucketStartTime(cl.Now().Add(time.Hour*24), 0)
	require.Equal(t, startA, startB)

	// 15 Days later
	startC := getCurrentBucketStartTime(cl.Now().Add(time.Hour*24*15), 0)
	require.NotEqual(t, startC, startB)
}

func TestGetCurrentBucketStartTimeIsWithinBounds(t *testing.T) {
	require.NoError(t, quick.Check(func(timeSinceUnixEpoch time.Duration, offset time.Duration) bool {
		if offset < 0 {
			offset = -offset
		}
		if timeSinceUnixEpoch < 0 {
			timeSinceUnixEpoch = -timeSinceUnixEpoch
		}

		offset = offset % certValidity
		// Bound this to 100 years
		timeSinceUnixEpoch = timeSinceUnixEpoch % (time.Hour * 24 * 365 * 100)
		// Start a bit further in the future to avoid edge cases around epoch
		timeSinceUnixEpoch += time.Hour * 24 * 365
		start := time.UnixMilli(timeSinceUnixEpoch.Milliseconds())

		bucketStart := getCurrentBucketStartTime(start.Add(-clockSkewAllowance), offset)
		return !bucketStart.After(start.Add(-clockSkewAllowance)) || bucketStart.Equal(start.Add(-clockSkewAllowance))
	}, nil))
}

// TestSerializedCertHashesReturnsOuterCopy guards the copy in
// SerializedCertHashes. cacheSerializedCertHashes truncates the hash slice and
// re-appends into the same backing array across rotations, so returning the
// manager's own slice would expose the caller to it changing underneath them.
//
// Only the outer slice is checked, matching what SerializedCertHashes promises.
// Do not extend this to scribble over the hash bytes: they are shared with the
// manager by design, so want aliases them and cannot be an oracle for their
// contents. Such a test would zero the manager's live certificate hashes and
// still report PASS.
func TestSerializedCertHashesReturnsOuterCopy(t *testing.T) {
	cl := clock.NewMock()
	cl.Add(time.Hour * 24 * 365)
	priv, _, err := test.SeededTestKeyPair(crypto.Ed25519, 256, 0)
	require.NoError(t, err)
	m, err := newCertManager(priv, cl)
	require.NoError(t, err)
	defer m.Close()

	first := m.SerializedCertHashes()
	require.NotEmpty(t, first)
	want := slices.Clone(first)

	// Scribble over the outer slots the way an owning caller may.
	clear(first)

	require.Equal(t, want, m.SerializedCertHashes(),
		"caller must own the outer slice returned by SerializedCertHashes")
}

// TestSerializedCertHashesConcurrentWithRotation guards the read lock and the
// copy in SerializedCertHashes. The background goroutine rewrites the hash slice
// in place during certificate rotation, so a caller reading the slice at the same
// time used to race with that write and could see a torn result. Run with -race
// it fails on both an unsynchronized getter and one that hands out the manager's
// own slice.
func TestSerializedCertHashesConcurrentWithRotation(t *testing.T) {
	cl := clock.NewMock()
	cl.Add(time.Hour * 24 * 365)
	priv, _, err := test.SeededTestKeyPair(crypto.Ed25519, 256, 0)
	require.NoError(t, err)
	m, err := newCertManager(priv, cl)
	require.NoError(t, err)
	defer m.Close()

	firstConf := m.GetConfig()

	// Readers hammer the getters while certificates roll in the background. Run
	// with -race: without the read lock the getter races with
	// cacheSerializedCertHashes rewriting the slice during rotation, and without
	// the copy the store below lands in the manager's own backing array. The
	// sibling getters share the same lock, so read them here too.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					// Write to a slot the caller owns. An indexed store is compiled
					// to an instrumented write; clear() lowers to a bulk memclr in
					// the runtime, which carries no instrumentation, so the race
					// detector would never see it.
					if h := m.SerializedCertHashes(); len(h) > 0 {
						h[0] = nil
					}
					_ = m.GetConfig()
					_ = m.AddrComponent()
					// Yield: four spinning readers otherwise starve the rotation
					// loop below, which costs seconds on a low-core CI runner.
					runtime.Gosched()
				}
			}
		})
	}

	// Advancing past a validity period forces the background goroutine to roll
	// the config, which rewrites the shared serializedCertHashes slice.
	for range 50 {
		cl.Add(certValidity)
		runtime.Gosched()
	}

	close(stop)
	wg.Wait()

	// Confirm rotation actually happened, so the reads above really did overlap
	// with writes rather than racing nothing.
	require.NotSame(t, firstConf, m.GetConfig(), "expected at least one certificate rotation")
}
