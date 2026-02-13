package peer_test

import (
	"bytes"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	. "github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/record"
	"github.com/libp2p/go-libp2p/core/test"
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
