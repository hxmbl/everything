//go:build !windows

package main

import (
	"os"
	"syscall"
)

func getInode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
