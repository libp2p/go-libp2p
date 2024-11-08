package memory

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamSimpleReadWriteClose(t *testing.T) {
	//client, server := getDetachedDataChannels(t)
	ra, wb := io.Pipe()
	rb, wa := io.Pipe()

	clientStr := newStream(0, ra, wa)
	serverStr := newStream(1, rb, wb)

	// send a foobar from the client
	n, err := clientStr.Write([]byte("foobar"))
	require.NoError(t, err)
	require.Equal(t, 6, n)
	require.NoError(t, clientStr.CloseWrite())
	// writing after closing should error
	_, err = clientStr.Write([]byte("foobar"))
	require.Error(t, err)
	//require.False(t, clientDone.Load())

	// now read all the data on the server side
	b, err := io.ReadAll(serverStr)
	require.NoError(t, err)
	require.Equal(t, []byte("foobar"), b)
	// reading again should give another io.EOF
	n, err = serverStr.Read(make([]byte, 10))
	require.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)
	//require.False(t, serverDone.Load())

	// send something back
	_, err = serverStr.Write([]byte("lorem ipsum"))
	require.NoError(t, err)
	require.NoError(t, serverStr.CloseWrite())

	// and read it at the client
	//require.False(t, clientDone.Load())
	b, err = io.ReadAll(clientStr)
	require.NoError(t, err)
	require.Equal(t, []byte("lorem ipsum"), b)

	// stream is only cleaned up on calling Close or Reset
	clientStr.Close()
	serverStr.Close()
	//require.Eventually(t, func() bool { return clientDone.Load() }, 5*time.Second, 100*time.Millisecond)
	// Need to call Close for cleanup. Otherwise the FIN_ACK is never read
	require.NoError(t, serverStr.Close())
	//require.Eventually(t, func() bool { return serverDone.Load() }, 5*time.Second, 100*time.Millisecond)
}
