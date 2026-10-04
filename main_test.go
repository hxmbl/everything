package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// TestIsSecretFilenameSecretWordPrefixes covers the bare-word rule on its own.
// Those words ("auth", "token", "password", "private", "aws", "secret",
// "api_key") are ordinary filename prefixes, so the rule has to stand down for
// source code and hold everywhere else: no extension, a data format, a
// credential format, or an extension nobody has an opinion about.
func TestIsSecretFilenameSecretWordPrefixes(t *testing.T) {
	cases := []struct {
		name string
		skip bool
	}{
		// A secret word plus a source extension is the user's own code. Every
		// one of these was dropped from real dumps before the rule learned to
		// look at the extension.
		{"tokenizer.go", false},
		{"authorization.go", false},
		{"authentication.go", false},
		{"auth.go", false},
		{"auth.ts", false},
		{"auth.tsx", false},
		{"auth.py", false},
		{"author.rs", false},
		{"authorizer.rb", false},
		{"authentic.go", false},
		{"authz.go", false},
		{"awsome.go", false},
		{"aws_test.go", false},
		{"aws-sdk.js", false},
		{"private.go", false},
		{"privately.go", false},
		{"secrets.go", false},
		{"secrets.js", false},
		{"secretsmanager.py", false},
		{"secrets_test.py", false},
		{"passwordless.go", false},
		{"passwords.py", false},
		{"tokenize.rs", false},
		{"tokens.go", false},
		{"api_keys.go", false},

		// No extension at all. There is no such thing as a source file called
		// "token", and these are also the directory names this rule prunes.
		{"token", true},
		{"tokens", true},
		{"auth", true},
		{"secrets", true},
		{"secret", true},
		{"password", true},
		{"passwords", true},
		{"private", true},
		{"aws", true},
		{"api_key", true},
		{"api-key", true},

		// Data and config formats: a secret word in one of these is a stored
		// value, not an identifier.
		{"token.json", true},
		{"tokens.yml", true},
		{"auth.conf", true},
		{"private.toml", true},
		{"aws.properties", true},
		{"api_key.ini", true},
		{"api-key.json", true},
		{"passwords.json", true},
		{"passwords.txt", true},
		{"secrets.yml", true},
		{"secrets.json", true},

		// Formats that can hold live credentials but are not in the data list.
		// These fail closed, which is the point of the exemption being a list.
		{"auth.sh", true},
		{"auth.sql", true},
		{"auth.html", true},
		{"auth.xml", true},
		{"auth.config", true},
		{"token.md", true},
		{"password.md", true},
		{"secrets.md", true},
		{"authentic.md", true},
		{"awsome.md", true},
		{"aws.credentials", true},
		{"credentials.txt", true},
		{"credentials.md", true},

		// Key and certificate extensions, which secretExtensions owns. The
		// bare-word rule must not become a second, weaker path to them.
		{"private.key", true},
		{"auth.pem", true},
		{"secret.asc", true},
		{"tokens.jks", true},
		{"auth.pfx", true},
		{"auth.cer", true},
		{"aws.crt", true},
		{"aws.der", true},
		{"auth.csr", true},
		{"auth.gpg", true},
	}
	for _, c := range cases {
		if got := isSecretFilename(c.name); got != c.skip {
			t.Errorf("isSecretFilename(%q) = %v, want %v", c.name, got, c.skip)
		}
	}
}

// TestSourceCodeExtsCannotOverlapSecretExts keeps the source exemption from
// eating a format the secret side already claims. An extension in both sets
// would make the same file secret in one rule and harmless in the next, which
// is the kind of drift the ignore lists are built to avoid.
func TestSourceCodeExtsCannotOverlapSecretExts(t *testing.T) {
	for ext := range sourceCodeExts {
		if !strings.HasPrefix(ext, ".") || len(ext) < 2 {
			t.Errorf("sourceCodeExts entry %q must be an extension with a leading dot", ext)
		}
		if strings.ContainsAny(ext, `/\ `) {
			t.Errorf("sourceCodeExts entry %q must be a bare extension", ext)
		}
		if secretDataExts[ext] {
			t.Errorf("sourceCodeExts entry %q is also a secretDataExts value", ext)
		}
		if secretExtensions[ext] {
			t.Errorf("sourceCodeExts entry %q is also a secretExtensions value", ext)
		}
	}
}

// TestShouldSkipSecretNamedDirectories records a deliberate limit of the fix:
// shouldSkip sees names, not extensions, so a *directory* called auth or
// secrets is still pruned whole. Losing a source subtree is worse than losing a
// file, but the tree renderer prunes on the same word and a directory of
// credentials cannot be rescued file by file, so this stays until the name
// predicate learns whether it is looking at a directory.
func TestShouldSkipSecretNamedDirectories(t *testing.T) {
	cfg := &Config{
		Exclude:         map[string]bool{},
		Include:         map[string]bool{},
		excludeAbsPaths: map[string]bool{},
	}
	dir := func(name string) os.DirEntry {
		return &fakeDirEntry{name: name, isDir: true}
	}

	for _, name := range []string{"auth", "secrets", "credentials", "private", "aws", "tokens"} {
		if !shouldSkip(filepath.Join("p", name), dir(name), cfg) {
			t.Errorf("directory %q should still be skipped", name)
		}
	}
	if shouldSkip(filepath.Join("p", "auth.go"), &fakeDirEntry{name: "auth.go"}, cfg) {
		t.Error("auth.go should not be skipped")
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
