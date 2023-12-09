package noise

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/p2p/security/noise/pb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestTransport(t *testing.T, typ, bits int) *Transport {
	priv, pub, err := crypto.GenerateKeyPair(typ, bits)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return &Transport{
		localID:    id,
		privateKey: priv,
	}
}

func newTestTransportWithMuxers(t *testing.T, typ, bits int, muxers []protocol.ID) *Transport {
	transport := newTestTransport(t, typ, bits)
	transport.muxers = muxers
	return transport
}

// Create a new pair of connected TCP sockets.
func newConnPair(t *testing.T) (net.Conn, net.Conn) {
	lstnr, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
		return nil, nil
	}

	var clientErr error
	var client net.Conn
	addr := lstnr.Addr()
	done := make(chan struct{})

	go func() {
		defer close(done)
		client, clientErr = net.Dial(addr.Network(), addr.String())
	}()

	server, err := lstnr.Accept()
	<-done

	lstnr.Close()

	if err != nil {
		t.Fatalf("Failed to accept: %v", err)
	}

	if clientErr != nil {
		t.Fatalf("Failed to connect: %v", clientErr)
	}

	return client, server
}

func connect(t *testing.T, initTransport, respTransport *Transport) (*secureSession, *secureSession) {
	init, resp := newConnPair(t)

	var initConn sec.SecureConn
	var initErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		initConn, initErr = initTransport.SecureOutbound(context.Background(), init, respTransport.localID)
	}()

	respConn, respErr := respTransport.SecureInbound(context.Background(), resp, "")
	<-done

	if initErr != nil {
		t.Fatal(initErr)
	}

	if respErr != nil {
		t.Fatal(respErr)
	}

	return initConn.(*secureSession), respConn.(*secureSession)
}

func TestDeadlines(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)

	init, resp := newConnPair(t)
	defer init.Close()
	defer resp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err := initTransport.SecureOutbound(ctx, init, respTransport.localID)
	if err == nil {
		t.Fatalf("expected i/o timeout err; got: %s", err)
	}

	var neterr net.Error
	if ok := errors.As(err, &neterr); !ok || !neterr.Timeout() {
		t.Fatalf("expected i/o timeout err; got: %s", err)
	}
}

func TestIDs(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)

	initConn, respConn := connect(t, initTransport, respTransport)
	defer initConn.Close()
	defer respConn.Close()

	if initConn.LocalPeer() != initTransport.localID {
		t.Fatal("Initiator Local Peer ID mismatch.")
	}

	if respConn.RemotePeer() != initTransport.localID {
		t.Fatal("Responder Remote Peer ID mismatch.")
	}

	if initConn.LocalPeer() != respConn.RemotePeer() {
		t.Fatal("Responder Local Peer ID mismatch.")
	}

	// TODO: check after stage 0 of handshake if updated
	if initConn.RemotePeer() != respTransport.localID {
		t.Errorf("Initiator Remote Peer ID mismatch. expected %x got %x", respTransport.localID, initConn.RemotePeer())
	}
}

func TestKeys(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)

	initConn, respConn := connect(t, initTransport, respTransport)
	defer initConn.Close()
	defer respConn.Close()

	pk1 := respConn.RemotePublicKey()
	pk2 := initTransport.privateKey.GetPublic()
	if !pk1.Equals(pk2) {
		t.Errorf("Public key mismatch. expected %x got %x", pk1, pk2)
	}

	pk3 := initConn.RemotePublicKey()
	pk4 := respTransport.privateKey.GetPublic()
	if !pk3.Equals(pk4) {
		t.Errorf("Public key mismatch. expected %x got %x", pk3, pk4)
	}
}

func TestPeerIDMatch(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)
	init, resp := newConnPair(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := initTransport.SecureOutbound(context.Background(), init, respTransport.localID)
		assert.NoError(t, err)
		assert.Equal(t, respTransport.localID, conn.RemotePeer())
		b := make([]byte, 6)
		_, err = conn.Read(b)
		assert.NoError(t, err)
		assert.Equal(t, []byte("foobar"), b)
	}()

	conn, err := respTransport.SecureInbound(context.Background(), resp, initTransport.localID)
	require.NoError(t, err)
	require.Equal(t, initTransport.localID, conn.RemotePeer())
	_, err = conn.Write([]byte("foobar"))
	require.NoError(t, err)
}

func TestPeerIDMismatchOutboundFailsHandshake(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)
	init, resp := newConnPair(t)

	errChan := make(chan error)
	go func() {
		_, err := initTransport.SecureOutbound(context.Background(), init, "a-random-peer-id")
		errChan <- err
	}()

	_, err := respTransport.SecureInbound(context.Background(), resp, "")
	require.Error(t, err)

	initErr := <-errChan
	require.Error(t, initErr, "expected initiator to fail with peer ID mismatch error")
	var mismatchErr sec.ErrPeerIDMismatch
	require.ErrorAs(t, initErr, &mismatchErr)
	require.Equal(t, mismatchErr.Expected, peer.ID("a-random-peer-id"))
	require.Equal(t, mismatchErr.Actual, respTransport.localID)
}

func TestPeerIDMismatchInboundFailsHandshake(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)
	init, resp := newConnPair(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := initTransport.SecureOutbound(context.Background(), init, respTransport.localID)
		assert.NoError(t, err)
		_, err = conn.Read([]byte{0})
		assert.Error(t, err)
	}()

	_, err := respTransport.SecureInbound(context.Background(), resp, "a-random-peer-id")
	require.Error(t, err, "expected responder to fail with peer ID mismatch error")
	var mismatchErr sec.ErrPeerIDMismatch
	require.ErrorAs(t, err, &mismatchErr)
	require.Equal(t, mismatchErr.Expected, peer.ID("a-random-peer-id"))
	require.Equal(t, mismatchErr.Actual, initTransport.localID)
	<-done
}

func TestPeerIDInboundCheckDisabled(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)
	init, resp := newConnPair(t)

	initSessionTransport, err := initTransport.WithSessionOptions(DisablePeerIDCheck())
	require.NoError(t, err)
	errChan := make(chan error)
	go func() {
		_, err := initSessionTransport.SecureInbound(context.Background(), init, "test")
		errChan <- err
	}()
	_, err = respTransport.SecureOutbound(context.Background(), resp, initTransport.localID)
	require.NoError(t, err)
	initErr := <-errChan
	require.NoError(t, initErr)
}

func TestPeerIDOutboundNoCheck(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)
	init, resp := newConnPair(t)

	initSessionTransport, err := initTransport.WithSessionOptions(DisablePeerIDCheck())
	require.NoError(t, err)

	errChan := make(chan error)
	go func() {
		_, err := initSessionTransport.SecureOutbound(context.Background(), init, "test")
		errChan <- err
	}()

	_, err = respTransport.SecureInbound(context.Background(), resp, "")
	require.NoError(t, err)
	initErr := <-errChan
	require.NoError(t, initErr)
}

func TestLargePayloads(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)

	initConn, respConn := connect(t, initTransport, respTransport)
	defer initConn.Close()
	defer respConn.Close()

	// enough to require a couple Noise messages, with a size that
	// isn't a neat multiple of Noise message size, just in case
	rnd := rand.New(rand.NewSource(1234))
	const size = 100000
	before := make([]byte, size)
	rnd.Read(before)

	if _, err := initConn.Write(before); err != nil {
		t.Fatal(err)
	}

	after := make([]byte, len(before))
	afterLen, err := io.ReadFull(respConn, after)
	if err != nil {
		t.Fatal(err)
	}

	if len(before) != afterLen {
		t.Errorf("expected to read same amount of data as written. written=%d read=%d", len(before), afterLen)
	}
	if !bytes.Equal(before, after) {
		t.Error("Message mismatch.")
	}
}

// Tests XX handshake
func TestHandshakeXX(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)

	initConn, respConn := connect(t, initTransport, respTransport)
	defer initConn.Close()
	defer respConn.Close()

	before := []byte("hello world")
	_, err := initConn.Write(before)
	if err != nil {
		t.Fatal(err)
	}

	after := make([]byte, len(before))
	_, err = respConn.Read(after)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(before, after) {
		t.Errorf("Message mismatch. %v != %v", before, after)
	}
}

func TestBufferEqEncPayload(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)

	initConn, respConn := connect(t, initTransport, respTransport)
	defer initConn.Close()
	defer respConn.Close()

	before := []byte("hello world")
	_, err := initConn.Write(before)
	require.NoError(t, err)

	after := make([]byte, len(before)+chacha20poly1305.Overhead)
	afterLen, err := respConn.Read(after)
	require.NoError(t, err)

	require.Len(t, before, afterLen)
	require.Equal(t, before, after[:len(before)])
}

func TestBufferEqDecryptedPayload(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)

	initConn, respConn := connect(t, initTransport, respTransport)
	defer initConn.Close()
	defer respConn.Close()

	before := []byte("hello world")
	_, err := initConn.Write(before)
	require.NoError(t, err)

	after := make([]byte, len(before)+1)
	afterLen, err := respConn.Read(after)
	require.NoError(t, err)

	require.Len(t, before, afterLen)
	require.Equal(t, before, after[:len(before)])
}

func TestReadUnencryptedFails(t *testing.T) {
	// case1 buffer > len(msg)
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)

	initConn, respConn := connect(t, initTransport, respTransport)
	defer initConn.Close()
	defer respConn.Close()

	before := []byte("hello world")
	msg := make([]byte, len(before)+LengthPrefixLength)
	binary.BigEndian.PutUint16(msg, uint16(len(before)))
	copy(msg[LengthPrefixLength:], before)
	n, err := initConn.insecureConn.Write(msg)
	require.NoError(t, err)
	require.Len(t, msg, n)

	after := make([]byte, len(msg)+1)
	afterLen, err := respConn.Read(after)
	require.Error(t, err)
	require.Equal(t, 0, afterLen)

	// case2: buffer < len(msg)
	initTransport = newTestTransport(t, crypto.Ed25519, 2048)
	respTransport = newTestTransport(t, crypto.Ed25519, 2048)

	initConn, respConn = connect(t, initTransport, respTransport)
	defer initConn.Close()
	defer respConn.Close()

	before = []byte("hello world")
	msg = make([]byte, len(before)+LengthPrefixLength)
	binary.BigEndian.PutUint16(msg, uint16(len(before)))
	copy(msg[LengthPrefixLength:], before)
	n, err = initConn.insecureConn.Write(msg)
	require.NoError(t, err)
	require.Len(t, msg, n)

	after = make([]byte, 1)
	afterLen, err = respConn.Read(after)
	require.Error(t, err)
	require.Equal(t, 0, afterLen)
}

func TestPrologueMatches(t *testing.T) {
	commonPrologue := []byte("test")
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)

	initConn, respConn := newConnPair(t)

	done := make(chan struct{})

	go func() {
		defer close(done)
		tpt, err := initTransport.
			WithSessionOptions(Prologue(commonPrologue))
		require.NoError(t, err)
		conn, err := tpt.SecureOutbound(context.Background(), initConn, respTransport.localID)
		require.NoError(t, err)
		defer conn.Close()
	}()

	tpt, err := respTransport.
		WithSessionOptions(Prologue(commonPrologue))
	require.NoError(t, err)
	conn, err := tpt.SecureInbound(context.Background(), respConn, "")
	require.NoError(t, err)
	defer conn.Close()
	<-done
}

func TestPrologueDoesNotMatchFailsHandshake(t *testing.T) {
	initPrologue, respPrologue := []byte("initPrologue"), []byte("respPrologue")
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)

	initConn, respConn := newConnPair(t)

	done := make(chan struct{})

	go func() {
		defer close(done)
		tpt, err := initTransport.
			WithSessionOptions(Prologue(initPrologue))
		require.NoError(t, err)
		_, err = tpt.SecureOutbound(context.Background(), initConn, respTransport.localID)
		require.Error(t, err)
	}()

	tpt, err := respTransport.WithSessionOptions(Prologue(respPrologue))
	require.NoError(t, err)

	_, err = tpt.SecureInbound(context.Background(), respConn, "")
	require.Error(t, err)
	<-done
}

type earlyDataHandler struct {
	send     func(context.Context, net.Conn, peer.ID) *pb.NoiseExtensions
	received func(context.Context, net.Conn, *pb.NoiseExtensions) error
}

func (e *earlyDataHandler) Send(ctx context.Context, conn net.Conn, id peer.ID) *pb.NoiseExtensions {
	if e.send == nil {
		return nil
	}
	return e.send(ctx, conn, id)
}

func (e *earlyDataHandler) Received(ctx context.Context, conn net.Conn, ext *pb.NoiseExtensions) error {
	if e.received == nil {
		return nil
	}
	return e.received(ctx, conn, ext)
}

func TestEarlyDataAccepted(t *testing.T) {
	handshake := func(t *testing.T, client, server EarlyDataHandler) {
		t.Helper()
		initTransport, err := newTestTransport(t, crypto.Ed25519, 2048).WithSessionOptions(EarlyData(client, nil))
		require.NoError(t, err)
		tpt := newTestTransport(t, crypto.Ed25519, 2048)
		respTransport, err := tpt.WithSessionOptions(EarlyData(nil, server))
		require.NoError(t, err)

		initConn, respConn := newConnPair(t)

		errChan := make(chan error)
		go func() {
			_, err := respTransport.SecureInbound(context.Background(), initConn, "")
			errChan <- err
		}()

		conn, err := initTransport.SecureOutbound(context.Background(), respConn, tpt.localID)
		require.NoError(t, err)
		select {
		case <-time.After(500 * time.Millisecond):
			t.Fatal("timeout")
		case err := <-errChan:
			require.NoError(t, err)
		}
		defer conn.Close()
	}

	var receivedExtensions *pb.NoiseExtensions
	receivingEDH := &earlyDataHandler{
		received: func(_ context.Context, _ net.Conn, ext *pb.NoiseExtensions) error {
			receivedExtensions = ext
			return nil
		},
	}
	sendingEDH := &earlyDataHandler{
		send: func(context.Context, net.Conn, peer.ID) *pb.NoiseExtensions {
			return &pb.NoiseExtensions{WebtransportCerthashes: [][]byte{[]byte("foobar")}}
		},
	}

	t.Run("client sending", func(t *testing.T) {
		handshake(t, sendingEDH, receivingEDH)
		require.Equal(t, [][]byte{[]byte("foobar")}, receivedExtensions.WebtransportCerthashes)
		receivedExtensions = nil
	})

	t.Run("server sending", func(t *testing.T) {
		handshake(t, receivingEDH, sendingEDH)
		require.Equal(t, [][]byte{[]byte("foobar")}, receivedExtensions.WebtransportCerthashes)
		receivedExtensions = nil
	})
}

func TestEarlyDataRejected(t *testing.T) {
	handshake := func(t *testing.T, client, server EarlyDataHandler) (clientErr, serverErr error) {
		initTransport, err := newTestTransport(t, crypto.Ed25519, 2048).WithSessionOptions(EarlyData(client, nil))
		require.NoError(t, err)
		tpt := newTestTransport(t, crypto.Ed25519, 2048)
		respTransport, err := tpt.WithSessionOptions(EarlyData(nil, server))
		require.NoError(t, err)

		initConn, respConn := newConnPair(t)

		errChan := make(chan error)
		go func() {
			_, err := respTransport.SecureInbound(context.Background(), initConn, "")
			errChan <- err
		}()

		// As early data is sent with the last handshake message, the handshake will appear
		// to succeed for the client.
		var conn sec.SecureConn
		conn, clientErr = initTransport.SecureOutbound(context.Background(), respConn, tpt.localID)
		if clientErr == nil {
			_, clientErr = conn.Read([]byte{0})
		}

		select {
		case <-time.After(500 * time.Millisecond):
			t.Fatal("timeout")
		case err := <-errChan:
			serverErr = err
		}
		return
	}

	receivingEDH := &earlyDataHandler{
		received: func(context.Context, net.Conn, *pb.NoiseExtensions) error { return errors.New("nope") },
	}
	sendingEDH := &earlyDataHandler{
		send: func(context.Context, net.Conn, peer.ID) *pb.NoiseExtensions {
			return &pb.NoiseExtensions{WebtransportCerthashes: [][]byte{[]byte("foobar")}}
		},
	}

	t.Run("client sending", func(t *testing.T) {
		clientErr, serverErr := handshake(t, sendingEDH, receivingEDH)
		require.Error(t, clientErr)
		require.EqualError(t, serverErr, "nope")
	})

	t.Run("server sending", func(t *testing.T) {
		clientErr, serverErr := handshake(t, receivingEDH, sendingEDH)
		require.Error(t, serverErr)
		require.EqualError(t, clientErr, "nope")
	})
}

func TestEarlyfffDataAcceptedWithNoHandler(t *testing.T) {
	clientEDH := &earlyDataHandler{
		send: func(ctx context.Context, conn net.Conn, id peer.ID) *pb.NoiseExtensions {
			return &pb.NoiseExtensions{WebtransportCerthashes: [][]byte{[]byte("foobar")}}
		},
	}
	initTransport, err := newTestTransport(t, crypto.Ed25519, 2048).WithSessionOptions(EarlyData(clientEDH, nil))
	require.NoError(t, err)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)

	initConn, respConn := newConnPair(t)

	errChan := make(chan error)
	go func() {
		_, err := respTransport.SecureInbound(context.Background(), initConn, "")
		errChan <- err
	}()

	conn, err := initTransport.SecureOutbound(context.Background(), respConn, respTransport.localID)
	require.NoError(t, err)
	defer conn.Close()

	select {
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout")
	case err := <-errChan:
		require.NoError(t, err)
	}
}

type noiseEarlyDataTestCase struct {
	clientProtos   []protocol.ID
	serverProtos   []protocol.ID
	expectedResult protocol.ID
}

func TestHandshakeWithTransportEarlyData(t *testing.T) {
	tests := []noiseEarlyDataTestCase{
		{
			clientProtos:   nil,
			serverProtos:   nil,
			expectedResult: "",
		},
		{
			clientProtos:   []protocol.ID{"muxer1"},
			serverProtos:   []protocol.ID{"muxer1"},
			expectedResult: "muxer1",
		},
		{
			clientProtos:   []protocol.ID{"muxer1"},
			serverProtos:   []protocol.ID{},
			expectedResult: "",
		},
		{
			clientProtos:   []protocol.ID{},
			serverProtos:   []protocol.ID{"muxer2"},
			expectedResult: "",
		},
		{
			clientProtos:   []protocol.ID{"muxer2"},
			serverProtos:   []protocol.ID{"muxer1"},
			expectedResult: "",
		},
		{
			clientProtos:   []protocol.ID{"muxer1", "muxer2"},
			serverProtos:   []protocol.ID{"muxer2", "muxer1"},
			expectedResult: "muxer1",
		},
		{
			clientProtos:   []protocol.ID{"muxer3", "muxer2", "muxer1"},
			serverProtos:   []protocol.ID{"muxer2", "muxer1"},
			expectedResult: "muxer2",
		},
		{
			clientProtos:   []protocol.ID{"muxer1", "muxer2"},
			serverProtos:   []protocol.ID{"muxer3"},
			expectedResult: "",
		},
	}

	noiseHandshake := func(t *testing.T, initProtos, respProtos []protocol.ID, expectedProto protocol.ID) {
		initTransport := newTestTransportWithMuxers(t, crypto.Ed25519, 2048, initProtos)
		respTransport := newTestTransportWithMuxers(t, crypto.Ed25519, 2048, respProtos)

		initConn, respConn := connect(t, initTransport, respTransport)
		defer initConn.Close()
		defer respConn.Close()

		require.Equal(t, expectedProto, initConn.connectionState.StreamMultiplexer)
		require.Equal(t, expectedProto != "", initConn.connectionState.UsedEarlyMuxerNegotiation)
		require.Equal(t, expectedProto, respConn.connectionState.StreamMultiplexer)
		require.Equal(t, expectedProto != "", respConn.connectionState.UsedEarlyMuxerNegotiation)

		initData := []byte("Test data for noise transport")
		_, err := initConn.Write(initData)
		require.NoError(t, err)

		respData := make([]byte, len(initData))
		_, err = respConn.Read(respData)
		require.NoError(t, err)
		require.Equal(t, initData, respData)
	}

	for _, test := range tests {
		t.Run("Transport EarlyData Test", func(t *testing.T) {
			noiseHandshake(t, test.clientProtos, test.serverProtos, test.expectedResult)
		})
	}
}
