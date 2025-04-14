package libp2ptls

import (
	"crypto/x509"
	"encoding/hex"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto/pb"
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
		require.NoError(t, err)

		// Extract the x509 certificate
		x509Cert, err := x509.ParseCertificate(id.config.Certificates[0].Certificate[0])
		require.NoError(t, err)

		// verify the common name and email are not set
		require.Empty(t, x509Cert.Subject.CommonName)
		require.Empty(t, x509Cert.EmailAddresses)
	})

	t.Run("NewIdentity with custom template", func(t *testing.T) {
		tmpl, err := certTemplate()
		require.NoError(t, err)

		tmpl.Subject.CommonName = cn
		tmpl.EmailAddresses = []string{email}

		// Generate an identity using the custom template
		id, err := NewIdentity(key, WithCertTemplate(tmpl))
		require.NoError(t, err)

		// Extract the x509 certificate
		x509Cert, err := x509.ParseCertificate(id.config.Certificates[0].Certificate[0])
		require.NoError(t, err)

		// verify the common name and email are set
		assert.Equal(t, cn, x509Cert.Subject.CommonName)
		assert.Equal(t, email, x509Cert.EmailAddresses[0])
	})
}

func TestVectors(t *testing.T) {
	type testcase struct {
		name    string
		data    string
		peerID  string
		keyType pb.KeyType
		error   string
	}

	testcases := []testcase{
		{
			name:    "ED25519 Peer ID",
			data:    "308201ae30820156a0030201020204499602d2300a06082a8648ce3d040302302031123010060355040a13096c69627032702e696f310a300806035504051301313020170d3735303130313133303030305a180f34303936303130313133303030305a302031123010060355040a13096c69627032702e696f310a300806035504051301313059301306072a8648ce3d020106082a8648ce3d030107034200040c901d423c831ca85e27c73c263ba132721bb9d7a84c4f0380b2a6756fd601331c8870234dec878504c174144fa4b14b66a651691606d8173e55bd37e381569ea37c307a3078060a2b0601040183a25a0101046a3068042408011220a77f1d92fedb59dddaea5a1c4abd1ac2fbde7d7b879ed364501809923d7c11b90440d90d2769db992d5e6195dbb08e706b6651e024fda6cfb8846694a435519941cac215a8207792e42849cccc6cd8136c6e4bde92a58c5e08cfd4206eb5fe0bf909300a06082a8648ce3d0403020346003043021f50f6b6c52711a881778718238f650c9fb48943ae6ee6d28427dc6071ae55e702203625f116a7a454db9c56986c82a25682f7248ea1cb764d322ea983ed36a31b77",
			peerID:  "12D3KooWM6CgA9iBFZmcYAHA6A2qvbAxqfkmrYiRQuz3XEsk4Ksv",
			keyType: pb.KeyType_Ed25519,
		},
		{
			name:    "ECDSA Peer ID",
			data:    "308201f63082019da0030201020204499602d2300a06082a8648ce3d040302302031123010060355040a13096c69627032702e696f310a300806035504051301313020170d3735303130313133303030305a180f34303936303130313133303030305a302031123010060355040a13096c69627032702e696f310a300806035504051301313059301306072a8648ce3d020106082a8648ce3d030107034200040c901d423c831ca85e27c73c263ba132721bb9d7a84c4f0380b2a6756fd601331c8870234dec878504c174144fa4b14b66a651691606d8173e55bd37e381569ea381c23081bf3081bc060a2b0601040183a25a01010481ad3081aa045f0803125b3059301306072a8648ce3d020106082a8648ce3d03010703420004bf30511f909414ebdd3242178fd290f093a551cf75c973155de0bb5a96fedf6cb5d52da7563e794b512f66e60c7f55ba8a3acf3dd72a801980d205e8a1ad29f2044730450220064ea8124774caf8f50e57f436aa62350ce652418c019df5d98a3ac666c9386a022100aa59d704a931b5f72fb9222cb6cc51f954d04a4e2e5450f8805fe8918f71eaae300a06082a8648ce3d04030203470030440220799395b0b6c1e940a7e4484705f610ab51ed376f19ff9d7c16757cfbf61b8d4302206205c03fbb0f95205c779be86581d3e31c01871ad5d1f3435bcf375cb0e5088a",
			peerID:  "QmfXbAwNjJLXfesgztEHe8HwgVDCMMpZ9Eax1HYq6hn9uE",
			keyType: pb.KeyType_ECDSA,
		},
		{
			name:    "secp256k1 Peer ID",
			data:    "308201ba3082015fa0030201020204499602d2300a06082a8648ce3d040302302031123010060355040a13096c69627032702e696f310a300806035504051301313020170d3735303130313133303030305a180f34303936303130313133303030305a302031123010060355040a13096c69627032702e696f310a300806035504051301313059301306072a8648ce3d020106082a8648ce3d030107034200040c901d423c831ca85e27c73c263ba132721bb9d7a84c4f0380b2a6756fd601331c8870234dec878504c174144fa4b14b66a651691606d8173e55bd37e381569ea38184308181307f060a2b0601040183a25a01010471306f0425080212210206dc6968726765b820f050263ececf7f71e4955892776c0970542efd689d2382044630440220145e15a991961f0d08cd15425bb95ec93f6ffa03c5a385eedc34ecf464c7a8ab022026b3109b8a3f40ef833169777eb2aa337cfb6282f188de0666d1bcec2a4690dd300a06082a8648ce3d0403020349003046022100e1a217eeef9ec9204b3f774a08b70849646b6a1e6b8b27f93dc00ed58545d9fe022100b00dafa549d0f03547878338c7b15e7502888f6d45db387e5ae6b5d46899cef0",
			peerID:  "16Uiu2HAkutTMoTzDw1tCvSRtu6YoixJwS46S1ZFxW8hSx9fWHiPs",
			keyType: pb.KeyType_Secp256k1,
		},
		{
			name:  "Invalid certificate",
			data:  "308201f73082019da0030201020204499602d2300a06082a8648ce3d040302302031123010060355040a13096c69627032702e696f310a300806035504051301313020170d3735303130313133303030305a180f34303936303130313133303030305a302031123010060355040a13096c69627032702e696f310a300806035504051301313059301306072a8648ce3d020106082a8648ce3d030107034200040c901d423c831ca85e27c73c263ba132721bb9d7a84c4f0380b2a6756fd601331c8870234dec878504c174144fa4b14b66a651691606d8173e55bd37e381569ea381c23081bf3081bc060a2b0601040183a25a01010481ad3081aa045f0803125b3059301306072a8648ce3d020106082a8648ce3d03010703420004bf30511f909414ebdd3242178fd290f093a551cf75c973155de0bb5a96fedf6cb5d52da7563e794b512f66e60c7f55ba8a3acf3dd72a801980d205e8a1ad29f204473045022100bb6e03577b7cc7a3cd1558df0da2b117dfdcc0399bc2504ebe7de6f65cade72802206de96e2a5be9b6202adba24ee0362e490641ac45c240db71fe955f2c5cf8df6e300a06082a8648ce3d0403020348003045022100e847f267f43717358f850355bdcabbefb2cfbf8a3c043b203a14788a092fe8db022027c1d04a2d41fd6b57a7e8b3989e470325de4406e52e084e34a3fd56eef0d0df",
			error: "signature invalid",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := hex.DecodeString(tc.data)
			require.NoError(t, err)

			cert, err := x509.ParseCertificate(data)
			require.NoError(t, err)
			key, err := (&DefaultCertManager{}).VerifyCertChain([]*x509.Certificate{cert})
			if tc.error != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.error)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.keyType, key.Type())
			id, err := peer.IDFromPublicKey(key)
			require.NoError(t, err)
			expectedID, err := peer.Decode(tc.peerID)
			require.NoError(t, err)
			require.Equal(t, expectedID, id)
		})
	}
}
