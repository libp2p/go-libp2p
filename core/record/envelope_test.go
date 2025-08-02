package record_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	. "github.com/libp2p/go-libp2p/core/record"
	"github.com/libp2p/go-libp2p/core/record/pb"
	"github.com/libp2p/go-libp2p/core/test"

	"google.golang.org/protobuf/proto"
)

type simpleRecord struct {
	testDomain *string
	testCodec  []byte
	message    string
}

func (r *simpleRecord) Domain() string {
	if r.testDomain != nil {
		return *r.testDomain
	}
	return "libp2p-testing"
}

func (r *simpleRecord) Codec() []byte {
	if r.testCodec != nil {
		return r.testCodec
	}
	return []byte("/libp2p/testdata")
}

func (r *simpleRecord) MarshalRecord() ([]byte, error) {
	return []byte(r.message), nil
}

func (r *simpleRecord) UnmarshalRecord(buf []byte) error {
	r.message = string(buf)
	return nil
}

// Make an envelope, verify & open it, marshal & unmarshal it
func TestEnvelopeHappyPath(t *testing.T) {
	var (
		rec            = &simpleRecord{message: "hello world!"}
		priv, pub, err = test.RandTestKeyPair(crypto.Ed25519, 256)
	)

	test.AssertNilError(t, err)

	payload, err := rec.MarshalRecord()
	test.AssertNilError(t, err)

	envelope, err := Seal(rec, priv)
	test.AssertNilError(t, err)

	if !envelope.PublicKey.Equals(pub) {
		t.Error("envelope has unexpected public key")
	}

	if !bytes.Equal(rec.Codec(), envelope.PayloadType) {
		t.Error("PayloadType does not match record Codec")
	}

	serialized, err := envelope.Marshal()
	test.AssertNilError(t, err)

	RegisterType(&simpleRecord{})
	deserialized, rec2, err := ConsumeEnvelope(serialized, rec.Domain())
	test.AssertNilError(t, err)

	if !bytes.Equal(deserialized.RawPayload, payload) {
		t.Error("payload of envelope does not match input")
	}

	if !envelope.Equal(deserialized) {
		t.Error("round-trip serde results in unequal envelope structures")
	}

	typedRec, ok := rec2.(*simpleRecord)
	if !ok {
		t.Error("expected ConsumeEnvelope to return record with type registered for payloadType")
	}
	if typedRec.message != "hello world!" {
		t.Error("unexpected alteration of record")
	}
}

func TestConsumeTypedEnvelope(t *testing.T) {
	var (
		rec        = simpleRecord{message: "hello world!"}
		priv, _, _ = test.RandTestKeyPair(crypto.Ed25519, 256)
	)

	envelope, err := Seal(&rec, priv)
	test.AssertNilError(t, err)

	envelopeBytes, err := envelope.Marshal()
	test.AssertNilError(t, err)

	rec2 := &simpleRecord{}
	_, err = ConsumeTypedEnvelope(envelopeBytes, rec2)
	test.AssertNilError(t, err)

	if rec2.message != "hello world!" {
		t.Error("unexpected alteration of record")
	}
}

func TestMakeEnvelopeFailsWithEmptyDomain(t *testing.T) {
	var (
		rec          = simpleRecord{message: "hello world!"}
		domain       = ""
		priv, _, err = test.RandTestKeyPair(crypto.Ed25519, 256)
	)

	if err != nil {
		t.Fatal(err)
	}

	// override domain with empty string
	rec.testDomain = &domain

	_, err = Seal(&rec, priv)
	test.ExpectError(t, err, "making an envelope with an empty domain should fail")
}

func TestMakeEnvelopeFailsWithEmptyPayloadType(t *testing.T) {
	var (
		rec          = simpleRecord{message: "hello world!"}
		priv, _, err = test.RandTestKeyPair(crypto.Ed25519, 256)
	)

	if err != nil {
		t.Fatal(err)
	}

	// override payload with empty slice
	rec.testCodec = []byte{}

	_, err = Seal(&rec, priv)
	test.ExpectError(t, err, "making an envelope with an empty payloadType should fail")
}

type failingRecord struct {
	allowMarshal   bool
	allowUnmarshal bool
}

func (r failingRecord) Domain() string {
	return "testing"
}

func (r failingRecord) Codec() []byte {
	return []byte("doesn't matter")
}

func (r failingRecord) MarshalRecord() ([]byte, error) {
	if r.allowMarshal {
		return []byte{}, nil
	}
	return nil, errors.New("marshal failed")
}
func (r failingRecord) UnmarshalRecord(_ []byte) error {
	if r.allowUnmarshal {
		return nil
	}
	return errors.New("unmarshal failed")
}

func TestSealFailsIfRecordMarshalFails(t *testing.T) {
	var (
		priv, _, err = test.RandTestKeyPair(crypto.Ed25519, 256)
	)

	if err != nil {
		t.Fatal(err)
	}
	rec := failingRecord{}
	_, err = Seal(rec, priv)
	test.ExpectError(t, err, "Seal should fail if Record fails to marshal")
}

func TestConsumeEnvelopeFailsIfEnvelopeUnmarshalFails(t *testing.T) {
	_, _, err := ConsumeEnvelope([]byte("not an Envelope protobuf"), "doesn't-matter")
	test.ExpectError(t, err, "ConsumeEnvelope should fail if Envelope fails to unmarshal")
}

func TestConsumeEnvelopeFailsIfRecordUnmarshalFails(t *testing.T) {
	var (
		priv, _, err = test.RandTestKeyPair(crypto.Ed25519, 256)
	)

	if err != nil {
		t.Fatal(err)
	}

	RegisterType(failingRecord{})
	rec := failingRecord{allowMarshal: true}
	env, err := Seal(rec, priv)
	test.AssertNilError(t, err)
	envBytes, err := env.Marshal()
	test.AssertNilError(t, err)

	_, _, err = ConsumeEnvelope(envBytes, rec.Domain())
	test.ExpectError(t, err, "ConsumeEnvelope should fail if Record fails to unmarshal")
}

func TestConsumeTypedEnvelopeFailsIfRecordUnmarshalFails(t *testing.T) {
	var (
		priv, _, err = test.RandTestKeyPair(crypto.Ed25519, 256)
	)

	if err != nil {
		t.Fatal(err)
	}

	RegisterType(failingRecord{})
	rec := failingRecord{allowMarshal: true}
	env, err := Seal(rec, priv)
	test.AssertNilError(t, err)
	envBytes, err := env.Marshal()
	test.AssertNilError(t, err)

	rec2 := failingRecord{}
	_, err = ConsumeTypedEnvelope(envBytes, rec2)
	test.ExpectError(t, err, "ConsumeTypedEnvelope should fail if Record fails to unmarshal")
}

func TestEnvelopeValidateFailsForDifferentDomain(t *testing.T) {
	var (
		rec          = &simpleRecord{message: "hello world"}
		priv, _, err = test.RandTestKeyPair(crypto.Ed25519, 256)
	)

	test.AssertNilError(t, err)

	envelope, err := Seal(rec, priv)
	test.AssertNilError(t, err)

	serialized, err := envelope.Marshal()
	test.AssertNilError(t, err)

	// try to open our modified envelope
	_, _, err = ConsumeEnvelope(serialized, "wrong-domain")
	test.ExpectError(t, err, "should not be able to open envelope with incorrect domain")
}

func TestEnvelopeValidateFailsIfPayloadTypeIsAltered(t *testing.T) {
	var (
		rec          = &simpleRecord{message: "hello world!"}
		domain       = "libp2p-testing"
		priv, _, err = test.RandTestKeyPair(crypto.Ed25519, 256)
	)

	test.AssertNilError(t, err)

	envelope, err := Seal(rec, priv)
	test.AssertNilError(t, err)

	serialized := alterMessageAndMarshal(t, envelope, func(msg *pb.Envelope) {
		msg.PayloadType = []byte("foo")
	})

	// try to open our modified envelope
	_, _, err = ConsumeEnvelope(serialized, domain)
	test.ExpectError(t, err, "should not be able to open envelope with modified PayloadType")
}

func TestEnvelopeValidateFailsIfContentsAreAltered(t *testing.T) {
	var (
		rec          = &simpleRecord{message: "hello world!"}
		domain       = "libp2p-testing"
		priv, _, err = test.RandTestKeyPair(crypto.Ed25519, 256)
	)

	test.AssertNilError(t, err)

	envelope, err := Seal(rec, priv)
	test.AssertNilError(t, err)

	serialized := alterMessageAndMarshal(t, envelope, func(msg *pb.Envelope) {
		msg.Payload = []byte("totally legit, trust me")
	})

	// try to open our modified envelope
	_, _, err = ConsumeEnvelope(serialized, domain)
	test.ExpectError(t, err, "should not be able to open envelope with modified payload")
}

// Since we're outside of the crypto package (to avoid import cycles with test package),
// we can't alter the fields in a Envelope directly. This helper marshals
// the envelope to a protobuf and calls the alterMsg function, which should
// alter the protobuf message.
// Returns the serialized altered protobuf message.
func alterMessageAndMarshal(t *testing.T, envelope *Envelope, alterMsg func(*pb.Envelope)) []byte {
	t.Helper()

	serialized, err := envelope.Marshal()
	test.AssertNilError(t, err)

	msg := pb.Envelope{}
	err = proto.Unmarshal(serialized, &msg)
	test.AssertNilError(t, err)

	alterMsg(&msg)
	serialized, err = proto.Marshal(&msg)
	test.AssertNilError(t, err)

	return serialized
}
