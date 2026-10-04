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
// list would tax every run; the worst case is a broken pipe, where every
// remaining file records a write error. The skippedTotal counter is never
// capped, so the summary can still report the true number of omitted files.
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

// isInteractive checks if stdout is connected to an interactive terminal.
// This is used to determine whether to apply color output or warn about large dumps.
func isInteractive() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
