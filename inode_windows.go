//go:build windows

package main

import "os"

func getInode(fi os.FileInfo) uint64 {
	return 0
}
