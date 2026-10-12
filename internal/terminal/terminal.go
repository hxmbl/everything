// Terminal detection and interactive-stdout checks. The character-device mode
// prefilter and the termios probe live together because only their combination
// is correct: /dev/null carries the same mode bits as a real tty.

package terminal

import "os"

// modeLooksInteractive reports whether a file has the mode bits a terminal has,
// i.e. it is a character device. This is only a cheap prefilter: on Unix,
// os.Stat sets ModeDevice together with ModeCharDevice for every S_IFCHR (see
// fillFileStatFromSys in os/stat_unix.go), so /dev/null carries byte-for-byte
// the same mode bits as a real tty ("Dcrw-rw-rw-"). Mode bits alone therefore
// cannot distinguish a terminal from a device node; isTerminal has to.
func modeLooksInteractive(fi os.FileInfo) bool {
	return fi.Mode()&os.ModeCharDevice != 0
}

// interactiveFile reports whether f is a genuine interactive terminal. The
// character-device check rules out regular files, directories, FIFOs and
// sockets cheaply, without an ioctl; isTerminal then separates a real tty from
// other character devices such as /dev/null, /dev/zero or /dev/full.
//
// isTTY is a parameter so tests can supply a deterministic probe instead of
// depending on the process's own stdout.
func interactiveFile(f *os.File, isTTY func(*os.File) bool) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	if !modeLooksInteractive(fi) {
		return false
	}
	return isTTY(f)
}

// IsInteractive checks if stdout is connected to an interactive terminal.
// This is used to determine whether to apply color output or warn about large dumps.
//
// Testing ModeCharDevice alone is not enough: /dev/null is a character device,
// so `everything dir > /dev/null` would look interactive and silently enable
// color (which also costs seconds of chroma highlighting) and print a
// shell-safety warning to a stream that has no shell at all. isTerminal asks
// the kernel for the terminal attributes instead, which only a real tty has.
func IsInteractive() bool {
	return interactiveFile(os.Stdout, isTerminal)
}