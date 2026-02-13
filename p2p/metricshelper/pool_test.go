package metricshelper

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStringSlicePool(t *testing.T) {
	for range int(1e5) {
		s := GetStringSlice()
		require.Empty(t, *s)
		require.Equal(t, 8, cap(*s))
		*s = append(*s, "foo")
		*s = append(*s, "bar")
		if rand.Int()%3 == 0 {
			PutStringSlice(s)
		}
	}
}
