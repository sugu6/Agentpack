//go:build !dev

package main

import "os"

// isDevMode returns true if built with `-tags dev` or AGENTPACK_DEV=1 is set.
// The env var allows `wails dev` (which doesn't set build tags) to skip
// the single-instance lock for debugging multiple instances.
func isDevMode() bool {
	return os.Getenv("AGENTPACK_DEV") == "1"
}
