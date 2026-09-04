package testutils

import (
	"fmt"
	"net"
	"testing"
)

// FindFreePort attempts to find an unused tcp port
func FindFreePort(t *testing.T, host string, maxAttempts int) (int, error) {
	t.Helper()

	if host == "" {
		host = "localhost"
	}

	for range maxAttempts {
		addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			t.Logf("unable to resolve tcp addr: %v", err)
			continue
		}
		l, err := net.ListenTCP("tcp", addr)
		if err != nil {
			l.Close()
			t.Logf("unable to listen on addr %q: %v", addr, err)
			continue
		}

		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return port, nil

	}

	return 0, fmt.Errorf("no free port found")
}
