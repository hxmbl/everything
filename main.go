// Package everything is a tool for dumping entire project directories into a single file.
// It recursively walks directories and outputs file paths and contents, making it useful
// for feeding code to LLMs, code review preparation, or creating project snapshots.
// The tool automatically skips binaries, secrets, and common build artifacts.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// version is overridden at release time by goreleaser via
// -X main.version={{.Version}}, so the identifier and package must not move.
var version = "v1.10.0"

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

	// --stdout-safe refuses the raw dump; no other flag overrides it. --force
	// belongs to the file path only: it permits clobbering an existing
	// --output file, and has no meaning when the output is os.Stdout. Honoring
	// it here would defeat the whole point of the flag, since a dump pasted
	// into the user's shell is what it exists to prevent.
	if cfg.OutputPath == "" && isInteractive() && cfg.StdoutSafe {
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

	printOmittedDisclaimer(cfg)
}
