//go:build !windows

package main

import "syscall"

// outputNoFollow is the flag used to prevent following symlinks when opening files.
// On Unix systems, this is O_NOFOLLOW which prevents symlink attacks.
const outputNoFollow = syscall.O_NOFOLLOW
