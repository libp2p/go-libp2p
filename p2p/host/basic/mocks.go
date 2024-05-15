package basichost

//go:generate mockgen -package basichost -destination mock_nat_test.go github.com/libp2p/go-libp2p/p2p/host/basic NAT
type NAT nat
