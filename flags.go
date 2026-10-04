// Command-line parsing, flag validation, and help text.

package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2/styles"
)

// validateConfig performs final validation on the parsed configuration.
// It checks for conflicting options and invalid combinations.
func validateConfig(cfg *Config) error {
	// JSONL is a mode inside JSON mode, so --jsonl sets JSON as well and
	// JSON&&JSONL cannot tell the two flags apart. Only an explicitly typed
	// --json conflicts with --jsonl; on its own --jsonl is the common case.
	if cfg.jsonExplicit && cfg.JSONL {
		return fmt.Errorf("--json and --jsonl are mutually exclusive")
	}
	if cfg.MaxSize < 0 {
		return fmt.Errorf("--max-size must be non-negative")
	}
	if cfg.Runs < 0 || cfg.Runs > 10000 {
		return fmt.Errorf("--runs must be between 0 and 10000")
	}
	if cfg.Warmup < 0 || cfg.Warmup > 10000 {
		return fmt.Errorf("--warmup must be between 0 and 10000")
	}
	return nil
}

// parseArgs parses command-line arguments and returns a Config struct.
// It handles all flags, validation, and default values.
func parseArgs() *Config {
	return parseArgsFrom(os.Args[1:])
}

// parseArgsFrom parses the given arguments (without the program name) and
// returns a Config struct. It is the body of parseArgs, split out so tests
// can exercise flag handling without reading os.Args.
func parseArgsFrom(args []string) *Config {
	cfg := &Config{
		Exclude:           make(map[string]bool),
		Include:           make(map[string]bool),
		excludeAbsPaths:   make(map[string]bool),
		IgnoreVenv:        true,
		Warmup:            1,
		OmittedDisclaimer: true,
	}

	if exe, err := os.Executable(); err == nil {
		if resolved, linkErr := filepath.EvalSymlinks(exe); linkErr == nil {
			exe = resolved
		}
		if abs, absErr := filepath.Abs(exe); absErr == nil {
			cfg.excludeAbsPaths[abs] = true
			if fi, statErr := os.Stat(abs); statErr == nil {
				cfg.exeInode = getInode(fi)
			}
		}
	}

	colorExplicit := false

	knownFlags := map[string]bool{
		"--output": true, "--ignore-venv": true, "--include-venv": true,
		"--include-binary": true, "--include-binaries": true, "--theme": true,
		"--list-themes": true, "--color": true, "--highlight": true,
		"--no-color": true, "--stdout-safe": true, "--force": true,
		"--overwrite": true, "--json": true, "--jsonl": true, "--omitted-disclaimer": true,
		"--no-omitted-disclaimer": true,
		"--follow-symlinks":       true, "--exclude": true, "--ignore": true,
		"--include":  true,
		"--max-size": true, "--version": true, "-v": true,
		"--benchmark": true, "--bench": true, "--runs": true, "--warmup": true,
		"--help": true, "-h": true,
	}

	for i := 0; i < len(args); i++ {
		a := args[i]

		if !strings.HasPrefix(a, "-") {
			info, err := os.Stat(a)
			if err == nil && info.IsDir() {
				cfg.InputDirs = append(cfg.InputDirs, a)
				continue
			}
			if err == nil && !info.IsDir() {
				fmt.Fprintf(os.Stderr, "error: %q exists and is not a directory; refusing to use it as output. Use --output <path> (and --force to overwrite).\n", a)
				os.Exit(1)
			}
			if cfg.OutputPath == "" {
				cfg.OutputPath = a
				continue
			}
			fmt.Fprintf(os.Stderr, "error: unexpected argument %q (an output path was already given)\n", a)
			os.Exit(1)
		}

		if a == "--" {
			fmt.Fprintln(os.Stderr, "error: -- is not supported")
			os.Exit(1)
		}

		switch a {
		case "--output":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --output requires a file path argument")
				os.Exit(1)
			}
			if knownFlags[args[i]] && args[i] != "-" {
				fmt.Fprintf(os.Stderr, "error: --output requires a file path, got flag %q\n", args[i])
				os.Exit(1)
			}
			cfg.OutputPath = args[i]

		case "--ignore-venv":
			cfg.IgnoreVenv = true

		case "--include-venv":
			cfg.IgnoreVenv = false

		case "--include-binary", "--include-binaries":
			cfg.IncludeBinaries = true

		case "--theme":
			i++
			if i >= len(args) || (knownFlags[args[i]] && args[i] != "-") {
				fmt.Fprintln(os.Stderr, "error: --theme requires a theme name argument")
				os.Exit(1)
			}
			if !validTheme(args[i]) {
				fmt.Fprintf(os.Stderr, "error: unknown theme %q (see --list-themes)\n", args[i])
				os.Exit(1)
			}
			cfg.Theme = args[i]
			cfg.Color = true
			if err := saveTheme(cfg.Theme); err != nil {
				fmt.Fprintln(os.Stderr, "warning: failed to save theme preference:", err)
			}

		case "--list-themes":
			for _, name := range styles.Names() {
				fmt.Println(name)
			}
			os.Exit(0)

		case "--color", "--highlight":
			cfg.Color = true
			colorExplicit = true
			if err := saveColor(true); err != nil {
				fmt.Fprintln(os.Stderr, "warning: failed to save color preference:", err)
			}

		case "--no-color":
			cfg.Color = false
			colorExplicit = true
			if err := saveColor(false); err != nil {
				fmt.Fprintln(os.Stderr, "warning: failed to save color preference:", err)
			}

		case "--stdout-safe":
			cfg.StdoutSafe = true

		case "--force", "--overwrite":
			cfg.Force = true

		case "--json":
			cfg.JSON = true
			cfg.jsonExplicit = true

		case "--jsonl":
			cfg.JSON = true
			cfg.JSONL = true

		case "--omitted-disclaimer":
			// Deprecated alias: the disclaimer is on by default, so this is a
			// harmless no-op kept only for existing scripts.
			fmt.Fprintln(os.Stderr, "warning: --omitted-disclaimer is now the default and will be removed; use --no-omitted-disclaimer to disable")

		case "--no-omitted-disclaimer":
			cfg.OmittedDisclaimer = false

		case "--follow-symlinks":
			cfg.FollowSymlinks = true

		case "--exclude", "--ignore":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --exclude/--ignore requires a comma-separated list argument")
				os.Exit(1)
			}
			for _, name := range strings.Split(args[i], ",") {
				name = filepath.FromSlash(strings.TrimSpace(name))
				if name != "" {
					cfg.Exclude[name] = true
				}
			}

		case "--include":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --include requires a comma-separated list argument")
				os.Exit(1)
			}
			for _, name := range strings.Split(args[i], ",") {
				name = filepath.FromSlash(strings.TrimSpace(name))
				if name != "" {
					cfg.Include[name] = true
				}
			}

		case "--max-size":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --max-size requires a size argument (e.g. 1MB, 500KB)")
				os.Exit(1)
			}
			size, sizeErr := parseSize(args[i])
			if sizeErr != nil {
				fmt.Fprintf(os.Stderr, "error: --max-size: %v\n", sizeErr)
				os.Exit(1)
			}
			cfg.MaxSize = size

		case "--version", "-v":
			fmt.Println("everything", version)
			os.Exit(0)

		case "--benchmark", "--bench":
			cfg.Benchmark = true

		case "--runs":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --runs requires an integer argument")
				os.Exit(1)
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > 10000 {
				fmt.Fprintln(os.Stderr, "error: --runs requires a positive integer (max 10000)")
				os.Exit(1)
			}
			cfg.Runs = n

		case "--warmup":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --warmup requires an integer argument")
				os.Exit(1)
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 || n > 10000 {
				fmt.Fprintln(os.Stderr, "error: --warmup requires a non-negative integer (max 10000)")
				os.Exit(1)
			}
			cfg.Warmup = n

		case "--help", "-h":
			printHelp()
			os.Exit(0)

		default:
			fmt.Fprintf(os.Stderr, "error: unknown flag %q (see --help)\n", a)
			os.Exit(1)
		}
	}

	if err := validateConfig(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if !colorExplicit && cfg.OutputPath == "" && loadSavedColor() && isInteractive() {
		cfg.Color = true
	}

	if cfg.Color && cfg.Theme == "" {
		if t := loadSavedTheme(); t != "" {
			cfg.Theme = t
		}
		if cfg.Theme == "" {
			cfg.Theme = "monokai"
		}
	}

	return cfg
}

// validTheme checks if a theme name is valid by comparing against available chroma styles.
func validTheme(name string) bool {
	lower := strings.ToLower(name)
	for _, n := range styles.Names() {
		if strings.ToLower(n) == lower {
			return true
		}
	}
	return false
}

// printHelp prints the help message to stdout.
func printHelp() {
	fmt.Println(`everything – dump your project into a flat file

Dumps the paths + contents of every file in a project into a single stream,
so you can paste it into an LLM, grep it, or keep it as a snapshot. Junk,
binaries, and secrets are skipped automatically.

Usage:
  everything [flags] [input-dirs...] [output-path]

Positional arguments:
  Directories are scanned as input (default: scan ".").
  Any other non-flag argument is used as the output file path — but only if
  it does not already exist. Existing files are never silently clobbered by
  a positional argument; use --output (and --force) explicitly for that.

Output:
  --output <path>       Write to a file instead of stdout. The output file
                        is excluded from the scan, an existing file is
                        never overwritten unless you pass --force, and
                        symlinks are refused outright.
  --force, --overwrite  Allow overwriting an existing output file.
  --stdout-safe         Refuse to dump to an interactive terminal unless
                        --output is given.
  --json                Emit one JSON document: an array of {"path","content"}
                        objects, streamed to disk (no in-memory whole-output
                        buffering). No tree banner, no color.
  --jsonl               Emit JSON Lines instead: one {"path","content"}
                        object per line. Implies --json; mutually exclusive
                        with it. Pairs well with jq -s / streaming parsers.

Filtering:
  --exclude, --ignore <list>      Comma-separated names or paths to skip. Matching is
                        exact (file/dir name or path) - no globs.
  --max-size <size>     Skip files larger than this (B, KB, MB, GB, TB;
                        e.g. 1MB, 500KB). Omit or 0 for no limit. Invalid
                        values are an error, not "no limit".
  --include-binaries    Include binary files (skipped by default).
  --include-binary*     Alias for --include-binaries.
  --follow-symlinks     Read file symlinks instead of skipping them.
                        Directory symlinks are still skipped (cycle safety),
                        and special files (pipes/devices/sockets) always are.
  --include-venv        Stop auto-skipping .venv, venv, __pycache__,
                        node_modules (they're skipped by default).
  --include <list>      Comma-separated names or paths to scan even though they
                        are skipped by default (e.g. --include ".build,dist").
                        Undoes a default only: --exclude still wins.
  --no-omitted-disclaimer
                        Don't print the list of skipped files to stderr after
                        the scan (the list is printed by default).

Appearance:
  --color, --highlight  Syntax-highlight the output (preference is saved;
                        a saved preference only auto-applies on a real
                        terminal, so pipes/files stay clean unless you ask).
  --no-color            Turn coloring off again (preference is saved).
  --theme <name>        Highlight theme; implies --color. Default: monokai.
                        Invalid names are rejected; nothing is saved.
  --list-themes         Print every theme name accepted by --theme.

Other:
  --benchmark, --bench  Time a traversal instead of writing a snapshot.
                          Reads and counts the full content of every included
                          file (same filtering as a real dump: max-size,
                          binary and secret skipping), then reports file/dir
                          counts, total logical bytes, bytes actually read,
                          lines of code, characters, traversal rate, and peak
                          heap used. A warmup pass runs first so timings
                          reflect a warm OS page cache.
  --runs <n>            With --benchmark, repeat the traversal n times after
                          the warmup pass and report min/median/mean/max
                          times. Rate uses min. Default: 1.
  --warmup <n>          With --benchmark, untimed warmup passes before the
                          timed ones (default: 1). Pass 0 to measure a cold
                          cache.
  --version, -v         Print the version and exit.
  --help, -h            Print this help and exit.

Always skipped: .git, target/, build/, dist/, out/, bin/, vendor/, _build/,
.next/, .nuxt/, .svelte-kit/, .astro/, .cache/, .turbo/, .gradle/, .build/,
DerivedData/, Pods/, .tox/, .pytest_cache/, .mypy_cache/, .ruff_cache/,
htmlcov/, .nyc_output/, .terraform/, elm-stuff/, .dart_tool/, .stack-work/,
temp/, tmp/, logs/, .DS_Store, ._*, .vscode/, .idea/, .eclipse/, .settings/,
minified bundles (*.min.js, *.min.css), source maps (*.map), coverage reports,
*.log, symlinks (unless --follow-symlinks), pipes/devices/sockets,
binaries (unless --include-binaries), and secret-looking files: .env*, *.env,
id_rsa*/id_ed25519*/id_dsa*/id_ecdsa*, *.pem, *.key, *.p12, *.pfx, *.jks,
*.keystore, *.kdbx, *.crt, *.cer, *.der, *.csr, *.asc, *.gpg, *.pgp,
credentials*, client_secret*, *service-account*.json, secrets.*, secret*,
auth*, token*, password*, api_key*, private*, aws*, .netrc, .htpasswd,
.npmrc, .pypirc, .git-credentials, plus any file whose content contains a
PEM private key block. Skipped files are listed on stderr after the scan —
check that list before sharing; use --no-omitted-disclaimer to silence it.

Examples:
  everything --output snapshot.txt                recommended starting point
  everything --color --output out.txt             syntax highlighted file
  everything src/ lib/ --output ctx.txt           scan specific directories
  everything --exclude "vendor,tmp" --force --output clean.txt
  everything --max-size 1MB --output trimmed.txt   skip big files
  everything --json --output out.json              one JSON document (array)
  everything --jsonl --output out.jsonl            JSON Lines for scripts
  everything --color | less -R                    paged, highlighted viewing
  everything | grep "TODO"                        search the whole project
  everything --output ctx.txt                     see what got left out`)
}

// parseSize parses a human-readable size string (e.g., "1MB", "500KB") into bytes.
// Returns an error if the format is invalid or the value overflows int64.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}

	var multiplier int64 = 1
	switch {
	case strings.HasSuffix(s, "TB"):
		multiplier = 1 << 40
		s = strings.TrimSuffix(s, "TB")
	case strings.HasSuffix(s, "GB"):
		multiplier = 1 << 30
		s = strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		multiplier = 1 << 20
		s = strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		multiplier = 1 << 10
		s = strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}

	s = strings.TrimSpace(s)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q (expected e.g. 500KB, 1MB)", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("size must not be negative")
	}
	if multiplier > 1 && n > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("size overflows int64")
	}

	return n * multiplier, nil
}
