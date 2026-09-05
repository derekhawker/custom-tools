# Codesitter Go Implementation Plan

This plan tracks implementation of a Go-based wrapper around Tree-sitter for project indexing and semantic code queries across JavaScript, TypeScript, Go, Java, and Rust.

## Goals

- Provide `index`, `status`, and `query` commands.
- Require `--force` for indexing to prevent accidental long-running operations.
- Return consistent JSON envelopes for CLI and API-style consumption.
- Support incremental indexing for performance.

## Milestones Checklist

### 0) Project bootstrap
- [x] Create Go module and base folder layout (`cmd`, `internal/model`, `internal/cli`, `internal/indexer`, `internal/query`, `internal/store`).
- [x] Add CLI framework (`cobra` or stdlib `flag`) and command wiring.
- [x] Add shared error code constants and exit code mapping.

### 1) Shared response models
- [x] Implement `Envelope`, `ErrDetail`, `IndexResponse`, `StatusResponse`, `QueryResponse` structs.
- [x] Add JSON serialization tests for response contracts.

### 2) Storage layer (SQLite)
- [x] Choose driver (`modernc.org/sqlite` preferred for pure Go).
- [x] Create schema migration for `projects`, `files`, `symbols`, `relations`, `index_runs`.
- [x] Add repository methods for upsert/select operations.
- [x] Implement index lock mechanism (`flock` or lock table).

### 3) Index command (`codesitter index`)
- [x] Parse args: `--path`, `--force`, `--incremental`, `--lang`, `--include`, `--exclude`, `--max-files`, `--json`.
- [x] If `--force` is missing, return warning, `requires_force=true`, and exit code `2`.
- [x] Walk files with filters and language detection.
- [x] Support JavaScript, TypeScript, Go, Java, and Rust file detection and parsing.
- [x] Compute file hash/mtime and detect changed/deleted files.
- [x] Parse changed files via Tree-sitter and extract symbols/relations.
- [x] Persist index run stats and print `IndexResponse`.

### 4) Status command (`codesitter status`)
- [x] Return whether index exists for `--path`.
- [x] Report counts (files/symbols/relations) and language set.
- [x] Compute freshness (`stale_files`, `deleted_files`, `needs_reindex`).
- [x] Emit JSON/plain output consistently.

### 5) Query command (`codesitter query`)
- [x] Implement intent router for:
  - [x] `find_declarations --symbol`
  - [x] `find_references --symbol`
  - [x] `find_callers --symbol`
  - [x] `find_callees --symbol`
  - [x] `get_file_outline --file`
  - [x] `find_importers --module`
- [x] Return normalized `QueryResult` objects with snippet and score.
- [x] Handle missing index with `NOT_INDEXED` and exit code `5`.

### 6) Incremental indexing correctness
- [x] Re-index only changed files by content hash.
- [x] Remove records for deleted files.
- [x] Recompute impacted relations.
- [x] Add integration test for two consecutive runs.

### 7) Quality and developer experience
- [x] Add unit tests for argument validation and error paths.
- [x] Add integration tests for `index -> status -> query` flow.
- [x] Add benchmark command or timing logs for indexing.
- [x] Write README usage examples and troubleshooting notes.

## Initial Command Examples

```bash
codesitter index --path ~/programs/javascript/homepage --force --lang js,ts,tsx --json
codesitter index --path ~/programs/rust/project --force --lang rust --json
codesitter status --path ~/programs/javascript/homepage --json
codesitter query find_declarations --path ~/programs/javascript/homepage --symbol renderPage --limit 20 --json
```

## Definition of Done

- `index` blocks without `--force` and returns the expected warning payload.
- `index --force` runs successfully and persists index metadata.
- `status` reflects fresh/stale index state accurately.
- `query` returns deterministic, normalized results for supported intents.
- Tests cover critical command behavior and response contracts.
