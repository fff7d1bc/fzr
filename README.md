# fzr

`fzr` is a small fzy-like path picker with built-in filesystem search.

It is meant for the workflow where you would normally use

```
find . | sort | fzy
```

That pipeline is useful, but it treats paths as plain text. `fzr` scans the
filesystem itself, keeps path metadata separate from display text, and ranks
path-shaped matches directly.

The basic job stays simple. Open the picker, type a few fragments, choose a
path, and get the selected relative path on stdout.

## Quick Start

```
fzr -i .
```

Interactive mode scans the directory tree and opens a picker. The picker UI is
drawn on stderr, and the selected path is printed on stdout.

```
selected/path.txt
```

Esc cancels the picker. Cancel exits with status `1` and prints no path.
External SIGINT, SIGTERM, and SIGHUP restore the terminal before `fzr`
terminates from the same signal, preserving the shell's signal-derived status.

Non-interactive listing is available too.

```
fzr .
fzr --files .
fzr --dirs .
fzr --sort=mtime .
```

## Usage

```
fzr [options] root
fzr --eval zsh
```

Commands that scan need an explicit root. With no root, or with only scan
options and no root, `fzr` prints help. A root path that does not exist is an
error. During traversal, permission-denied paths and paths that disappear while
scanning are skipped.

Common examples:

```
fzr .
fzr -f src
fzr -d .
fzr -s mtime .
fzr --ignore-common .
fzr --ignore target --ignore dist .
fzr -i .
fzr -i -f ~/src
fzr -i -c .
fzr -i --style yellow,bold,underline .
fzr -i --style plain .
```

Options

- `-i`, `--interactive` opens the picker.
- `-f`, `--files` lists files only.
- `-d`, `--dirs` lists directories only.
- `-s`, `--sort=path|mtime` chooses path order or modification-time order.
- `-c`, `--case-sensitive` makes interactive matching case-sensitive.
- `-C`, `--ignore-common` skips `.git`, `.terraform`, `node_modules`, `venv`,
  `.venv`, `__pycache__`, `.tox`, and `.cache`.
- `-I`, `--ignore NAME` skips directories with this basename. Can be repeated.
- `--follow-symlinks` follows symlinked directories and files.
- `--style STYLE` sets the interactive match highlight style.
- `--eval SHELL` prints a shell integration script. Currently supports `zsh`.
- `-h`, `--help` prints help.

`--files` and `--dirs` cannot be used together.

In non-interactive mode, `--sort=mtime` prints oldest paths first. In
interactive mode, Ctrl-Space can sort the current query's matched paths newest
first.

## Interactive Picker

The picker reserves one prompt line and ten result rows. It does not switch to a
full-screen alternate buffer.

```
> query
```

Keys

- Type printable characters to edit the query.
- Backspace deletes before the cursor.
- Left and right arrows move inside the query.
- Home and End move to the start and end of the query.
- Ctrl-U clears the query.
- Up and down arrows move the selection.
- Ctrl-N and Ctrl-P also move the selection.
- Ctrl-Space sorts current matches by modification time, newest first.
- Enter prints the selected path.
- Esc cancels.

On macOS, Ctrl-Space may be assigned to input source switching. If Ctrl-Space
does nothing in `fzr`, check the macOS keyboard shortcuts for input sources.

Directories are shown with a trailing `/`. Matching uses the real candidate
path, not display-only markers.

The picker escapes control characters and other non-printing filename bytes in
Go-style notation such as `\\n`, `\\x1b`, and `\\u202e`. A literal backslash is
shown as `\\\\`. These escapes are display-only: matching and the selected path
on stdout continue to use the original filesystem name.

Matched characters are green, bold, and underlined by default. Use
`--style yellow,bold,underline` to switch to yellow, or `--style plain` to
disable match styling. Style tokens are comma-separated and support `green`,
`yellow`, `bold`, `underline`, and `plain`.

## Matching

Matching is case-insensitive by default. Use `-c` or `--case-sensitive` for
case-sensitive matching.

Spaces split the query into required fragments.

```
alpha beta .mkv
```

That query requires all three fragments to match somewhere in the path. It works
like staged filtering without a separate commit step.

Use spaces when the query is made of separate facts: path words, extensions,
dates, numbers, or other fragments that must all be present. Each fragment is
matched independently, so this is the right form when you want staged filtering
without caring much about the order you typed the fragments.

Leave text glued together when you want one fuzzy abbreviation of the path. In
that form the whole query is scored as one ordered fuzzy sequence, so it can rank
differently from the same text split into fragments.

Ranking prefers

- contiguous substring matches over scattered fuzzy matches
- matches at path boundaries such as `/`, `-`, `_`, space, and `.`
- stronger token matches without making space-separated token order significant
- separate occurrences for repeated words when possible
- bounded numeric tokens such as episode `10` over `10` inside `1080p`
- bounded dotted version fragments such as `385` matching `3.8.5`, including
  typed prefixes such as `38`
- bounded numeric endings in glued fuzzy queries such as `...1080p10`

Example paths

```
media/series/sample-show/s01/Sample Show - 01 (1080p).mkv
media/series/sample-show/s01/Sample Show - 10 (1080p).mkv
media/series/sample-show/s01/Sample Show - 11 (1080p).mkv
```

Query

```
sample show 1080p 10
```

The episode `10` path ranks first. The `10` inside `1080p` can still match as a
fallback, but a separate path component or filename token is stronger.

## How Matching Differs From fzy

For a glued query with no spaces, `fzr` keeps the same basic feel as fzy. It
uses an ordered fuzzy alignment and rewards compact matches, consecutive
characters, and path-like boundaries.

`fzr` differs once the input looks like path search instead of a single
abbreviation.

- Spaces split the query into required fragments instead of becoming one fuzzy
  sequence. `alpha beta mkv` means all three fragments must match the path.
- Fragment order is mostly not important. `alpha beta mkv` and `mkv alpha beta`
  are meant to find the same kind of result.
- A fragment that appears as a real substring is stronger than a scattered fuzzy
  match.
- Path boundaries matter. Matches after `/`, `-`, `_`, space, and `.` rank
  better than matches buried inside a word.
- Repeated fragments prefer separate occurrences when possible, so a query like
  `name 1080p 1080p` can match one `1080p` in a directory and another in a
  filename.
- Numeric fragments prefer bounded occurrences. This helps `10` find an episode
  or numbered file instead of the `10` inside `1080p`.
- Numeric fragments can also weakly match one bounded dotted version run, so
  `385` can find `3.8.5` without matching arbitrary scattered digits. Typed
  prefixes such as `38` match too, which keeps interactive narrowing stable.
- Glued fuzzy queries with a trailing number also prefer a bounded numeric
  ending when there is a good one.

The matcher still stays lightweight. Glued fuzzy scoring uses dynamic alignment
only over the useful part of a path, and falls back to a cheaper scorer for very
large windows. Interactive narrowing also reuses the current result list when
you append to the query.

In practice, use spaces for separate facts and glued text for one abbreviation.

```
project report pdf 2026
prjreppdf2026
```

The first form behaves like staged filtering. The second form behaves like one
ordered fuzzy abbreviation.

## Large Trees

Scanning starts immediately and results are added as they are discovered. A
directory's immediate entries are discovered before its descendants, so nearby
paths can appear in the picker while a large subtree is still being scanned.

Filtering has a short delay on large candidate sets so typing does not turn into
a slow per-character refresh. When you add text to the end of the query and no
new scan results arrived, `fzr` narrows the existing match list instead of
starting from every known path again.

While scanning is still discovering paths, filtered results are applied from
stable snapshots of the paths known so far. New scan batches can make that
snapshot briefly stale, and `fzr` schedules another filtering pass instead of
blocking the UI or reranking every batch synchronously. When scanning finishes,
the current query is refreshed against the complete discovered set.

That means a query can get cheaper as it becomes more specific. If a first
fragment leaves a small matched list, the next appended fragment filters that
list. Backspacing or editing earlier text falls back to a broader search because
previously rejected paths may become valid again.

The delay depends on the number of candidates being filtered.

- 1,000 or fewer candidates filter immediately.
- 1,001 to 9,999 candidates wait 100ms after the last edit.
- 10,000 or more candidates wait 250ms after the last edit.

When narrowing an existing result list, the delay is based on the size of that
current list rather than the total number of discovered paths.

The status line uses `total` for discovered paths so far and `matched` for all
ranked matches for the current query. When it says `showing top N`, the picker
has a larger matched set but exposes only the active top-ranked subset for
selection and current-result actions.

Multi-token searches can use a soft cutoff. If the top matches look strong, the
picker can expose the top 50. If matches look mixed or weaker, it can expose up
to 200. Single-token searches and small result sets can show all matches. This
keeps weak tail matches out of selection and out of normal current-result
actions without discarding the full ranked match list internally.

Ctrl-Space first applies pending filtering for paths discovered so far, then
sorts all matched paths for the current query newest first, including matches
outside the normal top 50 or top 200 window. It does not wait for the whole tree
scan to finish. It does not necessarily stat the whole discovered tree, and it
does not stat directories. Mtime values are cached during the picker session.
The stat call reads metadata, not file contents, so it can be fast after
traversal has warmed filesystem caches.

## Ignoring Directories

By default, `fzr` does not ignore anything.

Use `--ignore-common` for noisy project directories

```
fzr --ignore-common .
fzr -i --ignore-common .
```

It skips these directory basenames.

- `.git`
- `.terraform`
- `node_modules`
- `venv`
- `.venv`
- `__pycache__`
- `.tox`
- `.cache`

Use `--ignore` for your own directory basenames

```
fzr --ignore target --ignore dist .
```

Ignore matching is by directory basename. A directory named `target` is skipped
wherever it appears below the root.

## Symlinks

By default, symlinks are listed but not followed.

Use `--follow-symlinks` to follow symlinked files and directories:

```
fzr --follow-symlinks .
fzr -i --follow-symlinks .
```

When following symlinked directories, `fzr` avoids directory cycles. If two
different symlink paths point at the same directory, both paths can appear.

## Output

Non-interactive mode prints one relative path per line.

Paths are written to stdout exactly as stored by the filesystem. Because the
format is line-oriented, filenames containing carriage returns or newlines
cannot be represented unambiguously. Interactive selection is also followed by
a newline, and shell command substitution cannot preserve trailing newlines in
a selected filename.

Interactive mode writes UI to stderr and prints only the selected path to
stdout. That makes command substitution work cleanly.

```
path="$(fzr -i .)"
```

## Integration

### Zsh

Recommended Ctrl-F widget setup

```
if command -v fzr >/dev/null 2>&1; then
    eval "$(fzr --eval zsh)"
fi
```

The widget runs `fzr -i --ignore-common`, then inserts the selected relative
path into `LBUFFER` using zsh's `${(q)}` escaping. Paths with spaces, quotes,
`$`, brackets, parentheses, and glob characters stay safe and editable.

The generated widget also understands the path-like word touching the cursor.
If your prompt already contains a directory prefix such as `~/tmp/` or `src*/`,
Ctrl-F searches from that directory. Whitespace before the cursor starts a
separate path insertion from the current directory.

## Build And Install

Building requires Go 1.26 or newer.

Build the binary

```
make -B build
```

The binary is written to

```
build/<goos>-<goarch>/bin/fzr
```

Install it

```
make install
```

As root, install copies to `/usr/local/bin`. As a normal user, it copies to
`$HOME/.local/bin`.

Useful project commands

```
make test
make -B build
make static
make bench
make clean
```

## Benchmarks

Benchmarks build their test data in memory from deterministic fake paths. Go
cache, module cache, profiles, and benchmark binaries stay under `./build`.

Run the normal benchmark set

```
make bench
```

Run a focused matcher benchmark

```
make bench BENCH='RankEntries/(100000|1000000)' BENCHTIME=1s COUNT=1
```

Generate CPU and heap profiles under `./build`

```
make bench-profile BENCH=RankEntries/100000 BENCHTIME=2s
```

Benchmark a real directory tree when needed

```
FZR_BENCH_ROOT=/path/to/large/tree make bench BENCH=CollectEntriesRealRoot BENCHTIME=1x
```

The real-tree benchmark is opt-in. Do not commit copied paths or output from
that mode.

Benchmark areas

- `RankEntries` measures full-query matcher cost across synthetic corpus sizes,
  including token-heavy searches, glued fuzzy searches, and broad-match stress
  searches.
- `RankMatchesNarrowing` measures the append-at-end fast path after a previous
  query narrowed the list.
- `EffectiveMatchesWindow` measures the bounded effective match set used by
  strong multi-token searches.
- `PickerApplyQuery` includes picker model state and query application cost.
- `RenderPickerVisibleRows` keeps rendering scoped to the prompt and ten result
  rows.
- `CollectEntries*` measures filesystem traversal and scanner allocation cost.

Performance notes

- ASCII paths use a byte-based matcher path. Unicode paths use the rune matcher.
- Query tokens are prepared once per query, not once per candidate.
- Glued fuzzy matching uses dynamic scoring over the useful match window.
  Oversized windows fall back to a cheaper greedy score.
- Space-separated fragments are required filters. Non-overlapping token chains
  help repeated words and numeric fragments rank the intended occurrence higher.
- Strong multi-token searches keep full ranked matches internally but expose a
  smaller effective match set for selection and current-result actions.
- Very broad queries can still be expensive because retaining and sorting a huge
  match set dominates matcher scoring cost.
