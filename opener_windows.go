//go:build windows

package main

// outputNoFollow is 0 on Windows systems where O_NOFOLLOW is not available.
// Symlink protection is handled differently on Windows.
const outputNoFollow = 0
