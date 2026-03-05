package noise

import (
	"context"
	"io"
	"math/rand"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/sec"
)

type testMode int

const (
	readBufferGtEncMsg testMode = iota
	readBufferLtPlainText
)

var bcs = map[string]struct {
	m testMode
}{
	"readBuffer > encrypted message": {
		readBufferGtEncMsg,
	},
	"readBuffer < decrypted plaintext": {
		readBufferLtPlainText,
	},
}

func makeTransport(b *testing.B) *Transport {
	b.Helper()

	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	if err != nil {
		b.Fatal(err)
	}
	tpt, err := New(ID, priv, nil)
	if err != nil {
		b.Fatalf("error constructing transport: %v", err)
	}
	return tpt
}

type benchenv struct {
	*testing.B

	initTpt *Transport
	respTpt *Transport
	rndSrc  rand.Source
}

func setupEnv(b *testing.B) *benchenv {
	b.StopTimer()
	defer b.StartTimer()
	initTpt := makeTransport(b)
	respTpt := makeTransport(b)

	return &benchenv{
		B:       b,
		initTpt: initTpt,
		respTpt: respTpt,
		rndSrc:  rand.NewSource(42),
	}
}

func (b benchenv) connect(stopTimer bool) (*secureSession, *secureSession) {
	initConn, respConn := net.Pipe()

	if stopTimer {
		b.StopTimer()
		defer b.StartTimer()
	}

	var initSession sec.SecureConn
	var initErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		initSession, initErr = b.initTpt.SecureOutbound(context.TODO(), initConn, b.respTpt.localID)
	}()

	respSession, respErr := b.respTpt.SecureInbound(context.TODO(), respConn, "")
	<-done

	if initErr != nil {
		b.Fatal(initErr)
	}

	if respErr != nil {
		b.Fatal(respErr)
	}

	return initSession.(*secureSession), respSession.(*secureSession)
}

func drain(r io.Reader, done chan<- error, writeTo io.Writer) {
	_, err := io.Copy(writeTo, r)
	done <- err
}

type discardWithBuffer struct {
	buf []byte
	io.Writer
}

func (d *discardWithBuffer) ReadFrom(r io.Reader) (n int64, err error) {
	readSize := 0
	for {
		readSize, err = r.Read(d.buf)
		n += int64(readSize)
		if err != nil {
			if err == io.EOF {
				return n, nil
			}
			return
		}
	}
}

func sink(dst io.WriteCloser, src io.Reader, done chan<- error, buf []byte) {
	_, err := io.CopyBuffer(dst, src, buf)
	if err != nil {
		done <- err
	}
	done <- dst.Close()
}

func pipeRandom(src rand.Source, w io.WriteCloser, r io.Reader, n int64, plainTextBuf []byte,
	writeTo io.Writer) error {
	rnd := rand.New(src)
	lr := io.LimitReader(rnd, n)

	writeCh := make(chan error, 1)
	readCh := make(chan error, 1)

	go sink(w, lr, writeCh, plainTextBuf)
	go drain(r, readCh, writeTo)

	writeDone := false
	readDone := false
	for !(readDone && writeDone) {
		select {
		case err := <-readCh:
			if err != nil && err != io.EOF {
				return err
			}
			readDone = true
		case err := <-writeCh:
			if err != nil && err != io.EOF {
				return err
			}
			writeDone = true
		}
	}

	return nil
}

func benchDataTransfer(b *benchenv, dataSize int64, m testMode) {
	var totalBytes int64
	var totalTime time.Duration

	plainTextBufs := make([][]byte, 61)
	writeTos := make(map[int]io.Writer)
	for i := range plainTextBufs {
		var rbuf []byte
		// plaintext will be 2 KB to 62 KB
		plainTextBufs[i] = make([]byte, (i+2)*1024)
		switch m {
		case readBufferGtEncMsg:
			rbuf = make([]byte, len(plainTextBufs[i])+chacha20poly1305.Overhead+1)
		case readBufferLtPlainText:
			rbuf = make([]byte, len(plainTextBufs[i])-2)
		}
		writeTos[i] = &discardWithBuffer{rbuf, io.Discard}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		initSession, respSession := b.connect(true)

		start := time.Now()

		bufi := i % len(plainTextBufs)
		err := pipeRandom(b.rndSrc, initSession, respSession, dataSize, plainTextBufs[bufi], writeTos[bufi])
		if err != nil {
			b.Fatalf("error sending random data: %s", err)
		}
		elapsed := time.Since(start)
		totalTime += elapsed
		totalBytes += dataSize
	}
	bytesPerSec := float64(totalBytes) / totalTime.Seconds()
	b.ReportMetric(bytesPerSec, "bytes/sec")
}

func BenchmarkTransfer1MB(b *testing.B) {
	for n, bc := range bcs {
		b.Run(n, func(b *testing.B) {
			benchDataTransfer(setupEnv(b), 1024*1024, bc.m)
		})
	}

}

func BenchmarkTransfer100MB(b *testing.B) {
	for n, bc := range bcs {
		b.Run(n, func(b *testing.B) {
			benchDataTransfer(setupEnv(b), 1024*1024*100, bc.m)
		})
	}
}

func BenchmarkTransfer500Mb(b *testing.B) {
	for n, bc := range bcs {
		b.Run(n, func(b *testing.B) {
			benchDataTransfer(setupEnv(b), 1024*1024*500, bc.m)
		})
	}
}

func (b benchenv) benchHandshake() {
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		i, r := b.connect(false)
		b.StopTimer()
		err := i.Close()
		if err != nil {
			b.Errorf("error closing session: %s", err)
		}
		err = r.Close()
		if err != nil {
			b.Errorf("error closing session: %s", err)
		}
		b.StartTimer()
	}
}

func BenchmarkHandshakeXX(b *testing.B) {
	env := setupEnv(b)
	env.benchHandshake()
}
