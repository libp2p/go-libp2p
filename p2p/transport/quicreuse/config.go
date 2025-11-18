package quicreuse

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlogwriter"
)

var quicConfig = &quic.Config{
	MaxIncomingStreams:         256,
	MaxIncomingUniStreams:      5,              // allow some unidirectional streams, in case we speak WebTransport
	MaxStreamReceiveWindow:     10 * (1 << 20), // 10 MB
	MaxConnectionReceiveWindow: 15 * (1 << 20), // 15 MB
	KeepAlivePeriod:            15 * time.Second,
	Versions:                   []quic.Version{quic.Version1},
	// We don't use datagrams (yet), but this is necessary for WebTransport
	EnableDatagrams: true,
}

func defaultConnectionTracerWithSchemas(addr string, isClient bool, connID quic.ConnectionID, eventSchemas []string) qlogwriter.Trace {
	qlogDir := os.Getenv("QLOGDIR")
	if qlogDir == "" {
		return nil
	}
	if _, err := os.Stat(qlogDir); os.IsNotExist(err) {
		if err := os.MkdirAll(qlogDir, 0o755); err != nil {
			log.Error("failed to create qlog dir", "dir", qlogDir, "err", err)
		}
	}
	label := "server"
	if isClient {
		label = "client"
	}
	path := fmt.Sprintf("%s/%s_%s_%s.sqlog", strings.TrimRight(qlogDir, "/"), addr, connID, label)
	f, err := os.Create(path)
	if err != nil {
		log.Error("Failed to create qlog file", "path", path, "err", err.Error())
	}
	fileSeq := qlogwriter.NewConnectionFileSeq(
		newBufferedWriteCloser(bufio.NewWriter(f), f),
		isClient,
		connID,
		eventSchemas,
	)
	go fileSeq.Run()
	return fileSeq
}

type bufferedWriteCloser struct {
	*bufio.Writer
	io.Closer
}

// NewBufferedWriteCloser creates an io.WriteCloser from a bufio.Writer and an io.Closer
func newBufferedWriteCloser(writer *bufio.Writer, closer io.Closer) io.WriteCloser {
	return &bufferedWriteCloser{
		Writer: writer,
		Closer: closer,
	}
}

func (h bufferedWriteCloser) Close() error {
	if err := h.Flush(); err != nil {
		return err
	}
	return h.Closer.Close()
}
