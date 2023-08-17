package peer_test

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	. "github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"

	b58 "github.com/mr-tron/base58/base58"
	mh "github.com/multiformats/go-multihash"
)

var (
	gen1 keyset // generated
	gen2 keyset // generated
	man  keyset // manual
)

func hash(b []byte) []byte {
	h, _ := mh.Sum(b, mh.SHA2_256, -1)
	return []byte(h)
}

func init() {
	if err := gen1.generate(); err != nil {
		panic(err)
	}
	if err := gen2.generate(); err != nil {
		panic(err)
	}

	skManBytes = strings.Replace(skManBytes, "\n", "", -1)
	if err := man.load(hpkpMan, skManBytes); err != nil {
		panic(err)
	}
}

type keyset struct {
	sk   ic.PrivKey
	pk   ic.PubKey
	hpk  string
	hpkp string
}

func (ks *keyset) generate() error {
	var err error
	ks.sk, ks.pk, err = test.RandTestKeyPair(ic.RSA, 2048)
	if err != nil {
		return err
	}

	bpk, err := ic.MarshalPublicKey(ks.pk)
	if err != nil {
		return err
	}

	ks.hpk = string(hash(bpk))
	ks.hpkp = b58.Encode([]byte(ks.hpk))
	return nil
}

func (ks *keyset) load(hpkp, skBytesStr string) error {
	skBytes, err := base64.StdEncoding.DecodeString(skBytesStr)
	if err != nil {
		return err
	}

	ks.sk, err = ic.UnmarshalPrivateKey(skBytes)
	if err != nil {
		return err
	}

	ks.pk = ks.sk.GetPublic()
	bpk, err := ic.MarshalPublicKey(ks.pk)
	if err != nil {
		return err
	}

	ks.hpk = string(hash(bpk))
	ks.hpkp = b58.Encode([]byte(ks.hpk))
	if ks.hpkp != hpkp {
		return fmt.Errorf("hpkp doesn't match key. %s", hpkp)
	}
	return nil
}

func TestIDMatchesPublicKey(t *testing.T) {
	test := func(ks keyset) {
		p1, err := Decode(ks.hpkp)
		if err != nil {
			t.Fatal(err)
		}

		if ks.hpk != string(p1) {
			t.Error("p1 and hpk differ")
		}

		if !p1.MatchesPublicKey(ks.pk) {
			t.Fatal("p1 does not match pk")
		}

		p2, err := IDFromPublicKey(ks.pk)
		if err != nil {
			t.Fatal(err)
		}

		if p1 != p2 {
			t.Error("p1 and p2 differ", p1.String(), p2.String())
		}

		if p2.String() != ks.hpkp {
			t.Error("hpkp and p2.String differ", ks.hpkp, p2.String())
		}
	}

	test(gen1)
	test(gen2)
	test(man)
}

func TestIDMatchesPrivateKey(t *testing.T) {
	test := func(ks keyset) {
		p1, err := Decode(ks.hpkp)
		if err != nil {
			t.Fatal(err)
		}

		if ks.hpk != string(p1) {
			t.Error("p1 and hpk differ")
		}

		if !p1.MatchesPrivateKey(ks.sk) {
			t.Fatal("p1 does not match sk")
		}

		p2, err := IDFromPrivateKey(ks.sk)
		if err != nil {
			t.Fatal(err)
		}

		if p1 != p2 {
			t.Error("p1 and p2 differ", p1.String(), p2.String())
		}
	}

	test(gen1)
	test(gen2)
	test(man)
}

func TestIDEncoding(t *testing.T) {
	test := func(ks keyset) {
		p1, err := Decode(ks.hpkp)
		if err != nil {
			t.Fatal(err)
		}

		if ks.hpk != string(p1) {
			t.Error("p1 and hpk differ")
		}

		c := ToCid(p1)
		p2, err := FromCid(c)
		if err != nil || p1 != p2 {
			t.Fatal("failed to round-trip through CID:", err)
		}
		p3, err := Decode(c.String())
		if err != nil {
			t.Fatal(err)
		}
		if p3 != p1 {
			t.Fatal("failed to round trip through CID string")
		}

		if ks.hpkp != p1.String() {
			t.Fatal("should always encode peer IDs as base58 by default")
		}
	}

	test(gen1)
	test(gen2)
	test(man)

	exampleCid := "bafkreifoybygix7fh3r3g5rqle3wcnhqldgdg4shzf4k3ulyw3gn7mabt4"
	_, err := Decode(exampleCid)
	if err == nil {
		t.Fatal("should refuse to decode a non-peer ID CID")
	}

	c := ToCid("")
	if c.Defined() {
		t.Fatal("cid of empty peer ID should have been undefined")
	}
}

func TestPublicKeyExtraction(t *testing.T) {
	t.Skip("disabled until libp2p/go-libp2p-crypto#51 is fixed")
	// Happy path
	_, originalPub, err := ic.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	id, err := IDFromPublicKey(originalPub)
	if err != nil {
		t.Fatal(err)
	}

	extractedPub, err := id.ExtractPublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if extractedPub == nil {
		t.Fatal("failed to extract public key")
	}
	if !originalPub.Equals(extractedPub) {
		t.Fatal("extracted public key doesn't match")
	}

	// Test invalid multihash (invariant of the type of public key)
	pk, err := ID("").ExtractPublicKey()
	if err == nil {
		t.Fatal("expected an error")
	}
	if pk != nil {
		t.Fatal("expected a nil public key")
	}

	// Shouldn't work for, e.g. RSA keys (too large)

	_, rsaPub, err := ic.GenerateKeyPair(ic.RSA, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaId, err := IDFromPublicKey(rsaPub)
	if err != nil {
		t.Fatal(err)
	}
	extractedRsaPub, err := rsaId.ExtractPublicKey()
	if err != ErrNoPublicKey {
		t.Fatal(err)
	}
	if extractedRsaPub != nil {
		t.Fatal("expected to fail to extract public key from rsa ID")
	}
}

func TestValidate(t *testing.T) {
	// Empty peer ID invalidates
	err := ID("").Validate()
	if err == nil {
		t.Error("expected error")
	} else if err != ErrEmptyPeerID {
		t.Error("expected error message: " + ErrEmptyPeerID.Error())
	}

	// Non-empty peer ID validates
	p, err := test.RandPeerID()
	if err != nil {
		t.Fatal(err)
	}

	err = p.Validate()
	if err != nil {
		t.Error("expected nil, but found " + err.Error())
	}
}

var (
	hpkpMan    = `QmcJeseojbPW9hSejUM1sQ1a2QmbrryPK4Z8pWbRUPaYEn`
	skManBytes = `
CAASqAkwggSkAgEAAoIBAQC3hjPtPli71gFNzGJ6rUhYdb65BDwW7IrniEaZKi6z
tW4Iz0MouEJY8GPG1iQfqZKp5w9H2ENh4I1bk2dsezrJ7Nneg4Eqd78CmeHTAgaP
3PKsxohdMo/TOFNxwl8SkEF8FyVbio2TCoijYNHUuprZuq7MPEAJYr3Z1eEkM/xR
pMp3YI9S2SYsZQxbmmQ0/GfHOEvYajdow1qttreVTQkvmCppKtNLEU5InpX/W5fe
aQCj0pd7l74daZgM2WWz3juEUCVG7tdRUPg7ix1TYosbN96CKC3q2MJxe/wJ9gR5
Jvjnaaaoon+mci5vrKzxdKBDmZ/ZbLiHDfVljMkbdOQLAgMBAAECggEAEULaF3JJ
vkD+lmamzIsHxuosKhKv5CgTWHuEyFsjUVu7IbD8zBOoidzyRX1WoHO+i6Rj14oL
rGUGZpqSm61rdhqE01zjBS+GE6SNjN8f5uANIxr5MGrVBDTEBGsXrhNLVXSH2vhJ
II9ZEqTEl5GFhvz7+9Ge5EMZQCfRqSoKjVMdrs+Rueuusr9p0wNg9PH1myA+cXGt
iNZA17Rj2IiWVZLDgYNo4DVQUt4mFb+wTJW4NSspGKaFebpn0hf4z21laoGoJqTC
cNETJw+QwQ0uDaRoYotTLT2/55e8XBFTdcTg5cmbZoKgMyGqZEHfRyD9reVDAZlM
EZwKtrm41kz94QKBgQDmPp5zVtFXQNONmje1NE0IjCaUKcqURXk4ZiILztfT9XLC
OXAUCs3TCq21jirCkZZ6gLfo12Wx0xJYmsKlaUOGNTa8FI5Xa7OyheYKixUvV6FW
J95P/sNuWscTjh7oZHgZk/L3yKrNzNBz7awComwV6qciXW7EP1uACHf5fS/RdQKB
gQDMDa38W9OeegRDrhCeYGsniJK7btOCzhNooruQKPPXxk+O4dyJm7VBbC/3Ch55
a83W66T4k0Q7ysLVRT5Vqd5z3AM0sEM3ZoxUKCinG3NwPxVeXcoLasyEiq1vOFK6
GqZKCMThCj7ZpbkWy0DPJagnYfZGC62lammuj+XQx7mvfwKBgQCTKhka/bXmgD/3
9UeAIcLPIM2TzDZ4mQNHIjjGtVnMV8kXDaFung06xEuNjSYVoPq+qEFkqTCN/axv
R9P76BFJ2f93LehhRizggacsvAM5dFhh+i+lj+AYTBuMiz2EKpt9NcyJxhAuZKgk
QRi9wlU1mPtlArVG6HwylLcil3qV9QKBgQDJHtaU/KEY+2TGnIMuxxP2lEsjyLla
nOlOYc8C6Qpma8UwrHelfj5p7Eteb6/Xt6Tbp8kjZGuFj3T3plcpMdPbWEgkn3Kw
4TeBH0/qXUkrolHagBDLrglEvjbxf48ydV/fasM6l9GYzhofWFhZk+EoaArHwWz2
tGrTrmsynBjt2wKBgErdYe+zZ2Wo+wXQGAoZi4pfcwiw4a97Kdh0dx+WZz7acHms
h+V20VRmEHm5h8WnJ/Wv5uK94t6NY17wzjQ7y2BN5mY5cA2cZAcpeqtv/N06tH4S
cn1UEuRB8VpwkjaPUNZhqtYK40qff2OTdJy8taFtQiN7fz9euWTC78zjph2s
`
)
