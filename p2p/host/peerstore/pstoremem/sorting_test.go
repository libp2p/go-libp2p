package pstoremem

import (
	"sort"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestAddressSorting(t *testing.T) {
	u1 := ma.StringCast("/ip4/152.12.23.53/udp/1234/utp")
	u2l := ma.StringCast("/ip4/127.0.0.1/udp/1234/utp")
	local := ma.StringCast("/ip4/127.0.0.1/tcp/1234")
	norm := ma.StringCast("/ip4/6.5.4.3/tcp/1234")

	l := addrList{local, u1, u2l, norm}
	sort.Sort(l)
	require.Equal(t, addrList{u2l, u1, local, norm}, l)
}
