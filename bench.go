// Benchmark mode (--benchmark). A separate traversal that measures rather
// than emits; main dispatches here and exits before the dump path runs.

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

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
