//go:build go1.21

package main

import "testing"

// Needed so that we run at least one test in Go 1.21 so that our go test command exits successfully
func TestNothing(t *testing.T) {}
