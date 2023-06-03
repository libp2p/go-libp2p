package test

import (
	"context"
	"sort"
	"testing"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	pstore "github.com/libp2p/go-libp2p/core/peerstore"
	pt "github.com/libp2p/go-libp2p/core/test"

	"github.com/stretchr/testify/require"
)

var keyBookSuite = map[string]func(kb pstore.KeyBook) func(*testing.T){
	"AddGetPrivKey":         testKeybookPrivKey,
	"AddGetPubKey":          testKeyBookPubKey,
	"PeersWithKeys":         testKeyBookPeers,
	"PubKeyAddedOnRetrieve": testInlinedPubKeyAddedOnRetrieve,
	"Delete":                testKeyBookDelete,
}

type KeyBookFactory func() (pstore.KeyBook, func())

func TestKeyBook(t *testing.T, factory KeyBookFactory) {
	for name, test := range keyBookSuite {
		// Create a new peerstore.
		kb, closeFunc := factory()

		// Run the test.
		t.Run(name, test(kb))

		// Cleanup.
		if closeFunc != nil {
			closeFunc()
		}
	}
}

func testKeybookPrivKey(kb pstore.KeyBook) func(t *testing.T) {
	return func(t *testing.T) {
		if peers := kb.PeersWithKeys(context.Background()); len(peers) > 0 {
			t.Error("expected peers to be empty on init")
		}

		priv, _, err := pt.RandTestKeyPair(ic.RSA, 2048)
		if err != nil {
			t.Fatal(err)
		}

		id, err := peer.IDFromPrivateKey(priv)
		if err != nil {
			t.Error(err)
		}

		if res := kb.PrivKey(context.Background(), id); res != nil {
			t.Error("retrieving private key should have failed")
		}

		err = kb.AddPrivKey(context.Background(), id, priv)
		if err != nil {
			t.Error(err)
		}

		if res := kb.PrivKey(context.Background(), id); !priv.Equals(res) {
			t.Error("retrieved private key did not match stored private key")
		}

		if peers := kb.PeersWithKeys(context.Background()); len(peers) != 1 || peers[0] != id {
			t.Error("list of peers did not include test peer")
		}
	}
}

func testKeyBookPubKey(kb pstore.KeyBook) func(t *testing.T) {
	return func(t *testing.T) {
		if peers := kb.PeersWithKeys(context.Background()); len(peers) > 0 {
			t.Error("expected peers to be empty on init")
		}

		_, pub, err := pt.RandTestKeyPair(ic.RSA, 2048)
		if err != nil {
			t.Fatal(err)
		}

		id, err := peer.IDFromPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}

		if res := kb.PubKey(context.Background(), id); res != nil {
			t.Error("retrieving public key should have failed")
		}

		err = kb.AddPubKey(context.Background(), id, pub)
		if err != nil {
			t.Error(err)
		}

		if res := kb.PubKey(context.Background(), id); !pub.Equals(res) {
			t.Error("retrieved public key did not match stored public key")
		}

		if peers := kb.PeersWithKeys(context.Background()); len(peers) != 1 || peers[0] != id {
			t.Error("list of peers did not include test peer")
		}
	}
}

func testKeyBookPeers(kb pstore.KeyBook) func(t *testing.T) {
	return func(t *testing.T) {
		if peers := kb.PeersWithKeys(context.Background()); len(peers) > 0 {
			t.Error("expected peers to be empty on init")
		}

		var peers peer.IDSlice
		for i := 0; i < 10; i++ {
			// Add a public key.
			_, pub, err := pt.RandTestKeyPair(ic.RSA, 2048)
			if err != nil {
				t.Fatal(err)
			}
			p1, err := peer.IDFromPublicKey(pub)
			if err != nil {
				t.Fatal(err)
			}
			kb.AddPubKey(context.Background(), p1, pub)

			// Add a private key.
			priv, _, err := pt.RandTestKeyPair(ic.RSA, 2048)
			if err != nil {
				t.Fatal(err)
			}
			p2, err := peer.IDFromPrivateKey(priv)
			if err != nil {
				t.Fatal(err)
			}
			kb.AddPrivKey(context.Background(), p2, priv)

			peers = append(peers, []peer.ID{p1, p2}...)
		}

		kbPeers := kb.PeersWithKeys(context.Background())
		sort.Sort(kbPeers)
		sort.Sort(peers)

		for i, p := range kbPeers {
			if p != peers[i] {
				t.Errorf("mismatch of peer at index %d", i)
			}
		}
	}
}

func testInlinedPubKeyAddedOnRetrieve(kb pstore.KeyBook) func(t *testing.T) {
	return func(t *testing.T) {
		t.Skip("key inlining disabled for now: see libp2p/specs#111")

		if peers := kb.PeersWithKeys(context.Background()); len(peers) > 0 {
			t.Error("expected peers to be empty on init")
		}

		// Key small enough for inlining.
		_, pub, err := ic.GenerateKeyPair(ic.Ed25519, 256)
		if err != nil {
			t.Error(err)
		}

		id, err := peer.IDFromPublicKey(pub)
		if err != nil {
			t.Error(err)
		}

		pubKey := kb.PubKey(context.Background(), id)
		if !pubKey.Equals(pub) {
			t.Error("mismatch between original public key and keybook-calculated one")
		}
	}
}

func testKeyBookDelete(kb pstore.KeyBook) func(t *testing.T) {
	return func(t *testing.T) {
		// don't use an ed25519 key here, otherwise the key book might try to derive the pubkey from the peer ID
		priv, pub, err := ic.GenerateKeyPair(ic.RSA, 2048)
		require.NoError(t, err)
		p, err := peer.IDFromPublicKey(pub)
		require.NoError(t, err)
		require.NoError(t, kb.AddPubKey(context.Background(), p, pub))
		require.NoError(t, kb.AddPrivKey(context.Background(), p, priv))
		require.NotNil(t, kb.PrivKey(context.Background(), p))
		require.NotNil(t, kb.PubKey(context.Background(), p))
		kb.RemovePeer(context.Background(), p)
		require.Nil(t, kb.PrivKey(context.Background(), p))
		require.Nil(t, kb.PubKey(context.Background(), p))
	}
}

var keybookBenchmarkSuite = map[string]func(kb pstore.KeyBook) func(*testing.B){
	"PubKey":        benchmarkPubKey,
	"AddPubKey":     benchmarkAddPubKey,
	"PrivKey":       benchmarkPrivKey,
	"AddPrivKey":    benchmarkAddPrivKey,
	"PeersWithKeys": benchmarkPeersWithKeys,
}

func BenchmarkKeyBook(b *testing.B, factory KeyBookFactory) {
	ordernames := make([]string, 0, len(keybookBenchmarkSuite))
	for name := range keybookBenchmarkSuite {
		ordernames = append(ordernames, name)
	}
	sort.Strings(ordernames)
	for _, name := range ordernames {
		bench := keybookBenchmarkSuite[name]
		kb, closeFunc := factory()

		b.Run(name, bench(kb))

		if closeFunc != nil {
			closeFunc()
		}
	}
}

func benchmarkPubKey(kb pstore.KeyBook) func(*testing.B) {
	return func(b *testing.B) {
		_, pub, err := pt.RandTestKeyPair(ic.RSA, 2048)
		if err != nil {
			b.Fatal(err)
		}

		id, err := peer.IDFromPublicKey(pub)
		if err != nil {
			b.Fatal(err)
		}

		err = kb.AddPubKey(context.Background(), id, pub)
		if err != nil {
			b.Fatal(err)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			kb.PubKey(context.Background(), id)
		}
	}
}

func benchmarkAddPubKey(kb pstore.KeyBook) func(*testing.B) {
	return func(b *testing.B) {
		_, pub, err := pt.RandTestKeyPair(ic.RSA, 2048)
		if err != nil {
			b.Fatal(err)
		}

		id, err := peer.IDFromPublicKey(pub)
		if err != nil {
			b.Fatal(err)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			kb.AddPubKey(context.Background(), id, pub)
		}
	}
}

func benchmarkPrivKey(kb pstore.KeyBook) func(*testing.B) {
	return func(b *testing.B) {
		priv, _, err := pt.RandTestKeyPair(ic.RSA, 2048)
		if err != nil {
			b.Fatal(err)
		}

		id, err := peer.IDFromPrivateKey(priv)
		if err != nil {
			b.Fatal(err)
		}

		err = kb.AddPrivKey(context.Background(), id, priv)
		if err != nil {
			b.Fatal(err)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			kb.PrivKey(context.Background(), id)
		}
	}
}

func benchmarkAddPrivKey(kb pstore.KeyBook) func(*testing.B) {
	return func(b *testing.B) {
		priv, _, err := pt.RandTestKeyPair(ic.RSA, 2048)
		if err != nil {
			b.Fatal(err)
		}

		id, err := peer.IDFromPrivateKey(priv)
		if err != nil {
			b.Fatal(err)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			kb.AddPrivKey(context.Background(), id, priv)
		}
	}
}

func benchmarkPeersWithKeys(kb pstore.KeyBook) func(*testing.B) {
	return func(b *testing.B) {
		for i := 0; i < 10; i++ {
			priv, pub, err := pt.RandTestKeyPair(ic.RSA, 2048)
			if err != nil {
				b.Fatal(err)
			}

			id, err := peer.IDFromPublicKey(pub)
			if err != nil {
				b.Fatal(err)
			}

			err = kb.AddPubKey(context.Background(), id, pub)
			if err != nil {
				b.Fatal(err)
			}
			err = kb.AddPrivKey(context.Background(), id, priv)
			if err != nil {
				b.Fatal(err)
			}
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			kb.PeersWithKeys(context.Background())
		}
	}
}
