# codesitter

`codesitter` is a Go CLI wrapper for indexing JavaScript, TypeScript, Go, Java, Rust, CSS, SCSS, and Sass repositories and running semantic-style code queries.

## Features

- Safe indexing guard: `index` requires `--force`.
- Incremental file indexing by content hash.
- Persistent SQLite index under `.codesitter/index.db`.
- Tree-sitter symbol extraction for supported languages.
- Query intents for declarations, references, callers, callees, file outlines, and importers.
- JSON output for LLM/tool integration.

## Supported languages

- JavaScript: `js`, `javascript` (`.js`, `.mjs`, `.cjs`)
- TypeScript: `ts`, `typescript` (`.ts`), and `tsx` (`.tsx`)
- Go: `go`, `golang` (`.go`)
- Java: `java` (`.java`)
- Rust: `rs`, `rust` (`.rs`)
- CSS: `css` (`.css`)
- SCSS: `scss` (`.scss`)
- Sass: `sass` (`.sass`)

## Build

```bash
go build ./cmd/codesitter
```

## Commands

Index a project:

```bash
./codesitter index --path ~/programs/javascript/homepage --force --lang js,ts,tsx --json
./codesitter index --path ~/programs/rust/project --force --lang rust --json
./codesitter index --path ~/programs/web/styles --force --lang css,scss,sass --json
```

Indexing automatically prunes common dependency, cache, metadata, and generated-output directories at any depth, including `node_modules`, `vendor`, `bower_components`, `jspm_packages`, `dist`, `out`, `build`, `target`, and `coverage`. A supplied `--exclude` pattern adds file-level exclusions to these defaults.

Check index status:

```bash
./codesitter status --path ~/programs/javascript/homepage --json
```

Run queries:

```bash
./codesitter query find_declarations --path ~/programs/javascript/homepage --symbol renderPage --limit 20 --json
./codesitter query find_references --path ~/programs/javascript/homepage --symbol renderPage --json
./codesitter query find_callers --path ~/programs/javascript/homepage --symbol helper --json
./codesitter query find_callees --path ~/programs/javascript/homepage --symbol renderPage --json
./codesitter query get_file_outline --path ~/programs/javascript/homepage --file src/pages/home.ts --json
./codesitter query find_importers --path ~/programs/javascript/homepage --module ./utils --json
```

## Exit codes

- `0`: success
- `1`: generic error
- `2`: force required
- `3`: invalid input
- `4`: index lock active
- `5`: project not indexed

## Troubleshooting

- If index is blocked, rerun with `--force`.
- If you get `LOCKED`, wait for the current run or remove stale `.codesitter/index.lock`.
- If query returns `NOT_INDEXED`, run `index --force` first.
- If `status` shows stale/deleted files, rerun `index --force`.

## Notes

- Symbol extraction uses Tree-sitter and is intentionally lightweight.
- Relations are currently inferred with lightweight source-text matching.
