//go:build !js
// +build !js

package testing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenSwarm(t *testing.T) {
	swarm := GenSwarm(t)
	require.NoError(t, swarm.Close())
	GenUpgrader(t, swarm, nil)
}
