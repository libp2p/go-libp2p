//go:build gomock || generate

package natmanager

//go:generate sh -c "go run go.uber.org/mock/mockgen -build_flags=\"-tags=gomock\" -package natmanager -destination mock_nat_test.go github.com/libp2p/go-libp2p/p2p/host/natmanager NAT"
type NAT nat
