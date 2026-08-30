//go:build !windows

package main

import (
	"os"
	"syscall"
)

// getInode returns the inode number from file info on Unix systems.
// This is used to detect when we're about to read the same file multiple times.
func getInode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
