package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

var version = "v1.7.5"

var (
	peekPool = sync.Pool{New: func() any {
		b := make([]byte, peekSize)
		return &b
	}}
	lineBufPool = sync.Pool{New: func() any {
		b := make([]byte, 64*1024)
		return &b
	}}

	lexerCache sync.Map
)

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
	outputInode       uint64
	exeInode          uint64
	Benchmark         bool
	Runs              int

	excludeAbsPaths map[string]bool
}

func (cfg *Config) recordSkip(msg string) {
	if cfg.OmittedDisclaimer {
		cfg.SkippedFiles = append(cfg.SkippedFiles, msg)
	}
}

func isInteractive() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

const treeIgnorePattern = ".git|target|node_modules|.venv|venv|__pycache__|" +
	"*.pem|*.key|*.p12|*.pfx|*.jks|*.keystore|*.jceks|*.kdbx|" +
	"credentials*|client_secret*|client-secret*|*service-account*|secrets.*|" +
	"*.env|id_rsa*|id_ed25519*|id_ecdsa*|id_dsa*"

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

func main() {
	cfg := parseArgs()

	if cfg.Benchmark {
		runBenchmark(cfg)
		os.Exit(0)
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

	walkDirs := cfg.InputDirs
	if len(walkDirs) == 0 {
		walkDirs = []string{"."}
	}

	if !cfg.JSON {
		tryPrintTree(writer, walkDirs)
	}

	for _, root := range walkDirs {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				cfg.recordSkip(fmt.Sprintf("  unreadable: %s: %v", path, err))
				return nil
			}

			if shouldSkip(path, d, cfg) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				if isSecretFilename(d.Name()) {
					cfg.recordSkip(fmt.Sprintf("  secret: %s", path))
				}
				return nil
			}

			if d.IsDir() {
				return nil
			}

			var info os.FileInfo
			if d.Type()&os.ModeSymlink != 0 {
				if !cfg.FollowSymlinks {
					cfg.recordSkip(fmt.Sprintf("  symlink: %s", path))
					return nil
				}
				target, statErr := os.Stat(path)
				if statErr != nil {
					return nil
				}
				if target.IsDir() {
					cfg.recordSkip(fmt.Sprintf("  dir symlink: %s", path))
					return nil
				}
				if !target.Mode().IsRegular() {
					return nil
				}
				info = target
			} else {
				if !d.Type().IsRegular() {
					return nil
				}
				entryInfo, infoErr := d.Info()
				if infoErr != nil {
					return nil
				}
				info = entryInfo
			}

			if cfg.MaxSize > 0 && info.Size() > cfg.MaxSize {
				cfg.recordSkip(fmt.Sprintf("  too large: %s (%d bytes)", path, info.Size()))
				return nil
			}

			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()

			peekp := peekPool.Get().(*[]byte)
			peek := *peekp
			n, err := io.ReadFull(f, peek)
			if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
				peekPool.Put(peekp)
				return nil
			}
			peek = peek[:n]

			if !cfg.IncludeBinaries && isBinary(peek) {
				peekPool.Put(peekp)
				cfg.recordSkip(fmt.Sprintf("  binary: %s", path))
				return nil
			}

			if hasPrivateKeyMarker(peek) {
				peekPool.Put(peekp)
				cfg.recordSkip(fmt.Sprintf("  secret: %s", path))
				return nil
			}

			const highlightLimit = 1 << 20
			useHighlight := cfg.Color && info.Size() <= highlightLimit

			if cfg.JSON {
				writeJSONLine(writer, path, peek, f)
			} else {
				fmt.Fprintf(writer, "==== FILE: %s ====\n", path)
				if useHighlight {
					emitHighlighted(writer, path, readWholeFile(peek, f), cfg.Theme)
				} else {
					writer.Write(peek)
					copyRest(writer, f)
				}
				writer.Write([]byte("\n\n"))
			}
			peekPool.Put(peekp)

			return nil
		})

		if err != nil {
			fmt.Fprintln(os.Stderr, "error: traversal failed:", err)
			_ = cleanup()
			os.Exit(1)
		}
	}

	if err := cleanup(); err != nil {
		fmt.Fprintln(os.Stderr, "error writing output:", err)
		os.Exit(1)
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
		files, dirs, totalBytes, lines, chars int64
		elapsed                                 time.Duration
		memUsed                                 int64
	}

	results := make([]runResult, 0, runs)

	for r := 0; r < runs; r++ {
		runtime.GC()
		var memBefore runtime.MemStats
		runtime.ReadMemStats(&memBefore)

		start := time.Now()
		files, dirs, totalBytes, lines, chars := benchTraverse(cfg, walkDirs)
		elapsed := time.Since(start)

		var memAfter runtime.MemStats
		runtime.ReadMemStats(&memAfter)
		memUsed := int64(memAfter.Alloc) - int64(memBefore.Alloc)
		if memUsed < 0 {
			memUsed = 0
		}

		results = append(results, runResult{files, dirs, totalBytes, lines, chars, elapsed, memUsed})
	}

	var files, dirs, totalBytes, lines, chars int64
	var durs []time.Duration
	var sumDur time.Duration
	var sumMem int64
	for _, res := range results {
		files = res.files
		dirs = res.dirs
		totalBytes = res.totalBytes
		lines = res.lines
		chars = res.chars
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
	printStat("LOC:", formatNum(lines))
	printStat("Characters:", formatNum(chars))
	fmt.Println()

	fmt.Printf("%-13s%d runs\n", "Traversal:", runs)
	printStat("  min:", formatDuration(minDur))
	printStat("  median:", formatDuration(medianDur))
	printStat("  mean:", formatDuration(meanDur))
	printStat("  max:", formatDuration(maxDur))
	fmt.Println()

	if meanDur > 0 {
		printStat("Rate (mean):", fmt.Sprintf("%s files/s", formatNum(int64(float64(files)/meanDur.Seconds()))))
		fmt.Printf("%-13s%s/s\n", "", formatBytes(int64(float64(totalBytes)/meanDur.Seconds())))
	} else {
		printStat("Rate (mean):", "n/a")
	}
	fmt.Println()
	printStat("Memory (mean):", formatBytes(meanMem))
	fmt.Println()
	fmt.Printf("version:     %s\n", version)
}

func benchTraverse(cfg *Config, walkDirs []string) (files, dirs, totalBytes, lines, chars int64) {
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

			var info os.FileInfo
			if d.Type()&os.ModeSymlink != 0 {
				if !cfg.FollowSymlinks {
					return nil
				}
				target, statErr := os.Stat(path)
				if statErr != nil || target.IsDir() || !target.Mode().IsRegular() {
					return nil
				}
				info = target
			} else {
				if !d.Type().IsRegular() {
					return nil
				}
				entryInfo, infoErr := d.Info()
				if infoErr != nil {
					return nil
				}
				info = entryInfo
			}

			if cfg.MaxSize > 0 && info.Size() > cfg.MaxSize {
				return nil
			}

			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()

			peekp := peekPool.Get().(*[]byte)
			peek := *peekp
			n, err := io.ReadFull(f, peek)
			if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
				peekPool.Put(peekp)
				return nil
			}
			peek = peek[:n]

			if !cfg.IncludeBinaries && isBinary(peek) {
				peekPool.Put(peekp)
				return nil
			}

			if hasPrivateKeyMarker(peek) {
				peekPool.Put(peekp)
				return nil
			}

			fileLines, fileChars := countLinesAndChars(f, peek[:n])
			peekPool.Put(peekp)

			files++
			totalBytes += info.Size()
			lines += fileLines
			chars += fileChars

			return nil
		})
	}
	return
}

func countLinesAndChars(f *os.File, head []byte) (int64, int64) {
	bufp := lineBufPool.Get().(*[]byte)
	buf := *bufp

	n := copy(buf, head)
	rest := head[n:]
	if len(rest) > 0 {
		n += copy(buf[n:], rest)
	}

	var totalLines int64
	var totalChars int64
	last := byte('\n')

	for {
		for _, b := range buf[:n] {
			if b == '\n' {
				totalLines++
			}
		}

		runes := utf8.RuneCount(buf[:n])
		totalChars += int64(runes)

		if n > 0 {
			last = buf[n-1]
		}
		if n < len(buf) {
			break
		}
		n, _ = f.Read(buf)
		if n == 0 {
			break
		}
	}
	lineBufPool.Put(bufp)

	if last != '\n' {
		totalLines++
	}
	return totalLines, totalChars
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
		Exclude:         make(map[string]bool),
		excludeAbsPaths: make(map[string]bool),
		IgnoreVenv:      true,
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
		"--overwrite": true, "--json": true, "--omitted-disclaimer": true,
		"--follow-symlinks": true, "--exclude": true, "--ignore": true,
		"--max-size": true, "--version": true, "-v": true,
		"--benchmark": true, "--bench": true, "--runs": true,
		"--help": true, "-h": true,
	}

	args := os.Args[1:]

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
				name = filepath.FromSlash(strings.TrimSpace(name))
				if name != "" {
					cfg.Exclude[name] = true
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

		case "--help", "-h":
			printHelp()
			os.Exit(0)

		default:
			fmt.Fprintf(os.Stderr, "error: unknown flag %q (see --help)\n", a)
			os.Exit(1)
		}
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

func validTheme(name string) bool {
	lower := strings.ToLower(name)
	for _, n := range styles.Names() {
		if strings.ToLower(n) == lower {
			return true
		}
	}
	return false
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
  --json                Emit JSON Lines (one {"path","content"} object per
                        line). No tree banner, no color.

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
  --omitted-disclaimer  Print the list of skipped files to stderr after the
                        scan finishes.

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
                          Reports file/dir counts, total bytes, lines of
                          code, traversal rate, and memory used. A quick
                          "view" of a project's size and how fast everything
                          can scan it.
  --runs <n>            With --benchmark, repeat the traversal n times and
                          report min/median/mean/max traversal times, plus
                          mean memory usage. Default: 1.
  --version, -v         Print the version and exit.
  --help, -h            Print this help and exit.

Always skipped: .git, target/, .DS_Store, ._*, symlinks (unless
--follow-symlinks), pipes/devices/sockets, binaries (unless
--include-binaries), and secret-looking files: .env*, *.env, id_rsa*/
id_ed25519*/id_dsa*/id_ecdsa*, *.pem, *.key, *.p12, *.pfx, *.jks,
*.keystore, *.kdbx, credentials*, client_secret*, *service-account*.json,
secrets.*, .netrc, .htpasswd, .npmrc, .pypirc, .git-credentials, plus any
file whose content contains a PEM private key block. Skipped files are
listed with --omitted-disclaimer — check it before sharing a dump.

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

func setupOutput(cfg *Config) (io.Writer, func() error) {
	if cfg.OutputPath == "" {
		if fi, err := os.Stdout.Stat(); err == nil {
			cfg.stdoutInode = getInode(fi)
		}
		return os.Stdout, func() error { return nil }
	}

	absOut, _ := filepath.Abs(cfg.OutputPath)

	if err := validateOutputPath(cfg.OutputPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cfg.excludeAbsPaths[absOut] = true

	if fi, lstatErr := os.Lstat(cfg.OutputPath); lstatErr == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintf(os.Stderr, "Refusing to write through a symlink: %s\n", cfg.OutputPath)
			os.Exit(1)
		}
		if !cfg.Force {
			fmt.Fprintf(os.Stderr, "Refusing to overwrite existing file: %s. Use --force to overwrite.\n", cfg.OutputPath)
			os.Exit(1)
		}
	}

	f, err := os.OpenFile(cfg.OutputPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|outputNoFollow, 0o666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot write output file %q: %v\n", cfg.OutputPath, err)
		os.Exit(1)
	}

	if fi, statErr := f.Stat(); statErr == nil {
		cfg.outputInode = getInode(fi)
	}

	bw := bufio.NewWriterSize(f, 256*1024)
	return bw, func() error {
		flushErr := bw.Flush()
		closeErr := f.Close()
		if flushErr != nil {
			return flushErr
		}
		return closeErr
	}
}

//
// SKIP LOGIC (unified = single source of truth)
//

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

	if base == ".git" || strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) || strings.HasPrefix(path, ".git"+string(filepath.Separator)) {
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

	if cfg.Exclude[base] || cfg.Exclude[path] {
		return true
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

//
// SECRETS GUARD
//

var secretExtensions = map[string]bool{
	".pem": true, ".key": true, ".p12": true, ".pfx": true, ".jks": true,
	".keystore": true, ".jceks": true, ".kdbx": true,
}

var secretDataExts = map[string]bool{
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true,
	".conf": true, ".cfg": true, ".txt": true, ".properties": true,
}

var binaryMagics = [][]byte{
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

func isSecretFilename(name string) bool {
	lower := strings.ToLower(name)

	if strings.HasPrefix(lower, ".env") || strings.Contains(lower, ".env.") ||
		strings.HasSuffix(lower, ".env") {
		return true
	}
	if strings.HasPrefix(lower, "id_rsa") || strings.HasPrefix(lower, "id_ed25519") ||
		strings.HasPrefix(lower, "id_dsa") || strings.HasPrefix(lower, "id_ecdsa") {
		return true
	}
	switch lower {
	case ".htpasswd", ".netrc", ".npmrc", ".pypirc", ".git-credentials", "credentials":
		return true
	}
	if strings.HasPrefix(lower, "credentials.") || strings.HasPrefix(lower, "client_secret") ||
		strings.HasPrefix(lower, "client-secret") {
		return true
	}
	if (strings.Contains(lower, "service-account") || strings.Contains(lower, "service_account")) &&
		filepath.Ext(lower) == ".json" {
		return true
	}
	if lower == "secrets" || strings.HasPrefix(lower, "secrets.") {
		if lower == "secrets" || secretDataExts[filepath.Ext(lower)] {
			return true
		}
	}

	ext := filepath.Ext(lower)
	if secretExtensions[ext] {
		return true
	}

	return false
}

var pemBeginMarker = []byte("-----BEGIN")
var pemPrivateKeyMarker = []byte("PRIVATE KEY")

func hasPrivateKeyMarker(data []byte) bool {
	n := len(data)
	if n > 4096 {
		n = 4096
	}
	window := bytes.TrimPrefix(data[:n], []byte{0xef, 0xbb, 0xbf})
	if len(window) < 10 {
		return false
	}
	return bytes.Contains(window, pemBeginMarker) && bytes.Contains(window, pemPrivateKeyMarker)
}

//
// HELPERS
//

const peekSize = 8192

func copyRest(w io.Writer, r io.Reader) {
	bufp := lineBufPool.Get().(*[]byte)
	_, _ = io.CopyBuffer(w, r, *bufp)
	lineBufPool.Put(bufp)
}

func readWholeFile(head []byte, f *os.File) []byte {
	rest, err := io.ReadAll(f)
	if err != nil && len(rest) == 0 {
		out := make([]byte, len(head))
		copy(out, head)
		return out
	}
	out := make([]byte, 0, len(head)+len(rest))
	out = append(out, head...)
	out = append(out, rest...)
	return out
}

const hexDigits = "0123456789abcdef"

type jsonEscaper struct {
	dst      io.Writer
	pending  [utf8.UTFMax]byte
	nPending int
	err      error
}

func utf8SeqLen(b byte) int {
	switch {
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	default:
		return 0
	}
}

func (e *jsonEscaper) writeByteEscape(b byte) {
	var esc [6]byte
	esc[0], esc[1], esc[2], esc[3] = '\\', 'u', '0', '0'
	esc[4], esc[5] = hexDigits[b>>4], hexDigits[b&0xf]
	e.emitRaw(esc[:])
}

func (e *jsonEscaper) emitRaw(p []byte) {
	if e.err != nil || len(p) == 0 {
		return
	}
	_, e.err = e.dst.Write(p)
}

func (e *jsonEscaper) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, e.err
	}
	total := len(p)
	data := p
	if e.nPending > 0 {
		data = make([]byte, 0, e.nPending+len(p))
		data = append(data, e.pending[:e.nPending]...)
		data = append(data, p...)
	}
	e.nPending = 0

	holdFrom := len(data)
	if n := len(data); n > 0 {
		limit := n - utf8.UTFMax
		if limit < 0 {
			limit = 0
		}
		for i := n - 1; i >= limit; i-- {
			b := data[i]
			if b < 0x80 {
				break
			}
			if b&0xC0 == 0x80 {
				continue
			}
			if need := utf8SeqLen(b); need > 0 && i+need > n {
				holdFrom = i
			}
			break
		}
	}

	start := 0
	flushTo := func(end int) {
		if end > start {
			e.emitRaw(data[start:end])
			start = end
		}
	}

	i := 0
	for i < holdFrom && e.err == nil {
		b := data[i]
		if b >= 0x80 {
			r, size := utf8.DecodeRune(data[i:])
			if r == utf8.RuneError && size == 1 {
				flushTo(i)
				e.emitRaw([]byte(`\ufffd`))
				i++
				start = i
				continue
			}
			i += size
			continue
		}

		var esc []byte
		switch b {
		case '"':
			esc = []byte(`\"`)
		case '\\':
			esc = []byte(`\\`)
		case '\n':
			esc = []byte(`\n`)
		case '\r':
			esc = []byte(`\r`)
		case '\t':
			esc = []byte(`\t`)
		}
		if esc != nil {
			flushTo(i)
			e.emitRaw(esc)
			i++
			start = i
			continue
		}
		if b < 0x20 {
			flushTo(i)
			e.writeByteEscape(b)
			i++
			start = i
			continue
		}
		i++
	}
	if e.err == nil {
		flushTo(holdFrom)
		e.nPending = copy(e.pending[:], data[holdFrom:])
	} else {
		e.nPending = 0
	}

	return total, e.err
}

func (e *jsonEscaper) Close() error {
	if e.nPending > 0 {
		e.nPending = 0
		e.emitRaw([]byte(`\ufffd`))
	}
	return e.err
}

func writeJSONString(w io.Writer, s []byte) {
	e := &jsonEscaper{dst: w}
	e.Write(s)
	e.Close()
}

func writeJSONLine(w io.Writer, path string, head []byte, rest io.Reader) {
	pb, err := json.Marshal(path)
	if err != nil {
		return
	}
	w.Write([]byte(`{"path":`))
	w.Write(pb)
	w.Write([]byte(`,"content":"`))
	e := &jsonEscaper{dst: w}
	e.Write(head)
	bufp := lineBufPool.Get().(*[]byte)
	_, _ = io.CopyBuffer(e, rest, *bufp)
	lineBufPool.Put(bufp)
	e.Close()
	w.Write([]byte("\"}\n"))
}

func emitHighlighted(w io.Writer, path string, data []byte, themeName string) {
	lexer := matchLexer(path)
	iterator, err := lexer.Tokenise(nil, string(data))
	if err != nil {
		w.Write(data)
		return
	}
	formatter := formatters.Get("terminal")
	if formatter == nil {
		formatter = formatters.Fallback
	}
	formatter.Format(w, styles.Get(themeName), iterator)
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

func matchLexer(path string) chroma.Lexer {
	ext := filepath.Ext(path)
	if v, ok := lexerCache.Load(ext); ok {
		return v.(chroma.Lexer)
	}
	lexer := lexers.Match(path)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	lexerCache.Store(ext, lexer)
	return lexer
}

func isBinary(peek []byte) bool {
	for _, m := range binaryMagics {
		if len(peek) >= len(m) && hasMagic(peek, m) {
			return true
		}
	}

	if len(peek) == 0 {
		return false
	}

	controlCount := 0
	for _, b := range peek {
		if b == 0 {
			return true
		}
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
