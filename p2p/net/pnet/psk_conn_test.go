package pnet

import (
	"bytes"
	"context"
	"crypto/rand"
	"net"
	"testing"
)

func setupPSKConns(_ context.Context, t *testing.T) (net.Conn, net.Conn) {
	testPSK := make([]byte, 32) // null bytes are as good test key as any other key
	conn1, conn2 := net.Pipe()

	psk1, err := NewProtectedConn(testPSK, conn1)
	if err != nil {
		t.Fatal(err)
	}
	psk2, err := NewProtectedConn(testPSK, conn2)
	if err != nil {
		t.Fatal(err)
	}
	return psk1, psk2
}

func TestPSKSimpelMessges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()

	psk1, psk2 := setupPSKConns(ctx, t)
	msg1 := []byte("hello world")
	out1 := make([]byte, len(msg1))

	wch := make(chan error)
	go func() {
		_, err := psk1.Write(msg1)
		wch <- err
	}()
	n, err := psk2.Read(out1)
	if err != nil {
		t.Fatal(err)
	}

	err = <-wch
	if err != nil {
		t.Fatal(err)
	}

	if n != len(out1) {
		t.Fatalf("expected to read %d bytes, read: %d", len(out1), n)
	}

	if !bytes.Equal(msg1, out1) {
		t.Fatalf("input and output are not the same")
	}
}

func TestPSKFragmentation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()

	psk1, psk2 := setupPSKConns(ctx, t)

	in := make([]byte, 1000)
	if _, err := rand.Read(in); err != nil {
		t.Fatal(err)
	}

	out := make([]byte, 100)

	wch := make(chan error)
	go func() {
		_, err := psk1.Write(in)
		wch <- err
	}()

	for i := 0; i < 10; i++ {
		if _, err := psk2.Read(out); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(in[:100], out) {
			t.Fatalf("input and output are not the same")
		}
		in = in[100:]
	}

	if err := <-wch; err != nil {
		t.Fatal(err)
	}
}
