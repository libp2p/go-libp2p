package crypto

import (
	"crypto/rand"
	"testing"
)

func TestNetworkCookie(t *testing.T) {
	ncA, err := ParseNetworkCookie("001234")
	if err != nil {
		t.Fatalf("error parsing network cookie: %v", err)
	}
	if ncA.String() != "001234" {
		t.Errorf("bad network cookie string")
	}
	ncB, err := ParseNetworkCookie("234567")
	if err != nil {
		t.Fatalf("error parsing network cookie: %v", err)
	}
	ncEmpty, err := ParseNetworkCookie("")
	if err != nil {
		t.Fatalf("error parsing empty network cookie: %v", err)
	}

	if ncA.Empty() || ncB.Empty() {
		t.Errorf("non-empty network cookie reported as empty")
	}
	if !ncEmpty.Empty() {
		t.Errorf("empty network cookie reported as non-empty")
	}

	if ncA.Equal(ncB) || ncA.Equal(ncEmpty) {
		t.Errorf("non-equal cookies are reported as equal")
	}

	if !ncA.Equal(ncA) || !ncEmpty.Equal(nil) || !ncEmpty.Equal([]byte{}) || !ncA.Equal([]byte{0, 0x12, 0x34}) {
		t.Errorf("equal cookies are reported as non-equal")
	}

	priv, _, err := GenerateECDSAKeyPair(rand.Reader)
	if err != nil {
		t.Fatalf("error generating key: %v", err)
	}

	if !NetworkCookieFromPrivKey(priv).Empty() {
		t.Errorf("unexpected non-empty network cookie from priv key")
	}

	priv1 := AddNetworkCookieToPrivKey(priv, ncA)
	if !NetworkCookieFromPrivKey(priv1).Equal(ncA) {
		t.Errorf("bad network cookie from priv key")
	}
	if StripNetworkCookieFromPrivKey(priv1) != priv {
		t.Errorf("StripNetworkCookieFromPrivKey didn't return the original key")
	}

	if AddNetworkCookieToPrivKey(priv, ncEmpty) != priv {
		t.Errorf("adding empty network cookie shouldn't change the key")
	}
}
