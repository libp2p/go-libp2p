package libp2p_test

import (
	"testing"

	// Import all protobuf packages to ensure their `init` functions run.
	// This may not be strictly necessary if they are imported in the `libp2p` package, but
	// we do it here in case the imports in non-test files change.
	_ "github.com/libp2p/go-libp2p/core/crypto/pb"
	_ "github.com/libp2p/go-libp2p/core/peer/pb"
	_ "github.com/libp2p/go-libp2p/core/record/pb"
	_ "github.com/libp2p/go-libp2p/core/sec/insecure/pb"
	_ "github.com/libp2p/go-libp2p/p2p/host/autonat/pb"
	_ "github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoreds/pb"
	_ "github.com/libp2p/go-libp2p/p2p/protocol/autonatv2/pb"
	_ "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
	_ "github.com/libp2p/go-libp2p/p2p/protocol/holepunch/pb"
	_ "github.com/libp2p/go-libp2p/p2p/protocol/identify/pb"
	_ "github.com/libp2p/go-libp2p/p2p/security/noise/pb"
	_ "github.com/libp2p/go-libp2p/p2p/transport/webrtc/pb"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

//go:generate scripts/gen-proto.sh .

func TestProtoImportsAndPathsAreConsistent(t *testing.T) {
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		imports := fd.Imports()
		for i := 0; i < imports.Len(); i++ {
			path := imports.Get(i).Path()
			if _, err := protoregistry.GlobalFiles.FindFileByPath(path); err != nil {
				t.Fatalf("find dependency %s: %v", path, err)
			}
		}
		return true
	})
}
