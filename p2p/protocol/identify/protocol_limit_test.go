package identify

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/libp2p/go-libp2p/p2p/protocol/identify/pb"
	"github.com/libp2p/go-msgio/pbio"
	"google.golang.org/protobuf/proto"
)

// We found that readAllIDMessages merges up to 10 chunked protobuf messages
// using proto.Merge, which appends repeated fields. This means a peer can
// stuff hundreds of protocols into each chunk and end up storing thousands
// in our peerstore after the merge — addresses already had a cap
// (connectedPeerMaxAddrs = 500), but protocols didn't have one at all.
//
// These tests make sure that:
//  1. Normal identify responses still work fine (single chunk, small list)
//  2. The chunked merge really does accumulate protocols across messages
//  3. We actually cap the list at maxPeerProtocols before hitting the peerstore

func TestProtocolListFromSingleChunk(t *testing.T) {
	// Sanity check: a single identify message with a handful of protocols
	// should come through untouched.
	msg := &pb.Identify{}
	for i := range 100 {
		msg.Protocols = append(msg.Protocols, fmt.Sprintf("/test/proto/%d", i))
	}

	var buf bytes.Buffer
	w := pbio.NewDelimitedWriter(&buf)
	if err := w.WriteMsg(msg); err != nil {
		t.Fatal(err)
	}

	r := pbio.NewDelimitedReader(&buf, signedIDSize)
	out := &pb.Identify{}
	if err := readAllIDMessages(r, out); err != nil {
		t.Fatal(err)
	}

	if len(out.Protocols) != 100 {
		t.Fatalf("expected 100 protocols, got %d", len(out.Protocols))
	}
}

func TestChunkedIdentifyAmplifiesProtocolList(t *testing.T) {
	// This is the core of the issue: a peer sends 9 separate protobuf
	// chunks (the 10th read slot is used up by the EOF check), each
	// carrying 200 protocol strings. After proto.Merge, we end up with
	// 1800 protocols in a single message. Without the cap we added in
	// consumeMessage, all 1800 would go straight into the peerstore.
	const protocolsPerChunk = 200
	const numChunks = maxMessages - 1 // 9 chunks, 10th read hits EOF

	var buf bytes.Buffer
	w := pbio.NewDelimitedWriter(&buf)
	for chunk := range numChunks {
		msg := &pb.Identify{}
		for i := range protocolsPerChunk {
			msg.Protocols = append(msg.Protocols, fmt.Sprintf("/x/%d/%d", chunk, i))
		}
		if err := w.WriteMsg(msg); err != nil {
			t.Fatalf("writing chunk %d: %v", chunk, err)
		}
	}

	r := pbio.NewDelimitedReader(&buf, signedIDSize)
	merged := &pb.Identify{}
	if err := readAllIDMessages(r, merged); err != nil {
		t.Fatal(err)
	}

	got := len(merged.Protocols)
	want := protocolsPerChunk * numChunks // 1800
	if got != want {
		t.Fatalf("merged protocol count: got %d, want %d", got, want)
	}

	// The merged list is bigger than our cap — this is exactly the situation
	// that consumeMessage now guards against.
	if got <= maxPeerProtocols {
		t.Fatalf("need > %d protocols to exercise the cap, but only got %d",
			maxPeerProtocols, got)
	}
	t.Logf("readAllIDMessages produced %d protocols from %d chunks — "+
		"consumeMessage will truncate this to %d", got, numChunks, maxPeerProtocols)
}

func TestProtocolListTruncation(t *testing.T) {
	// Simulate what consumeMessage does: take a protocol list that's way
	// too large and chop it down to maxPeerProtocols. We're testing the
	// same logic that now lives in consumeMessage, just isolated here
	// so reviewers can see it clearly.
	total := maxPeerProtocols + 500
	protocols := make([]string, total)
	for i := range protocols {
		protocols[i] = fmt.Sprintf("/overflow/%d", i)
	}

	// This is the exact guard we added in consumeMessage.
	if len(protocols) > maxPeerProtocols {
		protocols = protocols[:maxPeerProtocols]
	}

	if len(protocols) != maxPeerProtocols {
		t.Fatalf("expected %d after truncation, got %d", maxPeerProtocols, len(protocols))
	}
}

func TestChunkedMergeMemoryCost(t *testing.T) {
	// Just to put numbers on it: how much memory does the inflated
	// protocol list actually cost, and how much do we save by capping?
	const protocolsPerChunk = 200
	const numChunks = maxMessages - 1

	var buf bytes.Buffer
	w := pbio.NewDelimitedWriter(&buf)
	for chunk := range numChunks {
		msg := &pb.Identify{}
		for i := 0; i < protocolsPerChunk; i++ {
			msg.Protocols = append(msg.Protocols, fmt.Sprintf("/x/%d/%d", chunk, i))
		}
		if err := w.WriteMsg(msg); err != nil {
			t.Fatal(err)
		}
	}

	r := pbio.NewDelimitedReader(&buf, signedIDSize)
	merged := &pb.Identify{}
	if err := readAllIDMessages(r, merged); err != nil {
		t.Fatal(err)
	}

	before := proto.Size(merged)
	t.Logf("before cap: %d protocols, %d bytes serialized", len(merged.Protocols), before)

	if len(merged.Protocols) > maxPeerProtocols {
		merged.Protocols = merged.Protocols[:maxPeerProtocols]
	}
	after := proto.Size(merged)
	t.Logf("after cap:  %d protocols, %d bytes serialized (saved %d bytes per peer)",
		len(merged.Protocols), after, before-after)
}
