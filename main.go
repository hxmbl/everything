package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var version = "release-0.13"

type Config struct {
	OutputPath string
	Exclude    map[string]bool

	MaxSize  int64
	NoBinary bool

	IgnoreGit  bool
	IgnoreVenv bool
	Force      bool
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

	writer, cleanup := setupOutput(cfg)
	defer cleanup()

	tryPrintTree(writer)

	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		name := d.Name()

		if d.IsDir() {
			if shouldSkipDir(name, cfg) {
				return filepath.SkipDir
			}
			return nil
		}

		if shouldSkipFile(path, name, cfg) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		if cfg.MaxSize > 0 && info.Size() > cfg.MaxSize {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		if cfg.NoBinary && isBinary(data) {
			return nil
		}

		fmt.Fprintf(writer, "==== FILE: %s ====\n", path)
		writer.Write(data)
		writer.Write([]byte("\n\n"))

		return nil
	})

	if err != nil {
		panic(err)
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

	args := os.Args[1:]

	for i := 0; i < len(args); i++ {
		a := args[i]

		// positional output
		if !strings.HasPrefix(a, "-") && cfg.OutputPath == "" {
			cfg.OutputPath = a
			continue
		}

		switch a {
		case "--output":
			i++
			if i < len(args) {
				cfg.OutputPath = args[i]
			}

		case "--ignore-git":
			cfg.IgnoreGit = true

		case "--ignore-venv":
			cfg.IgnoreVenv = true

		case "--include-venv":
			cfg.IgnoreVenv = false

		case "--no-binary":
			cfg.NoBinary = true

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
  everything [flags] [output-path]

Flags:
  --output <path>     Write to file (auto-excluded from scan)
  --exclude <list>    Comma-separated names/paths to skip
  --max-size <size>   Skip files larger than this (e.g. 1MB, 500KB)
  --no-binary         Skip binary files (files containing null bytes)
  --force             Overwrite existing output file
  --ignore-git        Skip .git directory
  --ignore-venv       (default) Skip .venv, venv, __pycache__, node_modules
  --include-venv      Don't skip venv/pycache/node_modules
  --version, -v       Show version
  --help, -h          Show this help

Examples:
  everything > out.txt
  everything --output context.txt --no-binary
  everything --exclude "node_modules,.git" --max-size 1MB`)
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

	if !cfg.Force {
		if _, err := os.Stat(cfg.OutputPath); err == nil {
			fmt.Println("Refusing to overwrite existing file:", cfg.OutputPath)
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
// IGNORE LOGIC (centralised = important improvement)
//

func shouldSkipDir(name string, cfg *Config) bool {
	if cfg.IgnoreGit && name == ".git" {
		return true
	}

	if cfg.IgnoreVenv {
		switch name {
		case ".venv", "venv", "__pycache__", "node_modules":
			return true
		}
	}

	if cfg.Exclude[name] {
		return true
	}

	return false
}

func shouldSkipFile(path, name string, cfg *Config) bool {
	if name == ".DS_Store" || strings.HasPrefix(name, "._") {
		return true
	}

	abs, _ := filepath.Abs(path)

	if cfg.Exclude[name] || cfg.Exclude[path] || cfg.Exclude[abs] {
		return true
	}

	return false
}

//
// HELPERS
//

func isBinary(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}
