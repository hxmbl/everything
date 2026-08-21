package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

var version = "v1.3.3"

type Config struct {
	OutputPath string
	InputDirs  []string
	Exclude    map[string]bool

	MaxSize int64

	IgnoreVenv        bool
	Force             bool
	IncludeBinaries   bool
	StdoutSafe        bool
	Color             bool
	Theme             string
	FollowSymlinks    bool
	JSON              bool
	OmittedDisclaimer bool
	SkippedFiles      []string
	stdoutInode       uint64
	Benchmark         bool
	Runs              int
}

func isInteractive() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func tryPrintTree(writer io.Writer) {
	cmd := exec.Command("tree", "-n", "-I", "target")

	cmd.Stdout = writer
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return
	}

	fmt.Fprint(writer, "\n")
}

func main() {
	cfg := parseArgs()

	if cfg.Benchmark {
		runBenchmark(cfg)
		os.Exit(0)
	}

	if cfg.Color && cfg.Theme == "" {
		if t := loadSavedTheme(); t != "" {
			cfg.Theme = t
		}
	}

	if cfg.OutputPath == "" && isInteractive() && !cfg.StdoutSafe {
		fmt.Fprintln(os.Stderr, "Warning: large stdout dumps can break shell input. Use --output or pipe to less.")
		fmt.Fprintln(os.Stderr, "Tip: everything --output snapshot.txt")
	}

	if cfg.OutputPath == "" && isInteractive() && cfg.StdoutSafe && !cfg.Force {
		fmt.Fprintln(os.Stderr, "Refusing unsafe raw stdout dump. Use --output to write to a file.")
		os.Exit(1)
	}

	writer, cleanup := setupOutput(cfg)
	defer cleanup()

	if !cfg.JSON {
		tryPrintTree(writer)
	}

	walkDirs := cfg.InputDirs
	if len(walkDirs) == 0 {
		walkDirs = []string{"."}
	}

	for _, root := range walkDirs {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}

			if shouldSkip(path, d, cfg) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			if d.IsDir() {
				return nil
			}

			fi, err := os.Lstat(path)
			if err == nil && fi.Mode()&os.ModeSymlink != 0 {
				if !cfg.FollowSymlinks {
					cfg.SkippedFiles = append(cfg.SkippedFiles, fmt.Sprintf("  symlink: %s", path))
					return nil
				}
				if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
					cfg.SkippedFiles = append(cfg.SkippedFiles, fmt.Sprintf("  dir symlink: %s", path))
					return nil
				}
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}

			if cfg.MaxSize > 0 && info.Size() > cfg.MaxSize {
				cfg.SkippedFiles = append(cfg.SkippedFiles, fmt.Sprintf("  too large: %s (%d bytes)", path, info.Size()))
				return nil
			}

			if looksLikePrivateKey(path) {
				cfg.SkippedFiles = append(cfg.SkippedFiles, fmt.Sprintf("  secret: %s", path))
				return nil
			}

			data, err := readFileFiltered(path, cfg.IncludeBinaries)
			if err != nil {
				return nil
			}
			if data == nil {
				cfg.SkippedFiles = append(cfg.SkippedFiles, fmt.Sprintf("  binary: %s", path))
				return nil
			}

			if cfg.JSON {
				type jsonLine struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				}
				b, _ := json.Marshal(jsonLine{Path: path, Content: string(data)})
				writer.Write(b)
				writer.Write([]byte("\n"))
			} else {
				fmt.Fprintf(writer, "==== FILE: %s ====\n", path)
				if cfg.Color {
					lexer := lexers.Match(path)
					if lexer == nil {
						lexer = lexers.Fallback
					}
					lexer = chroma.Coalesce(lexer)
					iterator, err := lexer.Tokenise(nil, string(data))
					if err == nil {
						formatter := formatters.Get("terminal")
						if formatter == nil {
							formatter = formatters.Fallback
						}
						themeName := cfg.Theme
						if themeName == "" {
							themeName = "monokai"
						}
						formatter.Format(writer, styles.Get(themeName), iterator)
					}
				} else {
					writer.Write(data)
				}
				writer.Write([]byte("\n\n"))
			}

			return nil
		})

		if err != nil {
			panic(err)
		}
	}

	if cfg.OmittedDisclaimer && len(cfg.SkippedFiles) > 0 {
		fmt.Fprintln(os.Stderr, "---")
		fmt.Fprintln(os.Stderr, "Omitted files:")
		for _, s := range cfg.SkippedFiles {
			fmt.Fprintln(os.Stderr, s)
		}
	}
}

//
// BENCHMARK
//

func runBenchmark(cfg *Config) {
	walkDirs := cfg.InputDirs
	if len(walkDirs) == 0 {
		walkDirs = []string{"."}
	}

	runs := cfg.Runs
	if runs < 1 {
		runs = 1
	}

	type runResult struct {
		files, dirs, totalBytes int64
		elapsed                 time.Duration
		memUsed                 int64
	}

	results := make([]runResult, 0, runs)

	for r := 0; r < runs; r++ {
		runtime.GC()
		var memBefore runtime.MemStats
		runtime.ReadMemStats(&memBefore)

		start := time.Now()
		files, dirs, totalBytes := benchTraverse(cfg, walkDirs)
		elapsed := time.Since(start)

		var memAfter runtime.MemStats
		runtime.ReadMemStats(&memAfter)
		memUsed := int64(memAfter.Alloc) - int64(memBefore.Alloc)

		results = append(results, runResult{files, dirs, totalBytes, elapsed, memUsed})
	}

	var files, dirs, totalBytes int64
	var durs []time.Duration
	var sumDur time.Duration
	var sumMem int64
	for _, res := range results {
		files = res.files
		dirs = res.dirs
		totalBytes = res.totalBytes
		durs = append(durs, res.elapsed)
		sumDur += res.elapsed
		sumMem += res.memUsed
	}

	minDur := durs[0]
	maxDur := durs[0]
	for _, d := range durs {
		if d < minDur {
			minDur = d
		}
		if d > maxDur {
			maxDur = d
		}
	}
	medianDur := medianDuration(durs)
	meanDur := sumDur / time.Duration(len(durs))
	meanMem := sumMem / int64(len(durs))

	pathLabel := strings.Join(walkDirs, ", ")
	if pathLabel == "." {
		pathLabel = "./"
	}

	fmt.Println("Everything Benchmark")
	fmt.Println()
	printStat("Path:", pathLabel)
	printStat("Runs:", formatNum(int64(runs)))
	fmt.Println()

	printStat("Files:", formatNum(files))
	printStat("Directories:", formatNum(dirs))
	printStat("Bytes:", formatBytes(totalBytes))
	fmt.Println()

	fmt.Printf("%-13s%d runs\n", "Traversal:", runs)
	printStat("  min:", formatDuration(minDur))
	printStat("  median:", formatDuration(medianDur))
	printStat("  mean:", formatDuration(meanDur))
	printStat("  max:", formatDuration(maxDur))
	fmt.Println()

	printStat("Rate (mean):", fmt.Sprintf("%s files/s", formatNum(int64(float64(files)/meanDur.Seconds()))))
	fmt.Printf("%-13s%s/s\n", "", formatBytes(int64(float64(totalBytes)/meanDur.Seconds())))
	fmt.Println()

	printStat("Memory (mean):", formatBytes(meanMem))
	fmt.Println()
	fmt.Printf("version:     %s\n", version)
}

func benchTraverse(cfg *Config, walkDirs []string) (files, dirs, totalBytes int64) {
	for _, root := range walkDirs {
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}

			if shouldSkip(path, d, cfg) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			if d.IsDir() {
				dirs++
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}

			files++
			totalBytes += info.Size()

			return nil
		})
	}
	return
}

func medianDuration(durs []time.Duration) time.Duration {
	n := len(durs)
	if n == 0 {
		return 0
	}
	sorted := make([]time.Duration, n)
	copy(sorted, durs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func printStat(label, value string) {
	fmt.Printf("%-13s%s\n", label, value)
}

func formatNum(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	val := float64(b) / float64(unit)
	for _, u := range []string{"KB", "MB", "GB", "TB", "PB"} {
		if val < float64(unit) {
			return fmt.Sprintf("%.2f %s", val, u)
		}
		val /= float64(unit)
	}
	return fmt.Sprintf("%.2f PB", val)
}

func formatDuration(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.2f s", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000.0)
	default:
		return fmt.Sprintf("%.1f µs", float64(d.Nanoseconds())/1000.0)
	}
}

//

// CONFIG
//

func parseArgs() *Config {
	cfg := &Config{
		Exclude:    make(map[string]bool),
		IgnoreVenv: true,
	}

	if exe, err := os.Executable(); err == nil {
		cfg.Exclude[exe] = true
		cfg.Exclude[filepath.Base(exe)] = true
	}

	colorExplicit := false

	args := os.Args[1:]

	for i := 0; i < len(args); i++ {
		a := args[i]

		if !strings.HasPrefix(a, "-") {
			if info, err := os.Stat(a); err == nil && info.IsDir() {
				cfg.InputDirs = append(cfg.InputDirs, a)
				continue
			}
			if cfg.OutputPath == "" {
				cfg.OutputPath = a
				continue
			}
		}

		switch a {
		case "--output":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --output requires a file path argument")
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
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --theme requires a theme name argument")
				os.Exit(1)
			}
			cfg.Theme = args[i]
			cfg.Color = true
			saveTheme(cfg.Theme)

		case "--list-themes":
			for _, name := range styles.Names() {
				fmt.Println(name)
			}
			os.Exit(0)

		case "--color", "--highlight":
			cfg.Color = true
			colorExplicit = true
			saveColor(true)

		case "--no-color":
			cfg.Color = false
			colorExplicit = true
			saveColor(false)

		case "--stdout-safe":
			cfg.StdoutSafe = true

		case "--force", "--overwrite":
			cfg.Force = true

		case "--json":
			cfg.JSON = true

		case "--omitted-disclaimer":
			cfg.OmittedDisclaimer = true

		case "--follow-symlinks":
			cfg.FollowSymlinks = true

		case "--exclude", "--ignore":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --exclude/--ignore requires a comma-separated list argument")
				os.Exit(1)
			}
			for _, name := range strings.Split(args[i], ",") {
				cfg.Exclude[strings.TrimSpace(name)] = true
			}

		case "--max-size":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --max-size requires a size argument (e.g. 1MB, 500KB)")
				os.Exit(1)
			}
			cfg.MaxSize = parseSize(args[i])

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
			if err != nil || n < 1 {
				fmt.Fprintln(os.Stderr, "error: --runs requires a positive integer")
				os.Exit(1)
			}
			cfg.Runs = n

		case "--help", "-h":
			printHelp()
			os.Exit(0)
		}
	}

	if !colorExplicit && loadSavedColor() {
		cfg.Color = true
	}

	return cfg
}

func printHelp() {
	fmt.Println(`everything – dump your project into a flat file

Dumps the paths + contents of every file in a project into a single stream,
so you can paste it into an LLM, grep it, or keep it as a snapshot. Junk,
binaries, and secrets are skipped automatically.

Usage:
  everything [flags] [input-dirs...] [output-path]

Positional arguments:
  Directories are scanned as input (default: scan ".").
  Any other non-flag argument is used as the output file path. Tip: a typo
  in a directory name just becomes a file you didn't mean to write.

Output:
  --output <path>       Write to a file instead of stdout. The output file
                        is excluded from the scan, and an existing file is
                        never overwritten unless you pass --force.
  --force, --overwrite  Allow overwriting an existing output file.
  --stdout-safe         Refuse to dump to an interactive terminal unless
                        --output is given.
  --json                Emit JSON Lines (one {"path","content"} object per
                        line). No tree banner, no color.

Filtering:
  --exclude, --ignore <list>      Comma-separated names or paths to skip. Matching is
                        exact (file/dir name or path) - no globs.
  --max-size <size>     Skip files larger than this (B, KB, MB, GB, TB;
                        e.g. 1MB, 500KB). Omit or 0 for no limit.
  --include-binaries    Include binary files (skipped by default).
  --include-binary*     Alias for --include-binaries.
  --follow-symlinks     Follow symlinks instead of skipping them.
  --include-venv        Stop auto-skipping .venv, venv, __pycache__,
                        node_modules (they're skipped by default).
  --omitted-disclaimer  Print the list of skipped files to stderr after the
                        scan finishes.

Appearance:
  --color, --highlight  Syntax-highlight the output (preference is saved).
  --no-color            Turn coloring off again (preference is saved).
  --theme <name>        Highlight theme; implies --color. Default: monokai.
  --list-themes         Print every theme name accepted by --theme.

Other:
  --benchmark, --bench  Time a traversal instead of writing a snapshot.
                          Reports file/dir counts, total bytes, traversal
                          rate, and memory used. A quick "view" of a project's
                          size and how fast everything can scan it.
  --runs <n>            With --benchmark, repeat the traversal n times and
                          report min/median/mean/max traversal times, plus
                          mean memory usage. Default: 1.
  --version, -v         Print the version and exit.
  --help, -h            Print this help and exit.

Always skipped: .git, target/, .DS_Store, ._*, symlinks (unless
--follow-symlinks), binaries (unless --include-binaries), secret files
(.env*, id_rsa*/id_ed25519*/id_ecdsa*, *.pem, *.key, *.p12, *.pfx, *.jks,
credentials, .netrc, .htpasswd, PEM private keys)

Examples:
  everything --output snapshot.txt                recommended starting point
  everything --color --output out.txt             syntax highlighted file
  everything src/ lib/ --output ctx.txt           scan specific directories
  everything --exclude "vendor,tmp" --force --output clean.txt
  everything --max-size 1MB --output trimmed.txt   skip big files
  everything --json --output out.jsonl            for scripts
  everything --color | less -R                    paged, highlighted viewing
  everything | grep "TODO"                        search the whole project
  everything --omitted-disclaimer --output ctx.txt  see what got left out`)
}

func parseSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	s = strings.ToUpper(s)

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
		multiplier = 1
		s = strings.TrimSuffix(s, "B")
	}

	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}

	return n * multiplier
}

//
// OUTPUT SAFETY
//

func validateOutputPath(path string) error {
	if path == "/dev/stdin" || path == "/dev/stdout" || path == "/dev/stderr" {
		return fmt.Errorf("refusing to write to %s", path)
	}
	if abs, _ := filepath.Abs(path); filepath.Dir(abs) == "/dev" {
		return fmt.Errorf("refusing to write to device file: %s", abs)
	}
	return nil
}

func setupOutput(cfg *Config) (io.Writer, func()) {
	if cfg.OutputPath == "" {
		if fi, err := os.Stdout.Stat(); err == nil {
			cfg.stdoutInode = getInode(fi)
		}
		return os.Stdout, func() {}
	}

	absOut, _ := filepath.Abs(cfg.OutputPath)

	if err := validateOutputPath(cfg.OutputPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cfg.Exclude[absOut] = true
	cfg.Exclude[filepath.Base(cfg.OutputPath)] = true

	absProj, _ := filepath.Abs(".")
	if strings.HasPrefix(absOut, filepath.Clean(absProj)+string(filepath.Separator)) {
		cfg.Exclude[absOut] = true
	}

	if !cfg.Force {
		if _, err := os.Stat(cfg.OutputPath); err == nil {
			fmt.Fprintf(os.Stderr, "Refusing to overwrite existing file: %s. Use --force to overwrite.\n", cfg.OutputPath)
			os.Exit(1)
		}
	}

	f, err := os.Create(cfg.OutputPath)
	if err != nil {
		panic(err)
	}

	return f, func() { f.Close() }
}

//
// SKIP LOGIC (unified = single source of truth)
//

func shouldSkip(path string, d os.DirEntry, cfg *Config) bool {
	base := d.Name()
	abs, _ := filepath.Abs(path)

	if cfg.stdoutInode != 0 {
		if fi, err := os.Stat(path); err == nil && getInode(fi) == cfg.stdoutInode {
			return true
		}
	}

	if base == ".DS_Store" || strings.HasPrefix(base, "._") {
		return true
	}

	if base == ".git" || strings.HasPrefix(path, ".git"+string(filepath.Separator)) || strings.HasPrefix(abs, ".git"+string(filepath.Separator)) {
		return true
	}

	if base == "target" {
		return true
	}

	if cfg.IgnoreVenv {
		switch base {
		case ".venv", "venv", "__pycache__", "node_modules":
			return true
		}
	}

	if cfg.Exclude[base] || cfg.Exclude[path] || cfg.Exclude[abs] {
		return true
	}

	if isSecretFilename(base) {
		return true
	}

	return false
}

//
// SECRETS GUARD
//

var secretExtensions = map[string]bool{
	".pem": true, ".key": true, ".p12": true, ".pfx": true, ".jks": true,
}

func isSecretFilename(name string) bool {
	lower := strings.ToLower(name)

	if strings.HasPrefix(lower, ".env") {
		return true
	}
	if strings.HasPrefix(lower, "id_rsa") || strings.HasPrefix(lower, "id_ed25519") ||
		strings.HasPrefix(lower, "id_dsa") || strings.HasPrefix(lower, "id_ecdsa") {
		return true
	}
	if lower == ".htpasswd" || lower == ".netrc" || lower == "credentials.json" ||
		lower == "credentials.yml" || lower == "credentials.yaml" {
		return true
	}

	ext := filepath.Ext(lower)
	if secretExtensions[ext] {
		return true
	}

	return false
}

var pemPrivateKeyPrefix = []byte("-----BEGIN ")

func looksLikePrivateKey(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 128)
	n, err := f.Read(buf)
	if err != nil || n < 10 {
		return false
	}
	buf = buf[:n]

	if !bytes.HasPrefix(buf, pemPrivateKeyPrefix) {
		return false
	}

	upper := bytes.ToUpper(buf)
	return bytes.Contains(upper, []byte("PRIVATE KEY"))
}

//
// HELPERS
//

const peekSize = 8192

func readFileFiltered(path string, includeBinaries bool) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	peek := make([]byte, peekSize)
	n, err := io.ReadFull(f, peek)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	peek = peek[:n]

	if !includeBinaries && isBinary(peek) {
		return nil, nil
	}

	if n < peekSize {
		return peek, nil
	}

	rest, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	return append(peek, rest...), nil
}

func hasMagic(peek, magic []byte) bool {
	if len(peek) < len(magic) {
		return false
	}
	for i, b := range magic {
		if peek[i] != b {
			return false
		}
	}
	return true
}

func isBinary(peek []byte) bool {
	magics := [][]byte{
		{0x7f, 'E', 'L', 'F'},
		{'M', 'Z'},
		{'%', 'P', 'D', 'F'},
		{0x89, 'P', 'N', 'G'},
		{'P', 'K', 0x03, 0x04},
		{0x1f, 0x8b},
		{0x42, 0x5a},
		{0xfd, 0x37, 0x7a, 0x58, 0x5a},
		{0xfe, 0xed, 0xfa, 0xce},
		{0xfe, 0xed, 0xfa, 0xcf},
		{0xce, 0xfa, 0xed, 0xfe},
		{0xcf, 0xfa, 0xed, 0xfe},
	}
	for _, m := range magics {
		if hasMagic(peek, m) {
			return true
		}
	}

	for _, b := range peek {
		if b == 0 {
			return true
		}
	}

	if len(peek) == 0 {
		return false
	}
	controlCount := 0
	for _, b := range peek {
		if b < 0x20 && b != 0x09 && b != 0x0a && b != 0x0d {
			controlCount++
		}
	}
	return float64(controlCount)/float64(len(peek)) > 0.10
}

func colorCachePath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "everything", "color")
}

func loadSavedColor() bool {
	data, err := os.ReadFile(colorCachePath())
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "true"
}

func saveColor(on bool) {
	path := colorCachePath()
	if path == "" {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0755)
	val := "false"
	if on {
		val = "true"
	}
	os.WriteFile(path, []byte(val), 0644)
}

func themeCachePath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "everything", "theme")
}

func loadSavedTheme() string {
	data, err := os.ReadFile(themeCachePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func saveTheme(name string) {
	path := themeCachePath()
	if path == "" {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte(name), 0644)
}
