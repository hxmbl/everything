// Configuration state, skip accounting, and terminal detection.

package main

import (
	"fmt"
	"os"
)

// Config holds the configuration for the everything tool.
// It includes input/output paths, filtering options, and display preferences.
type Config struct {
	OutputPath string
	InputDirs  []string
	Exclude    map[string]bool
	Include    map[string]bool

	MaxSize int64

	IgnoreVenv        bool
	Force             bool
	IncludeBinaries   bool
	StdoutSafe        bool
	Color             bool
	Theme             string
	FollowSymlinks    bool
	JSON              bool
	JSONL             bool
	jsonExplicit      bool
	OmittedDisclaimer bool
	SkippedFiles      []string
	skippedTotal      int
	stdoutInode       uint64
	outputInode       uint64
	exeInode          uint64
	Benchmark         bool
	Runs              int
	Warmup            int

	excludeAbsPaths map[string]bool
}

// maxStoredSkips caps how many skip messages are kept in memory for the
// omitted-file disclaimer. The disclaimer is on by default, so an unbounded
// list would tax every run; the worst case is a full disk, where every
// remaining file records a write error. A broken pipe is not that case: on
// Unix the runtime kills the process with SIGPIPE on EPIPE to stdout, so no
// write error is ever recorded. The skippedTotal counter is never capped, so
// the summary can still report the true number of omitted files.
const maxStoredSkips = 1000

// recordSkip records a skip message if OmittedDisclaimer is enabled.
// This is used to track which files were skipped and why. At most
// maxStoredSkips messages are kept; skippedTotal counts every skip.
func (cfg *Config) recordSkip(msg string) {
	if !cfg.OmittedDisclaimer {
		return
	}
	cfg.skippedTotal++
	if len(cfg.SkippedFiles) < maxStoredSkips {
		cfg.SkippedFiles = append(cfg.SkippedFiles, msg)
	}
}

// printOmittedDisclaimer writes the list of skipped files to stderr after the
// scan. It prints at most the stored entries plus a trailing line reporting how
// many more files were omitted.
func printOmittedDisclaimer(cfg *Config) {
	if !cfg.OmittedDisclaimer || len(cfg.SkippedFiles) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, "---")
	fmt.Fprintln(os.Stderr, "Omitted files:")
	for _, s := range cfg.SkippedFiles {
		fmt.Fprintln(os.Stderr, s)
	}
	if extra := cfg.skippedTotal - len(cfg.SkippedFiles); extra > 0 {
		fmt.Fprintf(os.Stderr, "  ... and %d more omitted files (use --no-omitted-disclaimer to silence this)\n", extra)
	}
}

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

// isInteractive checks if stdout is connected to an interactive terminal.
// This is used to determine whether to apply color output or warn about large dumps.
//
// Testing ModeCharDevice alone is not enough: /dev/null is a character device,
// so `everything dir > /dev/null` would look interactive and silently enable
// color (which also costs seconds of chroma highlighting) and print a
// shell-safety warning to a stream that has no shell at all. isTerminal asks
// the kernel for the terminal attributes instead, which only a real tty has.
func isInteractive() bool {
	return interactiveFile(os.Stdout, isTerminal)
}
