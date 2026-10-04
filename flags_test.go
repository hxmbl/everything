// Tests for command-line parsing, flag validation, and help output.

package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"100", 100, false},
		{"100B", 100, false},
		{"500KB", 500 << 10, false},
		{"1mb", 1 << 20, false},
		{"2 GB", 2 << 30, false},
		{"1TB", 1 << 40, false},
		{"0KB", 0, false},
		{" 1MB ", 1 << 20, false},
		{"1KBx", 0, true},
		{"-5MB", 0, true},
		{"abc", 0, true},
		{"16777216TB", 0, true},
		{"9999999999999999999999", 0, true},
		{"", 0, true},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSize(%q) = %d, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSize(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}

	over, err := parseSize("9223372036854775807TB")
	if err == nil || over != 0 {
		t.Errorf("parseSize overflow: got (%d, %v), want error", over, err)
	}
}

func TestParseSizeEdgeCases(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"0", 0, false},
		{"0B", 0, false},
		{"1", 1, false},
		{"1023", 1023, false},
		{"  1024  ", 1024, false},
		{"-1", 0, true},
		{"1.5MB", 0, true},
		{"", 0, true},
		{"  ", 0, true},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSize(%q) expected error, got %d", c.in, got)
			}
		} else {
			if err != nil {
				t.Errorf("parseSize(%q) unexpected error: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
			}
		}
	}
}

func TestValidateConfig(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{"valid config", &Config{}, false},
		// --jsonl sets JSON too, so this is the state --jsonl produces and it
		// must validate. Only an explicitly typed --json conflicts with it.
		{"jsonl alone", &Config{JSON: true, JSONL: true}, false},
		{"json alone", &Config{JSON: true}, false},
		{"json and jsonl conflict", &Config{JSON: true, JSONL: true, jsonExplicit: true}, true},
		{"negative max size", &Config{MaxSize: -1}, true},
		{"runs too high", &Config{Runs: 10001}, true},
		{"runs negative", &Config{Runs: -1}, true},
		{"warmup too high", &Config{Warmup: 10001}, true},
		{"warmup negative", &Config{Warmup: -1}, true},
		{"valid bounds", &Config{Runs: 5, Warmup: 2, MaxSize: 1000}, false},
	}
	for _, c := range cases {
		err := validateConfig(c.cfg)
		if c.wantErr && err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
		}
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// whatever was written to it. Both the deprecation warning and the omitted-file
// disclaimer are stderr-only, so tests redirect rather than let them leak into
// the test log.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w

	// Drain concurrently: the disclaimer can be tens of kilobytes, which would
	// otherwise fill the pipe buffer and deadlock fn.
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	os.Stderr = orig
	w.Close()
	out := <-done
	r.Close()
	return out
}

func TestParseArgsDefaults(t *testing.T) {
	cfg := parseArgsFrom(nil)
	if !cfg.OmittedDisclaimer {
		t.Error("OmittedDisclaimer = false, want true (default-on)")
	}
	if !cfg.IgnoreVenv {
		t.Error("IgnoreVenv = false, want true")
	}
	if cfg.Warmup != 1 {
		t.Errorf("Warmup = %d, want 1", cfg.Warmup)
	}
	if cfg.OutputPath != "" {
		t.Errorf("OutputPath = %q, want empty", cfg.OutputPath)
	}
	if cfg.InputDirs != nil {
		t.Errorf("InputDirs = %v, want nil", cfg.InputDirs)
	}
	if cfg.Exclude == nil || cfg.Include == nil || cfg.excludeAbsPaths == nil {
		t.Error("expected the Exclude, Include, and excludeAbsPaths maps to be initialized")
	}
}

func TestParseArgsOmittedDisclaimer(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"on when no disclaimer flag is given", nil, true},
		{"opt out", []string{"--no-omitted-disclaimer"}, false},
		{"deprecated alias is a no-op", []string{"--omitted-disclaimer"}, true},
		{"deprecated alias does not undo the opt out", []string{"--no-omitted-disclaimer", "--omitted-disclaimer"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cfg *Config
			captureStderr(t, func() { cfg = parseArgsFrom(c.args) })
			if cfg.OmittedDisclaimer != c.want {
				t.Errorf("OmittedDisclaimer = %v, want %v", cfg.OmittedDisclaimer, c.want)
			}
		})
	}
}

func TestParseArgsNoOmittedDisclaimerTakesNoValue(t *testing.T) {
	// The flag must be registered as a no-argument flag, otherwise the next
	// argument is swallowed as its value.
	cfg := parseArgsFrom([]string{"--no-omitted-disclaimer", "--json", "--max-size", "1MB"})
	if cfg.OmittedDisclaimer {
		t.Error("OmittedDisclaimer = true, want false")
	}
	if !cfg.JSON {
		t.Error("JSON = false, want true (flag was swallowed as a value)")
	}
	if cfg.MaxSize != 1<<20 {
		t.Errorf("MaxSize = %d, want %d", cfg.MaxSize, 1<<20)
	}
}

func TestOmittedDisclaimerAliasWarns(t *testing.T) {
	var cfg *Config
	out := captureStderr(t, func() { cfg = parseArgsFrom([]string{"--omitted-disclaimer"}) })
	if !cfg.OmittedDisclaimer {
		t.Error("OmittedDisclaimer = false, want true")
	}
	if !strings.Contains(out, "--omitted-disclaimer is now the default") {
		t.Errorf("missing deprecation warning, got %q", out)
	}
}

func TestNoOmittedDisclaimerIsSilent(t *testing.T) {
	// Nothing may be persisted or warned about: the opt-out is per-invocation.
	out := captureStderr(t, func() { parseArgsFrom([]string{"--no-omitted-disclaimer"}) })
	if out != "" {
		t.Errorf("got stderr output %q, want none", out)
	}
}

func TestParseArgsFlags(t *testing.T) {
	dir := t.TempDir()
	cfg := parseArgsFrom([]string{"--json", "--exclude", "vendor,tmp", "--output", "out.txt", dir})
	if !cfg.JSON || cfg.JSONL {
		t.Errorf("JSON = %v, JSONL = %v, want true/false", cfg.JSON, cfg.JSONL)
	}
	if !cfg.Exclude["vendor"] || !cfg.Exclude["tmp"] {
		t.Errorf("Exclude = %v, want vendor and tmp", cfg.Exclude)
	}
	if cfg.OutputPath != "out.txt" {
		t.Errorf("OutputPath = %q, want %q", cfg.OutputPath, "out.txt")
	}
	if len(cfg.InputDirs) != 1 || cfg.InputDirs[0] != dir {
		t.Errorf("InputDirs = %v, want [%s]", cfg.InputDirs, dir)
	}
}

func TestParseArgsAliases(t *testing.T) {
	cfg := parseArgsFrom([]string{"--overwrite", "--ignore", "a,b", "--include-binary"})
	if !cfg.Force {
		t.Error("--overwrite did not set Force")
	}
	if !cfg.Exclude["a"] || !cfg.Exclude["b"] {
		t.Errorf("Exclude = %v, want a and b", cfg.Exclude)
	}
	if !cfg.IncludeBinaries {
		t.Error("--include-binary did not set IncludeBinaries")
	}
}

func TestParseArgsJSON(t *testing.T) {
	cfg := parseArgsFrom([]string{"--json"})
	if !cfg.JSON || cfg.JSONL {
		t.Errorf("JSON = %v, JSONL = %v, want true/false", cfg.JSON, cfg.JSONL)
	}
	if !cfg.jsonExplicit {
		t.Error("jsonExplicit = false, want true (--json was typed)")
	}
}

func TestParseArgsJSONL(t *testing.T) {
	cfg := parseArgsFrom([]string{"--jsonl"})
	if !cfg.JSON || !cfg.JSONL {
		t.Errorf("JSON = %v, JSONL = %v, want true/true", cfg.JSON, cfg.JSONL)
	}
	if cfg.jsonExplicit {
		t.Error("jsonExplicit = true, want false (--json was not typed)")
	}
}
