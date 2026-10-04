// Regression tests for terminal detection.
//
// isInteractive() reads the real process stdout, so it cannot be unit tested
// directly. What can be tested is the decision it is built from: the
// character-device prefilter (modeLooksInteractive) and the composition that
// also consults the termios probe (interactiveFile).

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubFileInfo is a synthetic os.FileInfo carrying only a mode.
type stubFileInfo struct {
	mode os.FileMode
}

func (s stubFileInfo) Name() string       { return "stub" }
func (s stubFileInfo) Size() int64        { return 0 }
func (s stubFileInfo) Mode() os.FileMode  { return s.mode }
func (s stubFileInfo) ModTime() time.Time { return time.Time{} }
func (s stubFileInfo) IsDir() bool        { return s.mode.IsDir() }
func (s stubFileInfo) Sys() any           { return nil }

func TestModeLooksInteractive(t *testing.T) {
	tests := []struct {
		name string
		mode os.FileMode
		want bool
	}{
		// A real tty and /dev/null have identical mode bits: os.Stat sets
		// ModeDevice together with ModeCharDevice for every S_IFCHR
		// (fillFileStatFromSys in os/stat_unix.go), so both render as
		// "Dcrw-rw-rw-". Mode bits cannot tell them apart, which is why
		// the caller also has to run the termios probe.
		{"tty char device", os.ModeCharDevice | os.ModeDevice | 0660, true},
		{"dev null char device", os.ModeCharDevice | os.ModeDevice | 0666, true},
		{"block device", os.ModeDevice | 0660, false},
		{"regular file", 0644, false},
		{"regular file no perm bits", 0, false},
		{"directory", os.ModeDir | 0755, false},
		{"fifo", os.ModeNamedPipe | 0644, false},
		{"symlink (lstat view)", os.ModeSymlink | 0777, false},
		{"socket", os.ModeSocket | 0644, false},
		{"named pipe char bits only", os.ModeCharDevice | 0600, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modeLooksInteractive(stubFileInfo{mode: tt.mode}); got != tt.want {
				t.Errorf("modeLooksInteractive(%v) = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

// The pre-fix code was `fi.Mode()&os.ModeCharDevice != 0`. These are the values
// that predicate was actually handed, and it called both a tty and /dev/null
// interactive. interactiveFile has to disagree with it about the device nodes.
func TestInteractiveFileRejectsDeviceNodes(t *testing.T) {
	for _, path := range []string{"/dev/null", "/dev/zero"} {
		t.Run(path, func(t *testing.T) {
			f, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				t.Skipf("cannot open %s: %v", path, err)
			}
			defer f.Close()

			fi, err := f.Stat()
			if err != nil {
				t.Fatalf("stat %s: %v", path, err)
			}
			if !modeLooksInteractive(fi) {
				t.Fatalf("precondition: %s is not reported as a character device (mode %v)", path, fi.Mode())
			}
			preFix := fi.Mode()&os.ModeCharDevice != 0
			if !preFix {
				t.Fatalf("precondition: the pre-fix predicate should call %s interactive", path)
			}
			if interactiveFile(f, isTerminal) {
				t.Errorf("interactiveFile(%s) = true, want false: it is a device node, not a terminal", path)
			}
		})
	}
}

func TestInteractiveFileRejectsNonTTYStreams(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(regular, nil, 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(regular, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	tests := []struct {
		name string
		file *os.File
	}{
		{"regular file", f},
		{"pipe", w},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if interactiveFile(tt.file, isTerminal) {
				t.Errorf("interactiveFile(%s) = true, want false", tt.name)
			}
		})
	}

	// A file whose Stat fails (already closed) must not be treated as a tty.
	closed, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if interactiveFile(closed, isTerminal) {
		t.Error("interactiveFile(closed file) = true, want false")
	}
}

// The probe, not the mode bits, is the authority: whatever it says about a
// character device wins, so a real tty still reports interactive and a device
// node still does not.
func TestInteractiveFileHonoursTTYProbe(t *testing.T) {
	f, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("cannot open /dev/null: %v", err)
	}
	defer f.Close()

	yes := func(*os.File) bool { return true }
	no := func(*os.File) bool { return false }

	if !interactiveFile(f, yes) {
		t.Error("interactiveFile(char device, probe says tty) = false, want true")
	}
	if interactiveFile(f, no) {
		t.Error("interactiveFile(char device, probe says no tty) = true, want false")
	}

	// A stream that is not a character device is rejected before the probe is
	// consulted, so no syscall is issued for the common file/pipe case.
	called := false
	spy := func(*os.File) bool { called = true; return true }
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if interactiveFile(w, spy) {
		t.Error("interactiveFile(pipe, probe says tty) = true, want false")
	}
	if called {
		t.Error("probe was called for a pipe; the character-device prefilter should have short-circuited")
	}
}

// isTerminal must answer false for a character device that has no termios and
// true only for a terminal, so it is what protects the --stdout-safe refusal.
func TestIsTerminalRejectsDeviceNode(t *testing.T) {
	f, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("cannot open /dev/null: %v", err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("isTerminal(/dev/null) = true, want false")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(w) {
		t.Error("isTerminal(pipe) = true, want false")
	}
}
