package backoff

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBackoff(t *testing.T) {
	origSince := since
	defer func() { since = origSince }()

	var timeSince time.Duration
	since = func(time.Time) time.Duration {
		return timeSince
	}

	var maybeErr error
	b := &ExpBackoff{}
	f := func() error { return maybeErr }

	err, ran := b.Run(f)
	require.True(t, ran)
	require.NoError(t, err)

	maybeErr = errors.New("some error")
	err, ran = b.Run(f)
	require.True(t, ran)
	require.Error(t, err)

	// Rerun again
	_, ran = b.Run(f)
	require.False(t, ran)

	timeSince = 100*time.Millisecond + 1
	err, ran = b.Run(f)
	require.True(t, ran)
	require.Error(t, err)

	timeSince = 100*time.Millisecond + 1
	_, ran = b.Run(f)
	require.False(t, ran)

	timeSince = 200*time.Millisecond + 1
	err, ran = b.Run(f)
	require.True(t, ran)
	require.Error(t, err)

	for timeSince < defaultMaxDelay*4 {
		timeSince *= 2
		err, ran = b.Run(f)
		require.True(t, ran)
		require.Error(t, err)
	}

	require.Equal(t, defaultMaxDelay, b.calcDelay())
}
