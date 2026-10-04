// Tests for skip accounting and the omitted-file disclaimer.

package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestRecordSkipCapsStoredList(t *testing.T) {
	cfg := &Config{OmittedDisclaimer: true}
	const overflow = 37
	total := maxStoredSkips + overflow
	for i := 0; i < total; i++ {
		cfg.recordSkip(fmt.Sprintf("  skip %d", i))
	}
	if len(cfg.SkippedFiles) != maxStoredSkips {
		t.Errorf("stored %d entries, want %d", len(cfg.SkippedFiles), maxStoredSkips)
	}
	if cfg.skippedTotal != total {
		t.Errorf("skippedTotal = %d, want %d (uncapped)", cfg.skippedTotal, total)
	}
	if last := cfg.SkippedFiles[len(cfg.SkippedFiles)-1]; last != fmt.Sprintf("  skip %d", maxStoredSkips-1) {
		t.Errorf("last stored entry = %q, want %q", last, fmt.Sprintf("  skip %d", maxStoredSkips-1))
	}
}

func TestRecordSkipDisabled(t *testing.T) {
	cfg := &Config{}
	cfg.recordSkip("  skip 0")
	if len(cfg.SkippedFiles) != 0 || cfg.skippedTotal != 0 {
		t.Errorf("recorded a skip while disabled: stored=%d total=%d", len(cfg.SkippedFiles), cfg.skippedTotal)
	}
}

func TestPrintOmittedDisclaimerOverflow(t *testing.T) {
	cfg := &Config{OmittedDisclaimer: true}
	const overflow = 37
	for i := 0; i < maxStoredSkips+overflow; i++ {
		cfg.recordSkip(fmt.Sprintf("  skip %d", i))
	}
	out := captureStderr(t, func() { printOmittedDisclaimer(cfg) })
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != maxStoredSkips+3 {
		t.Fatalf("printed %d lines, want %d", len(lines), maxStoredSkips+3)
	}
	if lines[0] != "---" || lines[1] != "Omitted files:" {
		t.Errorf("header = %q / %q, want %q / %q", lines[0], lines[1], "---", "Omitted files:")
	}
	if lines[2] != "  skip 0" {
		t.Errorf("first entry = %q, want a two-space indent", lines[2])
	}
	wantTail := fmt.Sprintf("  ... and %d more omitted files (use --no-omitted-disclaimer to silence this)", overflow)
	if last := lines[len(lines)-1]; last != wantTail {
		t.Errorf("overflow line = %q, want %q", last, wantTail)
	}
}

func TestPrintOmittedDisclaimerNoOverflow(t *testing.T) {
	cfg := &Config{OmittedDisclaimer: true}
	cfg.recordSkip("  skip 0")
	out := captureStderr(t, func() { printOmittedDisclaimer(cfg) })
	want := "---\nOmitted files:\n  skip 0\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestPrintOmittedDisclaimerSilentWhenDisabled(t *testing.T) {
	cfg := &Config{}
	cfg.recordSkip("  skip 0")
	if out := captureStderr(t, func() { printOmittedDisclaimer(cfg) }); out != "" {
		t.Errorf("got %q, want no output", out)
	}
}
