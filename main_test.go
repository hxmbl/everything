package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestIsSecretFilename(t *testing.T) {
	cases := []struct {
		name string
		skip bool
	}{
		{"credentials", true},
		{"credentials.json", true},
		{"credentials.yml", true},
		{"credentials.txt", true},
		{"CREDENTIALS", true},
		{"prod.env", true},
		{"config.env", true},
		{"staging.env.local", true},
		{".env", true},
		{".env.local", true},
		{".ENVIRONMENT", true},
		{"environment", false},
		{".npmrc", true},
		{".pypirc", true},
		{".git-credentials", true},
		{"client_secret.json", true},
		{"client_secret_1234.apps.googleusercontent.com.json", true},
		{"service-account.json", true},
		{"myproj-abc123-service-account.json", true},
		{"service_account_key.JSON", true},
		{"secrets.yaml", true},
		{"secrets", true},
		{"secrets.txt", true},
		{"id_rsa", true},
		{"id_rsa.bak", true},
		{"id_ed25519_old", true},
		{"server.key", true},
		{"cert.pem", true},
		{"backup.p12", true},
		{"keystore.jks", true},
		{"store.keystore", true},
		{"vault.kdbx", true},
		{".netrc", true},
		{".htpasswd", true},

		{"main.go", false},
		{"notes.txt", false},
		{"env.example", false},
		{"provenance.go", false},
		{"secrets.go", false},
		{"secrets_test.py", false},
		{"credentials.md", true},
		{"tokens.go", false},
		{"readme.md", false},
	}
	for _, c := range cases {
		if got := isSecretFilename(c.name); got != c.skip {
			t.Errorf("isSecretFilename(%q) = %v, want %v", c.name, got, c.skip)
		}
	}
}

func TestHasPrivateKeyMarker(t *testing.T) {
	key := "-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----"

	cases := []struct {
		name string
		data string
		want bool
	}{
		{"plain key at byte 0", key, true},
		{"leading newline", "\n" + key, true},
		{"leading spaces", "   \n\t" + key, true},
		{"utf8 bom", "\xef\xbb\xbf" + key, true},
		{"header past offset 128", strings.Repeat("x", 200) + "\n" + key, true},
		{"json wrapped", `{"key":"` + key + `"}`, true},
		{"openssh", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaA==", true},
		{"pkcs8", "-----BEGIN PRIVATE KEY-----\nMIIEvQ==", true},
		{"pgp", "-----BEGIN PGP PRIVATE KEY BLOCK-----\nabc=", true},
		{"prose mention only", "// this file parses PRIVATE KEY headers", false},
		{"begin without private key", "-----BEGIN CERTIFICATE-----\nabc=", false},
		{"empty", "", false},
		{"tiny", "short", false},
		{"marker beyond window", strings.Repeat("y", 5000) + key, false},
	}
	for _, c := range cases {
		if got := hasPrivateKeyMarker([]byte(c.data)); got != c.want {
			t.Errorf("hasPrivateKeyMarker(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

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

func TestIsBinary(t *testing.T) {
	if isBinary([]byte("plain text file\n")) {
		t.Error("text flagged as binary")
	}
	if !isBinary([]byte{0x7f, 'E', 'L', 'F', 0x00}) {
		t.Error("ELF not flagged")
	}
	if !isBinary([]byte("has\x00nul")) {
		t.Error("NUL not flagged")
	}
	if isBinary([]byte{}) {
		t.Error("empty flagged as binary")
	}
	// Test magic bytes
	if !isBinary([]byte{'P', 'K', 0x03, 0x04}) { // ZIP
		t.Error("ZIP not flagged")
	}
	if !isBinary([]byte{0x89, 'P', 'N', 'G'}) { // PNG
		t.Error("PNG not flagged")
	}
	// Test control character density
	controlHeavy := make([]byte, 100)
	for i := range controlHeavy {
		controlHeavy[i] = 0x01
	}
	if !isBinary(controlHeavy) {
		t.Error("control-heavy data not flagged")
	}
}

func TestReadWholeFile(t *testing.T) {
	// Test normal read - head is provided externally, file is at position after head
	f, err := os.CreateTemp("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	content := "hello world"
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	// Read the head first (simulating the peek)
	head := make([]byte, 3)
	n, err := f.Read(head)
	if err != nil {
		t.Fatal(err)
	}
	head = head[:n]

	// Now read the rest using readWholeFile (file position is after head)
	data, err := readWholeFile(head, f)
	if err != nil {
		t.Errorf("readWholeFile failed: %v", err)
	}
	if string(data) != content {
		t.Errorf("got %q, want %q", string(data), content)
	}

	// Test error case - unreadable file
	badFile, err := os.CreateTemp("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(badFile.Name())
	badFile.Close()

	// Try to read from closed file
	_, err = readWholeFile([]byte("x"), badFile)
	if err == nil {
		t.Error("expected error for closed file")
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

func TestHasMagic(t *testing.T) {
	// Test exact match
	if !hasMagic([]byte{'P', 'K', 0x03, 0x04, 0x05}, []byte{'P', 'K', 0x03, 0x04}) {
		t.Error("exact magic match failed")
	}
	// Test too short
	if hasMagic([]byte{'P', 'K'}, []byte{'P', 'K', 0x03, 0x04}) {
		t.Error("should fail on too short data")
	}
	// Test mismatch
	if hasMagic([]byte{'P', 'K', 0x03, 0x05}, []byte{'P', 'K', 0x03, 0x04}) {
		t.Error("should fail on mismatch")
	}
}

func TestValidateConfig(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{"valid config", &Config{}, false},
		{"json and jsonl conflict", &Config{JSON: true, JSONL: true}, true},
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
