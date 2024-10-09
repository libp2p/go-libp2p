//go:build nocover

package handshake

import "testing"

func TestParsePeerIDAuthSchemeParamsNoAllocNoCover(t *testing.T) {
	str := []byte(`libp2p-PeerID peer-id="<server-peer-id-string>", sig="<base64-signature-bytes>", public-key="<base64-encoded-public-key-bytes>", bearer="<base64-encoded-opaque-blob>"`)

	allocs := testing.AllocsPerRun(1000, func() {
		p := params{}
		err := p.parsePeerIDAuthSchemeParams(str)
		if err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 0 {
		t.Fatalf("alloc test failed expected 0 received %0.2f", allocs)
	}
}
