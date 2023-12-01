package libp2ptls

import (
	"crypto/x509"
	"encoding/hex"
	"testing"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewIdentityCertificates(t *testing.T) {
	_, key := createPeer(t)
	cn := "a.test.name"
	email := "unittest@example.com"

	t.Run("NewIdentity with default template", func(t *testing.T) {
		// Generate an identity using the default template
		id, err := NewIdentity(key)
		assert.NoError(t, err)

		// Extract the x509 certificate
		x509Cert, err := x509.ParseCertificate(id.config.Certificates[0].Certificate[0])
		assert.NoError(t, err)

		// verify the common name and email are not set
		assert.Empty(t, x509Cert.Subject.CommonName)
		assert.Empty(t, x509Cert.EmailAddresses)
	})

	t.Run("NewIdentity with custom template", func(t *testing.T) {
		tmpl, err := certTemplate()
		assert.NoError(t, err)

		tmpl.Subject.CommonName = cn
		tmpl.EmailAddresses = []string{email}

		// Generate an identity using the custom template
		id, err := NewIdentity(key, WithCertTemplate(tmpl))
		assert.NoError(t, err)

		// Extract the x509 certificate
		x509Cert, err := x509.ParseCertificate(id.config.Certificates[0].Certificate[0])
		assert.NoError(t, err)

		// verify the common name and email are set
		assert.Equal(t, cn, x509Cert.Subject.CommonName)
		assert.Equal(t, email, x509Cert.EmailAddresses[0])
	})
}

func TestVerifyNetworkCookie(t *testing.T) {
	peerIDA, keyA := createPeer(t)
	peerIDB, keyB := createPeer(t)

	type testcase struct {
		name    string
		cookieA string
		cookieB string
		error   string
	}

	testcases := []testcase{
		{
			name: "No cookie",
		},
		{
			name:    "Matching cookie",
			cookieA: "424344aa",
			cookieB: "424344aa",
		},
		{
			name:    "Non-matching cookie",
			cookieA: "424344aa",
			cookieB: "010203",
			error:   "bad network cookie (wrong network?)",
		},
		{
			name:    "Cookie vs no cookie",
			cookieA: "424344aa",
			error:   "bad network cookie (wrong network?)",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cookieA, err := ic.ParseNetworkCookie(string(tc.cookieA))
			assert.NoError(t, err)
			idA, err := NewIdentity(ic.AddNetworkCookieToPrivKey(keyA, cookieA))
			assert.NoError(t, err)

			cookieB, err := ic.ParseNetworkCookie(string(tc.cookieB))
			assert.NoError(t, err)
			idB, err := NewIdentity(ic.AddNetworkCookieToPrivKey(keyB, cookieB))
			assert.NoError(t, err)

			tlsCfgA, _ := idA.ConfigForPeer(peerIDB)
			tlsCfgB, _ := idB.ConfigForPeer(peerIDA)

			err = tlsCfgA.VerifyPeerCertificate(tlsCfgB.Certificates[0].Certificate, nil)
			if tc.error != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.error)
			} else {
				require.NoError(t, err)
			}

			err = tlsCfgB.VerifyPeerCertificate(tlsCfgA.Certificates[0].Certificate, nil)
			if tc.error != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.error)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestVectors(t *testing.T) {
	type testcase struct {
		name      string
		data      string
		peerID    string
		netCookie string
		error     string
	}

	testcases := []testcase{
		{
			name:   "ED25519 Peer ID",
			data:   "308201773082011ea003020102020900f5bd0debaa597f52300a06082a8648ce3d04030230003020170d3735303130313030303030305a180f34303936303130313030303030305a30003059301306072a8648ce3d020106082a8648ce3d030107034200046bf9871220d71dcb3483ecdfcbfcc7c103f8509d0974b3c18ab1f1be1302d643103a08f7a7722c1b247ba3876fe2c59e26526f479d7718a85202ddbe47562358a37f307d307b060a2b0601040183a25a01010101ff046a30680424080112207fda21856709c5ae12fd6e8450623f15f11955d384212b89f56e7e136d2e17280440aaa6bffabe91b6f30c35e3aa4f94b1188fed96b0ffdd393f4c58c1c047854120e674ce64c788406d1c2c4b116581fd7411b309881c3c7f20b46e54c7e6fe7f0f300a06082a8648ce3d040302034700304402207d1a1dbd2bda235ff2ec87daf006f9b04ba076a5a5530180cd9c2e8f6399e09d0220458527178c7e77024601dbb1b256593e9b96d961b96349d1f560114f61a87595",
			peerID: "12D3KooWJRSrypvnpHgc6ZAgyCni4KcSmbV7uGRaMw5LgMKT18fq",
		},
		{
			name:   "ECDSA Peer ID",
			data:   "308201c030820166a003020102020900eaf419a6e3edb4a6300a06082a8648ce3d04030230003020170d3735303130313030303030305a180f34303936303130313030303030305a30003059301306072a8648ce3d020106082a8648ce3d030107034200048dbf1116c7c608d6d5292bd826c3feb53483a89fce434bf64538a359c8e07538ff71f6766239be6a146dcc1a5f3bb934bcd4ae2ae1d4da28ac68b4a20593f06ba381c63081c33081c0060a2b0601040183a25a01010101ff0481ae3081ab045f0803125b3059301306072a8648ce3d020106082a8648ce3d0301070342000484b93fa456a74bd0153919f036db7bc63c802f055bc7023395d0203de718ee0fc7b570b767cdd858aca6c7c4113ff002e78bd2138ac1a3b26dde3519e06979ad04483046022100bc84014cea5a41feabdf4c161096564b9ccf4b62fbef4fe1cd382c84e11101780221009204f086a84cb8ed8a9ddd7868dc90c792ee434adf62c66f99a08a5eba11615b300a06082a8648ce3d0403020348003045022054b437be9a2edf591312d68ff24bf91367ad4143f76cf80b5658f232ade820da022100e23b48de9df9c25d4c83ddddf75d2676f0b9318ee2a6c88a736d85eab94a912f",
			peerID: "QmZcrvr3r4S3QvwFdae3c2EWTfo792Y14UpzCZurhmiWeX",
		},
		{
			name:   "secp256k1 Peer ID",
			data:   "3082018230820128a003020102020900f3b305f55622cfdf300a06082a8648ce3d04030230003020170d3735303130313030303030305a180f34303936303130313030303030305a30003059301306072a8648ce3d020106082a8648ce3d0301070342000458f7e9581748ff9bdd933b655cc0e5552a1248f840658cc221dec2186b5a2fe4641b86ab7590a3422cdbb1000cf97662f27e5910d7569f22feed8829c8b52e0fa38188308185308182060a2b0601040183a25a01010101ff0471306f042508021221026b053094d1112bce799dc8026040ae6d4eb574157929f1598172061f753d9b1b04463044022040712707e97794c478d93989aaa28ae1f71c03af524a8a4bd2d98424948a782302207b61b7f074b696a25fb9e0059141a811cccc4cc28042d9301b9b2a4015e87470300a06082a8648ce3d04030203480030450220143ae4d86fdc8675d2480bb6912eca5e39165df7f572d836aa2f2d6acfab13f8022100831d1979a98f0c4a6fb5069ca374de92f1a1205c962a6d90ad3d7554cb7d9df4",
			peerID: "16Uiu2HAm2dSCBFxuge46aEt7U1oejtYuBUZXxASHqmcfVmk4gsbx",
		},
		{
			name:      "ECDSA Peer ID with netCookie",
			data:      "308201fd308201a4a003020102020838eac2a7e9695e39300a06082a8648ce3d040302301e311c301a06035504051313333933393837353437393830353238313331333020170d3233313133303233303630315a180f32313233313130373030303630315a301e311c301a06035504051313333933393837353437393830353238313331333059301306072a8648ce3d020106082a8648ce3d0301070342000428ecde7e10c3e03f13355a3be97f8bcd7969f11fe9b1132d6ad3708b8ab8a23426a68a8e70fa324abc110822c2d92357a08debcb4aedcc8086a1e197c20ae081a381c93081c63081c3060a2b0601040183a25a01010481b43081b1045f0803125b3059301306072a8648ce3d020106082a8648ce3d03010703420004089b6bbb86e428bcef0ddb4cb50bb800cf2a7b40624535cfd4d77d26b6e5d4e7638eba12fe7a62349c7d787c2d5dc6685b150811885a7c0864a54b2e5b9867bc04483046022100b3e337b966a79a920843ab5ef26adc0f0e1fa8772b10d989b8fc2a739b77d8e5022100b29e170c9db29a7fc42c8365c7d423acab72810a74c2b502fa97bcb9b7d2b26d0404424344aa300a06082a8648ce3d040302034700304402203cf9b63ec8717be19a8a4071adeb3c79aeab91a75e94b962bbcab7551801e641022014dca9d5bf8e27777c0d91b48bdd60301c16d51a87f69d8c90af4b37338d7250",
			peerID:    "QmeXd8w4utstC9X47U5DPK9CM2dNQssAmJVKY8Bpne42qj",
			netCookie: "424344aa",
		},
		{
			name:      "bad network cookie",
			data:      "308201fd308201a4a003020102020838eac2a7e9695e39300a06082a8648ce3d040302301e311c301a06035504051313333933393837353437393830353238313331333020170d3233313133303233303630315a180f32313233313130373030303630315a301e311c301a06035504051313333933393837353437393830353238313331333059301306072a8648ce3d020106082a8648ce3d0301070342000428ecde7e10c3e03f13355a3be97f8bcd7969f11fe9b1132d6ad3708b8ab8a23426a68a8e70fa324abc110822c2d92357a08debcb4aedcc8086a1e197c20ae081a381c93081c63081c3060a2b0601040183a25a01010481b43081b1045f0803125b3059301306072a8648ce3d020106082a8648ce3d03010703420004089b6bbb86e428bcef0ddb4cb50bb800cf2a7b40624535cfd4d77d26b6e5d4e7638eba12fe7a62349c7d787c2d5dc6685b150811885a7c0864a54b2e5b9867bc04483046022100b3e337b966a79a920843ab5ef26adc0f0e1fa8772b10d989b8fc2a739b77d8e5022100b29e170c9db29a7fc42c8365c7d423acab72810a74c2b502fa97bcb9b7d2b26d0404424344aa300a06082a8648ce3d040302034700304402203cf9b63ec8717be19a8a4071adeb3c79aeab91a75e94b962bbcab7551801e641022014dca9d5bf8e27777c0d91b48bdd60301c16d51a87f69d8c90af4b37338d7250",
			peerID:    "QmeXd8w4utstC9X47U5DPK9CM2dNQssAmJVKY8Bpne42qj",
			netCookie: "012345",
			error:     "bad network cookie (wrong network?)",
		},
		{
			name:  "Invalid certificate",
			data:  "308201773082011da003020102020830a73c5d896a1109300a06082a8648ce3d04030230003020170d3735303130313030303030305a180f34303936303130313030303030305a30003059301306072a8648ce3d020106082a8648ce3d03010703420004bbe62df9a7c1c46b7f1f21d556deec5382a36df146fb29c7f1240e60d7d5328570e3b71d99602b77a65c9b3655f62837f8d66b59f1763b8c9beba3be07778043a37f307d307b060a2b0601040183a25a01010101ff046a3068042408011220ec8094573afb9728088860864f7bcea2d4fd412fef09a8e2d24d482377c20db60440ecabae8354afa2f0af4b8d2ad871e865cb5a7c0c8d3dbdbf42de577f92461a0ebb0a28703e33581af7d2a4f2270fc37aec6261fcc95f8af08f3f4806581c730a300a06082a8648ce3d040302034800304502202dfb17a6fa0f94ee0e2e6a3b9fb6e986f311dee27392058016464bd130930a61022100ba4b937a11c8d3172b81e7cd04aedb79b978c4379c2b5b24d565dd5d67d3cb3c",
			error: "signature invalid",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := hex.DecodeString(tc.data)
			require.NoError(t, err)

			cert, err := x509.ParseCertificate(data)
			require.NoError(t, err)
			nc, err := ic.ParseNetworkCookie(tc.netCookie)
			if err != nil {
				require.NoError(t, err)
			}
			key, err := PubKeyFromCertChain([]*x509.Certificate{cert}, nc)
			if tc.error != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.error)
				return
			}
			require.NoError(t, err)
			id, err := peer.IDFromPublicKey(key)
			require.NoError(t, err)
			expectedID, err := peer.Decode(tc.peerID)
			require.NoError(t, err)
			require.Equal(t, expectedID, id)
		})
	}
}
