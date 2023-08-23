package quicreuse

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/logging"
	"github.com/stretchr/testify/require"
)

func createLogDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "libp2p-quic-transport-test")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func getFile(t *testing.T, dir string) os.FileInfo {
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	info, err := files[0].Info()
	require.NoError(t, err)
	return info
}

func TestSaveQlog(t *testing.T) {
	qlogDir := createLogDir(t)
	logger := newQlogger(qlogDir, logging.PerspectiveServer, quic.ConnectionIDFromBytes([]byte{0xde, 0xad, 0xbe, 0xef}))
	file := getFile(t, qlogDir)
	require.Equal(t, string(file.Name()[0]), ".")
	require.Truef(t, strings.HasSuffix(file.Name(), ".qlog.swp"), "expected %s to have the .qlog.swp file ending", file.Name())
	// close the logger. This should move the file.
	require.NoError(t, logger.Close())
	file = getFile(t, qlogDir)
	require.NotEqual(t, string(file.Name()[0]), ".")
	require.Truef(t, strings.HasSuffix(file.Name(), ".qlog.zst"), "expected %s to have the .qlog.zst file ending", file.Name())
	require.Contains(t, file.Name(), "server")
	require.Contains(t, file.Name(), "deadbeef")
}

func TestQlogBuffering(t *testing.T) {
	qlogDir := createLogDir(t)
	logger := newQlogger(qlogDir, logging.PerspectiveServer, quic.ConnectionIDFromBytes([]byte("connid")))
	initialSize := getFile(t, qlogDir).Size()
	// Do a small write.
	// Since the writter is buffered, this should not be written to disk yet.
	logger.Write([]byte("foobar"))
	require.Equal(t, getFile(t, qlogDir).Size(), initialSize)
	// Close the logger. This should flush the buffer to disk.
	require.NoError(t, logger.Close())
	finalSize := getFile(t, qlogDir).Size()
	t.Logf("initial log file size: %d, final log file size: %d\n", initialSize, finalSize)
	require.Greater(t, finalSize, initialSize)
}

func TestQlogCompression(t *testing.T) {
	qlogDir := createLogDir(t)
	logger := newQlogger(qlogDir, logging.PerspectiveServer, quic.ConnectionIDFromBytes([]byte("connid")))
	logger.Write([]byte("foobar"))
	require.NoError(t, logger.Close())
	compressed, err := os.ReadFile(qlogDir + "/" + getFile(t, qlogDir).Name())
	require.NoError(t, err)
	require.NotEqual(t, compressed, "foobar")
	c, err := zstd.NewReader(bytes.NewReader(compressed))
	require.NoError(t, err)
	data, err := io.ReadAll(c)
	require.NoError(t, err)
	require.Equal(t, data, []byte("foobar"))
}
