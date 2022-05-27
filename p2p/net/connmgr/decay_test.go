package connmgr

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/connmgr"
	"github.com/libp2p/go-libp2p-core/peer"
	tu "github.com/libp2p/go-libp2p-core/test"

	"github.com/benbjohnson/clock"
	"github.com/stretchr/testify/require"
)

const TestResolution = 50 * time.Millisecond

func waitForTag(t *testing.T, mgr *BasicConnMgr, id peer.ID) {
	t.Helper()
	require.Eventually(t, func() bool { return mgr.GetTagInfo(id) != nil }, 500*time.Millisecond, 10*time.Millisecond)
}

func TestDecayExpire(t *testing.T) {
	id := tu.RandPeerIDFatal(t)
	mgr, decay, mockClock := testDecayTracker(t)

	tag, err := decay.RegisterDecayingTag("pop", 250*time.Millisecond, connmgr.DecayExpireWhenInactive(1*time.Second), connmgr.BumpSumUnbounded())
	require.NoError(t, err)
	require.NoError(t, tag.Bump(id, 10))

	waitForTag(t, mgr, id)
	require.Equal(t, 10, mgr.GetTagInfo(id).Value)

	mockClock.Add(250 * time.Millisecond)
	mockClock.Add(250 * time.Millisecond)
	mockClock.Add(250 * time.Millisecond)
	mockClock.Add(250 * time.Millisecond)

	require.Zero(t, mgr.GetTagInfo(id).Value)
}

func TestMultipleBumps(t *testing.T) {
	id := tu.RandPeerIDFatal(t)
	mgr, decay, _ := testDecayTracker(t)

	tag, err := decay.RegisterDecayingTag("pop", 250*time.Millisecond, connmgr.DecayExpireWhenInactive(1*time.Second), connmgr.BumpSumBounded(10, 20))
	require.NoError(t, err)

	require.NoError(t, tag.Bump(id, 5))

	waitForTag(t, mgr, id)
	require.Equal(t, mgr.GetTagInfo(id).Value, 10)

	require.NoError(t, tag.Bump(id, 100))
	require.Eventually(t, func() bool { return mgr.GetTagInfo(id).Value == 20 }, 100*time.Millisecond, 10*time.Millisecond, "expected tag value to decay to 20")
}

func TestMultipleTagsNoDecay(t *testing.T) {
	id := tu.RandPeerIDFatal(t)
	mgr, decay, _ := testDecayTracker(t)

	tag1, err := decay.RegisterDecayingTag("beep", 250*time.Millisecond, connmgr.DecayNone(), connmgr.BumpSumBounded(0, 100))
	require.NoError(t, err)
	tag2, err := decay.RegisterDecayingTag("bop", 250*time.Millisecond, connmgr.DecayNone(), connmgr.BumpSumBounded(0, 100))
	require.NoError(t, err)
	tag3, err := decay.RegisterDecayingTag("foo", 250*time.Millisecond, connmgr.DecayNone(), connmgr.BumpSumBounded(0, 100))
	require.NoError(t, err)

	_ = tag1.Bump(id, 100)
	_ = tag2.Bump(id, 100)
	_ = tag3.Bump(id, 100)
	_ = tag1.Bump(id, 100)
	_ = tag2.Bump(id, 100)
	_ = tag3.Bump(id, 100)

	waitForTag(t, mgr, id)

	// all tags are upper-bounded, so the score must be 300
	ti := mgr.GetTagInfo(id)
	require.Equal(t, ti.Value, 300)

	for _, s := range []string{"beep", "bop", "foo"} {
		if v, ok := ti.Tags[s]; !ok || v != 100 {
			t.Fatalf("expected tag %s to be 100; was = %d", s, v)
		}
	}
}

func TestCustomFunctions(t *testing.T) {
	id := tu.RandPeerIDFatal(t)
	mgr, decay, mockClock := testDecayTracker(t)

	tag1, err := decay.RegisterDecayingTag("beep", 250*time.Millisecond, connmgr.DecayFixed(10), connmgr.BumpSumUnbounded())
	require.NoError(t, err)
	tag2, err := decay.RegisterDecayingTag("bop", 100*time.Millisecond, connmgr.DecayFixed(5), connmgr.BumpSumUnbounded())
	require.NoError(t, err)
	tag3, err := decay.RegisterDecayingTag("foo", 50*time.Millisecond, connmgr.DecayFixed(1), connmgr.BumpSumUnbounded())
	require.NoError(t, err)

	_ = tag1.Bump(id, 1000)
	_ = tag2.Bump(id, 1000)
	_ = tag3.Bump(id, 1000)

	waitForTag(t, mgr, id)

	// no decay has occurred yet, so score must be 3000.
	require.Equal(t, 3000, mgr.GetTagInfo(id).Value)

	// only tag3 should tick.
	mockClock.Add(50 * time.Millisecond)
	require.Equal(t, 2999, mgr.GetTagInfo(id).Value)

	// tag3 will tick thrice, tag2 will tick twice.
	mockClock.Add(150 * time.Millisecond)
	require.Equal(t, 2986, mgr.GetTagInfo(id).Value)

	// tag3 will tick once, tag1 will tick once.
	mockClock.Add(50 * time.Millisecond)
	require.Equal(t, 2975, mgr.GetTagInfo(id).Value)
}

func TestMultiplePeers(t *testing.T) {
	ids := []peer.ID{tu.RandPeerIDFatal(t), tu.RandPeerIDFatal(t), tu.RandPeerIDFatal(t)}
	mgr, decay, mockClock := testDecayTracker(t)

	tag1, err := decay.RegisterDecayingTag("beep", 250*time.Millisecond, connmgr.DecayFixed(10), connmgr.BumpSumUnbounded())
	require.NoError(t, err)
	tag2, err := decay.RegisterDecayingTag("bop", 100*time.Millisecond, connmgr.DecayFixed(5), connmgr.BumpSumUnbounded())
	require.NoError(t, err)
	tag3, err := decay.RegisterDecayingTag("foo", 50*time.Millisecond, connmgr.DecayFixed(1), connmgr.BumpSumUnbounded())
	require.NoError(t, err)

	_ = tag1.Bump(ids[0], 1000)
	_ = tag2.Bump(ids[0], 1000)
	_ = tag3.Bump(ids[0], 1000)

	_ = tag1.Bump(ids[1], 500)
	_ = tag2.Bump(ids[1], 500)
	_ = tag3.Bump(ids[1], 500)

	_ = tag1.Bump(ids[2], 100)
	_ = tag2.Bump(ids[2], 100)
	_ = tag3.Bump(ids[2], 100)

	// allow the background goroutine to process bumps.
	require.Eventually(t, func() bool {
		return mgr.GetTagInfo(ids[0]) != nil && mgr.GetTagInfo(ids[1]) != nil && mgr.GetTagInfo(ids[2]) != nil
	}, 100*time.Millisecond, 10*time.Millisecond)

	mockClock.Add(3 * time.Second)

	require.Eventually(t, func() bool { return mgr.GetTagInfo(ids[0]).Value == 2670 }, 500*time.Millisecond, 10*time.Millisecond)
	require.Equal(t, 1170, mgr.GetTagInfo(ids[1]).Value)
	require.Equal(t, 40, mgr.GetTagInfo(ids[2]).Value)
}

func TestLinearDecayOverwrite(t *testing.T) {
	id := tu.RandPeerIDFatal(t)
	mgr, decay, mockClock := testDecayTracker(t)

	tag1, err := decay.RegisterDecayingTag("beep", 250*time.Millisecond, connmgr.DecayLinear(0.5), connmgr.BumpOverwrite())
	require.NoError(t, err)

	_ = tag1.Bump(id, 1000)
	waitForTag(t, mgr, id)

	require.Equal(t, 1000, mgr.GetTagInfo(id).Value)
	mockClock.Add(250 * time.Millisecond)
	require.Equal(t, 500, mgr.GetTagInfo(id).Value)

	mockClock.Add(250 * time.Millisecond)
	require.Equal(t, 250, mgr.GetTagInfo(id).Value)

	_ = tag1.Bump(id, 1000)
	require.Eventually(t, func() bool { return mgr.GetTagInfo(id).Value == 1000 }, 500*time.Millisecond, 10*time.Millisecond, "expected value to be 1000")
}

func TestResolutionMisaligned(t *testing.T) {
	var (
		id                    = tu.RandPeerIDFatal(t)
		mgr, decay, mockClock = testDecayTracker(t)
		require               = require.New(t)
	)

	tag1, err := decay.RegisterDecayingTag("beep", time.Duration(float64(TestResolution)*1.4), connmgr.DecayFixed(1), connmgr.BumpOverwrite())
	require.NoError(err)

	tag2, err := decay.RegisterDecayingTag("bop", time.Duration(float64(TestResolution)*2.4), connmgr.DecayFixed(1), connmgr.BumpOverwrite())
	require.NoError(err)

	_ = tag1.Bump(id, 1000)
	_ = tag2.Bump(id, 1000)
	// allow the background goroutine to process bumps.
	<-time.After(500 * time.Millisecond)

	// first tick.
	mockClock.Add(TestResolution)
	require.Equal(1000, mgr.GetTagInfo(id).Tags["beep"])
	require.Equal(1000, mgr.GetTagInfo(id).Tags["bop"])

	// next tick; tag1 would've ticked.
	mockClock.Add(TestResolution)
	require.Equal(999, mgr.GetTagInfo(id).Tags["beep"])
	require.Equal(1000, mgr.GetTagInfo(id).Tags["bop"])

	// next tick; tag1 would've ticked twice, tag2 once.
	mockClock.Add(TestResolution)
	require.Equal(998, mgr.GetTagInfo(id).Tags["beep"])
	require.Equal(999, mgr.GetTagInfo(id).Tags["bop"])

	require.Equal(1997, mgr.GetTagInfo(id).Value)
}

func TestTagRemoval(t *testing.T) {
	id1, id2 := tu.RandPeerIDFatal(t), tu.RandPeerIDFatal(t)
	mgr, decay, mockClock := testDecayTracker(t)

	tag1, err := decay.RegisterDecayingTag("beep", TestResolution, connmgr.DecayFixed(1), connmgr.BumpOverwrite())
	require.NoError(t, err)

	tag2, err := decay.RegisterDecayingTag("bop", TestResolution, connmgr.DecayFixed(1), connmgr.BumpOverwrite())
	require.NoError(t, err)

	// id1 has both tags; id2 only has the first tag.
	_ = tag1.Bump(id1, 1000)
	_ = tag2.Bump(id1, 1000)
	_ = tag1.Bump(id2, 1000)

	waitForTag(t, mgr, id1)
	waitForTag(t, mgr, id2)

	// first tick.
	mockClock.Add(TestResolution)
	require.Equal(t, 999, mgr.GetTagInfo(id1).Tags["beep"])
	require.Equal(t, 999, mgr.GetTagInfo(id1).Tags["bop"])
	require.Equal(t, 999, mgr.GetTagInfo(id2).Tags["beep"])

	require.Equal(t, 999*2, mgr.GetTagInfo(id1).Value)
	require.Equal(t, 999, mgr.GetTagInfo(id2).Value)

	// remove tag1 from p1.
	require.NoError(t, tag1.Remove(id1))

	// next tick. both peers only have 1 tag, both at 998 value.
	mockClock.Add(TestResolution)
	require.Eventually(t, func() bool { return mgr.GetTagInfo(id1).Tags["beep"] == 0 }, 500*time.Millisecond, 10*time.Millisecond)
	require.Equal(t, 998, mgr.GetTagInfo(id1).Tags["bop"])
	require.Equal(t, 998, mgr.GetTagInfo(id2).Tags["beep"])

	require.Equal(t, 998, mgr.GetTagInfo(id1).Value)
	require.Equal(t, 998, mgr.GetTagInfo(id2).Value)

	// remove tag1 from p1 again; no error.
	require.NoError(t, tag1.Remove(id1))
}

func TestTagClosure(t *testing.T) {
	id := tu.RandPeerIDFatal(t)
	mgr, decay, mockClock := testDecayTracker(t)

	tag1, err := decay.RegisterDecayingTag("beep", TestResolution, connmgr.DecayFixed(1), connmgr.BumpOverwrite())
	require.NoError(t, err)
	tag2, err := decay.RegisterDecayingTag("bop", TestResolution, connmgr.DecayFixed(1), connmgr.BumpOverwrite())
	require.NoError(t, err)

	_ = tag1.Bump(id, 1000)
	_ = tag2.Bump(id, 1000)
	waitForTag(t, mgr, id)

	// nothing has happened.
	mockClock.Add(TestResolution)
	require.Equal(t, 999, mgr.GetTagInfo(id).Tags["beep"])
	require.Equal(t, 999, mgr.GetTagInfo(id).Tags["bop"])
	require.Equal(t, 999*2, mgr.GetTagInfo(id).Value)

	// next tick; tag1 would've ticked.
	mockClock.Add(TestResolution)
	require.Equal(t, 998, mgr.GetTagInfo(id).Tags["beep"])
	require.Equal(t, 998, mgr.GetTagInfo(id).Tags["bop"])
	require.Equal(t, 998*2, mgr.GetTagInfo(id).Value)

	// close the tag.
	require.NoError(t, tag1.Close())

	// allow the background goroutine to process the closure.
	require.Eventually(t, func() bool { return mgr.GetTagInfo(id).Value == 998 }, 500*time.Millisecond, 10*time.Millisecond)

	// a second closure should not error.
	require.NoError(t, tag1.Close())

	// bumping a tag after it's been closed should error.
	require.Error(t, tag1.Bump(id, 5))
}

func testDecayTracker(tb testing.TB) (*BasicConnMgr, connmgr.Decayer, *clock.Mock) {
	mockClock := clock.NewMock()
	cfg := &DecayerCfg{
		Resolution: TestResolution,
		Clock:      mockClock,
	}

	mgr, err := NewConnManager(10, 10, WithGracePeriod(time.Second), DecayerConfig(cfg))
	require.NoError(tb, err)
	decay, ok := connmgr.SupportsDecay(mgr)
	if !ok {
		tb.Fatalf("connmgr does not support decay")
	}
	tb.Cleanup(func() {
		mgr.Close()
		decay.Close()
	})

	return mgr, decay, mockClock
}
