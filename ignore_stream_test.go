// Tests for the streamed `tree` banner: the bytes it writes, the lines it
// drops, and the memory it is not allowed to spend.

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// bufferedBanner is the pre-streaming implementation of the banner, kept
// verbatim as the oracle. Streaming is only allowed to change how the bytes
// arrive, never which bytes they are, and this is what says so.
func bufferedBanner(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "0 directories") ||
			strings.Contains(line, " directories, ") || strings.HasSuffix(line, " files") {
			fmt.Fprintln(&b, line)
			continue
		}
		if filterTreeLine(line) {
			continue
		}
		fmt.Fprintln(&b, line)
	}
	fmt.Fprint(&b, "\n")
	return b.String()
}

// TestTreeLineIsSecret covers the call-site rule, which is not part of
// filterTreeLine: the report lines and blank lines are never secrets, so a
// directory full of key files is still counted in the dump.
func TestTreeLineIsSecret(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"│   ", false},
		{"3 directories, 5 files", false},
		{"0 directories, 0 files", false},
		{"1 file", false},
		{"12 files", false},
		{"├── README.md", false},
		{"├── Passwords.md", true},
		{"├── link -> Tokens.md", true},
	}
	for _, c := range cases {
		if got := treeLineIsSecret(c.line); got != c.want {
			t.Errorf("treeLineIsSecret(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

// TestStreamTreeOutputMatchesBufferedBanner is the byte-identity proof for the
// rewrite. The awkward cases are the trailing newlines: the buffered form
// trimmed exactly one, then re-terminated every line and added a blank line,
// so "" produced two newlines and "a\n\n" produced three.
func TestStreamTreeOutputMatchesBufferedBanner(t *testing.T) {
	long := strings.Repeat("x", 300*1024) // well past any scanner token limit
	many := &strings.Builder{}
	for i := 0; i < 50000; i++ {
		fmt.Fprintf(many, "├── file%06d.txt\n", i)
	}

	cases := []struct {
		name   string
		output string
	}{
		{"no output at all", ""},
		{"single newline", "\n"},
		{"two newlines", "\n\n"},
		{"no trailing newline", "no trailing newline"},
		{"one line", ".\n"},
		{"root and report", ".\n\n0 directories, 0 files\n"},
		{"nested tree", ".\n├── a\n│   └── b\n└── c\n\n3 directories, 3 files\n"},
		{"secrets dropped", ".\n├── a.txt\n├── Passwords.md\n└── link -> Tokens.md\n\n2 directories, 4 files\n"},
		{"every line a secret", "Passwords.md\nTokens.md\n"},
		{"blank lines inside", ".\n\n├── a\n\n└── b\n\n2 directories, 2 files\n"},
		{"crlf line endings", ".\r\n├── a.txt\r\n\r\n1 directory, 2 files\r\n"},
		{"invalid utf-8", ".\n├── caf\xff.txt\n└── \xfe\xfe\n\n1 directory, 2 files\n"},
		{"nul byte", ".\n├── a\x00b.txt\n\n1 directory, 1 file\n"},
		{"only a report", "3 directories, 5 files\n"},
		{"line longer than any buffer", ".\n├── " + long + "\n\n1 directory, 1 file\n"},
		{"secret inside a long line", ".\n├── " + long + "Passwords.md\n\n1 directory, 1 file\n"},
		{"long line without a trailing newline", long},
		{"fifty thousand lines", many.String()},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got strings.Builder
			produced := streamTreeOutput(&got, strings.NewReader(c.output))

			if want := bufferedBanner(c.output); got.String() != want {
				t.Errorf("streamed banner differs from the buffered one\n got %d bytes\nwant %d bytes\n first difference at %d",
					got.Len(), len(want), firstDiff(got.String(), want))
			}
			if produced != (c.output != "") {
				t.Errorf("produced = %v, want %v", produced, c.output != "")
			}
		})
	}
}

// firstDiff reports where two strings start to differ, for a failure message
// that does not print megabytes.
func firstDiff(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// TestStreamTreeOutputWriterFailure pins the deadlock that a streaming rewrite
// can introduce: the writer is gone, but tree is still writing. Returning early
// would leave it blocked on a full pipe and the following Wait would never
// return, so the read has to run to EOF regardless.
func TestStreamTreeOutputWriterFailure(t *testing.T) {
	var written int
	big := strings.Repeat("├── file.txt\n", 200000) // ~2.4 MB, far past a pipe buffer

	done := make(chan bool, 1)
	go func() {
		var sink countingFailWriter
		produced := streamTreeOutput(&sink, strings.NewReader(big))
		written = sink.attempts
		done <- produced
	}()

	select {
	case produced := <-done:
		if !produced {
			t.Error("produced = false, want true")
		}
		if written != 1 {
			t.Errorf("wrote to the failing writer %d times, want 1 (stop after the first error)", written)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("streamTreeOutput did not return after a write error: the read stopped early")
	}
}

// countingFailWriter fails every write and remembers how often it was asked.
type countingFailWriter struct {
	attempts int
}

func (w *countingFailWriter) Write(p []byte) (int, error) {
	w.attempts++
	return 0, fmt.Errorf("writer is gone")
}

// peakHeapDuring samples the live heap while fn runs, the way bench.go samples
// it, and returns the peak growth over the pre-run baseline.
func peakHeapDuring(fn func()) int64 {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	var peak atomic.Int64
	stop := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if int64(m.Alloc) > peak.Load() {
				peak.Store(int64(m.Alloc))
			}
			time.Sleep(time.Millisecond)
		}
	}()

	fn()
	close(stop)
	sampler.Wait()

	return peak.Load() - int64(base.Alloc)
}

// fakeTree puts an executable named `tree` at the front of PATH so the banner
// can be driven with output that is cheap to produce and shaped on purpose. The
// script reads $FAKE_TREE_LINES and $FAKE_TREE_EXIT.
func fakeTree(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in for tree is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tree"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("writing the fake tree: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// awkTreeLines emits $FAKE_TREE_LINES lines of ordinary tree output.
const awkTreeLines = `awk -v n="$FAKE_TREE_LINES" 'BEGIN{
	print ".";
	for (i = 0; i < n; i++) {
		if (i % 7 == 3) print "├── Passwords.md";
		else if (i % 11 == 5) print "└── link -> Tokens.md";
		else printf "├── file%06d.txt\n", i;
	}
	printf "\n2 directories, %d files\n", n;
}'`

// TestTryPrintTreeDropsSecretsAndKeepsTheReport drives the whole path through a
// stand-in for tree, so both the filtering and the byte-for-byte shape of the
// banner are checked without a filesystem full of files.
func TestTryPrintTreeDropsSecretsAndKeepsTheReport(t *testing.T) {
	fakeTree(t, awkTreeLines)
	t.Setenv("FAKE_TREE_LINES", "40")

	var got strings.Builder
	tryPrintTree(&got, []string{"."})

	want := bufferedBanner(fakeTreeOutput(t, 40))
	if got.String() != want {
		t.Errorf("tryPrintTree banner differs from the buffered form\n got %q\nwant %q", got.String(), want)
	}
	for _, gone := range []string{"Passwords.md", "Tokens.md"} {
		if strings.Contains(got.String(), gone) {
			t.Errorf("%s reached the banner", gone)
		}
	}
	for _, kept := range []string{"file000000.txt", "file000039.txt", "2 directories, 40 files"} {
		if !strings.Contains(got.String(), kept) {
			t.Errorf("%q missing from the banner:\n%s", kept, got.String())
		}
	}
}

// fakeTreeOutput runs the stand-in directly, to get the bytes it produced for
// the oracle comparison above.
func fakeTreeOutput(t *testing.T, lines int) string {
	t.Helper()
	cmd := exec.Command("tree", "-n", "-I", treeIgnorePattern, ".")
	cmd.Env = append(os.Environ(), fmt.Sprintf("FAKE_TREE_LINES=%d", lines))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the fake tree: %v", err)
	}
	return string(out)
}

// TestTryPrintTreeNoOutputStillBanners pins the degenerate case. The buffered
// form wrote two newlines before giving up, and a run that produced nothing is
// exactly when a caller is most likely to be reading the output.
func TestTryPrintTreeNoOutputStillBanners(t *testing.T) {
	fakeTree(t, "exit 3\n")

	var got strings.Builder
	tryPrintTree(&got, []string{".", "."})

	if got.String() != "\n\n" {
		t.Errorf("banner for a failing tree = %q, want %q (one blank line, then the closing one)", got.String(), "\n\n")
	}
}

// TestTryPrintTreeStopsAfterAFailedRoot covers the bail-out: the first root
// fails with no output, so the second is never rendered.
func TestTryPrintTreeStopsAfterAFailedRoot(t *testing.T) {
	fakeTree(t, `if [ "$PWD" = "." ]; then exit 3; fi
awk -v n="$FAKE_TREE_LINES" 'BEGIN{ for (i=0;i<n;i++) print "├── file" i ".txt" }'`)

	var got strings.Builder
	tryPrintTree(&got, []string{".", "."})

	if strings.Contains(got.String(), "file0.txt") {
		t.Errorf("the second root was rendered after the first failed:\n%s", got.String())
	}
}

// TestTryPrintTreeWithoutTree is the LookPath bail-out: nothing on PATH means
// no banner at all, not an empty one and not an error.
func TestTryPrintTreeWithoutTree(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	var got strings.Builder
	tryPrintTree(&got, []string{"."})

	if got.String() != "" {
		t.Errorf("wrote %q with no tree on PATH, want nothing", got.String())
	}
}

// TestTryPrintTreePeakHeapIsFlat is the regression test for the memory bug.
// Collecting tree's whole output and splitting it cost the full size of the
// output twice over, on a banner that is decorative, and it was the only
// unbounded allocation left in the tool: 100k entries cost about 10 MB, and the
// cost grew in a straight line from there.
func TestTryPrintTreePeakHeapIsFlat(t *testing.T) {
	fakeTree(t, awkTreeLines)

	t.Setenv("FAKE_TREE_LINES", "100")
	small := peakHeapDuring(func() { tryPrintTree(io.Discard, []string{"."}) })

	// 400k lines is roughly 7.7 MB of tree output. Collected into a buffer and
	// split, that is 7.7 MB of buffer plus 6.4 MB of line headers, and it shows
	// up as live heap; streamed, the same run peaks around 3 MB, which is what
	// the collector lets accumulate between cycles rather than anything the
	// banner holds.
	t.Setenv("FAKE_TREE_LINES", "400000")
	large := peakHeapDuring(func() { tryPrintTree(io.Discard, []string{"."}) })

	const slack = 6 << 20 // measured at 48 MB with the buffered version
	if large > small+slack {
		t.Errorf("peak heap grew with the size of the tree: %d bytes for 100 lines, %d for 400000 (delta %d, budget %d)",
			small, large, large-small, slack)
	}
	t.Logf("peak heap: %d bytes at 100 lines, %d at 400000", small, large)
}

// TestTryPrintTreeWriterFailureCannotDeadlock is the same hazard as
// TestStreamTreeOutputWriterFailure, one level up: tree is a real child writing
// through a real pipe, so returning early would wedge both processes.
func TestTryPrintTreeWriterFailureCannotDeadlock(t *testing.T) {
	fakeTree(t, awkTreeLines)
	t.Setenv("FAKE_TREE_LINES", "400000")

	done := make(chan struct{})
	go func() {
		defer close(done)
		tryPrintTree(&countingFailWriter{}, []string{"."})
	}()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("tryPrintTree did not return after a write error")
	}
}
