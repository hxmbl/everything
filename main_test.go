// End-to-end tests. main() reads os.Args and calls os.Exit on every
// error path, so it is driven through a re-executed copy of this test binary
// rather than called in-process.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// everythingRunMainEnv tells a re-executed test binary to run main() with the
// arguments it was given instead of running the test suite.
const everythingRunMainEnv = "EVERYTHING_TEST_RUN_MAIN"

// TestMain exists only so tests can drive main() end-to-end: main() reads
// os.Args and calls os.Exit on every error path, so it cannot be exercised
// in-process. The gate has to live here, not in an individual test, so that a
// re-executed binary runs main() before any test output reaches the captured
// streams.
func TestMain(m *testing.M) {
	if os.Getenv(everythingRunMainEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// mainResult is what one main() run produced: its two output streams and the
// exit code.
type mainResult struct {
	stdout string
	stderr string
	code   int
}

// runMain re-executes this test binary with args and runs main() in the child.
// A rejected flag combination exits non-zero, which runMain reports rather than
// failing on, so the same helper serves both the working and the failing runs.
func runMain(t *testing.T, args ...string) mainResult {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), everythingRunMainEnv+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	code := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running main(%v): %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	return mainResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// jsonRecord is one emitted record, shared by both JSON output modes.
type jsonRecord struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// writeJSONLFixture creates a tree whose contents stress JSON escaping: quotes,
// backslashes, tabs, newlines, multi-byte UTF-8, invalid UTF-8 bytes, and an
// empty file. One file is deliberately named like a secret so the omitted-file
// disclaimer has something to report on stderr.
func writeJSONLFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"plain.txt":       "hello\n",
		"awkward.txt":     "quote \" backslash \\ tab \t newline \n café ☕ \xff\xfe done\n",
		"empty.txt":       "",
		"sub/nested.md":   "# nested\n\n    indented\n",
		"credentials.yml": "password: hunter2\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating parent of %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	return dir
}

func TestJSONLEndToEnd(t *testing.T) {
	dir := writeJSONLFixture(t)
	// The output files must live outside the scan root: anything written into
	// the tree being dumped shows up as a record in the next run.
	outDir := t.TempDir()
	jsonlPath := filepath.Join(outDir, "out.jsonl")
	jsonPath := filepath.Join(outDir, "out.json")

	res := runMain(t, "--jsonl", "--output", jsonlPath, dir)
	if res.code != 0 {
		t.Fatalf("--jsonl exited %d, want 0\nstderr: %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "credentials.yml") {
		t.Errorf("--jsonl stderr does not report the skipped file: %q", res.stderr)
	}
	raw, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("reading jsonl output: %v", err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Errorf("jsonl output does not end in a newline: %q", raw)
	}
	if strings.Contains(string(raw), "Omitted files") {
		t.Error("omitted-file disclaimer leaked into the jsonl output")
	}

	// Every line must be a complete object on its own. A raw newline or an
	// unescaped quote inside a record would break the parse below.
	var records []jsonRecord
	contents := make(map[string]string)
	for i, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		fields := map[string]string{}
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("line %d is not a standalone JSON object: %v\n%q", i+1, err, line)
		}
		if len(fields) != 2 {
			t.Fatalf("line %d has fields %v, want exactly path and content", i+1, fields)
		}
		path, content := fields["path"], fields["content"]
		if !strings.HasPrefix(path, dir+string(filepath.Separator)) {
			t.Errorf("line %d path %q is not under the scanned tree %q", i+1, path, dir)
		}
		contents[filepath.Base(path)] = content
		records = append(records, jsonRecord{Path: path, Content: content})
	}
	if len(records) != 4 {
		t.Fatalf("got %d records, want 4 (credentials.yml is skipped): %v", len(records), records)
	}

	// Escaping must survive a full round trip, including the invalid UTF-8
	// bytes, which are replaced rather than dropped.
	wantAwkward := "quote \" backslash \\ tab \t newline \n café ☕ \ufffd\ufffd done\n"
	if got := contents["awkward.txt"]; got != wantAwkward {
		t.Errorf("awkward.txt content = %q, want %q", got, wantAwkward)
	}
	if got := contents["empty.txt"]; got != "" {
		t.Errorf("empty.txt content = %q, want empty", got)
	}
	if got := contents["plain.txt"]; got != "hello\n" {
		t.Errorf("plain.txt content = %q, want %q", got, "hello\n")
	}
	if got := contents["nested.md"]; got != "# nested\n\n    indented\n" {
		t.Errorf("nested.md content = %q, want %q", got, "# nested\n\n    indented\n")
	}
	if _, ok := contents["credentials.yml"]; ok {
		t.Error("credentials.yml was emitted but should have been skipped as a secret")
	}

	// --json must still be exactly one JSON array holding the same records.
	jsonRes := runMain(t, "--json", "--output", jsonPath, dir)
	if jsonRes.code != 0 {
		t.Fatalf("--json exited %d, want 0\nstderr: %s", jsonRes.code, jsonRes.stderr)
	}
	rawArray, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("reading json output: %v", err)
	}
	var array []jsonRecord
	if err := json.Unmarshal(rawArray, &array); err != nil {
		t.Fatalf("--json output is not a single JSON array: %v\n%q", err, rawArray)
	}
	if len(array) != len(records) {
		t.Fatalf("--json has %d records, --jsonl has %d", len(array), len(records))
	}
	for i := range records {
		if records[i] != array[i] {
			t.Errorf("record %d differs:\n jsonl %+v\n json  %+v", i, records[i], array[i])
		}
	}

	// The modes must not be the same document: --jsonl is lines, not an array.
	var asArray []jsonRecord
	if err := json.Unmarshal(raw, &asArray); err == nil {
		t.Error("--jsonl output parsed as one JSON array, want one object per line")
	}

	// Piping to stdout must yield the same bytes as writing to a file.
	piped := runMain(t, "--jsonl", dir)
	if piped.code != 0 {
		t.Fatalf("--jsonl to stdout exited %d, want 0\nstderr: %s", piped.code, piped.stderr)
	}
	if piped.stdout != string(raw) {
		t.Errorf("piped jsonl differs from the file:\n got %q\nwant %q", piped.stdout, raw)
	}
}

func TestJSONAndJSONLConflictExits(t *testing.T) {
	// --jsonl on its own is fine; combining it with an explicit --json is an
	// error, in either order.
	for _, args := range [][]string{{"--json", "--jsonl"}, {"--jsonl", "--json"}} {
		res := runMain(t, args...)
		if res.code != 1 {
			t.Errorf("%v exited %d, want 1", args, res.code)
		}
		if !strings.Contains(res.stderr, "--json and --jsonl are mutually exclusive") {
			t.Errorf("%v stderr = %q, want the conflict message", args, res.stderr)
		}
		if res.stdout != "" {
			t.Errorf("%v wrote %q to stdout, want nothing", args, res.stdout)
		}
	}
}
