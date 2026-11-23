//go:build !unix

package main

import "os"

// reexec exits; the service manager (docker restart policy) starts the panel
// again.
func reexec() error {
	os.Exit(0)
	return nil
}
