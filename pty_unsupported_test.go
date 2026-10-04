//go:build !darwin && !linux

// Platforms without the pty harness in pty_test.go. The --stdout-safe refusal
// only fires when stdout is a real terminal, and there is no way to arrange
// one here with only the standard library, so the tests that need it are
// skipped instead of being asserted against a pipe.

package main

import "testing"

func runMainOnPTY(t *testing.T, args ...string) mainResult {
	t.Helper()
	t.Skip("no pty harness on this platform")
	return mainResult{}
}
