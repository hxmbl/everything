//go:build windows

// Platform shims for file identity and safe output opening.

package fs

import "os"

// GetInode returns 0 on Windows systems where inodes are not available.
// This means inode-based duplicate detection will not work on Windows.
func GetInode(fi os.FileInfo) uint64 {
	return 0
}