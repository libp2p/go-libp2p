package identify

import (
	"context"
	"fmt"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto/pb"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	recordPb "github.com/libp2p/go-libp2p/core/record/pb"
	blhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type FieldMask uint8

const (
	PublicKeyMask FieldMask = 1 << iota
	PayloadTypeMask
	PayloadMask
	SignatureMask
)

// byteToFieldMask converts a byte slice into a FieldMask.
// It reads the first byte of the slice as a uint8 and returns it as a FieldMask.
// If the slice is empty, it returns a FieldMask with a value of 0.
//
// Parameters:
//   - data: A byte slice that will be converted into a FieldMask.
//
// Returns:
//   - A FieldMask that represents the first byte of the input slice.
//     If the slice is empty, it returns a FieldMask with a value of 0.
func byteToFieldMask(data []byte) FieldMask {
	if len(data) < 1 {
		return FieldMask(0)
	}

	// Read the first byte as a uint8.
	mask := uint8(data[0])

	return FieldMask(mask)
}

// isAnyMaskSet checks if any of the defined masks (PublicKeyMask, PayloadTypeMask, PayloadMask, SignatureMask)
// are set in the given FieldMask. It returns true if any of these masks are set, false otherwise.
func isAnyMaskSet(mask FieldMask) bool {
	return (mask&PublicKeyMask != 0) ||
		(mask&PayloadTypeMask != 0) ||
		(mask&PayloadMask != 0) ||
		(mask&SignatureMask != 0)
}

// mutateEnvelope applies a mutation to an Envelope based on a provided FieldMask.
// The FieldMask is a bitmask that determines which fields of the Envelope should be mutated.
// If a bit in the mask is set, the corresponding field in the Envelope is mutated with the value from the mutation Envelope.
// If a bit in the mask is not set, the corresponding field in the Envelope remains unchanged.
//
// The bits in the FieldMask correspond to the following fields:
// - PublicKeyMask:   If set, the PublicKey field is mutated.
// - PayloadTypeMask: If set, the PayloadType field is mutated.
// - PayloadMask:     If set, the Payload field is mutated.
// - SignatureMask:   If set, the Signature field is mutated.
//
// envPb:     The Envelope to be mutated.
// mutation:  The Envelope containing the new values for the fields to be mutated.
// mask:      The FieldMask determining which fields should be mutated.
func mutateEnvelope(envPb *recordPb.Envelope, mutation *recordPb.Envelope, mask FieldMask) {
	if mask&PublicKeyMask != 0 {
		envPb.PublicKey = mutation.PublicKey
		fmt.Println("PublicKey field was mutated")
	}
	if mask&PayloadTypeMask != 0 {
		envPb.PayloadType = mutation.PayloadType
		fmt.Println("PayloadType field was mutated")
	}
	if mask&PayloadMask != 0 {
		envPb.Payload = mutation.Payload
		fmt.Println("Payload field was mutated")
	}
	if mask&SignatureMask != 0 {
		envPb.Signature = mutation.Signature
		fmt.Println("Signature field was mutated")
	}
}

// splitIntoChunks splits data into n chunks.
// If data is too short, the last chunks will be nil.
// If data is too long, the excess data will be ignored.
func splitIntoChunks(data []byte, n int) [][]byte {
	chunkSize := len(data) / n
	chunks := make([][]byte, n)
	for i := 0; i < n; i++ {
		if i*chunkSize < len(data) {
			end := (i + 1) * chunkSize
			if end > len(data) {
				end = len(data)
			}
			chunks[i] = data[i*chunkSize : end]
		}
	}
	return chunks
}

// getChunk returns the i-th chunk or defaultValue if the i-th chunk is nil.
func getChunk(chunks [][]byte, i int, defaultValue []byte) []byte {
	if i < len(chunks) && chunks[i] != nil {
		return chunks[i]
	}
	return defaultValue
}

// getKeyType returns a random KeyType value based on random.
func getKeyType(random int) *pb.KeyType {
	return pb.KeyType(random % 4).Enum()
}

// printEnvelopeDetails prints the details of a given Envelope.
// It checks if the PublicKey and its Type are not nil before printing them to avoid a nil pointer dereference.
// If the PublicKey or its Type is nil, it prints a message indicating that they are nil.
// It then prints the PayloadType, Payload, and Signature of the Envelope.
//
// envPb: The Envelope whose details are to be printed.
func printEnvelopeDetails(envPb *recordPb.Envelope) {
	if envPb.PublicKey != nil && envPb.PublicKey.Type != nil {
		fmt.Println("PublicKey Type:", envPb.PublicKey.Type.String())
	} else {
		fmt.Println("PublicKey Type is nil")
	}

	if envPb.PublicKey != nil {
		fmt.Println("PublicKey Data:", envPb.PublicKey.Data)
	} else {
		fmt.Println("PublicKey is nil")
	}

	fmt.Println("PayloadType:", envPb.PayloadType)
	fmt.Println("Payload:", envPb.Payload)
	fmt.Println("Signature:", envPb.Signature)
}

// mutateSignedPeerRecord applies a mutation to a SignedPeerRecord and sends an identify message with the mutated record.
// It first updates the snapshot of the idService, then creates a base identify response using the snapshot.
// It marshals the record from the snapshot, unmarshals it into an Envelope, applies the mutation to the Envelope,
// and replaces the SignedPeerRecord in the identify message with the mutated Envelope.
// Finally, it sends the identify message.
//
// Parameters:
//   - ids: The idService that holds the snapshot to be mutated.
//   - s: The network stream where the identify message will be sent.
//   - mutation: The Envelope that contains the mutation to be applied.
//   - mask: The FieldMask that specifies which fields of the Envelope should be mutated.
//
// Returns:
//   - An error if any step of the process fails (updating the snapshot, marshalling/unmarshalling the record or Envelope,
//     applying the mutation, replacing the SignedPeerRecord, or sending the identify message).
//     If everything succeeds, it returns nil.
func mutateSignedPeerRecord(ids *idService, s network.Stream, mutation *recordPb.Envelope, mask FieldMask) error {
	// Update the snapshot
	ids.updateSnapshot()
	ids.currentSnapshot.Lock()
	snapshot := ids.currentSnapshot.snapshot
	ids.currentSnapshot.Unlock()

	// Create the base identify response
	mes := ids.createBaseIdentifyResponse(s.Conn(), &snapshot)
	fmt.Println("Signed record is", snapshot.record)

	// Marshal the record
	marshalled, err := snapshot.record.Marshal()
	if err != nil {
		return err
	}

	// Unmarshal the envelope
	var envPb recordPb.Envelope
	err = proto.Unmarshal(marshalled, &envPb)
	if err != nil {
		return err
	}

	// Apply the mutation
	mutateEnvelope(&envPb, mutation, mask)

	// Print the mutated envelope
	printEnvelopeDetails(&envPb)

	// Replace the SignedPeerRecord with the corrupted envelope
	mes.SignedPeerRecord, err = proto.Marshal(&envPb)
	if err != nil {
		return err
	}

	// Write the identify message
	err = ids.writeChunkedIdentifyMsg(s, mes)
	if err != nil {
		return err
	}

	fmt.Println("Done sending msg")
	return nil
}

// randomSignedPeerRecord generates a random SignedPeerRecord (represented as an Envelope) based on the provided byte slice.
// It uses the first byte of the slice to determine the key type. If the slice is empty, it uses a default key type (RSA).
// It then splits the rest of the slice into four chunks and uses these to populate the fields of the Envelope.
// If a chunk is nil (because the slice is too short), it assigns a default value.
//
// Parameters:
//   - data: A byte slice that will be used to generate the SignedPeerRecord. The first byte determines the key type,
//     and the rest of the slice is split into four chunks to populate the fields of the Envelope.
//
// Returns:
//   - A pointer to an Envelope that represents the generated SignedPeerRecord. The fields of the Envelope are populated
//     with the chunks of the byte slice, and the key type is determined by the first byte of the slice.
func randomSignedPeerRecord(data []byte) *recordPb.Envelope {
	var keyType *pb.KeyType
	// If data is empty, use a default key type (RSA).
	if len(data) == 0 {
		keyType = getKeyType(0)
	} else {
		keyType = getKeyType(int(data[0]))
		data = data[1:]
	}

	// Split data into four chunks.
	chunks := splitIntoChunks(data, 4)
	// Extract chunks into separate variables
	// If a chunk is nil (because data is too short), assign a default value.
	publicKey := getChunk(chunks, 0, []byte{})
	payloadType := getChunk(chunks, 1, []byte{})
	payload := getChunk(chunks, 2, []byte{})
	signature := getChunk(chunks, 3, []byte{})

	// Create a new Envelope and assign each variable to a field.
	return &recordPb.Envelope{
		PublicKey: &pb.PublicKey{
			Type: keyType,
			Data: publicKey,
		},
		PayloadType: payloadType,
		Payload:     payload,
		Signature:   signature,
	}
}

// FuzzSignedPeerRecord is a fuzzing function used for testing the behavior of the SignedPeerRecord function.
// It takes a testing.F parameter and performs fuzzing on the data provided in the testcases.
// The function generates a new swarm for each host, creates a blank host, and starts the IDService.
// It then connects two hosts, mutates the signed peer record, and sends it to the other host.
// This function is used to test the robustness of the parsing of the signed peer record.
func FuzzSignedPeerRecord(f *testing.F) {
	// Seed corpus
	testcases := [][]byte{
		[]byte("Hello, world"),
		[]byte(" "),
		[]byte("!12345"),
		[]byte("Some random data"),
		[]byte("Another test case"),
	}
	for _, tc := range testcases {
		f.Add(tc) // Use f.Add to provide a seed corpus
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// Bail early if no mutation mask is set
		// No mutation mask set means that no mutations will be applied to the signed peer record,
		// so there's no point in running the test.
		mutationMask := byteToFieldMask(data)
		if !isAnyMaskSet(mutationMask) {
			t.Skip("No mutation mask set")
		}

		// Generate a new swarm for each host
		// We disable QUIC to avoid issues with closing UDP socket in QUIC
		swarm1 := swarmt.GenSwarm(t, swarmt.OptDisableQUIC)
		defer swarm1.Close()
		h1 := blhost.NewBlankHost(swarm1)
		defer h1.Close()
		ids1, err := NewIDService(h1)
		require.NoError(t, err)
		ids1.Start()
		defer ids1.Close()

		// We don't start the ID service on this host, so we can manually corrupt the peer record
		// and send it to the other host
		swarm2 := swarmt.GenSwarm(t, swarmt.OptDisableQUIC)
		defer swarm2.Close()
		h2 := blhost.NewBlankHost(swarm2)
		defer h2.Close()
		ids2, err := NewIDService(h2)
		require.NoError(t, err)
		defer ids2.Close()

		h2.Connect(context.Background(), peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()})
		require.Empty(t, h1.Peerstore().Addrs(h2.ID()), "h1 should not know about h2 since the peer record was not signed")

		s, err := h2.NewStream(context.Background(), h1.ID(), IDPush)
		require.NoError(t, err)
		defer s.Close()

		// The first byte of data is used for mutation mask, rest for signed peer record.
		data = data[1:]
		envPb := randomSignedPeerRecord(data)

		// Mutate (corrupt) the signed peer record using the fuzzing data
		err = mutateSignedPeerRecord(ids2, s, envPb, mutationMask)
		require.NoError(t, err)

		// Wait until h1 processes mutated signed peer record
		<-ids1.IdentifyWait(h2.Network().ConnsToPeer(h1.ID())[0])

		cab, ok := h1.Peerstore().(peerstore.CertifiedAddrBook)
		require.True(t, ok)
		require.Nil(t, cab.GetPeerRecord(h2.ID()), "h1 should not have a peer record for h2 since the peer record was corrupted")
	})
}
