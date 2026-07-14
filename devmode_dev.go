//go:build dev

package main

// isDevMode returns true when built with `-tags dev`.
// Wails dev mode (`wails dev`) does NOT set this tag by default.
// To enable dev mode (skip single-instance lock) during wails dev,
// either build with `wails build -tags dev` or set the environment
// variable AGENTPACK_DEV=1 (checked by the production variant).
func isDevMode() bool {
	return true
}
