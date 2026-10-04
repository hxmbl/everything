// Terminal detection via a termios ioctl, macOS flavour.
//go:build darwin

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// tcgets is TIOCGETA, the request that reads the terminal attribute struct.
// The request number encodes the size of struct termios (72 bytes on macOS:
// _IOR('t', 0x13, struct termios)), so it is platform specific -- see
// tty_other.go for the platforms this file does not cover.
const tcgets = 0x40487413

// isTerminal reports whether f is a real terminal. The ioctl succeeds only on
// devices that implement termios; /dev/null and the other /dev entries return
// ENOTTY. The buffer is deliberately oversized: the kernel writes exactly one
// struct termios into it and nothing here reads the contents back.
func isTerminal(f *os.File) bool {
	var buf [256]byte
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), tcgets, uintptr(unsafe.Pointer(&buf[0])))
	return errno == 0
}
