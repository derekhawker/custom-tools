# codesitter

`codesitter` is a Go CLI wrapper for indexing JavaScript/TypeScript repositories and running semantic-style code queries.

## Features

- Safe indexing guard: `index` requires `--force`.
- Incremental file indexing by content hash.
- Persistent SQLite index under `.codesitter/index.db`.
- Query intents for declarations, references, callers, callees, file outlines, and importers.
- JSON output for LLM/tool integration.

## Build

```bash
go build ./cmd/codesitter
```

## Commands

Index a project:

```bash
./codesitter index --path ~/programs/javascript/homepage --force --lang js,ts,tsx --json
```

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

- Current symbol extraction is lightweight pattern-based parsing.
- Tree-sitter parser integration is planned for deeper and more accurate extraction.
