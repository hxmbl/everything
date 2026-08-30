# everything
(vibecoded)

Dump your entire project into a single file, mostly because you're about to feed it to an LLM, ran out of cursor credits, and can't be bothered to copy 47 files.

Recursively walks your directory and prints every file's path + contents. That's it. That's the tool.

```bash
everything > audit.txt
```

Works with text (omg wow no way ✨). Works with any codebase.

## Installation

### Homebrew

```bash
brew install hxmbl/tap/everything
```

### go install (easiest)

```bash
go install github.com/hxmbl/everything@latest
```
(requires Go 1.26.2+, or whatever go you have lying around)

### Download a release binary

Grab the right tarball from the [releases page](https://github.com/Hxmbl/everything/releases), extract it, and drop it in your PATH.

### Build from source

```bash
git clone https://github.com/Hxmbl/everything
cd everything
go build -o everything && ./everything
```

## Quick Start

```bash
# Dump everything to a file (recommended)
everything --output audit.txt

# Or pipe to less for safe viewing
everything | less

# Syntax highlighted output
everything --color | less -R
```

## Features

- **Comprehensive project dumping**: Recursively walks directories and outputs file paths + contents
- **Smart filtering**: Automatically skips binaries, secrets, and common build artifacts
- **Multiple output formats**: Plain text, JSON, or JSON Lines
- **Syntax highlighting**: Optional color output with theme support
- **Safe by default**: Won't clobber existing files, skips secret files automatically
- **Performance profiling**: Built-in benchmark mode to measure traversal speed
- **Cross-platform**: Works on Linux, macOS, and Windows

## Flags that actually exist

| Flag                       | Description                                                  | Example                                |
| -------------------------- | ------------------------------------------------------------ | -------------------------------------- |
| `--output <path>`          | Write to file (auto-excludes itself, refuses clobber/symlinks) | `everything --output out.txt`    |
| `--exclude <list>`         | Comma-separated names/paths to skip (exact match, no globs)  | `--exclude "vendor,secrets.txt"`       | 
| `--max-size <n>`           | Skip files larger than this (B, KB, MB, GB, TB)              | `--max-size 1MB` or `--max-size 500KB` |
| `--include-binaries`       | Include binary files (skipped by default)                    | `--include-binaries`                   |
| `--force`                  | Overwrite existing output file (with `--output` only)        | `--force`                               |
| `--color`                  | Enable syntax highlighted output (persisted preference)      | `everything --color`                   |
| `--no-color`               | Disable color output (persisted)                             | `everything --no-color`                |
| `--theme <name>`           | Color theme (implies `--color`; default: monokai)             | `--theme dracula`                      |
| `--list-themes`            | List all available color themes                              | `--list-themes`                        |
| `--include-venv`           | Include venv directories (skipped by default)                | `--include-venv`                       |
| `--json`                   | Output JSON array of `{"path","content"}` objects             | `everything --json --output out.json` |
| `--jsonl`                  | Output JSON Lines (one object per line)                      | `everything --jsonl --output out.jsonl` |
| `--omitted-disclaimer`     | List skipped files on stderr at end of scan                  | `--omitted-disclaimer`                 |
| `--follow-symlinks`        | Read file symlinks (skipped by default)                      | `--follow-symlinks`                    |
| `--stdout-safe`            | Refuse to dump to interactive terminal without `--output`    | `--stdout-safe`                        |
| `--benchmark`              | Time traversal instead of writing snapshot                    | `everything --benchmark`               |
| `--runs <n>`               | With `--benchmark`, repeat traversal n times                 | `everything --benchmark --runs 5`     |
| `--warmup <n>`             | With `--benchmark`, untimed warmup passes (default: 1)       | `everything --benchmark --warmup 0`   |
| `--version`, `-v`          | Print version and exit                                       | `everything -v`                        |
| `--help`, `-h`             | Print help and exit                                          | `everything --help`                    |

**Positional arguments**: Directories become scan roots (default: `.`), other arguments become output path (only if doesn't exist). Use `--output <path> --force` to overwrite.

## Usage Examples

```bash
# Feed your Go project to an LLM
everything --output context.txt

# Exclude noise (exact name/path matches — no globs)
everything --exclude "vendor,pkg/generated" --output prompt.txt

# Pipe directly into grep
everything | grep "TODO\|FIXME\|HACK"

# Pretty-print the dump for skimming
everything --color | less -R

# Skip large generated files
everything --max-size 100KB --output clean.txt

# One JSON document (array of {"path","content"} objects)
everything --json --output out.json

# JSON Lines for scripts (streaming, one object per line)
everything --jsonl --output feed.jsonl

# Share project structure + contents
everything --output audit.txt
# (tree output included automatically if you have `tree` installed)

# See what got skipped (and why)
everything --output audit.txt --omitted-disclaimer

# Profile traversal speed/size (repeat 5x for stable stats)
everything --benchmark --runs 5
```

^ Have I ever actually done any of these? No. Do I plan to? No. But the options are there if you want them.

## What Gets Skipped Automatically

- The output file itself (prevents infinite loops)
- The running binary (prevents self-dumping)
- `.git/`, `target/`, `.DS_Store`, `._*` files (always)
- Symlinks (unless `--follow-symlinks`; directory symlinks always skipped)
- Pipes, devices, and sockets (prevents hanging)
- Binary files (unless `--include-binaries`)
- Venv/generated dirs: `.venv`, `venv`, `__pycache__`, `node_modules`
- Secret-looking files: `.env*`, `*.env`, `id_rsa*`, `id_ed25519*`, `id_dsa*`, `id_ecdsa*`, `*.pem`, `*.key`, `*.p12`, `*.pfx`, `*.jks`, `*.keystore`, `*.kdbx`, `credentials*`, `client_secret*`, `*service-account*.json`, `secrets.*`, `.netrc`, `.htpasswd`, `.npmrc`, `.pypirc`, `.git-credentials`
- Files whose first 4KB contains a PEM private key block (`-----BEGIN ... PRIVATE KEY`)

Run with `--omitted-disclaimer` to see exactly what got skipped before sharing.

## Benchmark Mode

Don't want a dump? Just want to know how big your project is and how fast `everything` can walk it? Pass `--benchmark` to time the traversal and report file/dir counts, total logical bytes, bytes actually read from disk, lines of code, characters, traversal rate, and peak heap used.

```bash
everything --benchmark
```

An untimed warmup pass runs first so timed runs see a warm OS page cache (cold first run can be 2-3x slower). Use `--runs <n>` to repeat the traversal n times for more stable stats: reports min, median, mean, and max traversal times plus peak heap. Rates are computed from the min (best-case, most reproducible) time.

```bash
everything --benchmark --runs 5
```

Pass `--warmup 0` to specifically measure a cold cache.

## Why This Exists

Started as a bash alias. Went skidding. Now it's a Go binary that still does the same thing but with flags.

Real use cases people actually use this for:
- **Dumping code into LLM prompts** (the main one)
- Code review prep
- Quick project snapshots for sharing
- Full-text search (pipe to grep: `everything | grep "func foo"`)

## Notes

- This tool stays simple. If it ever gets complicated, something went seriously wrong.
- The `tree` command output is included automatically if you have it installed. If not, it ghosts u.
- Use `--color` to enable syntax highlighting. Pipe to `less -R` for paged colored output.
- Note 3: yes

## License

No.
