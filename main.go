// Package everything is a tool for dumping entire project directories into a single file.
// It recursively walks directories and outputs file paths and contents, making it useful
// for feeding code to LLMs, code review preparation, or creating project snapshots.
// The tool automatically skips binaries, secrets, and common build artifacts.
//
// The entry point stays in the module root so `go install
// github.com/hxmbl/everything@latest` keeps working; the implementation lives
// in internal/ and is orchestrated here.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hxmbl/everything/internal/benchmark"
	"github.com/hxmbl/everything/internal/classify"
	"github.com/hxmbl/everything/internal/cli"
	"github.com/hxmbl/everything/internal/config"
	"github.com/hxmbl/everything/internal/ignore"
	"github.com/hxmbl/everything/internal/output"
	"github.com/hxmbl/everything/internal/terminal"
)

// version is overridden at release time by goreleaser via
// -X main.version={{.Version}}, so the identifier and package must not move:
// the linker always names the main package's symbols "main.*", which is why
// -X main.version keeps working from this root package.
var version = "v1.10.2"

// main is the entry point for the everything tool.
// It parses arguments, configures output, and runs the directory traversal.
func main() {
	cli.Version = version
	cfg := cli.ParseArgs()

	if cfg.Benchmark {
		benchmark.RunBenchmark(cfg, version)
		os.Exit(0)
	}

	if cfg.OutputPath == "" && terminal.IsInteractive() && !cfg.StdoutSafe {
		fmt.Fprintln(os.Stderr, "Warning: large stdout dumps can break shell input. Use --output or pipe to less.")
		fmt.Fprintln(os.Stderr, "Tip: everything --output snapshot.txt")
	}

	// --stdout-safe refuses the raw dump; no other flag overrides it. --force
	// belongs to the file path only: it permits clobbering an existing
	// --output file, and has no meaning when the output is os.Stdout. Honoring
	// it here would defeat the whole point of the flag, since a dump pasted
	// into the user's shell is what it exists to prevent.
	if cfg.OutputPath == "" && terminal.IsInteractive() && cfg.StdoutSafe {
		fmt.Fprintln(os.Stderr, "Refusing unsafe raw stdout dump. Use --output to write to a file.")
		os.Exit(1)
	}

	writer, cleanup := output.SetupOutput(cfg)

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
		ignore.TryPrintTree(writer, walkDirs)
	}

	for _, root := range walkDirs {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				cfg.RecordSkip(fmt.Sprintf("  unreadable: %s: %v", path, err))
				return nil
			}

			if ignore.ShouldSkip(path, d, cfg) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				if classify.IsSecretFilename(d.Name()) {
					cfg.RecordSkip(fmt.Sprintf("  secret: %s", path))
				}
				return nil
			}

			if d.IsDir() {
				return nil
			}

			info, err := output.GetFileInfo(path, d, cfg)
			if err != nil {
				return nil
			}
			if info == nil {
				return nil
			}

			if cfg.MaxSize > 0 && info.Size() > cfg.MaxSize {
				cfg.RecordSkip(fmt.Sprintf("  too large: %s (%d bytes)", path, info.Size()))
				return nil
			}

			return output.ProcessFile(path, info, writer, cfg, jsonArray, jsonSeparator, jsonClose)
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

	config.PrintOmittedDisclaimer(cfg)
}