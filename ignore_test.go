// Tests for the ignore lists and the unified skip decision.

package main

import (
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

// TestFilterTreeLine covers the one function that decides whether a line of
// `tree` output survives into the dump. It is a pure string predicate, so every
// branch is reachable without spawning anything, and it had no coverage at all
// before: neither tryPrintTree nor filterTreeLine was exercised by the suite.
func TestFilterTreeLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool // true == drop the line
	}{
		// Plain names, with and without a box-drawing prefix.
		{"plain", "README.md", false},
		{"plain with extension that looks secret", "notes.txt", false},
		{"branch", "├── main.go", false},
		{"last branch", "└── main.go", false},
		{"nested prefix", "│   └── main.go", false},
		{"nested mixed prefix", "│   ├── deep.go", false},
		{"deeper nesting", "    │   │   └── deep.go", false},

		// Names tree's own -I does not catch, because its patterns are
		// lowercase: the Go-side filter is the second line of defence.
		{"uppercase secret word", "├── Passwords.md", true},
		{"uppercase token word", "├── Tokens.md", true},
		{"aws prefix", "├── awsome-notes.txt", true},
		{"env file", "├── .env.local", true},
		{"ssh key", "├── id_ed25519", true},
		{"certificate", "├── server.pem", true},
		{"credentials", "├── credentials.yml", true},
		{"dir named like a secret", "└── secrets.json", true},
		{"nested secret", "│   └── Passwords.md", true},

		// The same words in source files are code, not secrets.
		{"auth.go", "├── auth.go", false},
		{"tokenizer.go", "└── tokenizer.go", false},
		{"private.go", "├── private.go", false},
		{"service account json", "├── service-account.json", true},
		{"secret manager go", "└── secretsmanager.go", false},

		// The " -> " branch: only the target decides, and only its base name.
		{"symlink to ordinary file", "├── link -> README.md", false},
		{"symlink to secret-looking target", "├── link -> Passwords.md", true},
		{"symlink to secret-looking file", "├── link -> .env.local", true},
		{"symlink to secret directory", "└── link -> secrets", true},
		{"symlink to ordinary directory", "├── link -> sub", false},
		{"dangling symlink to absolute path", "├── link -> /nonexistent/nowhere", false},
		{"symlink to secret-looking path, ordinary base", "├── link -> /etc/private/hosts", false},
		{"nested symlink to secret", "│   └── link -> Tokens.md", true},
		{"trailing spaces after target", "├── link -> README.md   ", false},

		// A name containing "/" is given the benefit of the doubt: the line is
		// kept rather than risk dropping real source. The root line of every
		// absolute-path run goes through this branch. The arrow is checked
		// first, so a name that merely contains " -> " is judged on what
		// follows the arrow -- wrong, but what the dump has always printed.
		{"absolute root", "/home/user/project", false},
		{"path in name", "├── a/b", false},
		{"name containing an arrow", "├── weird -> arrow.txt", false},
		{"arrow then a path", "├── link -> a/b", false},

		// Spaces, unicode, quotes and control bytes must not confuse it.
		{"spaces", "├── name with spaces.txt", false},
		{"quotes", "├── quoted\"name.txt", false},
		{"backslash", "├── back\\slash.txt", false},
		{"unicode", "├── café ☕ ünïcödé.txt", false},
		{"emoji", "├── emoji 🎉 party.txt", false},
		{"invalid utf-8", "├── caf\xff.txt", false},
		{"trailing tab", "├── tab\tinside.txt", false},
		{"secret word mid-name is not the word", "├── my private notes.txt", false},

		// The report lines are counts, not names, and must reach the dump:
		// dropping them would hide how big the tree was.
		{"summary", "3 directories, 5 files", false},
		{"summary singular", "1 directory, 1 file", false},
		{"files only", "12 files", false},
		{"file singular", "1 file", false},
		{"zero directories", "0 directories, 0 files", false},
	}

	for _, c := range cases {
		if got := filterTreeLine(c.line); got != c.want {
			t.Errorf("filterTreeLine(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}
