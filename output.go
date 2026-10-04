// Writing a file's bytes to the output, and opening the output safely.

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// peekSize is how many leading bytes every file is read before being
// classified or emitted; it bounds the binary and secret sniff.
const peekSize = 8192

var (
	// peekPool holds the per-file head buffers used by processFile and by
	// benchTraverse.
	peekPool = sync.Pool{New: func() any {
		b := make([]byte, peekSize)
		return &b
	}}
	// lineBufPool holds the streaming copy buffers shared by copyRest,
	// countLinesAndChars, and writeJSONRecord.
	lineBufPool = sync.Pool{New: func() any {
		b := make([]byte, 64*1024)
		return &b
	}}
)

//
// FILE EMISSION
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
// HELPERS
//

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
