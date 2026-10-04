// Terminal detection on platforms with no termios ioctl implementation here.
//go:build !linux && !darwin

package main

import "os"

// isTerminal falls back to the character-device mode check. This is the
// pre-fix behaviour: it is right for a real terminal and wrong for /dev/null
// and friends, so on these platforms `everything dir > /dev/null` can still
// enable color and print the stdout warning. The ioctl constants are
// per-kernel (the request number encodes struct sizes), so guessing one for an
// untested platform would silently disable color everywhere, which is worse.
//
// Windows is the notable case here: NUL is a character device there too, so the
// limitation applies to `> NUL` as well.

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return modeLooksInteractive(fi)
}
