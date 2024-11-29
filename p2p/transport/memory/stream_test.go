package memory

import (
	"errors"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/stretchr/testify/require"
	"io"
	"testing"
	"time"
)

func TestStreamSimpleReadWriteClose(t *testing.T) {
	t.Parallel()
	streamLocal, streamRemote := newStreamPair()

	// send a foobar from the client
	n, err := streamLocal.Write([]byte("foobar"))
	require.NoError(t, err)
	require.Equal(t, 6, n)
	require.NoError(t, streamLocal.CloseWrite())

	// writing after closing should error
	_, err = streamLocal.Write([]byte("foobar"))
	require.Error(t, err)

	// now read all the data on the server side
	b, err := io.ReadAll(streamRemote)
	require.NoError(t, err)
	require.Equal(t, []byte("foobar"), b)

	// reading again should give another io.EOF
	n, err = streamRemote.Read(make([]byte, 10))
	require.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)

	// send something back
	_, err = streamRemote.Write([]byte("lorem ipsum"))
	require.NoError(t, err)
	require.NoError(t, streamRemote.CloseWrite())

	// and read it at the client
	b, err = io.ReadAll(streamLocal)
	require.NoError(t, err)
	require.Equal(t, []byte("lorem ipsum"), b)

	// stream is only cleaned up on calling Close or Reset
	require.NoError(t, streamLocal.Close())
	require.NoError(t, streamRemote.Close())
}

func TestStreamPartialReads(t *testing.T) {
	t.Parallel()
	streamLocal, streamRemote := newStreamPair()

	_, err := streamRemote.Write([]byte("foobar"))
	require.NoError(t, err)
	require.NoError(t, streamRemote.CloseWrite())

	n, err := streamLocal.Read([]byte{}) // empty read
	require.NoError(t, err)
	require.Zero(t, n)
	b := make([]byte, 3)
	n, err = streamLocal.Read(b)
	require.Equal(t, 3, n)
	require.NoError(t, err)
	require.Equal(t, []byte("foo"), b)
	b, err = io.ReadAll(streamLocal)
	require.NoError(t, err)
	require.Equal(t, []byte("bar"), b)
}

func TestStreamResets(t *testing.T) {
	clientStr, serverStr := newStreamPair()

	// send a foobar from the client
	_, err := clientStr.Write([]byte("foobar"))
	require.NoError(t, err)
	_, err = serverStr.Write([]byte("lorem ipsum"))
	require.NoError(t, err)
	require.NoError(t, clientStr.Reset()) // resetting resets both directions
	// attempting to write more data should result in a reset error
	_, err = clientStr.Write([]byte("foobar"))
	require.ErrorIs(t, err, network.ErrReset)
	// read what the server sent
	b, err := io.ReadAll(clientStr)
	require.Empty(t, b)
	require.ErrorIs(t, err, network.ErrReset)

	// read the data on the server side
	b, err = io.ReadAll(serverStr)
	require.Equal(t, []byte("foobar"), b)
	require.ErrorIs(t, err, network.ErrReset)
	require.Eventually(t, func() bool {
		_, err := serverStr.Write([]byte("foobar"))
		return errors.Is(err, network.ErrReset)
	}, time.Second, 50*time.Millisecond)
	serverStr.Close()
}

func TestStreamReadAfterClose(t *testing.T) {
	clientStr, serverStr := newStreamPair()

	serverStr.Close()
	b := make([]byte, 1)
	_, err := clientStr.Read(b)
	require.Equal(t, io.EOF, err)
	_, err = clientStr.Read(nil)
	require.Equal(t, io.EOF, err)

	clientStr, serverStr = newStreamPair()

	serverStr.Reset()
	b = make([]byte, 1)
	_, err = clientStr.Read(b)
	require.ErrorIs(t, err, network.ErrReset)
	_, err = clientStr.Read(nil)
	require.ErrorIs(t, err, network.ErrReset)
}
