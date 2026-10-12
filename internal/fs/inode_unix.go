//go:build unix

// Platform shims for file identity and safe output opening.

package fs

import (
	"os"
	"syscall"
)

// GetInode returns the inode number from file info on Unix systems.
// This is used to detect when we're about to read the same file multiple times.
func GetInode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}