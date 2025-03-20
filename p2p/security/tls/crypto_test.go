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
			data:    "308201b030820156a00302010202081ef0074922d196fd300a06082a8648ce3d040302301e311c301a06035504051313343337323333323535383639393632313939323020170d3235303332303130333033305a180f32313235303232343131333033305a301e311c301a06035504051313343337323333323535383639393632313939323059301306072a8648ce3d020106082a8648ce3d03010703420004799542bfc7bfb7506ecd6d78857796b30e4127c44716fc2caa40922cc578ec9367e5b748c748a3ae576786b9fddeca36f40f2cc883b101e937511bff41ab5232a37c307a3078060a2b0601040183a25a0101046a3068042408011220970ec193ab5f6c556009767d5cdc0477d257807b41468a6f2007b40f03034fc70440db02949ac1e19fa61632baafa30d565eca7c12e84f0fc4341ade332b5ccbac60640fdc59213399d913e6c3c0f1111f92f66f04ee20cfe8f16cecfb7b5ee59205300a06082a8648ce3d040302034800304502203d33964353d80f393415c993a6462d47c7dacc38147ee445953019786ea7b66d022100a693ade35c4edb786bdb0bd09f1cb0c9a5b0bc6b61a97b639b4e3334371e10aa",
			peerID:  "12D3KooWKz2nHY8tmcX7ziGsF3gBoUZVvCXcmkvn86DaBsGktZfc",
			keyType: pb.KeyType_Ed25519,
		},
		{
			name:    "ECDSA Peer ID",
			data:    "308201f63082019ca00302010202081052b953fab8f4be300a06082a8648ce3d040302301e311c301a06035504051313313931333134363939343730373431363038313020170d3235303332303130333033305a180f32313235303232343131333033305a301e311c301a06035504051313313931333134363939343730373431363038313059301306072a8648ce3d020106082a8648ce3d03010703420004c2fc1d082c85b90d8a82413b9b34f9c9c5f93f79e6d1eb954b5fdfe41b26fc9a4c44f32844eb40be9b4728a59ec966816a5394d3a0c1f06334b6debecb36f0aea381c13081be3081bb060a2b0601040183a25a01010481ac3081a9045f0803125b3059301306072a8648ce3d020106082a8648ce3d03010703420004d287ad3dc5c97884b7ab987b660efc2aa8cde7f9814e0fb3a8a005bfd8cd4a6cd2fd961d3e2013256b5b59e1ca6c9e7e48febfb1ed90cd092ef24aa0ae2d0dc404463044022021e1ccaf2f3c77fde5ada1242b830e7a5c1ab25956ac5edbca4904ec47a09479022051884dbdda561b545abf3fe391341898a4b4ceccccb83507b445ed36a6b2eedc300a06082a8648ce3d040302034800304502200f0fd126fb521ef8543655ccd3c32b1df34be8eb61ba4d30a04eebdb2870f2b0022100e97a637a3e702f360b45eb7567647c6a46f9d2a53291332a89898f3d84afc24e",
			peerID:  "Qmf5QwyriEdqphhFWkFJsmfY4Sgsj5Cq47VTa5RAboELhM",
			keyType: pb.KeyType_ECDSA,
		},
		{
			name:    "secp256k1 Peer ID",
			data:    "308201b83082015fa003020102020831c14b384686f89a300a06082a8648ce3d040302301e311c301a06035504051313313435373936393932373437313131393132373020170d3235303332303130333033305a180f32313235303232343131333033305a301e311c301a06035504051313313435373936393932373437313131393132373059301306072a8648ce3d020106082a8648ce3d03010703420004e5da66e6dc811fef90f2b7a77ec92f8f7a96942899dc31ce649058cff9f9504cbe2c70212b616daef3fb52afa7d75b1c7880f48fdf0565cb7809ffb656b3b540a38184308181307f060a2b0601040183a25a01010471306f04250802122102d5dd09fddbcc150de9cb92e1777d7712ba0e20f526c7a842cc5f134a966cda780446304402202ece49e25a4b743f6965c70fd7c6efcf9101909a12fe81f2df98a5fcc203e49b02206e16a72fa8d1375d6117db99a960c324fe02f54d0853ecc19dda23a4f40949f0300a06082a8648ce3d0403020347003044022022587f895d257d5cf66da1d1c3627910b858443cb887f405e1948dc3e55d80b902200e2f07b93a2e4a487bb4bc721e9431ae723f921d91f84a875758d787c4302fa8",
			peerID:  "16Uiu2HAm9pWJoENCPfqs3NxD58ujsoi8PNAVpDDJxfbuVHSWj1VZ",
			keyType: pb.KeyType_Secp256k1,
		},
		{
			name:  "Invalid certificate 1",
			data:  "308201f83082019da00302010202081d051a136acdc4ea300a06082a8648ce3d040302301e311c301a06035504051313333037373732313536373332393634323238363020170d3235303332303130333830395a180f32313235303232343131333830395a301e311c301a06035504051313333037373732313536373332393634323238363059301306072a8648ce3d020106082a8648ce3d030107034200043168c3c9c49ec956c48446b64cc9c2c3d19eb7292ec8410ab9db14bef4946e5d14372ff5ae437b66b2fc724180bafeb8424a7bd4e119a02fcbbbabe039d9e6d7a381c23081bf3081bc060a2b0601040183a25a01010481ad3081aa045f0803125b3059301306072a8648ce3d020106082a8648ce3d03010703420004570acac25ebaaf7cc97c83858bff4c1bec26c9fdeb001b443c08cf26aee887099b36b73fa1aab6b3f729d8e9d8a7b789b5addcb79064769722a0da54cb4ceee804473045022100c253946d4c212698afb92095fdf281611f3fe7088f6cc1ccc71950509558459202206213b2c8fd07d53dc4554c54403116cb9d780d2fdd5b05c4447f1f187dbd26b6300a06082a8648ce3d0403020349003046022100c942bba92a2f3a1f639ae20c1c20e3bbea0f69d45c0ca67411a67c5ec71745f4022100896af401d8d137db9d075cb949b26c5808543540f3cf823352f53e920b5c7d55",
			error: "signature invalid",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := hex.DecodeString(tc.data)
			require.NoError(t, err)

			cert, err := x509.ParseCertificate(data)
			require.NoError(t, err)
			key, err := PubKeyFromCertChain([]*x509.Certificate{cert})
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
