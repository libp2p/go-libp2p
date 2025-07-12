package handshake

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestHandshake(t *testing.T) {
	for _, clientInitiated := range []bool{true, false} {
		t.Run(fmt.Sprintf("clientInitiated=%t", clientInitiated), func(t *testing.T) {
			hostname := "example.com"
			serverPriv, _, _ := crypto.GenerateEd25519Key(rand.Reader)
			clientPriv, _, _ := crypto.GenerateEd25519Key(rand.Reader)

			serverHandshake := PeerIDAuthHandshakeServer{
				Hostname: hostname,
				PrivKey:  serverPriv,
				TokenTTL: time.Hour,
				Hmac:     hmac.New(sha256.New, make([]byte, 32)),
			}

			clientHandshake := PeerIDAuthHandshakeClient{
				Hostname: hostname,
				PrivKey:  clientPriv,
			}
			if clientInitiated {
				clientHandshake.state = peerIDAuthClientInitiateChallenge
			}

			headers := make(http.Header)

			// Start the handshake
			if !clientInitiated {
				require.NoError(t, serverHandshake.ParseHeaderVal(nil))
				require.NoError(t, serverHandshake.Run())
				serverHandshake.SetHeader(headers)
			}

			// Server Inititated: Client receives the challenge and signs it. Also sends the challenge server
			// Client Inititated: Client forms the challenge and sends it
			require.NoError(t, clientHandshake.ParseHeader(headers))
			clear(headers)
			require.NoError(t, clientHandshake.Run())
			clientHandshake.AddHeader(headers)

			// Server Inititated: Server receives the sig and verifies it. Also signs the challenge-server (client authenticated)
			// Client Inititated: Server receives the challenge and signs it. Also sends the challenge-client
			serverHandshake.Reset()
			require.NoError(t, serverHandshake.ParseHeaderVal([]byte(headers.Get("Authorization"))))
			clear(headers)
			require.NoError(t, serverHandshake.Run())
			serverHandshake.SetHeader(headers)

			// Server Inititated: Client verifies sig and sets the bearer token for future requests  (server authenticated)
			// Client Inititated: Client verifies sig, and signs challenge. Sends it along with any application data (server authenticated)
			require.NoError(t, clientHandshake.ParseHeader(headers))
			clear(headers)
			require.NoError(t, clientHandshake.Run())
			clientHandshake.AddHeader(headers)

			// Server Inititated: Server verifies the bearer token
			// Client Inititated: Server verifies the sig, sets the bearer token (client authenticated)
			// and processes any application data
			serverHandshake.Reset()
			require.NoError(t, serverHandshake.ParseHeaderVal([]byte(headers.Get("Authorization"))))
			clear(headers)
			require.NoError(t, serverHandshake.Run())
			serverHandshake.SetHeader(headers)

			expectedClientPeerID, _ := peer.IDFromPrivateKey(clientPriv)
			expectedServerPeerID, _ := peer.IDFromPrivateKey(serverPriv)
			clientPeerID, err := serverHandshake.PeerID()
			require.NoError(t, err)
			require.Equal(t, expectedClientPeerID, clientPeerID)

			serverPeerID, err := clientHandshake.PeerID()
			require.NoError(t, err)
			require.Equal(t, expectedServerPeerID, serverPeerID)
		})
	}
}

func TestServerRefusesClientInitiatedHandshake(t *testing.T) {
	hostname := "example.com"
	serverPriv, _, _ := crypto.GenerateEd25519Key(rand.Reader)
	clientPriv, _, _ := crypto.GenerateEd25519Key(rand.Reader)

	serverHandshake := PeerIDAuthHandshakeServer{
		Hostname: hostname,
		PrivKey:  serverPriv,
		TokenTTL: time.Hour,
		Hmac:     hmac.New(sha256.New, make([]byte, 32)),
	}

	clientHandshake := PeerIDAuthHandshakeClient{
		Hostname: hostname,
		PrivKey:  clientPriv,
	}
	clientHandshake.SetInitiateChallenge()

	headers := make(http.Header)
	// Client initiates the handshake
	require.NoError(t, clientHandshake.Run())
	clientHandshake.AddHeader(headers)

	// Server receives the challenge-server, but chooses to reject it (simulating this by not passing the challenge)
	serverHandshake.Reset()
	require.NoError(t, serverHandshake.ParseHeaderVal(nil))
	clear(headers)
	require.NoError(t, serverHandshake.Run())
	serverHandshake.SetHeader(headers)

	// Client now runs the server-initiated handshake. Signs challenge-client; sends challenge-server
	require.NoError(t, clientHandshake.ParseHeader(headers))
	clear(headers)
	require.NoError(t, clientHandshake.Run())
	clientHandshake.AddHeader(headers)

	// Server verifies the challenge-client and signs the challenge-server
	serverHandshake.Reset()
	require.NoError(t, serverHandshake.ParseHeaderVal([]byte(headers.Get("Authorization"))))
	clear(headers)
	require.NoError(t, serverHandshake.Run())
	serverHandshake.SetHeader(headers)

	// Client verifies the challenge-server and sets the bearer token
	require.NoError(t, clientHandshake.ParseHeader(headers))
	clear(headers)
	require.NoError(t, clientHandshake.Run())
	clientHandshake.AddHeader(headers)

	expectedClientPeerID, _ := peer.IDFromPrivateKey(clientPriv)
	expectedServerPeerID, _ := peer.IDFromPrivateKey(serverPriv)
	clientPeerID, err := serverHandshake.PeerID()
	require.NoError(t, err)
	require.Equal(t, expectedClientPeerID, clientPeerID)

	serverPeerID, err := clientHandshake.PeerID()
	require.NoError(t, err)
	require.True(t, clientHandshake.HandshakeDone())
	require.Equal(t, expectedServerPeerID, serverPeerID)
}

func BenchmarkServerHandshake(b *testing.B) {
	clientHeader1 := make(http.Header)
	clientHeader2 := make(http.Header)
	headers := make(http.Header)

	hostname := "example.com"
	serverPriv, _, _ := crypto.GenerateEd25519Key(rand.Reader)
	clientPriv, _, _ := crypto.GenerateEd25519Key(rand.Reader)

	serverHandshake := PeerIDAuthHandshakeServer{
		Hostname: hostname,
		PrivKey:  serverPriv,
		TokenTTL: time.Hour,
		Hmac:     hmac.New(sha256.New, make([]byte, 32)),
	}

	clientHandshake := PeerIDAuthHandshakeClient{
		Hostname: hostname,
		PrivKey:  clientPriv,
	}
	require.NoError(b, serverHandshake.ParseHeaderVal(nil))
	require.NoError(b, serverHandshake.Run())
	serverHandshake.SetHeader(headers)

	// Client receives the challenge and signs it. Also sends the challenge server
	require.NoError(b, clientHandshake.ParseHeader(headers))
	clear(headers)
	require.NoError(b, clientHandshake.Run())
	clientHandshake.AddHeader(clientHeader1)

	// Server receives the sig and verifies it. Also signs the challenge server
	serverHandshake.Reset()
	require.NoError(b, serverHandshake.ParseHeaderVal([]byte(clientHeader1.Get("Authorization"))))
	clear(headers)
	require.NoError(b, serverHandshake.Run())
	serverHandshake.SetHeader(headers)

	// Client verifies sig and sets the bearer token for future requests
	require.NoError(b, clientHandshake.ParseHeader(headers))
	clear(headers)
	require.NoError(b, clientHandshake.Run())
	clientHandshake.AddHeader(clientHeader2)

	// Server verifies the bearer token
	serverHandshake.Reset()
	require.NoError(b, serverHandshake.ParseHeaderVal([]byte(clientHeader2.Get("Authorization"))))
	clear(headers)
	require.NoError(b, serverHandshake.Run())
	serverHandshake.SetHeader(headers)

	initialClientAuth := []byte(clientHeader1.Get("Authorization"))
	bearerClientAuth := []byte(clientHeader2.Get("Authorization"))
	_ = initialClientAuth
	_ = bearerClientAuth

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		serverHandshake.Reset()
		serverHandshake.ParseHeaderVal(nil)
		serverHandshake.Run()

		serverHandshake.Reset()
		serverHandshake.ParseHeaderVal(initialClientAuth)
		serverHandshake.Run()

		serverHandshake.Reset()
		serverHandshake.ParseHeaderVal(bearerClientAuth)
		serverHandshake.Run()
	}

}

func TestParsePeerIDAuthSchemeParams(t *testing.T) {
	str := `libp2p-PeerID sig="<base64-signature-bytes>", public-key="<base64-encoded-public-key-bytes>", bearer="<base64-encoded-opaque-blob>"`
	p := params{}
	expectedParam := params{
		sigB64:         []byte(`<base64-signature-bytes>`),
		publicKeyB64:   []byte(`<base64-encoded-public-key-bytes>`),
		bearerTokenB64: []byte(`<base64-encoded-opaque-blob>`),
	}
	err := p.parsePeerIDAuthSchemeParams([]byte(str))
	require.NoError(t, err)
	require.Equal(t, expectedParam, p)
}

func BenchmarkParsePeerIDAuthSchemeParams(b *testing.B) {
	str := []byte(`libp2p-PeerID peer-id="<server-peer-id-string>", sig="<base64-signature-bytes>", public-key="<base64-encoded-public-key-bytes>", bearer="<base64-encoded-opaque-blob>"`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := params{}
		err := p.parsePeerIDAuthSchemeParams(str)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestHeaderBuilder(t *testing.T) {
	hb := headerBuilder{}
	hb.writeScheme(PeerIDAuthScheme)
	hb.writeParam("peer-id", []byte("foo"))
	hb.writeParam("challenge-client", []byte("something-else"))
	hb.writeParam("hostname", []byte("example.com"))

	expected := `libp2p-PeerID peer-id="foo", challenge-client="something-else", hostname="example.com"`
	require.Equal(t, expected, hb.b.String())
}

func BenchmarkHeaderBuilder(b *testing.B) {
	h := headerBuilder{}
	scratch := make([]byte, 256)
	scratch = scratch[:0]

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.b.Grow(256)
		h.writeParamB64(scratch, "foo", []byte("bar"))
		h.clear()
	}
}

// Test Vectors
var zeroBytes = make([]byte, 64)
var zeroKey, _, _ = crypto.GenerateEd25519Key(bytes.NewReader(zeroBytes))

// Peer ID derived from the zero key
var zeroID, _ = peer.IDFromPublicKey(zeroKey.GetPublic())

func TestOpaqueStateRoundTrip(t *testing.T) {
	zeroBytes := [32]byte{}

	// To drop the monotonic clock reading
	timeAfterUnmarshal := time.Now()
	b, err := json.Marshal(timeAfterUnmarshal)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &timeAfterUnmarshal))
	hmac := hmac.New(sha256.New, zeroBytes[:])

	o := opaqueState{
		ChallengeClient: "foo-bar",
		CreatedTime:     timeAfterUnmarshal,
		IsToken:         true,
		PeerID:          zeroID,
		Hostname:        "example.com",
	}

	hmac.Reset()
	b, err = o.Marshal(hmac, nil)
	require.NoError(t, err)

	o2 := opaqueState{}

	hmac.Reset()
	err = o2.Unmarshal(hmac, b)
	require.NoError(t, err)
	require.EqualValues(t, o, o2)
}

func FuzzServerHandshakeNoPanic(f *testing.F) {
	zeroBytes := [32]byte{}
	hmac := hmac.New(sha256.New, zeroBytes[:])

	f.Fuzz(func(_ *testing.T, data []byte) {
		hmac.Reset()
		h := PeerIDAuthHandshakeServer{
			Hostname: "example.com",
			PrivKey:  zeroKey,
			Hmac:     hmac,
		}
		err := h.ParseHeaderVal(data)
		if err != nil {
			return
		}
		err = h.Run()
		if err != nil {
			return
		}
		h.PeerID()
	})
}

func BenchmarkOpaqueStateWrite(b *testing.B) {
	zeroBytes := [32]byte{}
	hmac := hmac.New(sha256.New, zeroBytes[:])
	o := opaqueState{
		ChallengeClient: "foo-bar",
		CreatedTime:     time.Now(),
	}
	d := make([]byte, 512)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hmac.Reset()
		_, err := o.Marshal(hmac, d[:0])
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOpaqueStateRead(b *testing.B) {
	zeroBytes := [32]byte{}
	hmac := hmac.New(sha256.New, zeroBytes[:])
	o := opaqueState{
		ChallengeClient: "foo-bar",
		CreatedTime:     time.Now(),
	}
	d := make([]byte, 256)
	d, err := o.Marshal(hmac, d[:0])
	require.NoError(b, err)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hmac.Reset()
		err := o.Unmarshal(hmac, d)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func FuzzParsePeerIDAuthSchemeParamsNoPanic(f *testing.F) {
	p := params{}
	// Just check that we don't panic
	f.Fuzz(func(_ *testing.T, data []byte) {
		p.parsePeerIDAuthSchemeParams(data)
	})
}

type specsExampleParameters struct {
	hostname      string
	serverPriv    crypto.PrivKey
	serverHmacKey [32]byte
	clientPriv    crypto.PrivKey
}

func TestSpecsExample(t *testing.T) {
	originalRandReader := randReader
	originalNowFn := nowFn
	randReader = bytes.NewReader(append(
		bytes.Repeat([]byte{0x11}, 32),
		bytes.Repeat([]byte{0x33}, 32)...,
	))
	nowFn = func() time.Time {
		return time.Unix(0, 0)
	}
	defer func() {
		randReader = originalRandReader
		nowFn = originalNowFn
	}()

	parameters := specsExampleParameters{
		hostname: "example.com",
	}
	serverPrivBytes, err := hex.AppendDecode(nil, []byte("0801124001010101010101010101010101010101010101010101010101010101010101018a88e3dd7409f195fd52db2d3cba5d72ca6709bf1d94121bf3748801b40f6f5c"))
	require.NoError(t, err)
	clientPrivBytes, err := hex.AppendDecode(nil, []byte("0801124002020202020202020202020202020202020202020202020202020202020202028139770ea87d175f56a35466c34c7ecccb8d8a91b4ee37a25df60f5b8fc9b394"))
	require.NoError(t, err)

	parameters.serverPriv, err = crypto.UnmarshalPrivateKey(serverPrivBytes)
	require.NoError(t, err)

	parameters.clientPriv, err = crypto.UnmarshalPrivateKey(clientPrivBytes)
	require.NoError(t, err)

	serverHandshake := PeerIDAuthHandshakeServer{
		Hostname: parameters.hostname,
		PrivKey:  parameters.serverPriv,
		TokenTTL: time.Hour,
		Hmac:     hmac.New(sha256.New, parameters.serverHmacKey[:]),
	}

	clientHandshake := PeerIDAuthHandshakeClient{
		Hostname: parameters.hostname,
		PrivKey:  parameters.clientPriv,
	}

	headers := make(http.Header)

	// Start the handshake
	require.NoError(t, serverHandshake.ParseHeaderVal(nil))
	require.NoError(t, serverHandshake.Run())
	serverHandshake.SetHeader(headers)
	initialWWWAuthenticate := headers.Get("WWW-Authenticate")

	// Client receives the challenge and signs it. Also sends the challenge server
	require.NoError(t, clientHandshake.ParseHeader(headers))
	clear(headers)
	require.NoError(t, clientHandshake.Run())
	clientHandshake.AddHeader(headers)
	clientAuthentication := headers.Get("Authorization")

	// Server receives the sig and verifies it. Also signs the challenge server
	serverHandshake.Reset()
	require.NoError(t, serverHandshake.ParseHeaderVal([]byte(headers.Get("Authorization"))))
	clear(headers)
	require.NoError(t, serverHandshake.Run())
	serverHandshake.SetHeader(headers)
	serverAuthentication := headers.Get("Authentication-Info")

	// Client verifies sig and sets the bearer token for future requests
	require.NoError(t, clientHandshake.ParseHeader(headers))
	clear(headers)
	require.NoError(t, clientHandshake.Run())
	clientHandshake.AddHeader(headers)
	clientBearerToken := headers.Get("Authorization")

	params := params{}
	params.parsePeerIDAuthSchemeParams([]byte(initialWWWAuthenticate))
	challengeClient := params.challengeClient
	params.parsePeerIDAuthSchemeParams([]byte(clientAuthentication))
	challengeServer := params.challengeServer

	fmt.Println("### Parameters")
	fmt.Println("| Parameter | Value |")
	fmt.Println("| --- | --- |")
	fmt.Printf("| hostname | %s |\n", parameters.hostname)
	fmt.Printf("| Server Private Key (pb encoded as hex) | %s |\n", hex.EncodeToString(serverPrivBytes))
	fmt.Printf("| Server HMAC Key (hex) | %s |\n", hex.EncodeToString(parameters.serverHmacKey[:]))
	fmt.Printf("| Challenge Client | %s |\n", string(challengeClient))
	fmt.Printf("| Client Private Key (pb encoded as hex) | %s |\n", hex.EncodeToString(clientPrivBytes))
	fmt.Printf("| Challenge Server | %s |\n", string(challengeServer))
	fmt.Printf("| \"Now\" time | %s |\n", nowFn())
	fmt.Println()
	fmt.Println("### Handshake Diagram")

	fmt.Println("```mermaid")
	fmt.Printf(`sequenceDiagram
Client->>Server: Initial request
Server->>Client: WWW-Authenticate=%s
Client->>Server: Authorization=%s
Note left of Server: Server has authenticated Client
Server->>Client: Authentication-Info=%s
Note right of Client: Client has authenticated Server

Note over Client: Future requests use the bearer token
Client->>Server: Authorization=%s
`, initialWWWAuthenticate, clientAuthentication, serverAuthentication, clientBearerToken)
	fmt.Println("```")

}

func TestSpecsClientInitiatedExample(t *testing.T) {
	originalRandReader := randReader
	originalNowFn := nowFn
	randReader = bytes.NewReader(append(
		bytes.Repeat([]byte{0x33}, 32),
		bytes.Repeat([]byte{0x11}, 32)...,
	))
	nowFn = func() time.Time {
		return time.Unix(0, 0)
	}
	defer func() {
		randReader = originalRandReader
		nowFn = originalNowFn
	}()

	parameters := specsExampleParameters{
		hostname: "example.com",
	}
	serverPrivBytes, err := hex.AppendDecode(nil, []byte("0801124001010101010101010101010101010101010101010101010101010101010101018a88e3dd7409f195fd52db2d3cba5d72ca6709bf1d94121bf3748801b40f6f5c"))
	require.NoError(t, err)
	clientPrivBytes, err := hex.AppendDecode(nil, []byte("0801124002020202020202020202020202020202020202020202020202020202020202028139770ea87d175f56a35466c34c7ecccb8d8a91b4ee37a25df60f5b8fc9b394"))
	require.NoError(t, err)

	parameters.serverPriv, err = crypto.UnmarshalPrivateKey(serverPrivBytes)
	require.NoError(t, err)

	parameters.clientPriv, err = crypto.UnmarshalPrivateKey(clientPrivBytes)
	require.NoError(t, err)

	serverHandshake := PeerIDAuthHandshakeServer{
		Hostname: parameters.hostname,
		PrivKey:  parameters.serverPriv,
		TokenTTL: time.Hour,
		Hmac:     hmac.New(sha256.New, parameters.serverHmacKey[:]),
	}

	clientHandshake := PeerIDAuthHandshakeClient{
		Hostname: parameters.hostname,
		PrivKey:  parameters.clientPriv,
	}

	headers := make(http.Header)

	// Start the handshake
	clientHandshake.SetInitiateChallenge()
	require.NoError(t, clientHandshake.Run())
	clientHandshake.AddHeader(headers)
	clientChallenge := headers.Get("Authorization")

	// Server receives the challenge and signs it. Also sends challenge-client
	serverHandshake.Reset()
	require.NoError(t, serverHandshake.ParseHeaderVal([]byte(headers.Get("Authorization"))))
	clear(headers)
	require.NoError(t, serverHandshake.Run())
	serverHandshake.SetHeader(headers)
	serverAuthentication := headers.Get("WWW-Authenticate")
	params := params{}
	params.parsePeerIDAuthSchemeParams([]byte(serverAuthentication))
	challengeClient := params.challengeClient

	// Client verifies sig and signs the challenge-client
	require.NoError(t, clientHandshake.ParseHeader(headers))
	clear(headers)
	require.NoError(t, clientHandshake.Run())
	clientHandshake.AddHeader(headers)
	clientAuthentication := headers.Get("Authorization")

	// Server verifies sig and sets the bearer token
	serverHandshake.Reset()
	require.NoError(t, serverHandshake.ParseHeaderVal([]byte(headers.Get("Authorization"))))
	clear(headers)
	require.NoError(t, serverHandshake.Run())
	serverHandshake.SetHeader(headers)
	serverReplayWithBearer := headers.Get("Authentication-Info")

	params.parsePeerIDAuthSchemeParams([]byte(clientChallenge))
	challengeServer := params.challengeServer

	fmt.Println("### Parameters")
	fmt.Println("| Parameter | Value |")
	fmt.Println("| --- | --- |")
	fmt.Printf("| hostname | %s |\n", parameters.hostname)
	fmt.Printf("| Server Private Key (pb encoded as hex) | %s |\n", hex.EncodeToString(serverPrivBytes))
	fmt.Printf("| Server HMAC Key (hex) | %s |\n", hex.EncodeToString(parameters.serverHmacKey[:]))
	fmt.Printf("| Challenge Client | %s |\n", string(challengeClient))
	fmt.Printf("| Client Private Key (pb encoded as hex) | %s |\n", hex.EncodeToString(clientPrivBytes))
	fmt.Printf("| Challenge Server | %s |\n", string(challengeServer))
	fmt.Printf("| \"Now\" time | %s |\n", nowFn())
	fmt.Println()
	fmt.Println("### Handshake Diagram")

	fmt.Println("```mermaid")
	fmt.Printf(`sequenceDiagram
Client->>Server: Authorization=%s
Server->>Client: WWW-Authenticate=%s
Note right of Client: Client has authenticated Server

Client->>Server: Authorization=%s
Note left of Server: Server has authenticated Client
Server->>Client: Authentication-Info=%s
Note over Client: Future requests use the bearer token
`, clientChallenge, serverAuthentication, clientAuthentication, serverReplayWithBearer)
	fmt.Println("```")

}

func TestSigningExample(t *testing.T) {
	serverPrivBytes, err := hex.AppendDecode(nil, []byte("0801124001010101010101010101010101010101010101010101010101010101010101018a88e3dd7409f195fd52db2d3cba5d72ca6709bf1d94121bf3748801b40f6f5c"))
	require.NoError(t, err)
	serverPriv, err := crypto.UnmarshalPrivateKey(serverPrivBytes)
	require.NoError(t, err)
	clientPrivBytes, err := hex.AppendDecode(nil, []byte("0801124002020202020202020202020202020202020202020202020202020202020202028139770ea87d175f56a35466c34c7ecccb8d8a91b4ee37a25df60f5b8fc9b394"))
	require.NoError(t, err)
	clientPriv, err := crypto.UnmarshalPrivateKey(clientPrivBytes)
	require.NoError(t, err)
	clientPubKeyBytes, err := crypto.MarshalPublicKey(clientPriv.GetPublic())
	require.NoError(t, err)

	require.NoError(t, err)
	challenge := "ERERERERERERERERERERERERERERERERERERERERERE="

	hostname := "example.com"
	dataToSign, err := genDataToSign(nil, PeerIDAuthScheme, []sigParam{
		{"challenge-server", []byte(challenge)},
		{"client-public-key", clientPubKeyBytes},
		{"hostname", []byte(hostname)},
	})
	require.NoError(t, err)

	sig, err := sign(serverPriv, PeerIDAuthScheme, []sigParam{
		{"challenge-server", []byte(challenge)},
		{"client-public-key", clientPubKeyBytes},
		{"hostname", []byte(hostname)},
	})
	require.NoError(t, err)

	fmt.Println("### Signing Example")

	fmt.Println("| Parameter | Value |")
	fmt.Println("| --- | --- |")
	fmt.Printf("| hostname | %s |\n", hostname)
	fmt.Printf("| Server Private Key (pb encoded as hex) | %s |\n", hex.EncodeToString(serverPrivBytes))
	fmt.Printf("| challenge-server | %s |\n", string(challenge))
	fmt.Printf("| Client Public Key (pb encoded as hex) | %s |\n", hex.EncodeToString(clientPubKeyBytes))
	fmt.Printf("| data to sign ([percent encoded](https://datatracker.ietf.org/doc/html/rfc3986#section-2.1)) | %s |\n", url.PathEscape(string(dataToSign)))
	fmt.Printf("| data to sign (hex encoded) | %s |\n", hex.EncodeToString(dataToSign))
	fmt.Printf("| signature (base64 encoded) | %s |\n", base64.URLEncoding.EncodeToString(sig))
	fmt.Println()

	fmt.Println("Note that the `=` after the libp2p-PeerID scheme is actually the varint length of the challenge-server parameter.")

}
