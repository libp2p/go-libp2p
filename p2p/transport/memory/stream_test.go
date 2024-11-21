package memory

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamSimpleReadWriteClose(t *testing.T) {
	t.Parallel()
	clientStr, serverStr := newStreamPair()

	// send a foobar from the client
	n, err := clientStr.Write([]byte("foobar"))
	require.NoError(t, err)
	require.Equal(t, 6, n)
	require.NoError(t, clientStr.CloseWrite())

	// writing after closing should error
	_, err = clientStr.Write([]byte("foobar"))
	require.Error(t, err)

	// now read all the data on the server side
	b, err := io.ReadAll(serverStr)
	require.NoError(t, err)
	require.Equal(t, []byte("foobar"), b)

	// reading again should give another io.EOF
	n, err = serverStr.Read(make([]byte, 10))
	require.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)

	// send something back
	_, err = serverStr.Write([]byte("lorem ipsum"))
	require.NoError(t, err)
	require.NoError(t, serverStr.CloseWrite())

	// and read it at the client
	b, err = io.ReadAll(clientStr)
	require.NoError(t, err)
	require.Equal(t, []byte("lorem ipsum"), b)

	// stream is only cleaned up on calling Close or Reset
	clientStr.Close()
	serverStr.Close()
	// Need to call Close for cleanup. Otherwise the FIN_ACK is never read
	require.NoError(t, serverStr.Close())
}

func TestStreamPartialReads(t *testing.T) {
	t.Parallel()
	clientStr, serverStr := newStreamPair()

	_, err := serverStr.Write([]byte("foobar"))
	require.NoError(t, err)
	require.NoError(t, serverStr.CloseWrite())

	n, err := clientStr.Read([]byte{}) // empty read
	require.NoError(t, err)
	require.Zero(t, n)
	b := make([]byte, 3)
	n, err = clientStr.Read(b)
	require.Equal(t, 3, n)
	require.NoError(t, err)
	require.Equal(t, []byte("foo"), b)
	b, err = io.ReadAll(clientStr)
	require.NoError(t, err)
	require.Equal(t, []byte("bar"), b)
}
