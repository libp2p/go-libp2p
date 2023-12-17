package network

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefaultTimeout(t *testing.T) {
	ctx := context.Background()
	dur := GetDialPeerTimeout(ctx)
	if dur != DialPeerTimeout {
		t.Fatal("expected default peer timeout")
	}
}

func TestNonDefaultTimeout(t *testing.T) {
	customTimeout := time.Duration(1)
	ctx := context.WithValue(
		context.Background(),
		dialPeerTimeoutCtxKey{},
		customTimeout,
	)
	dur := GetDialPeerTimeout(ctx)
	if dur != customTimeout {
		t.Fatal("peer timeout doesn't match set timeout")
	}
}

func TestSettingTimeout(t *testing.T) {
	customTimeout := time.Duration(1)
	ctx := WithDialPeerTimeout(
		context.Background(),
		customTimeout,
	)
	dur := GetDialPeerTimeout(ctx)
	if dur != customTimeout {
		t.Fatal("peer timeout doesn't match set timeout")
	}
}

func TestSimultaneousConnect(t *testing.T) {
	t.Run("for the server", func(t *testing.T) {
		serverCtx := WithSimultaneousConnect(context.Background(), false, "foobar")
		ok, isClient, reason := GetSimultaneousConnect(serverCtx)
		require.True(t, ok)
		require.False(t, isClient)
		require.Equal(t, "foobar", reason)
	})
	t.Run("for the client", func(t *testing.T) {
		serverCtx := WithSimultaneousConnect(context.Background(), true, "foo")
		ok, isClient, reason := GetSimultaneousConnect(serverCtx)
		require.True(t, ok)
		require.True(t, isClient)
		require.Equal(t, "foo", reason)
	})
}
