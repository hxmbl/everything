// Package everything is a tool for dumping entire project directories into a single file.
// It recursively walks directories and outputs file paths and contents, making it useful
// for feeding code to LLMs, code review preparation, or creating project snapshots.
// The tool automatically skips binaries, secrets, and common build artifacts.
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
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

var version = "v1.9.1"

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

// Config holds the configuration for the everything tool.
// It includes input/output paths, filtering options, and display preferences.
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
	JSONL             bool
	OmittedDisclaimer bool
	SkippedFiles      []string
	stdoutInode       uint64
	outputInode       uint64
	exeInode          uint64
	Benchmark         bool
	Runs              int
	Warmup            int

	excludeAbsPaths map[string]bool
}

// recordSkip records a skip message if OmittedDisclaimer is enabled.
// This is used to track which files were skipped and why.
func (cfg *Config) recordSkip(msg string) {
	if cfg.OmittedDisclaimer {
		cfg.SkippedFiles = append(cfg.SkippedFiles, msg)
	}
}

// isInteractive checks if stdout is connected to an interactive terminal.
// This is used to determine whether to apply color output or warn about large dumps.
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

// main is the entry point for the everything tool.
// It parses arguments, configures output, and runs the directory traversal.
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

	jsonArray := cfg.JSON && !cfg.JSONL
	jsonNeedComma := false
	if jsonArray {
		if _, err := writer.Write([]byte("[\n")); err != nil {
			fmt.Fprintln(os.Stderr, "error writing json array start:", err)
			os.Exit(1)
		}
	}
	jsonSeparator := func() {
		if jsonArray {
			if jsonNeedComma {
				if _, err := writer.Write([]byte(",\n")); err != nil {
					fmt.Fprintln(os.Stderr, "error writing json separator:", err)
				}
			}
			jsonNeedComma = true
		}
	}
	jsonClose := func() {
		if jsonArray {
			if _, err := writer.Write([]byte("\n]\n")); err != nil {
				fmt.Fprintln(os.Stderr, "error writing json array end:", err)
			}
		}
	}

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

			info, err := getFileInfo(path, d, cfg)
			if err != nil {
				return nil
			}
			if info == nil {
				return nil
			}

			if cfg.MaxSize > 0 && info.Size() > cfg.MaxSize {
				cfg.recordSkip(fmt.Sprintf("  too large: %s (%d bytes)", path, info.Size()))
				return nil
			}

			return processFile(path, info, writer, cfg, jsonArray, jsonSeparator, jsonClose)
		})

		if err != nil {
			jsonClose()
			fmt.Fprintln(os.Stderr, "error: traversal failed:", err)
			if cleanupErr := cleanup(); cleanupErr != nil {
				fmt.Fprintln(os.Stderr, "error writing output:", cleanupErr)
			}
			os.Exit(1)
		}
	}

	jsonClose()

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

// runBenchmark executes a benchmark traversal of the configured directories.
// It measures file counts, sizes, lines of code, and traversal speed across multiple runs.
func runBenchmark(cfg *Config) {
	walkDirs := cfg.InputDirs
	if len(walkDirs) == 0 {
		walkDirs = []string{"."}
	}

	runs := cfg.Runs
	if runs < 1 {
		runs = 1
	}

	warmup := cfg.Warmup
	if warmup < 0 {
		warmup = 0
	}

	type runResult struct {
		files, dirs, totalBytes, bytesRead, lines, chars int64
		elapsed                                          time.Duration
	}

	// Sample live heap during the whole run to track its peak. ReadMemStats
	// is cheap in modern Go, so the 2ms sampler perturbs timings by ~1%.
	var peakHeap atomic.Int64
	stopSampling := make(chan struct{})
	var samplerDone sync.WaitGroup
	samplerDone.Add(1)
	go func() {
		defer samplerDone.Done()
		for {
			select {
			case <-stopSampling:
				return
			default:
			}
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if a := int64(m.Alloc); a > peakHeap.Load() {
				peakHeap.Store(a)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	defer func() {
		close(stopSampling)
		samplerDone.Wait()
	}()

	// Warmup passes: untimed, so the OS page cache and Go allocator are warm
	// before the timed runs. Without this the first run is 2-3x slower (cold
	// cache) and drags mean and median down.
	for i := 0; i < warmup; i++ {
		benchTraverse(cfg, walkDirs)
	}

	results := make([]runResult, 0, runs)

	for r := 0; r < runs; r++ {
		runtime.GC()

		start := time.Now()
		files, dirs, totalBytes, bytesRead, lines, chars := benchTraverse(cfg, walkDirs)
		elapsed := time.Since(start)

		results = append(results, runResult{files, dirs, totalBytes, bytesRead, lines, chars, elapsed})
	}

	var files, dirs, totalBytes, bytesRead, lines, chars int64
	var durs []time.Duration
	var sumDur time.Duration
	for _, res := range results {
		files = res.files
		dirs = res.dirs
		totalBytes = res.totalBytes
		bytesRead = res.bytesRead
		lines = res.lines
		chars = res.chars
		durs = append(durs, res.elapsed)
		sumDur += res.elapsed
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

	pathLabel := strings.Join(walkDirs, ", ")
	if pathLabel == "." {
		pathLabel = "./"
	}

	fmt.Println("Everything Benchmark")
	fmt.Println()
	printStat("Path:", pathLabel)
	printStat("Runs:", formatNum(int64(runs)))
	printStat("Warmup:", formatNum(int64(warmup)))
	fmt.Println()

	printStat("Files:", formatNum(files))
	printStat("Directories:", formatNum(dirs))
	printStat("Bytes:", formatBytes(totalBytes))
	printStat("Content read:", formatBytes(bytesRead))
	printStat("LOC:", formatNum(lines))
	printStat("Characters:", formatNum(chars))
	fmt.Println()

	fmt.Printf("%-13s%d runs\n", "Traversal:", runs)
	printStat("  min:", formatDuration(minDur))
	printStat("  median:", formatDuration(medianDur))
	printStat("  mean:", formatDuration(meanDur))
	printStat("  max:", formatDuration(maxDur))
	fmt.Println()

	if minDur > 0 {
		printStat("Rate (min):", fmt.Sprintf("%s files/s", formatNum(int64(float64(files)/minDur.Seconds()))))
		fmt.Printf("%-13s%s/s content\n", "", formatBytes(int64(float64(bytesRead)/minDur.Seconds())))
	} else {
		printStat("Rate (min):", "n/a")
	}
	fmt.Println()
	printStat("Peak heap:", formatBytes(peakHeap.Load()))
	fmt.Println()
	fmt.Printf("version:     %s\n", version)
}

// benchTraverse performs a single benchmark traversal, counting files, directories, bytes, lines, and characters.
// It applies the same filtering as a real dump but doesn't write output.
func benchTraverse(cfg *Config, walkDirs []string) (files, dirs, totalBytes, bytesRead, lines, chars int64) {
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
			bytesRead += int64(n)

			if !cfg.IncludeBinaries && isBinary(peek) {
				peekPool.Put(peekp)
				return nil
			}

			if hasPrivateKeyMarker(peek) {
				peekPool.Put(peekp)
				return nil
			}

			fileLines, fileChars, contentBytes := countLinesAndChars(f, peek)
			peekPool.Put(peekp)

			files++
			totalBytes += info.Size()
			bytesRead += contentBytes
			lines += fileLines
			chars += fileChars

			return nil
		})
	}
	return
}

// utf8RuneCountCarry counts the runes in b and holds back any trailing bytes
// that start a multi-byte sequence which may complete in a later read, so
// counting stays exact across buffer boundaries. It returns the rune count of
// the complete portion and how many trailing bytes to carry forward.
func utf8RuneCountCarry(b []byte) (int64, int) {
	n := len(b)
	keep := 0
	i := n - 1
	for i >= 0 && b[i]&0xC0 == 0x80 {
		i--
	}
	if i >= 0 {
		if need := utf8SeqLen(b[i]); need > 1 {
			if avail := n - i; avail < need {
				keep = avail
			}
		}
	}
	if keep == 0 {
		return int64(utf8.RuneCount(b)), 0
	}
	return int64(utf8.RuneCount(b[:n-keep])), keep
}

// countLinesAndChars counts newlines, runes, and bytes over the entire file,
// starting from the already-read head and continuing from f until EOF. The
// returned bytes count is only the content read beyond head (the peek length
// is accounted for by the caller). Line counting mirrors the dump's trailing
// newline semantics: a file not ending in '\n' gets one more line.
func countLinesAndChars(f *os.File, head []byte) (lines, chars, contentBytes int64) {
	bufp := lineBufPool.Get().(*[]byte)
	buf := *bufp
	defer lineBufPool.Put(bufp)

	n := copy(buf, head)

	var totalLines int64
	var totalChars int64
	last := byte('\n')

	carryLen := 0

	for {
		total := carryLen + n
		if total > 0 {
			for _, b := range buf[:total] {
				if b == '\n' {
					totalLines++
				}
			}
			last = buf[total-1]

			runes, keep := utf8RuneCountCarry(buf[:total])
			totalChars += runes
			if keep > 0 {
				copy(buf[:keep], buf[total-keep:])
			}
			carryLen = keep
		}

		var readErr error
		n, readErr = f.Read(buf[carryLen:])
		if readErr != nil && n == 0 {
			break
		}
		contentBytes += int64(n)
	}

	if carryLen > 0 {
		totalChars++ // trailing incomplete sequence counts as one rune
	}
	if last != '\n' {
		totalLines++
	}
	return totalLines, totalChars, contentBytes
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

// getFileInfo retrieves file information, handling symlinks and special files.
// Returns nil if the file should be skipped.
func getFileInfo(path string, d os.DirEntry, cfg *Config) (os.FileInfo, error) {
	var info os.FileInfo
	if d.Type()&os.ModeSymlink != 0 {
		if !cfg.FollowSymlinks {
			cfg.recordSkip(fmt.Sprintf("  symlink: %s", path))
			return nil, nil
		}
		target, statErr := os.Stat(path)
		if statErr != nil {
			return nil, nil
		}
		if target.IsDir() {
			cfg.recordSkip(fmt.Sprintf("  dir symlink: %s", path))
			return nil, nil
		}
		if !target.Mode().IsRegular() {
			return nil, nil
		}
		info = target
	} else {
		if !d.Type().IsRegular() {
			return nil, nil
		}
		entryInfo, infoErr := d.Info()
		if infoErr != nil {
			return nil, nil
		}
		info = entryInfo
	}
	return info, nil
}

// processFile handles the actual file reading and writing based on configuration.
// It manages JSON output, syntax highlighting, and error handling.
func processFile(path string, info os.FileInfo, writer io.Writer, cfg *Config, jsonArray bool, jsonSeparator func(), jsonClose func()) error {
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
		jsonSeparator()
		if jsonArray {
			if err := writeJSONRecord(writer, path, peek, f); err != nil {
				cfg.recordSkip(fmt.Sprintf("  json error: %s: %v", path, err))
			}
		} else {
			if err := writeJSONLine(writer, path, peek, f); err != nil {
				cfg.recordSkip(fmt.Sprintf("  json error: %s: %v", path, err))
			}
		}
	} else {
		if _, err := fmt.Fprintf(writer, "==== FILE: %s ====\n", path); err != nil {
			cfg.recordSkip(fmt.Sprintf("  write error: %s: %v", path, err))
		}
		if useHighlight {
			data, err := readWholeFile(peek, f)
			if err != nil {
				cfg.recordSkip(fmt.Sprintf("  read error: %s: %v", path, err))
				if _, writeErr := writer.Write(peek); writeErr != nil {
					cfg.recordSkip(fmt.Sprintf("  write error: %s: %v", path, writeErr))
				}
				if copyErr := copyRest(writer, f); copyErr != nil {
					cfg.recordSkip(fmt.Sprintf("  read error: %s: %v", path, copyErr))
				}
			} else {
				if err := emitHighlighted(writer, path, data, cfg.Theme); err != nil {
					cfg.recordSkip(fmt.Sprintf("  highlight error: %s: %v", path, err))
					if _, writeErr := writer.Write(data); writeErr != nil {
						cfg.recordSkip(fmt.Sprintf("  write error: %s: %v", path, writeErr))
					}
				}
			}
		} else {
			if _, err := writer.Write(peek); err != nil {
				cfg.recordSkip(fmt.Sprintf("  write error: %s: %v", path, err))
			}
			if err := copyRest(writer, f); err != nil {
				cfg.recordSkip(fmt.Sprintf("  read error: %s: %v", path, err))
			}
		}
		if _, err := writer.Write([]byte("\n\n")); err != nil {
			cfg.recordSkip(fmt.Sprintf("  write error: %s: %v", path, err))
		}
	}
	peekPool.Put(peekp)
	return nil
}

// validateConfig performs final validation on the parsed configuration.
// It checks for conflicting options and invalid combinations.
func validateConfig(cfg *Config) error {
	if cfg.JSON && cfg.JSONL {
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
	cfg := &Config{
		Exclude:         make(map[string]bool),
		excludeAbsPaths: make(map[string]bool),
		IgnoreVenv:      true,
		Warmup:          1,
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
		"--follow-symlinks": true, "--exclude": true, "--ignore": true,
		"--max-size": true, "--version": true, "-v": true,
		"--benchmark": true, "--bench": true, "--runs": true, "--warmup": true,
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

		case "--jsonl":
			cfg.JSON = true
			cfg.JSONL = true

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
  everything --json --output out.json              one JSON document (array)
  everything --jsonl --output out.jsonl            JSON Lines for scripts
  everything --color | less -R                    paged, highlighted viewing
  everything | grep "TODO"                        search the whole project
  everything --omitted-disclaimer --output ctx.txt  see what got left out`)
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

//
// OUTPUT SAFETY
//

// validateOutputPath checks if the output path is safe to write to.
// It refuses to write to device files like /dev/stdin, /dev/stdout, /dev/stderr.
func validateOutputPath(path string) error {
	if path == "/dev/stdin" || path == "/dev/stdout" || path == "/dev/stderr" {
		return fmt.Errorf("refusing to write to %s", path)
	}
	if abs, _ := filepath.Abs(path); filepath.Dir(abs) == "/dev" {
		return fmt.Errorf("refusing to write to device file: %s", abs)
	}
	return nil
}

// setupOutput configures the output writer based on the configuration.
// Returns the writer and a cleanup function that should be called when done.
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

// isSecretFilename checks if a filename matches known secret file patterns.
// This includes environment files, SSH keys, certificates, and credential files.
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

// hasPrivateKeyMarker checks if data contains a PEM private key block.
// It looks for "-----BEGIN" followed by "PRIVATE KEY" within the first 4KB.
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

// copyRest copies the remaining content from reader to writer using a pooled buffer.
// Returns any error that occurs during the copy operation.
func copyRest(w io.Writer, r io.Reader) error {
	bufp := lineBufPool.Get().(*[]byte)
	_, err := io.CopyBuffer(w, r, *bufp)
	lineBufPool.Put(bufp)
	return err
}

// readWholeFile reads the entire file content, combining the already-read head with the rest.
// Returns the combined data and any error that occurred while reading the rest.
func readWholeFile(head []byte, f *os.File) ([]byte, error) {
	rest, err := io.ReadAll(f)
	if err != nil && len(rest) == 0 {
		out := make([]byte, len(head))
		copy(out, head)
		return out, err
	}
	out := make([]byte, 0, len(head)+len(rest))
	out = append(out, head...)
	out = append(out, rest...)
	return out, nil
}

const hexDigits = "0123456789abcdef"

// jsonEscaper is a custom JSON string escaper that handles UTF-8 encoding correctly.
// It ensures that multi-byte UTF-8 sequences are not split across escape boundaries.
type jsonEscaper struct {
	dst      io.Writer
	pending  [utf8.UTFMax]byte
	nPending int
	err      error
}

// utf8SeqLen returns the expected length of a UTF-8 sequence starting with byte b.
// Returns 0 if b is not a valid UTF-8 sequence start byte.
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

// writeJSONString writes a byte slice as a JSON string to the writer.
// It handles UTF-8 encoding and proper JSON escaping.
func writeJSONString(w io.Writer, s []byte) {
	e := &jsonEscaper{dst: w}
	e.Write(s)
	e.Close()
}

// writeJSONLine writes a single JSON line record with path and content.
// It returns an error if writing fails.
func writeJSONLine(w io.Writer, path string, head []byte, rest io.Reader) error {
	if err := writeJSONRecord(w, path, head, rest); err != nil {
		return err
	}
	_, err := w.Write([]byte("\n"))
	return err
}

// writeJSONRecord writes a JSON record with path and content fields.
// It handles the file content in a streaming fashion to avoid loading large files into memory.
func writeJSONRecord(w io.Writer, path string, head []byte, rest io.Reader) error {
	pb, err := json.Marshal(path)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(`{"path":`)); err != nil {
		return err
	}
	if _, err := w.Write(pb); err != nil {
		return err
	}
	if _, err := w.Write([]byte(`,"content":"`)); err != nil {
		return err
	}
	e := &jsonEscaper{dst: w}
	if _, err := e.Write(head); err != nil {
		return err
	}
	bufp := lineBufPool.Get().(*[]byte)
	if _, err := io.CopyBuffer(e, rest, *bufp); err != nil {
		lineBufPool.Put(bufp)
		return err
	}
	lineBufPool.Put(bufp)
	if err := e.Close(); err != nil {
		return err
	}
	_, err = w.Write([]byte("\"}"))
	return err
}

// emitHighlighted applies syntax highlighting to the file content and writes it to the writer.
// It uses chroma lexers and formatters to detect the language and apply the theme.
func emitHighlighted(w io.Writer, path string, data []byte, themeName string) error {
	lexer := matchLexer(path)
	iterator, err := lexer.Tokenise(nil, string(data))
	if err != nil {
		_, err = w.Write(data)
		return err
	}
	formatter := formatters.Get("terminal")
	if formatter == nil {
		formatter = formatters.Fallback
	}
	return formatter.Format(w, styles.Get(themeName), iterator)
}

// hasMagic checks if the peek bytes start with the given magic sequence.
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

// matchLexer finds the appropriate chroma lexer for a file based on its path.
// It caches lexers by file extension to avoid repeated lookups.
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

// isBinary checks if the file content appears to be binary based on magic bytes and control character density.
// Returns true if the content matches known binary signatures or has too many control characters.
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

// colorCachePath returns the path to the color preference cache file.
func colorCachePath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "everything", "color")
}

// loadSavedColor loads the saved color preference from the cache file.
// Returns false if the file doesn't exist or cannot be read.
func loadSavedColor() bool {
	data, err := os.ReadFile(colorCachePath())
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "true"
}

// saveColor saves the color preference to the cache file.
// Returns an error if the cache directory cannot be created or the file cannot be written.
func saveColor(on bool) error {
	path := colorCachePath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	val := "false"
	if on {
		val = "true"
	}
	return os.WriteFile(path, []byte(val), 0644)
}

// themeCachePath returns the path to the theme preference cache file.
func themeCachePath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "everything", "theme")
}

// loadSavedTheme loads the saved theme preference from the cache file.
// Returns an empty string if the file doesn't exist or cannot be read.
func loadSavedTheme() string {
	data, err := os.ReadFile(themeCachePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// saveTheme saves the theme preference to the cache file.
// Returns an error if the cache directory cannot be created or the file cannot be written.
func saveTheme(name string) error {
	path := themeCachePath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(name), 0644)
}
