//go:build !unix && !windows

package main

import "os"

// getInode returns 0 on platforms with no usable Stat_t, so inode-based
// self-exclusion cannot run. The output-file and running-binary exclusions
// still work through Config.excludeAbsPaths, which is compared by path.
func getInode(fi os.FileInfo) uint64 {
	return 0
}
