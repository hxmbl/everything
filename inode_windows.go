//go:build windows

package main

import "os"

// getInode returns 0 on Windows systems where inodes are not available.
// This means inode-based duplicate detection will not work on Windows.
func getInode(fi os.FileInfo) uint64 {
	return 0
}
