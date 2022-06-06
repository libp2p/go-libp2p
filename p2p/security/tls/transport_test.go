package libp2ptls

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"math/big"
	mrand "math/rand"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	ic "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/sec"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createPeer(t *testing.T) (peer.ID, ic.PrivKey) {
	var priv ic.PrivKey
	var err error
	switch mrand.Int() % 4 {
	case 0:
		priv, _, err = ic.GenerateECDSAKeyPair(rand.Reader)
	case 1:
		priv, _, err = ic.GenerateRSAKeyPair(2048, rand.Reader)
	case 2:
		priv, _, err = ic.GenerateEd25519Key(rand.Reader)
	case 3:
		priv, _, err = ic.GenerateSecp256k1Key(rand.Reader)
	}
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)
	t.Logf("using a %s key: %s", priv.Type(), id.Pretty())
	return id, priv
}

func connect(t *testing.T) (net.Conn, net.Conn) {
	ln, err := net.ListenTCP("tcp", nil)
	require.NoError(t, err)
	defer ln.Close()
	serverConnChan := make(chan *net.TCPConn)
	go func() {
		conn, err := ln.Accept()
		assert.NoError(t, err)
		sconn := conn.(*net.TCPConn)
		serverConnChan <- sconn
	}()
	conn, err := net.DialTCP("tcp", nil, ln.Addr().(*net.TCPAddr))
	require.NoError(t, err)
	sconn := <-serverConnChan
	// On Windows we have to set linger to 0, otherwise we'll occasionally run into errors like the following:
	// "connectex: Only one usage of each socket address (protocol/network address/port) is normally permitted."
	// See https://github.com/libp2p/go-libp2p/issues/1529.
	conn.SetLinger(0)
	sconn.SetLinger(0)
	t.Cleanup(func() {
		conn.Close()
		sconn.Close()
	})
	return conn, sconn
}

func isWindowsTCPCloseError(err error) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	return strings.Contains(err.Error(), "wsarecv: An existing connection was forcibly closed by the remote host")
}

func TestHandshakeSucceeds(t *testing.T) {
	clientID, clientKey := createPeer(t)
	serverID, serverKey := createPeer(t)

	handshake := func(t *testing.T) {
		clientTransport, err := New(clientKey)
		require.NoError(t, err)
		serverTransport, err := New(serverKey)
		require.NoError(t, err)

		clientInsecureConn, serverInsecureConn := connect(t)

		serverConnChan := make(chan sec.SecureConn)
		go func() {
			serverConn, err := serverTransport.SecureInbound(context.Background(), serverInsecureConn, "")
			require.NoError(t, err)
			serverConnChan <- serverConn
		}()

		clientConn, err := clientTransport.SecureOutbound(context.Background(), clientInsecureConn, serverID)
		require.NoError(t, err)
		defer clientConn.Close()

		var serverConn sec.SecureConn
		select {
		case serverConn = <-serverConnChan:
		case <-time.After(250 * time.Millisecond):
			t.Fatal("expected the server to accept a connection")
		}
		defer serverConn.Close()

		require.Equal(t, clientConn.LocalPeer(), clientID)
		require.Equal(t, serverConn.LocalPeer(), serverID)
		require.True(t, clientConn.LocalPrivateKey().Equals(clientKey), "client private key mismatch")
		require.True(t, serverConn.LocalPrivateKey().Equals(serverKey), "server private key mismatch")
		require.Equal(t, clientConn.RemotePeer(), serverID)
		require.Equal(t, serverConn.RemotePeer(), clientID)
		require.True(t, clientConn.RemotePublicKey().Equals(serverKey.GetPublic()), "server public key mismatch")
		require.True(t, serverConn.RemotePublicKey().Equals(clientKey.GetPublic()), "client public key mismatch")
		// exchange some data
		_, err = serverConn.Write([]byte("foobar"))
		require.NoError(t, err)
		b := make([]byte, 6)
		_, err = clientConn.Read(b)
		require.NoError(t, err)
		require.Equal(t, string(b), "foobar")
	}

	t.Run("with extension not critical", func(t *testing.T) {
		handshake(t)
	})

	t.Run("with extension critical", func(t *testing.T) {
		extensionCritical = true
		t.Cleanup(func() { extensionCritical = false })

		handshake(t)
	})
}

// crypto/tls' cancellation logic works by spinning up a separate Go routine that watches the ctx.
// If the ctx is canceled, it kills the handshake.
// We need to make sure that the handshake doesn't complete before that Go routine picks up the cancellation.
type delayedConn struct {
	net.Conn
	delay time.Duration
}

func (c *delayedConn) Read(b []byte) (int, error) {
	time.Sleep(c.delay)
	return c.Conn.Read(b)
}

func TestHandshakeConnectionCancelations(t *testing.T) {
	_, clientKey := createPeer(t)
	serverID, serverKey := createPeer(t)

	clientTransport, err := New(clientKey)
	require.NoError(t, err)
	serverTransport, err := New(serverKey)
	require.NoError(t, err)

	t.Run("cancel outgoing connection", func(t *testing.T) {
		clientInsecureConn, serverInsecureConn := connect(t)

		errChan := make(chan error)
		go func() {
			_, err := serverTransport.SecureInbound(context.Background(), serverInsecureConn, "")
			errChan <- err
		}()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = clientTransport.SecureOutbound(ctx, clientInsecureConn, serverID)
		require.ErrorIs(t, err, context.Canceled)
		require.Error(t, <-errChan)
	})

	t.Run("cancel incoming connection", func(t *testing.T) {
		clientInsecureConn, serverInsecureConn := connect(t)

		errChan := make(chan error)
		go func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			conn, err := serverTransport.SecureInbound(ctx, &delayedConn{Conn: serverInsecureConn, delay: 5 * time.Millisecond}, "")
			// crypto/tls' context handling works by spinning up a separate Go routine that watches the context,
			// and closes the underlying connection when that context is canceled.
			// It is therefore not guaranteed (but very likely) that this happens _during_ the TLS handshake.
			if err == nil {
				_, err = conn.Read([]byte{0})
			}
			errChan <- err
		}()
		_, err = clientTransport.SecureOutbound(context.Background(), clientInsecureConn, serverID)
		require.Error(t, err)
		require.ErrorIs(t, <-errChan, context.Canceled)
	})
}

func TestPeerIDMismatch(t *testing.T) {
	_, clientKey := createPeer(t)
	serverID, serverKey := createPeer(t)

	serverTransport, err := New(serverKey)
	require.NoError(t, err)
	clientTransport, err := New(clientKey)
	require.NoError(t, err)

	t.Run("for outgoing connections", func(t *testing.T) {
		clientInsecureConn, serverInsecureConn := connect(t)

		errChan := make(chan error)
		go func() {
			conn, err := serverTransport.SecureInbound(context.Background(), serverInsecureConn, "")
			// crypto/tls' context handling works by spinning up a separate Go routine that watches the context,
			// and closes the underlying connection when that context is canceled.
			// It is therefore not guaranteed (but very likely) that this happens _during_ the TLS handshake.
			if err == nil {
				_, err = conn.Read([]byte{0})
			}
			errChan <- err
		}()

		// dial, but expect the wrong peer ID
		thirdPartyID, _ := createPeer(t)
		_, err = clientTransport.SecureOutbound(context.Background(), clientInsecureConn, thirdPartyID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "peer IDs don't match")

		var serverErr error
		select {
		case serverErr = <-errChan:
		case <-time.After(250 * time.Millisecond):
			t.Fatal("expected handshake to return on the server side")
		}
		require.Error(t, serverErr)
		require.Contains(t, serverErr.Error(), "tls: bad certificate")
	})

	t.Run("for incoming connections", func(t *testing.T) {
		clientInsecureConn, serverInsecureConn := connect(t)

		errChan := make(chan error)
		go func() {
			thirdPartyID, _ := createPeer(t)
			// expect the wrong peer ID
			_, err := serverTransport.SecureInbound(context.Background(), serverInsecureConn, thirdPartyID)
			errChan <- err
		}()

		conn, err := clientTransport.SecureOutbound(context.Background(), clientInsecureConn, serverID)
		require.NoError(t, err)
		_, err = conn.Read([]byte{0})
		require.Error(t, err)
		require.Contains(t, err.Error(), "tls: bad certificate")

		var serverErr error
		select {
		case serverErr = <-errChan:
		case <-time.After(250 * time.Millisecond):
			t.Fatal("expected handshake to return on the server side")
		}
		require.Error(t, serverErr)
		require.Contains(t, serverErr.Error(), "peer IDs don't match")
	})
}

func TestInvalidCerts(t *testing.T) {
	_, clientKey := createPeer(t)
	serverID, serverKey := createPeer(t)

	type transform struct {
		name     string
		apply    func(*Identity)
		checkErr func(*testing.T, error) // the error that the side validating the chain gets
	}

	invalidateCertChain := func(identity *Identity) {
		switch identity.config.Certificates[0].PrivateKey.(type) {
		case *rsa.PrivateKey:
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)
			identity.config.Certificates[0].PrivateKey = key
		case *ecdsa.PrivateKey:
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			require.NoError(t, err)
			identity.config.Certificates[0].PrivateKey = key
		default:
			t.Fatal("unexpected private key type")
		}
	}

	twoCerts := func(identity *Identity) {
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(1)}
		key1, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		key2, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		cert1DER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key1.Public(), key1)
		require.NoError(t, err)
		cert1, err := x509.ParseCertificate(cert1DER)
		require.NoError(t, err)
		cert2DER, err := x509.CreateCertificate(rand.Reader, tmpl, cert1, key2.Public(), key1)
		require.NoError(t, err)
		identity.config.Certificates = []tls.Certificate{{
			Certificate: [][]byte{cert2DER, cert1DER},
			PrivateKey:  key2,
		}}
	}

	getCertWithKey := func(key crypto.Signer, tmpl *x509.Certificate) tls.Certificate {
		cert, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
		require.NoError(t, err)
		return tls.Certificate{
			Certificate: [][]byte{cert},
			PrivateKey:  key,
		}
	}

	getCert := func(tmpl *x509.Certificate) tls.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		return getCertWithKey(key, tmpl)
	}

	expiredCert := func(identity *Identity) {
		cert := getCert(&x509.Certificate{
			SerialNumber: big.NewInt(1),
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(-time.Minute),
			ExtraExtensions: []pkix.Extension{
				{Id: extensionID, Value: []byte("foobar")},
			},
		})
		identity.config.Certificates = []tls.Certificate{cert}
	}

	noKeyExtension := func(identity *Identity) {
		cert := getCert(&x509.Certificate{
			SerialNumber: big.NewInt(1),
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
		})
		identity.config.Certificates = []tls.Certificate{cert}
	}

	unparseableKeyExtension := func(identity *Identity) {
		cert := getCert(&x509.Certificate{
			SerialNumber: big.NewInt(1),
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			ExtraExtensions: []pkix.Extension{
				{Id: extensionID, Value: []byte("foobar")},
			},
		})
		identity.config.Certificates = []tls.Certificate{cert}
	}

	unparseableKey := func(identity *Identity) {
		data, err := asn1.Marshal(signedKey{PubKey: []byte("foobar")})
		require.NoError(t, err)
		cert := getCert(&x509.Certificate{
			SerialNumber: big.NewInt(1),
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			ExtraExtensions: []pkix.Extension{
				{Id: extensionID, Value: data},
			},
		})
		identity.config.Certificates = []tls.Certificate{cert}
	}

	tooShortSignature := func(identity *Identity) {
		key, _, err := ic.GenerateSecp256k1Key(rand.Reader)
		require.NoError(t, err)
		keyBytes, err := ic.MarshalPublicKey(key.GetPublic())
		require.NoError(t, err)
		data, err := asn1.Marshal(signedKey{
			PubKey:    keyBytes,
			Signature: []byte("foobar"),
		})
		require.NoError(t, err)
		cert := getCert(&x509.Certificate{
			SerialNumber: big.NewInt(1),
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			ExtraExtensions: []pkix.Extension{
				{Id: extensionID, Value: data},
			},
		})
		identity.config.Certificates = []tls.Certificate{cert}
	}

	invalidSignature := func(identity *Identity) {
		key, _, err := ic.GenerateSecp256k1Key(rand.Reader)
		require.NoError(t, err)
		keyBytes, err := ic.MarshalPublicKey(key.GetPublic())
		require.NoError(t, err)
		signature, err := key.Sign([]byte("foobar"))
		require.NoError(t, err)
		data, err := asn1.Marshal(signedKey{
			PubKey:    keyBytes,
			Signature: signature,
		})
		require.NoError(t, err)
		cert := getCert(&x509.Certificate{
			SerialNumber: big.NewInt(1),
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			ExtraExtensions: []pkix.Extension{
				{Id: extensionID, Value: data},
			},
		})
		identity.config.Certificates = []tls.Certificate{cert}
	}

	transforms := []transform{
		{
			name:  "private key used in the TLS handshake doesn't match the public key in the cert",
			apply: invalidateCertChain,
			checkErr: func(t *testing.T, err error) {
				if err.Error() != "tls: invalid signature by the client certificate: ECDSA verification failure" &&
					err.Error() != "tls: invalid signature by the server certificate: ECDSA verification failure" {
					t.Fatalf("unexpected error message: %s", err)
				}
			},
		},
		{
			name:  "certificate chain contains 2 certs",
			apply: twoCerts,
			checkErr: func(t *testing.T, err error) {
				require.EqualError(t, err, "expected one certificates in the chain")
			},
		},
		{
			name:  "cert is expired",
			apply: expiredCert,
			checkErr: func(t *testing.T, err error) {
				require.Contains(t, err.Error(), "certificate has expired or is not yet valid")
			},
		},
		{
			name:  "cert doesn't have the key extension",
			apply: noKeyExtension,
			checkErr: func(t *testing.T, err error) {
				require.EqualError(t, err, "expected certificate to contain the key extension")
			},
		},
		{
			name:     "key extension not parseable",
			apply:    unparseableKeyExtension,
			checkErr: func(t *testing.T, err error) { require.Contains(t, err.Error(), "asn1") },
		},
		{
			name:  "key protobuf not parseable",
			apply: unparseableKey,
			checkErr: func(t *testing.T, err error) {
				require.Contains(t, err.Error(), "unmarshalling public key failed: proto:")
			},
		},
		{
			name:  "signature is malformed",
			apply: tooShortSignature,
			checkErr: func(t *testing.T, err error) {
				require.Contains(t, err.Error(), "signature verification failed:")
			},
		},
		{
			name:  "signature is invalid",
			apply: invalidSignature,
			checkErr: func(t *testing.T, err error) {
				require.Contains(t, err.Error(), "signature invalid")
			},
		},
	}

	for i := range transforms {
		tr := transforms[i]

		t.Run(fmt.Sprintf("client offending: %s", tr.name), func(t *testing.T) {
			serverTransport, err := New(serverKey)
			require.NoError(t, err)
			clientTransport, err := New(clientKey)
			require.NoError(t, err)
			tr.apply(clientTransport.identity)

			clientInsecureConn, serverInsecureConn := connect(t)

			serverErrChan := make(chan error)
			go func() {
				_, err := serverTransport.SecureInbound(context.Background(), serverInsecureConn, "")
				serverErrChan <- err
			}()

			conn, err := clientTransport.SecureOutbound(context.Background(), clientInsecureConn, serverID)
			require.NoError(t, err)
			clientErrChan := make(chan error)
			go func() {
				_, err := conn.Read([]byte{0})
				clientErrChan <- err
			}()
			select {
			case err := <-clientErrChan:
				require.Error(t, err)
				if err.Error() != "remote error: tls: error decrypting message" &&
					err.Error() != "remote error: tls: bad certificate" &&
					!isWindowsTCPCloseError(err) {
					t.Errorf("unexpected error: %s", err.Error())
				}
			case <-time.After(250 * time.Millisecond):
				t.Fatal("expected the server handshake to return")
			}

			select {
			case err := <-serverErrChan:
				require.Error(t, err)
				tr.checkErr(t, err)
			case <-time.After(250 * time.Millisecond):
				t.Fatal("expected the server handshake to return")
			}
		})

		t.Run(fmt.Sprintf("server offending: %s", tr.name), func(t *testing.T) {
			serverTransport, err := New(serverKey)
			require.NoError(t, err)
			tr.apply(serverTransport.identity)
			clientTransport, err := New(clientKey)
			require.NoError(t, err)

			clientInsecureConn, serverInsecureConn := connect(t)

			errChan := make(chan error)
			go func() {
				_, err := serverTransport.SecureInbound(context.Background(), serverInsecureConn, "")
				errChan <- err
			}()

			_, err = clientTransport.SecureOutbound(context.Background(), clientInsecureConn, serverID)
			require.Error(t, err)
			tr.checkErr(t, err)

			var serverErr error
			select {
			case serverErr = <-errChan:
			case <-time.After(250 * time.Millisecond):
				t.Fatal("expected the server handshake to return")
			}
			require.Error(t, serverErr)
			if !isWindowsTCPCloseError(serverErr) {
				require.Contains(t, serverErr.Error(), "remote error: tls:")
			}
		})
	}
}
