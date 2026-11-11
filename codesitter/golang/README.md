# codesitter

`codesitter` is a Go CLI and OpenCode tool for indexing JavaScript, TypeScript, Go, Java, Rust, CSS, SCSS, and Sass source, then returning bounded code-navigation evidence. Name-based relationships and text matches are explicitly heuristic; they are not compiler-grade semantic resolution.

## Features

- Safe indexing guard: `index` requires `--force`.
- Full reconciliation discovers and hashes all supported files in the selected languages, then reparses changed files only.
- Targeted updates accept explicitly changed and deleted project-relative file paths.
- Persistent SQLite index and indexed source snapshots under `.codesitter/index.db`.
- Tree-sitter symbol extraction for supported languages.
- Query intents for declarations, references, callers, callees, file outlines, and importers.
- Compact JSON and plain output designed for LLM/tool integration, with bounded snippets and completeness metadata.
- Query results default to 20 and are capped at 200 per request; `has_more` indicates whether additional matches exist.

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

Omitting file paths performs full reconciliation. `--lang` selects one or more supported languages; all matching source files are covered. Common dependency, cache, metadata, and generated-output directories are excluded by default. No include/exclude globs or file-count cap are supported because they create partial indexes.

After a full reconciliation, provide changed and deleted file paths for a targeted update:

```bash
./codesitter index --path ~/programs/javascript/homepage --force --file src/pages/home.ts --deleted src/pages/old-home.ts --json
```

Targeted updates require a usable existing index and only establish that the reported paths were updated. Queries read the indexed source snapshot; filesystem edits appear in query results only after reindexing. `status` reports the last full reconciliation and targeted update separately. Its freshness check detects file metadata changes, deletions, and newly added files in indexed languages; same-size edits with preserved modification times may not be detected until full reconciliation. Source snapshots increase the SQLite index size and remain local under `.codesitter/index.db`.

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
- If `status` shows stale, deleted, or new files, run a full reconciliation with `index --force`.

## Notes

- Symbol and call-site extraction uses Tree-sitter. Call targets use same-name matching and are labeled heuristic, ambiguous, or unresolved.
- Reference and most importer results are line-text matches and can include comments, strings, or declarations. Rust `use` statements are reported as syntax-like text matches; Cargo/type resolution is not performed.
- Queries use the stored snapshot rather than walking the project on every request. After targeted updates, they warn that only reported paths were checked. Use `status` for a filesystem freshness check.
