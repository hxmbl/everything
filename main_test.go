package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDirEntry is a minimal os.DirEntry for exercising shouldSkip without
// touching the filesystem.
type fakeDirEntry struct {
	name  string
	isDir bool
}

func (f *fakeDirEntry) Name() string               { return f.name }
func (f *fakeDirEntry) IsDir() bool                { return f.isDir }
func (f *fakeDirEntry) Type() os.FileMode          { return 0 }
func (f *fakeDirEntry) Info() (os.FileInfo, error) { return nil, os.ErrNotExist }

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

// TestDefaultIgnoreDirSet locks in the generated/cache directories that are
// skipped without any flag. Each one is a directory whose contents are produced
// by a tool rather than written by a person, and each was verified to appear in
// a real tree that otherwise polluted a scan.
func TestDefaultIgnoreDirSet(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// pre-existing defaults, kept
		{".git", true},
		{"target", true},
		{"build", true},
		{"dist", true},
		{"out", true},
		{"bin", true},
		{"vendor", true},
		{"coverage", true},
		{".next", true},
		{".nuxt", true},
		{".cache", true},
		{"temp", true},
		{"tmp", true},
		{"logs", true},
		{".vscode", true},
		{".idea", true},
		{".eclipse", true},
		{".settings", true},

		// Swift / Xcode: the largest gap this change closes
		{".build", true},
		{"DerivedData", true},
		{"Pods", true},
		{"Carthage", true},

		// Java / JVM
		{".gradle", true},
		{".m2", true},

		// JS framework caches
		{".svelte-kit", true},
		{".astro", true},
		{".parcel-cache", true},
		{".turbo", true},
		{".angular", true},
		{".expo", true},
		{".vercel", true},
		{".netlify", true},

		// other language caches
		{".dart_tool", true},
		{".pub-cache", true},
		{"elm-stuff", true},
		{".stack-work", true},
		{"_build", true},
		{".tox", true},
		{".nox", true},
		{".eggs", true},
		{".ipynb_checkpoints", true},
		{".terraform", true},

		// test / coverage output
		{"htmlcov", true},
		{".nyc_output", true},
		{".pytest_cache", true},
		{".mypy_cache", true},
		{".ruff_cache", true},

		// extra VCS and editor state
		{".svn", true},
		{".hg", true},
		{".bzr", true},
		{".fleet", true},
		{".history", true},

		// must never be swept up: these hold real, hand-written source
		{"src", false},
		{"lib", false},
		{"test", false},
		{"tests", false},
		{"docs", false},
		{"cmd", false},
		{"internal", false},
		{"pkg", false},
		{"api", false},
		{"web", false},
		{"scripts", false},
		{"config", false},
		{"deps", false},
		{"buildSrc", false},
		{"builder.go", false},
		{"combine.go", false},
		{"outpost.txt", false},
		{"distribution.go", false},
		{"main.go", false},
	}

	for _, c := range cases {
		if got := defaultIgnoreDirSet[c.name]; got != c.want {
			t.Errorf("defaultIgnoreDirSet[%q] = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestVenvIgnoreDirsSeparated verifies the dependency trees stay behind
// --ignore-venv rather than being folded into the always-on defaults, because
// they are large enough that a caller may deliberately want them.
func TestVenvIgnoreDirsSeparated(t *testing.T) {
	for _, name := range []string{".venv", "venv", "__pycache__", "node_modules"} {
		if defaultIgnoreDirSet[name] {
			t.Errorf("%q must not be in defaultIgnoreDirs; it belongs behind --ignore-venv", name)
		}
		if !venvIgnoreDirSet[name] {
			t.Errorf("venvIgnoreDirSet[%q] = false, want true", name)
		}
	}
}

func TestMatchesAnyPattern(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"app.min.js", true},
		{"app.min.mjs", true},
		{"styles.min.css", true},
		{"bundle.js.map", true},
		{".coverage", true},
		{"lcov.info", true},
		{"coverage.lcov", true},
		{"server.log", true},
		{"debug.log", true},
		{"npm-debug.log", true},
		{"scratch.tmp", true},
		{".file.swp", true},
		{"backup~", true},
		{"main.go", false},
		{"README.md", false},
		{"minify.go", false},
		{"mapper.go", false},
		{"coverage.go", false},
		{"logs.txt", false},
	}
	for _, c := range cases {
		if got := matchesAnyPattern(c.name, defaultIgnoreFiles); got != c.want {
			t.Errorf("matchesAnyPattern(%q, defaultIgnoreFiles) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestTreeIgnorePatternCoversEveryDefault guards the invariant that motivated
// this refactor: the `tree` renderer and the walker read one list. If a
// directory is skipped by shouldSkip but missing from treeIgnorePattern, the
// two have drifted again.
func TestTreeIgnorePatternCoversEveryDefault(t *testing.T) {
	re := treeIgnorePatternRe
	for _, dir := range defaultIgnoreDirs {
		if !re.MatchString(dir) {
			t.Errorf("defaultIgnoreDirs entry %q is missing from treeIgnorePattern", dir)
		}
	}
	for _, dir := range venvIgnoreDirs {
		if !re.MatchString(dir) {
			t.Errorf("venvIgnoreDirs entry %q is missing from treeIgnorePattern", dir)
		}
	}
	for _, glob := range append(defaultIgnoreFiles, treeSecretPatterns...) {
		if !re.MatchString(glob) {
			t.Errorf("file pattern %q is missing from treeIgnorePattern", glob)
		}
	}
}

// TestTreeIgnorePatternAnchored guards the component-boundary property. `tree
// -I` matches one path component at a time, so a pattern must match a whole
// component and never a substring of one: "bin" must not take combine.go with
// it, and ".build" must not be expressed in a way that also catches ".builds".
// TestDefaultIgnoreDirsAreLiteralComponents pins the property that makes
// `tree -I` safe: `tree -I` matches one path component at a time, so an entry
// is only well behaved if it is a literal name with no glob metacharacter.
// "bin" then spares combine.go and binary_packer.go, and ".build" does not
// drag in ".builds". Entries needing a glob belong in defaultIgnoreFiles,
// where matchesAnyPattern applies them to the base name deliberately.
func TestDefaultIgnoreDirsAreLiteralComponents(t *testing.T) {
	for _, dir := range append(append([]string{}, defaultIgnoreDirs...), venvIgnoreDirs...) {
		if strings.ContainsAny(dir, "*?[]") {
			t.Errorf("defaultIgnoreDirs entry %q contains a glob metacharacter; "+
				"tree -I would apply it as a pattern, not a literal name", dir)
		}
		if dir == "" {
			t.Error("empty entry in defaultIgnoreDirs")
		}
		if strings.ContainsAny(dir, `/\`) {
			t.Errorf("defaultIgnoreDirs entry %q contains a separator; entries are single components", dir)
		}
	}
}

// TestDefaultIgnoreDirsHaveNoDuplicates keeps the rendered alternation honest:
// a repeated entry is harmless to matching but signals a list that has drifted.
func TestDefaultIgnoreDirsHaveNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, dir := range append(append([]string{}, defaultIgnoreDirs...), venvIgnoreDirs...) {
		if seen[dir] {
			t.Errorf("duplicate entry %q in ignore dir lists", dir)
		}
		seen[dir] = true
	}
	for _, dir := range defaultIgnoreDirs {
		if venvIgnoreDirSet[dir] {
			t.Errorf("%q appears in both defaultIgnoreDirs and venvIgnoreDirs", dir)
		}
	}
}

// TestShouldSkipPrecedence pins the override order: an explicit --exclude beats
// --include, and --include lifts a default. This is what makes it safe to widen
// the defaults, because a caller can always get a directory back.
func TestShouldSkipPrecedence(t *testing.T) {
	base := func() *Config {
		return &Config{
			Exclude:         map[string]bool{},
			Include:         map[string]bool{},
			excludeAbsPaths: map[string]bool{},
			IgnoreVenv:      true,
		}
	}
	dir := func(name string) os.DirEntry {
		return &fakeDirEntry{name: name, isDir: true}
	}

	// default: skipped
	cfg := base()
	if !shouldSkip(filepath.Join("p", ".build"), dir(".build"), cfg) {
		t.Error("default: .build should be skipped")
	}

	// --include lifts the default
	cfg = base()
	cfg.Include[".build"] = true
	if shouldSkip(filepath.Join("p", ".build"), dir(".build"), cfg) {
		t.Error("--include should re-enable .build")
	}

	// --exclude still wins over --include
	cfg = base()
	cfg.Include[".build"] = true
	cfg.Exclude[".build"] = true
	if !shouldSkip(filepath.Join("p", ".build"), dir(".build"), cfg) {
		t.Error("--exclude must win over --include")
	}

	// venv dirs remain reversible via --include-venv, and also via --include
	cfg = base()
	cfg.IgnoreVenv = false
	if shouldSkip(filepath.Join("p", "node_modules"), dir("node_modules"), cfg) {
		t.Error("--include-venv should re-enable node_modules")
	}
	cfg = base()
	cfg.Include["node_modules"] = true
	if shouldSkip(filepath.Join("p", "node_modules"), dir("node_modules"), cfg) {
		t.Error("--include should re-enable node_modules")
	}

	// real source is never skipped by the new defaults
	cfg = base()
	for _, keep := range []string{"src", "test", "docs", "buildSrc"} {
		if shouldSkip(filepath.Join("p", keep), dir(keep), cfg) {
			t.Errorf("%q should not be skipped", keep)
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
}

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
