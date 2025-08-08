//go:build !linux && !darwin && !windows

package rcmgr

import "runtime"

// TODO: figure out how to get the number of file descriptors on Windows and other systems
func getNumFDs() int {
	log.Warn("cannot determine number of file descriptors", "os", runtime.GOOS)
	return 0
}
