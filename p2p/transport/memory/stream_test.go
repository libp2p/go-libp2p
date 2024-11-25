package memory

import (
	"github.com/stretchr/testify/require"
	"io"
	"testing"
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
