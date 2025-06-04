package relay

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"

	ma "github.com/multiformats/go-multiaddr"
)

func genKeyAndID(t *testing.T) (crypto.PrivKey, peer.ID) {
	t.Helper()
	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(key)
	require.NoError(t, err)
	return key, id
}

// TestMakeReservationWithP2PAddrs ensures that our reservation message builder
// sanitizes the input addresses
func TestMakeReservationWithP2PAddrs(t *testing.T) {
	selfKey, selfID := genKeyAndID(t)
	_, otherID := genKeyAndID(t)
	_, reserverID := genKeyAndID(t)

	addrs := []ma.Multiaddr{
		ma.StringCast("/ip4/1.2.3.4/tcp/1234"),                         // No p2p part
		ma.StringCast("/ip4/1.2.3.4/tcp/1235/p2p/" + selfID.String()),  // Already has p2p part
		ma.StringCast("/ip4/1.2.3.4/tcp/1236/p2p/" + otherID.String()), // Some other peer (?? Not expected, but we could get anything in this func)
	}

	rsvp := makeReservationMsg(selfKey, selfID, addrs, reserverID, time.Now().Add(time.Minute))
	require.NotNil(t, rsvp)

	expectedAddrs := []string{
		"/ip4/1.2.3.4/tcp/1234/p2p/" + selfID.String(),
		"/ip4/1.2.3.4/tcp/1235/p2p/" + selfID.String(),
	}

	addrsFromRsvp := make([]string, 0, len(rsvp.GetAddrs()))
	for _, addr := range rsvp.GetAddrs() {
		a, err := ma.NewMultiaddrBytes(addr)
		require.NoError(t, err)
		addrsFromRsvp = append(addrsFromRsvp, a.String())
	}

	require.Equal(t, expectedAddrs, addrsFromRsvp)
}
