package peer

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/libp2p/go-libp2p/core/internal/catch"

	ma "github.com/multiformats/go-multiaddr"
)

// Helper struct for decoding as we can't unmarshal into an interface (Multiaddr).
type addrInfoJson struct {
	ID    ID
	Addrs []string
}

func (pi AddrInfo) MarshalJSON() (res []byte, err error) {
	defer func() { catch.HandlePanic(recover(), &err, "libp2p addr info marshal") }()

	// Skip nil addresses to avoid panic if slice contains corrupted entries
	// (likely due to data race). Log warning for visibility.
	// See: https://github.com/ipfs/kubo/issues/11116
	addrs := make([]string, 0, len(pi.Addrs))
	for _, addr := range pi.Addrs {
		if addr == nil {
			fmt.Fprintf(os.Stderr, "go-libp2p: nil multiaddr in AddrInfo.MarshalJSON for peer %s (possible data race)\n", pi.ID)
			continue
		}
		addrs = append(addrs, addr.String())
	}
	return json.Marshal(&addrInfoJson{
		ID:    pi.ID,
		Addrs: addrs,
	})
}

func (pi *AddrInfo) UnmarshalJSON(b []byte) (err error) {
	defer func() { catch.HandlePanic(recover(), &err, "libp2p addr info unmarshal") }()
	var data addrInfoJson
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	addrs := make([]ma.Multiaddr, len(data.Addrs))
	for i, addr := range data.Addrs {
		maddr, err := ma.NewMultiaddr(addr)
		if err != nil {
			return err
		}
		addrs[i] = maddr
	}

	pi.ID = data.ID
	pi.Addrs = addrs
	return nil
}
