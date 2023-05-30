package test

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	pstore "github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/record"
	"github.com/libp2p/go-libp2p/core/test"

	mockClock "github.com/benbjohnson/clock"
	"github.com/multiformats/go-multiaddr"
)

var addressBookSuite = map[string]func(book pstore.AddrBook, clk *mockClock.Mock) func(*testing.T){
	"AddAddress":           testAddAddress,
	"Clear":                testClearWorks,
	"SetNegativeTTLClears": testSetNegativeTTLClears,
	"UpdateTTLs":           testUpdateTTLs,
	"NilAddrsDontBreak":    testNilAddrsDontBreak,
	"AddressesExpire":      testAddressesExpire,
	"ClearWithIter":        testClearWithIterator,
	"PeersWithAddresses":   testPeersWithAddrs,
	"CertifiedAddresses":   testCertifiedAddresses,
}

type AddrBookFactory func() (pstore.AddrBook, func())

func TestAddrBook(t *testing.T, factory AddrBookFactory, clk *mockClock.Mock) {
	for name, test := range addressBookSuite {
		// Create a new peerstore.
		ab, closeFunc := factory()

		// Run the test.
		t.Run(name, test(ab, clk))

		// Cleanup.
		if closeFunc != nil {
			closeFunc()
		}
	}
}

func testAddAddress(ab pstore.AddrBook, clk *mockClock.Mock) func(*testing.T) {
	return func(t *testing.T) {
		t.Run("add a single address", func(t *testing.T) {
			id := GeneratePeerIDs(1)[0]
			addrs := GenerateAddrs(1)

			ab.AddAddr(context.Background(), id, addrs[0], time.Hour)

			AssertAddressesEqual(t, addrs, ab.Addrs(context.Background(), id))
		})

		t.Run("idempotent add single address", func(t *testing.T) {
			id := GeneratePeerIDs(1)[0]
			addrs := GenerateAddrs(1)

			ab.AddAddr(context.Background(), id, addrs[0], time.Hour)
			ab.AddAddr(context.Background(), id, addrs[0], time.Hour)

			AssertAddressesEqual(t, addrs, ab.Addrs(context.Background(), id))
		})

		t.Run("add multiple addresses", func(t *testing.T) {
			id := GeneratePeerIDs(1)[0]
			addrs := GenerateAddrs(3)

			ab.AddAddrs(context.Background(), id, addrs, time.Hour)
			AssertAddressesEqual(t, addrs, ab.Addrs(context.Background(), id))
		})

		t.Run("idempotent add multiple addresses", func(t *testing.T) {
			id := GeneratePeerIDs(1)[0]
			addrs := GenerateAddrs(3)

			ab.AddAddrs(context.Background(), id, addrs, time.Hour)
			ab.AddAddrs(context.Background(), id, addrs, time.Hour)

			AssertAddressesEqual(t, addrs, ab.Addrs(context.Background(), id))
		})

		t.Run("adding an existing address with a later expiration extends its ttl", func(t *testing.T) {
			id := GeneratePeerIDs(1)[0]
			addrs := GenerateAddrs(3)

			ab.AddAddrs(context.Background(), id, addrs, time.Second)

			// same address as before but with a higher TTL
			ab.AddAddrs(context.Background(), id, addrs[2:], time.Hour)

			// after the initial TTL has expired, check that only the third address is present.
			clk.Add(1200 * time.Millisecond)
			AssertAddressesEqual(t, addrs[2:], ab.Addrs(context.Background(), id))

			// make sure we actually set the TTL
			ab.UpdateAddrs(context.Background(), id, time.Hour, 0)
			AssertAddressesEqual(t, nil, ab.Addrs(context.Background(), id))
		})

		t.Run("adding an existing address with an earlier expiration never reduces the expiration", func(t *testing.T) {
			id := GeneratePeerIDs(1)[0]
			addrs := GenerateAddrs(3)

			ab.AddAddrs(context.Background(), id, addrs, time.Hour)

			// same address as before but with a lower TTL
			ab.AddAddrs(context.Background(), id, addrs[2:], time.Second)

			// after the initial TTL has expired, check that all three addresses are still present (i.e. the TTL on
			// the modified one was not shortened).
			clk.Add(2100 * time.Millisecond)
			AssertAddressesEqual(t, addrs, ab.Addrs(context.Background(), id))
		})

		t.Run("adding an existing address with an earlier expiration never reduces the TTL", func(t *testing.T) {
			id := GeneratePeerIDs(1)[0]
			addrs := GenerateAddrs(1)

			ab.AddAddrs(context.Background(), id, addrs, 4*time.Second)
			// 4 seconds left
			clk.Add(2 * time.Second)
			// 2 second left
			ab.AddAddrs(context.Background(), id, addrs, 3*time.Second)
			// 3 seconds left
			clk.Add(1 * time.Second)
			// 2 seconds left.

			// We still have the address.
			AssertAddressesEqual(t, addrs, ab.Addrs(context.Background(), id))

			// The TTL wasn't reduced
			ab.UpdateAddrs(context.Background(), id, 4*time.Second, 0)
			AssertAddressesEqual(t, nil, ab.Addrs(context.Background(), id))
		})

		t.Run("accessing an empty peer ID", func(t *testing.T) {
			addrs := GenerateAddrs(5)
			ab.AddAddrs(context.Background(), "", addrs, time.Hour)
			AssertAddressesEqual(t, addrs, ab.Addrs(context.Background(), ""))
		})

		t.Run("add a /p2p address with valid peerid", func(t *testing.T) {
			peerId := GeneratePeerIDs(1)[0]
			addr := GenerateAddrs(1)
			p2pAddr := addr[0].Encapsulate(Multiaddr("/p2p/" + peerId.String()))
			ab.AddAddr(context.Background(), peerId, p2pAddr, time.Hour)
			AssertAddressesEqual(t, addr, ab.Addrs(context.Background(), peerId))
		})

		t.Run("add a /p2p address with invalid peerid", func(t *testing.T) {
			pids := GeneratePeerIDs(2)
			pid1 := pids[0]
			pid2 := pids[1]
			addr := GenerateAddrs(1)
			p2pAddr := addr[0].Encapsulate(Multiaddr("/p2p/" + pid1.String()))
			ab.AddAddr(context.Background(), pid2, p2pAddr, time.Hour)
			AssertAddressesEqual(t, nil, ab.Addrs(context.Background(), pid2))
		})
	}
}

func testClearWorks(ab pstore.AddrBook, clk *mockClock.Mock) func(t *testing.T) {
	return func(t *testing.T) {
		ids := GeneratePeerIDs(2)
		addrs := GenerateAddrs(5)

		ab.AddAddrs(context.Background(), ids[0], addrs[0:3], time.Hour)
		ab.AddAddrs(context.Background(), ids[1], addrs[3:], time.Hour)

		AssertAddressesEqual(t, addrs[0:3], ab.Addrs(context.Background(), ids[0]))
		AssertAddressesEqual(t, addrs[3:], ab.Addrs(context.Background(), ids[1]))

		ab.ClearAddrs(context.Background(), ids[0])
		AssertAddressesEqual(t, nil, ab.Addrs(context.Background(), ids[0]))
		AssertAddressesEqual(t, addrs[3:], ab.Addrs(context.Background(), ids[1]))

		ab.ClearAddrs(context.Background(), ids[1])
		AssertAddressesEqual(t, nil, ab.Addrs(context.Background(), ids[0]))
		AssertAddressesEqual(t, nil, ab.Addrs(context.Background(), ids[1]))
	}
}

func testSetNegativeTTLClears(m pstore.AddrBook, clk *mockClock.Mock) func(t *testing.T) {
	return func(t *testing.T) {
		id := GeneratePeerIDs(1)[0]
		addrs := GenerateAddrs(100)

		m.SetAddrs(context.Background(), id, addrs, time.Hour)
		AssertAddressesEqual(t, addrs, m.Addrs(context.Background(), id))

		// remove two addresses.
		m.SetAddr(context.Background(), id, addrs[50], -1)
		m.SetAddr(context.Background(), id, addrs[75], -1)

		// calculate the survivors
		survivors := append(addrs[0:50], addrs[51:]...)
		survivors = append(survivors[0:74], survivors[75:]...)

		AssertAddressesEqual(t, survivors, m.Addrs(context.Background(), id))

		// remove _all_ the addresses
		m.SetAddrs(context.Background(), id, survivors, -1)
		if len(m.Addrs(context.Background(), id)) != 0 {
			t.Error("expected empty address list after clearing all addresses")
		}

		// add half, but try to remove more than we added
		m.SetAddrs(context.Background(), id, addrs[:50], time.Hour)
		m.SetAddrs(context.Background(), id, addrs, -1)
		if len(m.Addrs(context.Background(), id)) != 0 {
			t.Error("expected empty address list after clearing all addresses")
		}

		// try to remove the same addr multiple times
		m.SetAddrs(context.Background(), id, addrs[:5], time.Hour)
		repeated := make([]multiaddr.Multiaddr, 10)
		for i := 0; i < len(repeated); i++ {
			repeated[i] = addrs[0]
		}
		m.SetAddrs(context.Background(), id, repeated, -1)
		if len(m.Addrs(context.Background(), id)) != 4 {
			t.Errorf("expected 4 addrs after removing one, got %d", len(m.Addrs(context.Background(), id)))
		}
	}
}

func testUpdateTTLs(m pstore.AddrBook, clk *mockClock.Mock) func(t *testing.T) {
	return func(t *testing.T) {
		t.Run("update ttl of peer with no addrs", func(t *testing.T) {
			id := GeneratePeerIDs(1)[0]

			// Shouldn't panic.
			m.UpdateAddrs(context.Background(), id, time.Hour, time.Minute)
		})

		t.Run("update to 0 clears addrs", func(t *testing.T) {
			id := GeneratePeerIDs(1)[0]
			addrs := GenerateAddrs(1)

			// Shouldn't panic.
			m.SetAddrs(context.Background(), id, addrs, time.Hour)
			m.UpdateAddrs(context.Background(), id, time.Hour, 0)
			if len(m.Addrs(context.Background(), id)) != 0 {
				t.Error("expected no addresses")
			}
		})

		t.Run("update ttls successfully", func(t *testing.T) {
			ids := GeneratePeerIDs(2)
			addrs1, addrs2 := GenerateAddrs(2), GenerateAddrs(2)

			// set two keys with different ttls for each peer.
			m.SetAddr(context.Background(), ids[0], addrs1[0], time.Hour)
			m.SetAddr(context.Background(), ids[0], addrs1[1], time.Minute)
			m.SetAddr(context.Background(), ids[1], addrs2[0], time.Hour)
			m.SetAddr(context.Background(), ids[1], addrs2[1], time.Minute)

			// Sanity check.
			AssertAddressesEqual(t, addrs1, m.Addrs(context.Background(), ids[0]))
			AssertAddressesEqual(t, addrs2, m.Addrs(context.Background(), ids[1]))

			// Will only affect addrs1[0].
			// Badger does not support subsecond TTLs.
			// https://github.com/dgraph-io/badger/issues/339
			m.UpdateAddrs(context.Background(), ids[0], time.Hour, 1*time.Second)

			// No immediate effect.
			AssertAddressesEqual(t, addrs1, m.Addrs(context.Background(), ids[0]))
			AssertAddressesEqual(t, addrs2, m.Addrs(context.Background(), ids[1]))

			// After a wait, addrs[0] is gone.
			clk.Add(2 * time.Second)
			AssertAddressesEqual(t, addrs1[1:2], m.Addrs(context.Background(), ids[0]))
			AssertAddressesEqual(t, addrs2, m.Addrs(context.Background(), ids[1]))

			// Will only affect addrs2[0].
			m.UpdateAddrs(context.Background(), ids[1], time.Hour, 1*time.Second)

			// No immediate effect.
			AssertAddressesEqual(t, addrs1[1:2], m.Addrs(context.Background(), ids[0]))
			AssertAddressesEqual(t, addrs2, m.Addrs(context.Background(), ids[1]))

			clk.Add(2 * time.Second)

			// First addrs is gone in both.
			AssertAddressesEqual(t, addrs1[1:], m.Addrs(context.Background(), ids[0]))
			AssertAddressesEqual(t, addrs2[1:], m.Addrs(context.Background(), ids[1]))
		})

	}
}

func testNilAddrsDontBreak(m pstore.AddrBook, clk *mockClock.Mock) func(t *testing.T) {
	return func(t *testing.T) {
		id := GeneratePeerIDs(1)[0]

		m.SetAddr(context.Background(), id, nil, time.Hour)
		m.AddAddr(context.Background(), id, nil, time.Hour)
	}
}

func testAddressesExpire(m pstore.AddrBook, clk *mockClock.Mock) func(t *testing.T) {
	return func(t *testing.T) {
		ids := GeneratePeerIDs(2)
		addrs1 := GenerateAddrs(3)
		addrs2 := GenerateAddrs(2)

		m.AddAddrs(context.Background(), ids[0], addrs1, time.Hour)
		m.AddAddrs(context.Background(), ids[1], addrs2, time.Hour)

		AssertAddressesEqual(t, addrs1, m.Addrs(context.Background(), ids[0]))
		AssertAddressesEqual(t, addrs2, m.Addrs(context.Background(), ids[1]))

		m.AddAddrs(context.Background(), ids[0], addrs1, 2*time.Hour)
		m.AddAddrs(context.Background(), ids[1], addrs2, 2*time.Hour)

		AssertAddressesEqual(t, addrs1, m.Addrs(context.Background(), ids[0]))
		AssertAddressesEqual(t, addrs2, m.Addrs(context.Background(), ids[1]))

		m.SetAddr(context.Background(), ids[0], addrs1[0], 100*time.Microsecond)
		clk.Add(100 * time.Millisecond)
		AssertAddressesEqual(t, addrs1[1:3], m.Addrs(context.Background(), ids[0]))
		AssertAddressesEqual(t, addrs2, m.Addrs(context.Background(), ids[1]))

		m.SetAddr(context.Background(), ids[0], addrs1[2], 100*time.Microsecond)
		clk.Add(100 * time.Millisecond)
		AssertAddressesEqual(t, addrs1[1:2], m.Addrs(context.Background(), ids[0]))
		AssertAddressesEqual(t, addrs2, m.Addrs(context.Background(), ids[1]))

		m.SetAddr(context.Background(), ids[1], addrs2[0], 100*time.Microsecond)
		clk.Add(100 * time.Millisecond)
		AssertAddressesEqual(t, addrs1[1:2], m.Addrs(context.Background(), ids[0]))
		AssertAddressesEqual(t, addrs2[1:], m.Addrs(context.Background(), ids[1]))

		m.SetAddr(context.Background(), ids[1], addrs2[1], 100*time.Microsecond)
		clk.Add(100 * time.Millisecond)
		AssertAddressesEqual(t, addrs1[1:2], m.Addrs(context.Background(), ids[0]))
		AssertAddressesEqual(t, nil, m.Addrs(context.Background(), ids[1]))

		m.SetAddr(context.Background(), ids[0], addrs1[1], 100*time.Microsecond)
		clk.Add(100 * time.Millisecond)
		AssertAddressesEqual(t, nil, m.Addrs(context.Background(), ids[0]))
		AssertAddressesEqual(t, nil, m.Addrs(context.Background(), ids[1]))
	}
}

func testClearWithIterator(m pstore.AddrBook, clk *mockClock.Mock) func(t *testing.T) {
	return func(t *testing.T) {
		ids := GeneratePeerIDs(2)
		addrs := GenerateAddrs(100)

		// Add the peers with 50 addresses each.
		m.AddAddrs(context.Background(), ids[0], addrs[:50], pstore.PermanentAddrTTL)
		m.AddAddrs(context.Background(), ids[1], addrs[50:], pstore.PermanentAddrTTL)

		if all := append(m.Addrs(context.Background(), ids[0]), m.Addrs(context.Background(), ids[1])...); len(all) != 100 {
			t.Fatal("expected pstore to contain both peers with all their maddrs")
		}

		// Since we don't fetch these peers, they won't be present in cache.

		m.ClearAddrs(context.Background(), ids[0])
		if all := append(m.Addrs(context.Background(), ids[0]), m.Addrs(context.Background(), ids[1])...); len(all) != 50 {
			t.Fatal("expected pstore to contain only addrs of peer 2")
		}

		m.ClearAddrs(context.Background(), ids[1])
		if all := append(m.Addrs(context.Background(), ids[0]), m.Addrs(context.Background(), ids[1])...); len(all) != 0 {
			t.Fatal("expected pstore to contain no addresses")
		}
	}
}

func testPeersWithAddrs(m pstore.AddrBook, clk *mockClock.Mock) func(t *testing.T) {
	return func(t *testing.T) {
		// cannot run in parallel as the store is modified.
		// go runs sequentially in the specified order
		// see https://blog.golang.org/subtests

		t.Run("empty addrbook", func(t *testing.T) {
			if peers := m.PeersWithAddrs(context.Background()); len(peers) != 0 {
				t.Fatal("expected to find no peers")
			}
		})

		t.Run("non-empty addrbook", func(t *testing.T) {
			ids := GeneratePeerIDs(2)
			addrs := GenerateAddrs(10)

			m.AddAddrs(context.Background(), ids[0], addrs[:5], pstore.PermanentAddrTTL)
			m.AddAddrs(context.Background(), ids[1], addrs[5:], pstore.PermanentAddrTTL)

			if peers := m.PeersWithAddrs(context.Background()); len(peers) != 2 {
				t.Fatal("expected to find 2 peers")
			}
		})
	}
}

func testCertifiedAddresses(m pstore.AddrBook, clk *mockClock.Mock) func(*testing.T) {
	return func(t *testing.T) {
		cab := m.(pstore.CertifiedAddrBook)

		priv, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
		if err != nil {
			t.Errorf("error generating testing keys: %v", err)
		}

		id, _ := peer.IDFromPrivateKey(priv)
		allAddrs := GenerateAddrs(10)
		certifiedAddrs := allAddrs[:5]
		uncertifiedAddrs := allAddrs[5:]
		rec1 := peer.NewPeerRecord()
		rec1.PeerID = id
		rec1.Addrs = certifiedAddrs
		signedRec1, err := record.Seal(rec1, priv)
		if err != nil {
			t.Errorf("error creating signed routing record: %v", err)
		}

		rec2 := peer.NewPeerRecord()
		rec2.PeerID = id
		rec2.Addrs = certifiedAddrs
		signedRec2, err := record.Seal(rec2, priv)
		if err != nil {
			t.Errorf("error creating signed routing record: %v", err)
		}

		// add a few non-certified addrs
		m.AddAddrs(context.Background(), id, uncertifiedAddrs, time.Hour)

		// make sure they're present
		AssertAddressesEqual(t, uncertifiedAddrs, m.Addrs(context.Background(), id))

		// add the signed record to addr book
		accepted, err := cab.ConsumePeerRecord(context.Background(), signedRec2, time.Hour)
		if err != nil {
			t.Errorf("error adding signed routing record to addrbook: %v", err)
		}
		if !accepted {
			t.Errorf("should have accepted signed peer record")
		}

		// the non-certified addrs should be gone & we should get only certified addrs back from Addrs
		// AssertAddressesEqual(t, certifiedAddrs, m.Addrs(context.Background(), id))
		AssertAddressesEqual(t, allAddrs, m.Addrs(context.Background(), id))

		// PeersWithAddrs should return a single peer
		if len(m.PeersWithAddrs(context.Background())) != 1 {
			t.Errorf("expected PeersWithAddrs to return 1, got %d", len(m.PeersWithAddrs(context.Background())))
		}

		// Adding an old record should fail
		accepted, err = cab.ConsumePeerRecord(context.Background(), signedRec1, time.Hour)
		if accepted {
			t.Error("We should have failed to accept a record with an old sequence number")
		}
		if err != nil {
			t.Errorf("expected no error, got: %s", err)
		}

		// once certified addrs exist, trying to add non-certified addrs should have no effect
		// m.AddAddrs(context.Background(), id, uncertifiedAddrs, time.Hour)
		// AssertAddressesEqual(t, certifiedAddrs, m.Addrs(context.Background(), id))
		// XXX: Disabled until signed records are required
		m.AddAddrs(context.Background(), id, uncertifiedAddrs, time.Hour)
		AssertAddressesEqual(t, allAddrs, m.Addrs(context.Background(), id))

		// we should be able to retrieve the signed peer record
		rec3 := cab.GetPeerRecord(context.Background(), id)
		if rec3 == nil || !signedRec2.Equal(rec3) {
			t.Error("unable to retrieve signed routing record from addrbook")
		}

		// Adding a new envelope should clear existing certified addresses.
		// Only the newly-added ones should remain
		certifiedAddrs = certifiedAddrs[:3]
		rec4 := peer.NewPeerRecord()
		rec4.PeerID = id
		rec4.Addrs = certifiedAddrs
		signedRec4, err := record.Seal(rec4, priv)
		test.AssertNilError(t, err)
		accepted, err = cab.ConsumePeerRecord(context.Background(), signedRec4, time.Hour)
		test.AssertNilError(t, err)
		if !accepted {
			t.Error("expected peer record to be accepted")
		}
		// AssertAddressesEqual(t, certifiedAddrs, m.Addrs(context.Background(), id))
		AssertAddressesEqual(t, allAddrs, m.Addrs(context.Background(), id))

		// update TTL on signed addrs to -1 to remove them.
		// the signed routing record should be deleted
		// m.SetAddrs(context.Background(), id, certifiedAddrs, -1)
		// XXX: Disabled until signed records are required
		m.SetAddrs(context.Background(), id, allAddrs, -1)
		if len(m.Addrs(context.Background(), id)) != 0 {
			t.Error("expected zero certified addrs after setting TTL to -1")
		}
		if cab.GetPeerRecord(context.Background(), id) != nil {
			t.Error("expected signed peer record to be removed when addresses expire")
		}

		// Test that natural TTL expiration clears signed peer records
		accepted, err = cab.ConsumePeerRecord(context.Background(), signedRec4, time.Second)
		if !accepted {
			t.Error("expected peer record to be accepted")
		}
		test.AssertNilError(t, err)
		AssertAddressesEqual(t, certifiedAddrs, m.Addrs(context.Background(), id))

		clk.Add(2 * time.Second)
		if cab.GetPeerRecord(context.Background(), id) != nil {
			t.Error("expected signed peer record to be removed when addresses expire")
		}

		// adding a peer record that's signed with the wrong key should fail
		priv2, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
		test.AssertNilError(t, err)
		env, err := record.Seal(rec4, priv2)
		test.AssertNilError(t, err)

		accepted, err = cab.ConsumePeerRecord(context.Background(), env, time.Second)
		if accepted || err == nil {
			t.Error("expected adding a PeerRecord that's signed with the wrong key to fail")
		}
	}
}
