//go:build !unix && !windows

// Platform shims for file identity and safe output opening.

package fs

// OutputNoFollow is 0 where there is no O_NOFOLLOW. The platform-independent
// os.Lstat check in SetupOutput still refuses to write through a symlink, with
// the usual check-then-open race that leaves.
const OutputNoFollow = 0