//go:build !unix && !windows

// Platform shims for file identity and safe output opening.

package fs

import "os"

// GetInode returns 0 on platforms with no usable Stat_t, so inode-based
// self-exclusion cannot run. The output-file and running-binary exclusions
// still work through Config.ExcludeAbsPaths, which is compared by path.
func GetInode(fi os.FileInfo) uint64 {
	return 0
}