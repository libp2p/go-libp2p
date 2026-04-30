package peer_test

import (
	"bytes"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	. "github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/record"
	"github.com/libp2p/go-libp2p/core/test"
	ma "github.com/multiformats/go-multiaddr"
)

func TestPeerRecordConstants(t *testing.T) {
	msgf := "Changing the %s may cause peer records to be incompatible with older versions. " +
		"If you've already thought that through, please update this test so that it passes with the new values."
	rec := PeerRecord{}
	if rec.Domain() != "libp2p-peer-record" {
		t.Errorf(msgf, "signing domain")
	}
	if !bytes.Equal(rec.Codec(), []byte{0x03, 0x01}) {
		t.Errorf(msgf, "codec value")
	}
}

func TestSignedPeerRecordFromEnvelope(t *testing.T) {
	priv, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	test.AssertNilError(t, err)

	addrs := test.GenerateTestAddrs(10)
	id, err := IDFromPrivateKey(priv)
	test.AssertNilError(t, err)

	rec := &PeerRecord{PeerID: id, Addrs: addrs, Seq: TimestampSeq()}
	envelope, err := record.Seal(rec, priv)
	test.AssertNilError(t, err)

	t.Run("is unaltered after round-trip serde", func(t *testing.T) {
		envBytes, err := envelope.Marshal()
		test.AssertNilError(t, err)

		env2, untypedRecord, err := record.ConsumeEnvelope(envBytes, PeerRecordEnvelopeDomain)
		test.AssertNilError(t, err)
		rec2, ok := untypedRecord.(*PeerRecord)
		if !ok {
			t.Error("unmarshaled record is not a *PeerRecord")
		}
		if !rec.Equal(rec2) {
			t.Error("expected peer record to be unaltered after round-trip serde")
		}
		if !envelope.Equal(env2) {
			t.Error("expected signed envelope to be unchanged after round-trip serde")
		}
	})
}

// Regression: PeerRecord must not write empty multiaddrs onto the wire.
// A zero-component Multiaddr encodes to zero bytes, and peers that skip
// the empty-input check render those bytes as "/". go-libp2p's own
// decoder errors on empty input via NewMultiaddrBytes and drops the
// entry, so a round-trip on go-libp2p alone hides the bug. The test
// inspects marshaled record bytes instead.
//
// See https://github.com/libp2p/js-libp2p/issues/3478#issuecomment-4322093929
func TestPeerRecordDropsEmptyMultiaddrs(t *testing.T) {
	priv, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	test.AssertNilError(t, err)
	id, err := IDFromPrivateKey(priv)
	test.AssertNilError(t, err)

	good, err := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/4001")
	test.AssertNilError(t, err)

	rec := &PeerRecord{
		PeerID: id,
		Addrs:  []ma.Multiaddr{nil, good, {}, good},
		Seq:    TimestampSeq(),
	}
	recBytes, err := rec.MarshalRecord()
	test.AssertNilError(t, err)

	rec2 := &PeerRecord{}
	if err := rec2.UnmarshalRecord(recBytes); err != nil {
		t.Fatal(err)
	}
	if len(rec2.Addrs) != 2 {
		t.Fatalf("expected 2 non-empty addrs after round-trip, got %d", len(rec2.Addrs))
	}

	// Re-encode and compare bytes. Without the fix, the original record
	// holds zero-length addr fields that get dropped on decode, so the
	// re-encoded bytes are shorter than the original.
	rec2Bytes, err := rec2.MarshalRecord()
	test.AssertNilError(t, err)
	if !bytes.Equal(recBytes, rec2Bytes) {
		t.Fatal("PeerRecord wire form contained empty multiaddr entries")
	}
}

// This is pretty much guaranteed to pass on Linux no matter how we implement it, but Windows has
// low clock precision. This makes sure we never get a duplicate.
func TestTimestampSeq(t *testing.T) {
	var last uint64
	for range 1000 {
		next := TimestampSeq()
		if next <= last {
			t.Errorf("non-increasing timestamp found: %d <= %d", next, last)
		}
		last = next
	}
}
