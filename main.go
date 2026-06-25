// TODO: Add Syntax highlighting themes then leave it cause its doing too much 

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

var version = "v0.15.3"

type Config struct {
	OutputPath string
	InputDirs  []string
	Exclude    map[string]bool

	MaxSize int64

	IgnoreVenv      bool
	Force           bool
	IncludeBinaries bool
	StdoutSafe      bool

	SyntaxHighlight bool
}

func isInteractive() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func tryPrintTree(writer io.Writer) {
	cmd := exec.Command("tree", "-n")

	cmd.Stdout = writer
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return
	}

	fmt.Fprint(writer, "\n")
}

func main() {
	cfg := parseArgs()

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

	tryPrintTree(writer)

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

			info, err := d.Info()
			if err != nil {
				return nil
			}

			if cfg.MaxSize > 0 && info.Size() > cfg.MaxSize {
				return nil
			}

			data, err := readFileFiltered(path, cfg.IncludeBinaries)
			if err != nil {
				return nil
			}
			if data == nil {
				return nil
			}

			fmt.Fprintf(writer, "==== FILE: %s ====\n", path)
			if cfg.SyntaxHighlight {
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
					formatter.Format(writer, styles.Get("monokai"), iterator)
				}
			} else {
				writer.Write(data)
			}
			writer.Write([]byte("\n\n"))

			return nil
		})

		if err != nil {
			panic(err)
		}
	}
}

//
// CONFIG
//

func parseArgs() *Config {
	cfg := &Config{
		Exclude:         make(map[string]bool),
		IgnoreVenv:      true,
		SyntaxHighlight: true,
	}

	if exe, err := os.Executable(); err == nil {
		cfg.Exclude[exe] = true
		cfg.Exclude[filepath.Base(exe)] = true
	}

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
			if i < len(args) {
				cfg.OutputPath = args[i]
			}

		case "--ignore-venv":
			cfg.IgnoreVenv = true

		case "--include-venv":
			cfg.IgnoreVenv = false

		case "--include-binary":
			cfg.IncludeBinaries = true

		case "--no-syntax-highlight":
			cfg.SyntaxHighlight = false

		case "--stdout-safe":
			cfg.StdoutSafe = true

		case "--force", "--overwrite":
			cfg.Force = true

		case "--exclude":
			i++
			if i < len(args) {
				for _, name := range strings.Split(args[i], ",") {
					cfg.Exclude[strings.TrimSpace(name)] = true
				}
			}

		case "--max-size":
			i++
			if i < len(args) {
				cfg.MaxSize = parseSize(args[i])
			}

		case "--version", "-v":
			fmt.Println("everything", version)
			os.Exit(0)

		case "--help", "-h":
			printHelp()
			os.Exit(0)
		}
	}

	return cfg
}

func printHelp() {
	fmt.Println(`everything – dump your project into a flat file

Usage:
  everything [flags] [input-dirs...] [output-path]

Positional arguments:
  Existing directories are scanned as input.
  A non-directory argument is used as the output path.

Flags:
  --output <path>        Write to file (auto-excluded from scan)
  --exclude <list>       Comma-separated names/paths to skip
  --max-size <size>      Skip files larger than this (e.g. 1MB, 500KB)
  --include-binary       Include binary files (skipped by default)
  --force                Overwrite existing output file
  --no-syntax-highlight  Don't attempt syntax highlighting (if you're into not having fun)
  --ignore-venv          (default) Skip .venv, venv, __pycache__, node_modules
  --include-venv         Don't skip venv/pycache/node_modules
  --stdout-safe          Require --output in interactive shells
  --version, -v          Show version
  --help, -h             Show this help

Always skipped: .git, .DS_Store, ._*, binaries (unless --include-binaries)

Examples:
  everything --output snapshot.txt   (recommended)
  everything | less                  (safe viewing)
  everything src/                    (scan src/ instead of .)
  everything src/ lib/ --output ctx.txt  (scan multiple dirs)
  everything --output context.txt --include-binaries
  everything --exclude "node_modules" --max-size 1MB`)
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

func setupOutput(cfg *Config) (io.Writer, func()) {
	if cfg.OutputPath == "" {
		return os.Stdout, func() {}
	}

	absOut, _ := filepath.Abs(cfg.OutputPath)

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

	if base == ".DS_Store" || strings.HasPrefix(base, "._") {
		return true
	}

	if base == ".git" || strings.HasPrefix(path, ".git"+string(filepath.Separator)) || strings.HasPrefix(abs, ".git"+string(filepath.Separator)) {
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

	return false
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
