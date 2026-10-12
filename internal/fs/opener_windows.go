//go:build windows

// Platform shims for file identity and safe output opening.

package fs

// OutputNoFollow is 0 on Windows systems where O_NOFOLLOW is not available.
// Symlink protection is handled differently on Windows.
const OutputNoFollow = 0