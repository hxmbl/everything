# everything
(vibecoded)

Dump your entire project into a single file, mostly because you're about to feed it to an LLM, ran out of cursor credit, and can't be bothered to copy-paste 47 files.

Recursively walks your directory and prints every file's path + contents. That's it. That's the tool.

```
everything > context-for-llm.txt
```

Works with any model. Works with any codebase. Zero dependencies (well, Go, but you already have that).



## Installation

Requires Go 1.20+.

### go install (easiest)

```bash
go install github.com/hxmbl/everything@latest
```

### Homebrew (if i ever get to this)

```bash
brew install hxmbl/everything/everything
```

### Download a release binary

Grab the right tarball from the [releases page](https://github.com/Hxmbl/everything/releases), extract it, and drop it in your PATH.

### Build from source

```bash
git clone https://github.com/Hxmbl/everything
cd everything
go build -o everything && ./everything
```



## Quick start

```bash
# Dump everything to a file (recommended)
everything --output snapshot.txt

# Or pipe to less for safe viewing
everything | less

# Syntax highlighted in less (new!)
everything --less | less -R
```

---

## Flags that actually exist

| Flag                       | What it does                                                 | Example                                |
| -------------------------- | ------------------------------------------------------------ | -------------------------------------- |
| `--output <path>`          | Write to file (auto-excludes itself from scan)               | `everything --output out.txt`          |
| `--exclude <list>`         | Comma-separated names/paths to skip                          | `--exclude "*.exe,secrets.txt"`        |
| `--max-size <n>`           | Skip files larger than this                                  | `--max-size 1MB` or `--max-size 500KB` |
| `--include-binaries`       | Include binary files (skipped by default)                    | `--include-binaries`                   |
| `--force` or `--overwrite` | Overwrite existing output file                               | `--force`/`--overwrite`                |
| `--theme <name>`           | Syntax highlighting theme (default: monokai)                 | `--theme dracula`                      |
| `--list-themes`            | List all available syntax highlighting themes                | `--list-themes`                        |
| `--no-syntax-highlight`    | Disable syntax highlighting                                  | `--no-syntax-highlight`                |
| `--less`                   | Force syntax highlighting (for piping to `less -R`)          | `everything --less \| less -R`         |
| `--ignore-venv`            | (on by default) Skip `.venv`, `venv`, `__pycache__`, `node_modules` | `--ignore-venv`                        |
| `--include-venv`           | Disable auto-venv skipping                                   | `--include-venv`                       |
| `--stdout-safe`            | Require `--output` in interactive shells                     | `--stdout-safe`                        |

Positional args work too — the first non-flag argument is treated as the output path.

---

## Common workflows

```bash
# Feed your Go project to an LLM
everything --output context.txt

# Exclude noise
everything --exclude "vendor,*.pb.go" --output prompt.txt

# Pipe directly into grep
everything | grep "TODO\|FIXME\|HACK"

# Skip large generated files
everything --max-size 100KB --output clean.txt

# Share project structure + contents
everything --output audit.txt
# (tree output is included automatically if you have `tree` installed)
```

^ Have I ever actually done any of these? No. Do I plan to? No. But the options are there if you want them.

---

## 

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
`.git/`, `.DS_Store`, `._*` files (always).
Binary files (unless you pass `--include-binaries`).
Venv/generated dirs by default (`.venv`, `venv`, `__pycache__`, `node_modules`).

---

## Notes

- This tool stays simple. If it ever gets complicated, something went seriously wrong.
- The `tree` command output is included automatically if you have it installed. If not, it ghosts u.
- Syntax highlighting is on by default when outputting to a terminal. Pipe to `less -R` with `--less` to keep the colors.
- Note 3: yes

---

## License

No.
