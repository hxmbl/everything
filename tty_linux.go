// Terminal detection via a termios ioctl, Linux flavour.
//go:build linux

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// tcgets is TCGETS, the request that reads the terminal attribute struct
// (asm-generic/ioctls.h). Linux uses TCGETS where macOS uses TIOCGETA.
const tcgets = 0x5401

// isTerminal reports whether f is a real terminal. The ioctl succeeds only on
// devices that implement termios; /dev/null and the other /dev entries return
// ENOTTY. The buffer is deliberately oversized: the kernel writes exactly one
// struct termios into it and nothing here reads the contents back.
func isTerminal(f *os.File) bool {
	var buf [256]byte
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), tcgets, uintptr(unsafe.Pointer(&buf[0])))
	return errno == 0
}
