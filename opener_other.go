//go:build !unix && !windows

package main

// outputNoFollow is 0 where there is no O_NOFOLLOW. The platform-independent
// os.Lstat check in setupOutput still refuses to write through a symlink, with
// the usual check-then-open race that leaves.
const outputNoFollow = 0
