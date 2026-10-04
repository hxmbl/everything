// Tests for the streaming JSON escaper and the record writers.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWriteJSONString(t *testing.T) {
	var buf bytes.Buffer
	writeJSONString(&buf, []byte("a\"b\\c\nd\te\x01f<>&g"))
	got := buf.String()
	if !strings.HasPrefix(got, `a\"b\\c\nd\te`) || !strings.HasSuffix(got, "f<>&g") {
		t.Fatalf("basic escapes wrong: %q", got)
	}
	if !bytes.Contains(buf.Bytes(), []byte("\\u0001")) {
		t.Fatalf("control char not escaped: %q", got)
	}
}

func TestWriteJSONStringUTF8(t *testing.T) {
	var buf bytes.Buffer
	writeJSONString(&buf, []byte("caf\xc3\xa9 \xff\xfe ok"))
	out := buf.String()
	if !strings.Contains(out, "café") {
		t.Fatalf("valid utf8 mangled: %q", out)
	}
	if strings.Count(out, "\\ufffd") != 2 {
		t.Fatalf("invalid bytes not replaced with \\ufffd: %q", out)
	}
}

func TestWriteJSONLineValid(t *testing.T) {
	var buf bytes.Buffer
	content := "line1\nline2 \"quoted\"\ttab"
	head := []byte(content[:3])
	rest := strings.NewReader(content[3:])
	if err := writeJSONLine(&buf, "some/path.txt", head, rest); err != nil {
		t.Fatalf("writeJSONLine failed: %v", err)
	}

	line := buf.String()
	if !strings.HasPrefix(line, `{"path":"some/path.txt","content":"`) {
		t.Fatalf("bad json line prefix: %q", line)
	}
	if !strings.HasSuffix(line, "}\n") {
		t.Fatalf("missing terminator: %q", line)
	}
	if !strings.Contains(line, `line1\nline2 \"quoted\"\ttab`) {
		t.Fatalf("content not escaped correctly: %q", line)
	}
}

func TestWriteJSONRecordNoTrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	content := "a\nbb \"q\" cc"
	head := []byte(content[:4])
	rest := strings.NewReader(content[4:])
	if err := writeJSONRecord(&buf, "x/y.txt", head, rest); err != nil {
		t.Fatalf("writeJSONRecord failed: %v", err)
	}

	got := buf.String()
	want := `{"path":"x/y.txt","content":"a\nbb \"q\" cc"}`
	if got != want {
		t.Fatalf("record mismatch\n got: %q\nwant: %q", got, want)
	}
	if json.Unmarshal([]byte(got), &map[string]any{}) != nil {
		t.Fatalf("record is not valid JSON: %q", got)
	}
}

func TestJSONEscaperChunkedUTF8(t *testing.T) {
	var buf bytes.Buffer
	e := &jsonEscaper{dst: &buf}

	input := []byte("héllo wörld café")
	for i := 0; i < len(input); i++ {
		e.Write(input[i : i+1])
	}
	e.Close()

	if buf.String() != "héllo wörld café" {
		t.Fatalf("chunked utf8 corrupted: %q", buf.String())
	}
}

func TestJSONEscaperTruncatedTail(t *testing.T) {
	var buf bytes.Buffer
	e := &jsonEscaper{dst: &buf}
	e.Write([]byte("ok \xc3"))
	e.Close()
	if buf.String() != "ok \\ufffd" {
		t.Fatalf("truncated tail mishandled: %q", buf.String())
	}
}

func TestJSONEscaperInvalidLeadNotHeld(t *testing.T) {
	var buf bytes.Buffer
	e := &jsonEscaper{dst: &buf}
	e.Write([]byte{0xff})
	e.Write([]byte("a"))
	e.Close()
	if buf.String() != "\\ufffda" {
		t.Fatalf("invalid lead handling wrong: %q", buf.String())
	}
}

func TestJSONLineBoundaryRune(t *testing.T) {
	// é split across the head/rest boundary must survive as one rune
	var buf bytes.Buffer
	head := []byte{'h', 0xC3}          // lead byte of é at end of head
	rest := strings.NewReader("\xA9!") // continuation in rest
	writeJSONLine(&buf, "p", head, rest)

	line := buf.String()
	if !strings.Contains(line, `h\u00e9!`) && !strings.Contains(line, `hé!`) {
		t.Fatalf("boundary-split rune corrupted: %q", line)
	}
	if strings.Count(line, "\\ufffd") != 0 {
		t.Fatalf("unexpected replacements: %q", line)
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSuffix(line, "\n")), &out); err != nil {
		t.Fatalf("invalid json: %v (%q)", err, line)
	}
}

func TestJSONEscaperEmptyWrite(t *testing.T) {
	var buf bytes.Buffer
	e := &jsonEscaper{dst: &buf}
	e.Write([]byte{0xC3}) // hold incomplete sequence
	e.Write(nil)          // empty write must NOT flush pending as replacement
	e.Write([]byte{0xA9})
	e.Close()
	if buf.String() != "é" {
		t.Fatalf("empty write corrupted pending: %q", buf.String())
	}
}

// countingWriter counts Write calls and keeps the bytes.
type countingWriter struct {
	writes int
	bytes  int64
	buf    bytes.Buffer
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.writes++
	c.bytes += int64(len(p))
	return c.buf.Write(p)
}

// TestJSONEscaperBatchesWrites is the regression test for the per-escape write:
// when the destination is os.Stdout, an unbuffered escaper turns every \n, \"
// and \u00XX into its own write(2), which is what made `everything --jsonl .`
// take minutes on a source tree while `--output` took seconds.
func TestJSONEscaperBatchesWrites(t *testing.T) {
	var payload bytes.Buffer
	for payload.Len() < 1<<20 {
		payload.WriteString("func café(path string) string {\n\treturn \"a\\tb\" + `x`\n}\n")
	}
	body := payload.Bytes()

	cw := &countingWriter{}
	head := body[:8192] // writeJSONRecord peeks, then streams the rest
	if err := writeJSONLine(cw, "src/café.go", head, bytes.NewReader(body[8192:])); err != nil {
		t.Fatalf("writeJSONLine failed: %v", err)
	}

	// Writes must be bounded by buffer fills plus the record's framing writes,
	// not by the number of escapes. Two staged flushes per fill leaves room for
	// the partial fill each chunk inherits from the one before it.
	const bufferSize = 64 * 1024
	const framingWrites = 6 // the literals writeJSONRecord writes around the content
	maxWrites := 2*int(cw.bytes/bufferSize) + framingWrites
	if cw.writes > maxWrites {
		t.Errorf("%d writes for %d escaped bytes: escapes are not batched, want <= %d",
			cw.writes, cw.bytes, maxWrites)
	}
	t.Logf("%d bytes in, %d escaped bytes out, %d writes (bound %d)",
		len(body), cw.bytes, cw.writes, maxWrites)

	// Batching must not disturb the record itself.
	out := cw.buf.String()
	if !strings.HasPrefix(out, `{"path":"src/café.go","content":"`) || !strings.HasSuffix(out, "}\n") {
		t.Errorf("record framing damaged: %.60q ... %.20q", out, out[len(out)-20:])
	}
}

// failWriter fails every write and counts the attempts.
type failWriter struct {
	attempts int
	err      error
}

func (f *failWriter) Write(p []byte) (int, error) {
	f.attempts++
	return 0, f.err
}

func TestJSONEscaperStickyWriteError(t *testing.T) {
	fw := &failWriter{err: errors.New("boom")}
	e := &jsonEscaper{dst: fw}
	payload := bytes.Repeat([]byte("a\nb\"c"), 40000) // well over one buffer
	if _, err := e.Write(payload); err == nil {
		t.Error("Write returned no error after the destination failed")
	}
	if _, err := e.Write(payload); err == nil {
		t.Error("second Write lost the sticky error")
	}
	if err := e.Close(); err == nil {
		t.Error("Close returned no error")
	}
	if fw.attempts != 1 {
		t.Errorf("destination saw %d writes, want exactly 1: writes did not stop after the first error", fw.attempts)
	}
}

func TestJSONEscaperBufferBoundaryRune(t *testing.T) {
	// A rune straddling the scratch buffer's boundary must land whole, exactly
	// as it did when every escape went straight to the destination.
	filler := bytes.Repeat([]byte("a"), 64*1024-2)
	for _, tc := range []struct{ tail, want string }{
		{"\xc3\xa9\n", "é" + `\n`},
		{"\xe2\x82\xac\n", "€" + `\n`},
		{"\xf0\x9f\x98\x80\n", "\U0001f600" + `\n`},
	} {
		var buf bytes.Buffer
		writeJSONString(&buf, append(filler, tc.tail...))
		if want := string(filler) + tc.want; buf.String() != want {
			t.Errorf("boundary-split rune corrupted (%d bytes out): %q...%q",
				buf.Len(), buf.String()[len(filler)-4:len(filler)+8], buf.String()[buf.Len()-8:])
		}
	}
}
