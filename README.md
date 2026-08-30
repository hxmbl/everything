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
(requires Go 1.26.2+)


### Download a release binary

Grab the right tarball from the [releases page](https://github.com/Hxmbl/everything/releases), extract it, and drop it in your PATH.

### Or Build from source

```bash
git clone https://github.com/Hxmbl/everything
cd everything
go build -o everything && ./everything
```

## Quick intro

```bash
# Dump everything to a file (recommended)
everything --output audit.txt

# Or pipe to less for safe viewing
everything | less

# Syntax highlighted output
everything --color | less -R
```

---

## Flags that actually exist

| Flag                       | What it does                                                 | Example                                |
| -------------------------- | ------------------------------------------------------------ | -------------------------------------- |
| `--output <path>`          | Write to file (auto-excludes itself from scan, refuses to clobber, refuses symlinks) | `everything --output out.txt`    |
| `--exclude <list>` or `--ignore <list>` | Comma-separated names/paths to skip (exact match, no globs)  | `--exclude "vendor,secrets.txt"`       | 
| `--max-size <n>`           | Skip files larger than this (`B`, `KB`, `MB`, `GB`, `TB`); invalid values are errors, not "no limit" | `--max-size 1MB` or `--max-size 500KB` |
| `--include-binaries`       | Include binary files (skipped by default)                    | `--include-binaries`                   |
| `--force` or `--overwrite` | Overwrite existing output file (explicit `--output` only — a positional arg never clobbers an existing file) | `--force`/`--overwrite`                |
| `--color` or `--highlight` | Enable syntax highlighted output (persisted; a saved preference only auto-applies on a real terminal) | `everything --color`                   |
| `--no-color`               | Disable color output (persisted)                             | `everything --no-color`                |
| `--theme <name>`           | Color theme (implies `--color`; default: monokai; invalid names are rejected) | `--theme dracula`                      |
| `--list-themes`            | List all available color themes                              | `--list-themes`                        |
| `--ignore-venv`            | (on by default) Skip `.venv`, `venv`, `__pycache__`, `node_modules` | `--ignore-venv`                        |
| `--include-venv`           | Disable auto-venv skipping                                   | `--include-venv`                       |
| `--json`                   | Output JSONL (one `{"path","content"}` object per line)      | `everything --json`                    |
| `--omitted-disclaimer`     | List skipped files on stderr at end of scan                  | `--omitted-disclaimer`                 |
| `--follow-symlinks`        | Read file symlinks (skipped by default; directory symlinks stay skipped, pipes/devices always skipped) | `--follow-symlinks`                    |
| `--stdout-safe`            | Refuse to dump raw to an interactive terminal without `--output` | `--stdout-safe`                    |
| `--benchmark`, `--bench`   | Time a traversal instead of writing a snapshot. Reads and counts the full content of every included file, then reports file/dir counts, logical bytes, content bytes read, lines of code, characters, traversal rate (at min), and peak heap | `everything --benchmark`    |
| `--runs <n>`               | With `--benchmark`, repeat the traversal n times after a warmup pass and report min/median/mean/max times + peak heap | `everything --benchmark --runs 5` |
| `--warmup <n>`             | With `--benchmark`, untimed warmup passes before the timed ones (default 1; pass 0 for a cold-cache measurement) | `everything --benchmark --warmup 0` |
| `--version`, `-v`          | Print version and exit                                       | `everything -v`                        |
| `--help`, `-h`             | Print help and exit                                          | `everything --help`                    |

Positional args work too — directories you pass become the scan roots (default is `.`), and any other bare argument becomes the output path — but only if it doesn't already exist. An existing file is refused instead of clobbered, so `everything src/ context.txt` scans `src/` into `context.txt`, while a typo like `everything srt/` just creates a new file. To overwrite something on purpose, say so: `--output <path> --force`.

---

## Common workflows

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

# JSONL for scripts (one {"path","content"} per line)
everything --json --output feed.jsonl

# Share project structure + contents
everything --output audit.txt
# (tree output is included automatically if you have `tree` installed)

# See what got skipped (and why)
everything --output audit.txt --omitted-disclaimer

# Profile traversal speed/size (repeat 5x for stable stats)
everything --benchmark --runs 5
```

^ Have I ever actually done any of these? No. Do I plan to? No. But the options are there if you want them.

---

## Why this exists

Don't know. Saves time everyone seemed to have anyway.

Started as a bash alias. Went skidding. Now it's a Go binary that still does the same thing but with flags.

Real use cases people actually use this for:
- **Dumping code into LLM prompts** (the main one)
- Code review prep
- Quick project snapshots for sharing
- "Where did I put that function?" full-text search (pipe it into `grep` — `everything | grep "func foo"`)  <-- Not like it's built into ur IDE or anything

---

## What gets skipped automatically

The output file itself (so it doesn't eat itself — no infinite loops).
The running binary (so it doesn't dump itself).
`.git/`, `target/`, `.DS_Store`, `._*` files (always).
Symlinks (unless you pass `--follow-symlinks`; directory symlinks always).
Pipes, devices, and sockets (so a stray fifo can't hang the scan).
Binary files (unless you pass `--include-binaries`).
Venv/generated dirs by default (`.venv`, `venv`, `__pycache__`, `node_modules`).
Secret-looking files: `.env*` and `*.env`, `id_rsa*`/`id_ed25519*`/`id_dsa*`/`id_ecdsa*`, `*.pem`/`*.key`/`*.p12`/`*.pfx`/`*.jks`/`*.keystore`/`*.kdbx`, `credentials*`, `client_secret*`, `*service-account*.json`, `secrets.*`, `.netrc`, `.htpasswd`, `.npmrc`, `.pypirc`, `.git-credentials`.
Anything whose first 4KB contains a PEM private key block (`-----BEGIN ... PRIVATE KEY`) — even if the name looks innocent or the header is wrapped in JSON/whitespace/BOM.

Run with `--omitted-disclaimer` to see exactly what got skipped before you share the dump.

---

## Notes

- This tool stays simple. If it ever gets complicated, something went seriously wrong.
- The `tree` command output is included automatically if you have it installed. If not, it ghosts u.
- Use `--color` to enable syntax highlighting. Pipe to `less -R` for paged colored output.
- Note 3: yes

---

## Benchmark mode

Don't want a dump? Just want to know how big your project is and how fast `everything` can walk it? Pass `--benchmark` and it skips the writing entirely — it just times the traversal and reports file/dir counts, total logical bytes, bytes actually read from disk, lines of code and characters, the traversal rate, and peak heap used. Benchmark traversal applies the same filtering as a real dump (max-size, binary and secret-file skipping) and reads the *full content* of every included file, so the numbers reflect what you'd actually get — including the content-read cost.

```bash
everything --benchmark
```

An untimed warmup pass runs first so the timed runs see a warm OS page cache (a cold first run can be 2-3x slower and would otherwise skew every stat). Throw in `--runs <n>` to repeat the traversal n times and get a more stable read: it reports the min, median, mean, and max traversal times plus peak heap. Rates are computed from the min (best-case, most reproducible) time.

```bash
everything --benchmark --runs 5
```

Pass `--warmup 0` if you specifically want to measure a cold cache.

---

## License

No.
