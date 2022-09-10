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
	"github.com/libp2p/go-libp2p/core/sec"

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

	sk := respConn.LocalPrivateKey()
	pk := sk.GetPublic()

	if !sk.Equals(respTransport.privateKey) {
		t.Error("Private key Mismatch.")
	}

	if !pk.Equals(initConn.RemotePublicKey()) {
		t.Errorf("Public key mismatch. expected %x got %x", pk, initConn.RemotePublicKey())
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
		assert.Equal(t, conn.RemotePeer(), respTransport.localID)
		b := make([]byte, 6)
		_, err = conn.Read(b)
		assert.NoError(t, err)
		assert.Equal(t, b, []byte("foobar"))
	}()

	conn, err := respTransport.SecureInbound(context.Background(), resp, initTransport.localID)
	require.NoError(t, err)
	require.Equal(t, conn.RemotePeer(), initTransport.localID)
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
	require.Contains(t, initErr.Error(), "but remote key matches")
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
	<-done
}

func makeLargePlaintext(size int) []byte {
	buf := make([]byte, size)
	rand.Read(buf)
	return buf
}

func TestLargePayloads(t *testing.T) {
	initTransport := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport := newTestTransport(t, crypto.Ed25519, 2048)

	initConn, respConn := connect(t, initTransport, respTransport)
	defer initConn.Close()
	defer respConn.Close()

	// enough to require a couple Noise messages, with a size that
	// isn't a neat multiple of Noise message size, just in case
	size := 100000

	before := makeLargePlaintext(size)
	_, err := initConn.Write(before)
	if err != nil {
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

	require.Equal(t, len(before), afterLen)
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

	require.Equal(t, len(before), afterLen)
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
	require.Equal(t, len(msg), n)

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
	require.Equal(t, len(msg), n)

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
	send     func(context.Context, net.Conn, peer.ID) []byte
	received func(context.Context, net.Conn, []byte) error
}

func (e *earlyDataHandler) Send(ctx context.Context, conn net.Conn, id peer.ID) []byte {
	if e.send == nil {
		return nil
	}
	return e.send(ctx, conn, id)
}

func (e *earlyDataHandler) Received(ctx context.Context, conn net.Conn, data []byte) error {
	if e.received == nil {
		return nil
	}
	return e.received(ctx, conn, data)
}

func TestEarlyDataAccepted(t *testing.T) {
	var receivedEarlyData []byte
	serverEDH := &earlyDataHandler{
		received: func(_ context.Context, _ net.Conn, data []byte) error {
			receivedEarlyData = data
			return nil
		},
	}
	clientEDH := &earlyDataHandler{
		send: func(ctx context.Context, conn net.Conn, id peer.ID) []byte { return []byte("foobar") },
	}
	initTransport, err := newTestTransport(t, crypto.Ed25519, 2048).WithSessionOptions(EarlyData(clientEDH))
	require.NoError(t, err)
	tpt := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport, err := tpt.WithSessionOptions(EarlyData(serverEDH))
	require.NoError(t, err)

	initConn, respConn := newConnPair(t)

	errChan := make(chan error)
	go func() {
		_, err := respTransport.SecureInbound(context.Background(), initConn, "")
		errChan <- err
	}()

	conn, err := initTransport.SecureOutbound(context.Background(), respConn, tpt.localID)
	require.NoError(t, err)
	defer conn.Close()

	require.Equal(t, []byte("foobar"), receivedEarlyData)
}

func TestEarlyDataRejected(t *testing.T) {
	serverEDH := &earlyDataHandler{
		received: func(_ context.Context, _ net.Conn, data []byte) error { return errors.New("nope") },
	}
	clientEDH := &earlyDataHandler{
		send: func(ctx context.Context, conn net.Conn, id peer.ID) []byte { return []byte("foobar") },
	}
	initTransport, err := newTestTransport(t, crypto.Ed25519, 2048).WithSessionOptions(EarlyData(clientEDH))
	require.NoError(t, err)
	tpt := newTestTransport(t, crypto.Ed25519, 2048)
	respTransport, err := tpt.WithSessionOptions(EarlyData(serverEDH))
	require.NoError(t, err)

	initConn, respConn := newConnPair(t)

	errChan := make(chan error)
	go func() {
		_, err := respTransport.SecureInbound(context.Background(), initConn, "")
		errChan <- err
	}()

	_, err = initTransport.SecureOutbound(context.Background(), respConn, tpt.localID)
	require.Error(t, err)

	select {
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout")
	case err := <-errChan:
		require.EqualError(t, err, "nope")
	}
}

func TestEarlyDataAcceptedWithNoHandler(t *testing.T) {
	clientEDH := &earlyDataHandler{
		send: func(ctx context.Context, conn net.Conn, id peer.ID) []byte { return []byte("foobar") },
	}
	initTransport, err := newTestTransport(t, crypto.Ed25519, 2048).WithSessionOptions(EarlyData(clientEDH))
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
