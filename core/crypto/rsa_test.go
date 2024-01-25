package crypto

import (
	"crypto/rand"
	"testing"
)

func TestRSABasicSignAndVerify(t *testing.T) {
	priv, pub, err := GenerateRSAKeyPair(2048, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("hello! and welcome to some awesome crypto primitives")

	sig, err := priv.Sign(data)
	if err != nil {
		t.Fatal(err)
	}

	ok, err := pub.Verify(data, sig)
	if err != nil {
		t.Fatal(err)
	}

	if !ok {
		t.Fatal("signature didn't match")
	}

	// change data
	data[0] = ^data[0]
	ok, err = pub.Verify(data, sig)
	if err == nil {
		t.Fatal("should have produced a verification error")
	}

	if ok {
		t.Fatal("signature matched and shouldn't")
	}
}

func TestRSASmallKey(t *testing.T) {
	_, _, err := GenerateRSAKeyPair(MinRsaKeyBits/2, rand.Reader)
	if err != ErrRsaKeyTooSmall {
		t.Fatal("should have refused to create small RSA key")
	}
	MinRsaKeyBits /= 2
	badPriv, badPub, err := GenerateRSAKeyPair(MinRsaKeyBits, rand.Reader)
	if err != nil {
		t.Fatalf("should have succeeded, got: %s", err)
	}
	pubBytes, err := MarshalPublicKey(badPub)
	if err != nil {
		t.Fatal(err)
	}
	privBytes, err := MarshalPrivateKey(badPriv)
	if err != nil {
		t.Fatal(err)
	}
	MinRsaKeyBits *= 2
	_, err = UnmarshalPublicKey(pubBytes)
	if err != ErrRsaKeyTooSmall {
		t.Fatal("should have refused to unmarshal a weak key")
	}
	_, err = UnmarshalPrivateKey(privBytes)
	if err != ErrRsaKeyTooSmall {
		t.Fatal("should have refused to unmarshal a weak key")
	}
}

func TestRSABigKeyFailsToGenerate(t *testing.T) {
	_, _, err := GenerateRSAKeyPair(maxRsaKeyBits*2, rand.Reader)
	if err != ErrRsaKeyTooBig {
		t.Fatal("should have refused to create too big RSA key")
	}
}

func TestRSABigKey(t *testing.T) {
	// Make the global limit smaller for this test to run faster.
	// Note we also change the limit below, but this is different
	origSize := maxRsaKeyBits
	maxRsaKeyBits = 2048
	defer func() { maxRsaKeyBits = origSize }() //

	maxRsaKeyBits *= 2
	badPriv, badPub, err := GenerateRSAKeyPair(maxRsaKeyBits, rand.Reader)
	if err != nil {
		t.Fatalf("should have succeeded, got: %s", err)
	}
	pubBytes, err := MarshalPublicKey(badPub)
	if err != nil {
		t.Fatal(err)
	}
	privBytes, err := MarshalPrivateKey(badPriv)
	if err != nil {
		t.Fatal(err)
	}
	maxRsaKeyBits /= 2
	_, err = UnmarshalPublicKey(pubBytes)
	if err != ErrRsaKeyTooBig {
		t.Fatal("should have refused to unmarshal a too big key")
	}
	_, err = UnmarshalPrivateKey(privBytes)
	if err != ErrRsaKeyTooBig {
		t.Fatal("should have refused to unmarshal a too big key")
	}
}

func TestRSASignZero(t *testing.T) {
	priv, pub, err := GenerateRSAKeyPair(2048, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	data := make([]byte, 0)
	sig, err := priv.Sign(data)
	if err != nil {
		t.Fatal(err)
	}

	ok, err := pub.Verify(data, sig)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("signature didn't match")
	}
}

func TestRSAMarshalLoop(t *testing.T) {
	priv, pub, err := GenerateRSAKeyPair(2048, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	privB, err := MarshalPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	privNew, err := UnmarshalPrivateKey(privB)
	if err != nil {
		t.Fatal(err)
	}

	if !priv.Equals(privNew) || !privNew.Equals(priv) {
		t.Fatal("keys are not equal")
	}

	pubB, err := MarshalPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubNew, err := UnmarshalPublicKey(pubB)
	if err != nil {
		t.Fatal(err)
	}

	if !pub.Equals(pubNew) || !pubNew.Equals(pub) {
		t.Fatal("keys are not equal")
	}
}
