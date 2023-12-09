package libp2ptls

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtensionGenerating(t *testing.T) {
	require.Equal(t, []int{1, 3, 6, 1, 4, 1, 53594, 13, 37}, getPrefixedExtensionID([]int{13, 37}))
}

func TestExtensionComparison(t *testing.T) {
	require.True(t, extensionIDEqual([]int{1, 2, 3, 4}, []int{1, 2, 3, 4}))
	require.False(t, extensionIDEqual([]int{1, 2, 3, 4}, []int{1, 2, 3}))
	require.False(t, extensionIDEqual([]int{1, 2, 3}, []int{1, 2, 3, 4}))
	require.False(t, extensionIDEqual([]int{1, 2, 3, 4}, []int{4, 3, 2, 1}))
}
