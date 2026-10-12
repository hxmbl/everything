// Configuration state, skip accounting, and output preferences.

package config

import (
	"fmt"
	"os"
)

// Config holds the configuration for the everything tool.
// It includes input/output paths, filtering options, and display preferences.
//
// The inode and absolute-path fields are exported because the walker (ignore),
// the flag parser (flags), and the output setup (output) all consult and update
// them from their own packages.
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
	JSONExplicit      bool
	OmittedDisclaimer bool
	SkippedFiles      []string
	SkippedTotal      int
	StdoutInode       uint64
	OutputInode       uint64
	ExeInode          uint64
	Benchmark         bool
	Runs              int
	Warmup            int

	ExcludeAbsPaths map[string]bool
}

// maxStoredSkips caps how many skip messages are kept in memory for the
// omitted-file disclaimer. The disclaimer is on by default, so an unbounded
// list would tax every run; the worst case is a full disk, where every
// remaining file records a write error. A broken pipe is not that case: on
// Unix the runtime kills the process with SIGPIPE on EPIPE to stdout, so no
// write error is ever recorded. The SkippedTotal counter is never capped, so
// the summary can still report the true number of omitted files.
const maxStoredSkips = 1000

// RecordSkip records a skip message if OmittedDisclaimer is enabled.
// This is used to track which files were skipped and why. At most
// maxStoredSkips messages are kept; SkippedTotal counts every skip.
func (cfg *Config) RecordSkip(msg string) {
	if !cfg.OmittedDisclaimer {
		return
	}
	cfg.SkippedTotal++
	if len(cfg.SkippedFiles) < maxStoredSkips {
		cfg.SkippedFiles = append(cfg.SkippedFiles, msg)
	}
}

// PrintOmittedDisclaimer writes the list of skipped files to stderr after the
// scan. It prints at most the stored entries plus a trailing line reporting how
// many more files were omitted.
func PrintOmittedDisclaimer(cfg *Config) {
	if !cfg.OmittedDisclaimer || len(cfg.SkippedFiles) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, "---")
	fmt.Fprintln(os.Stderr, "Omitted files:")
	for _, s := range cfg.SkippedFiles {
		fmt.Fprintln(os.Stderr, s)
	}
	if extra := cfg.SkippedTotal - len(cfg.SkippedFiles); extra > 0 {
		fmt.Fprintf(os.Stderr, "  ... and %d more omitted files (use --no-omitted-disclaimer to silence this)\n", extra)
	}
}