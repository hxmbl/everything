//go:build unix

// Platform shims for file identity and safe output opening.

package fs

import "syscall"

// OutputNoFollow is the flag used to prevent following symlinks when opening files.
// On Unix systems, this is O_NOFOLLOW which prevents symlink attacks.
const OutputNoFollow = syscall.O_NOFOLLOW