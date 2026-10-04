// What gets left out of a dump: the generated/dependency directory lists, the
// user-facing skip decision, and the `tree` renderer that has to agree with it.

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// IGNORE PATTERNS
//
// defaultIgnoreDirs is the single source of truth for directory names that are
// generated, cached, vendored, or otherwise not hand-written. It is consulted
// by shouldSkip and, through treeIgnoreDirsPattern, by the `tree` renderer, so
// the two cannot drift apart the way a hand-copied regex did.
//
// Every entry is matched against a single path component (the directory's own
// name), never a substring, so "bin" does not swallow "combine.go".
var defaultIgnoreDirs = []string{
	// Version control
	".git", ".svn", ".hg", ".bzr",

	// Compiler and bundler output
	"target", "build", "dist", "out", "bin", "vendor",
	"_build", // Elixir

	// Xcode / Swift
	".build", "DerivedData", "Pods", "Carthage",

	// Java / JVM
	".gradle", ".m2", ".ivy2",

	// JavaScript / TypeScript framework caches
	".next", ".nuxt", ".svelte-kit", ".astro", ".docusaurus", ".parcel-cache",
	".turbo", ".angular", ".expo", ".vercel", ".netlify",

	// Other language tool caches
	".dart_tool", ".pub-cache", "elm-stuff", ".stack-work",
	".tox", ".nox", ".eggs", ".ipynb_checkpoints", ".ropeproject",
	".terraform",

	// Test and coverage output
	"coverage", "htmlcov", ".nyc_output", ".pytest_cache", ".mypy_cache",
	".ruff_cache", ".cache",

	// Transient scratch
	"temp", "tmp", ".tmp", "logs",

	// Editor and IDE state
	".vscode", ".idea", ".eclipse", ".settings", ".fleet", ".history",
}

// venvIgnoreDirs are dependency trees that are skipped by default but are
// reversible with a single switch, because unlike build output they are large
// enough that someone scanning a vendored tree may actually want them.
var venvIgnoreDirs = []string{
	".venv", "venv", "__pycache__", "node_modules",
}

// defaultIgnoreFiles are generated artifacts that are text, so the binary
// sniff does not catch them. Compiled objects (.o, .so, .exe, .pyc) are
// deliberately absent: those are already binary and skipped as such.
var defaultIgnoreFiles = []string{
	// Minified bundles and source maps
	"*.min.js", "*.min.mjs", "*.min.css", "*.map",

	// Coverage reports
	".coverage", "lcov.info", "*.lcov",

	// Transient output and editor leftovers
	"*.log", "*.tmp", "*.swp", "*.swo", "*~",
	"npm-debug.log", "yarn-error.log",
}

var (
	defaultIgnoreDirSet = nameSet(defaultIgnoreDirs)
	venvIgnoreDirSet    = nameSet(venvIgnoreDirs)
)

// nameSet turns a list of exact names into a lookup set.
func nameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// matchesAnyPattern reports whether name matches any of the given glob
// patterns. Patterns are matched against the single path component, so a "*"
// never crosses a directory separator.
func matchesAnyPattern(name string, patterns []string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// treeSecretPatterns are the secret-looking name globs handed to `tree`.
// Kept verbatim from the original pattern: `tree -I` matches one path
// component at a time, and these forms are the ones that have always worked.
var treeSecretPatterns = []string{
	"*.pem", "*.key", "*.p12", "*.pfx", "*.jks", "*.keystore", "*.jceks",
	"*.kdbx", "*.crt", "*.cer", "*.der", "*.csr", "*.asc", "*.gpg", "*.pgp",
	"credentials*", "client_secret*", "client-secret*", "*service-account*",
	"secrets.*", "secret*", "auth*", "token*", "password*", "api_key*",
	"private*", "aws*",
	"*.env", "id_rsa*", "id_ed25519*", "id_ecdsa*", "id_dsa*",
}

// treeIgnorePattern renders every ignore list into the single alternation that
// `tree -I` expects. Deriving it here is the point: the renderer and the walker
// now read the same lists, so a directory skipped by shouldSkip cannot be
// missing from the rendered tree.
//
// One pattern has to serve two matchers, which differ in syntax. `tree -I`
// applies fnmatch-style globs to each path component, while Go's regexp wants
// `.pem` rather than a leading `*.pem`. Keeping the globs here and stripping
// the leading star for the Go-side assertions keeps both honest; see
// treeIgnorePatternRe.
var treeIgnorePattern = strings.Join(append(
	append(append([]string{}, defaultIgnoreDirs...), venvIgnoreDirs...),
	append(append([]string{}, defaultIgnoreFiles...), treeSecretPatterns...)...,
), "|")

// treeIgnorePatternRe is treeIgnorePattern rewritten for Go's regexp engine, so
// tests can assert the rendered pattern actually covers the lists. The leading
// "*" of a glob becomes "." (any character) and "." becomes "\.".
var treeIgnorePatternRe = regexp.MustCompile(
	strings.NewReplacer(".", `\.`, "*", ".").Replace(treeIgnorePattern),
)

func filterTreeLine(line string) bool {
	if idx := strings.Index(line, " -> "); idx >= 0 {
		target := strings.TrimSpace(line[idx+4:])
		return isSecretFilename(filepath.Base(target))
	}
	name := line
	for _, sep := range []string{"├── ", "└── ", "│   "} {
		if idx := strings.LastIndex(name, sep); idx >= 0 {
			name = name[idx+len(sep):]
			break
		}
	}
	if idx := strings.IndexByte(name, '/'); idx >= 0 {
		return false
	}
	return isSecretFilename(name)
}

// tryPrintTree attempts to print a directory tree using the 'tree' command if available.
// It filters out secret files and directories from the output.
func tryPrintTree(writer io.Writer, roots []string) {
	bin, err := exec.LookPath("tree")
	if err != nil {
		return
	}

	for _, root := range roots {
		var filtered bytes.Buffer
		cmd := exec.Command(bin, "-n", "-I", treeIgnorePattern, root)
		cmd.Stdout = &filtered
		cmd.Stderr = nil

		runErr := cmd.Run()

		for _, line := range strings.Split(strings.TrimSuffix(filtered.String(), "\n"), "\n") {
			if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "0 directories") ||
				strings.Contains(line, " directories, ") || strings.HasSuffix(line, " files") {
				fmt.Fprintln(writer, line)
				continue
			}
			if filterTreeLine(line) {
				continue
			}
			fmt.Fprintln(writer, line)
		}
		fmt.Fprint(writer, "\n")

		if runErr != nil && filtered.Len() == 0 {
			return
		}
	}
}

//
// SKIP LOGIC (unified = single source of truth)
//

// shouldSkip determines if a file or directory should be skipped during traversal.
// It checks inode conflicts, built-in skip patterns, user exclusions, and secret files.
func shouldSkip(path string, d os.DirEntry, cfg *Config) bool {
	base := d.Name()

	if cfg.stdoutInode != 0 || cfg.outputInode != 0 || cfg.exeInode != 0 {
		if fi, err := os.Stat(path); err == nil {
			if ino := getInode(fi); ino != 0 &&
				(ino == cfg.stdoutInode || ino == cfg.outputInode || ino == cfg.exeInode) {
				return true
			}
		}
	}

	if base == ".DS_Store" || strings.HasPrefix(base, "._") {
		return true
	}

	// Precedence, most specific first:
	//
	//  1. an explicit --exclude, because naming a path is a deliberate act
	//     and --include exists to undo a default, not to argue with a choice;
	//  2. the default generated/cache directories, which --include can lift;
	//  3. the dependency trees behind --ignore-venv.
	if cfg.Exclude[base] || cfg.Exclude[path] {
		return true
	}

	included := cfg.Include[base] || cfg.Include[path]
	if !included {
		if defaultIgnoreDirSet[base] {
			return true
		}
		if cfg.IgnoreVenv && venvIgnoreDirSet[base] {
			return true
		}
		if matchesAnyPattern(base, defaultIgnoreFiles) {
			return true
		}
	}

	if len(cfg.excludeAbsPaths) > 0 {
		if abs, err := filepath.Abs(path); err == nil && cfg.excludeAbsPaths[abs] {
			return true
		}
	}

	if isSecretFilename(base) {
		return true
	}

	return false
}
