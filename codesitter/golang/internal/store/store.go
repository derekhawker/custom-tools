package store

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const dbFileName = ".codesitter/index.db"
const lockFileName = ".codesitter/index.lock"
const CurrentIndexVersion = "4"

var statusExcludedDirs = map[string]struct{}{
	".angular": {}, ".cache": {}, ".codesitter": {}, ".git": {}, ".hg": {}, ".next": {},
	".nuxt": {}, ".parcel-cache": {}, ".pnpm": {}, ".svelte-kit": {}, ".svn": {}, ".turbo": {},
	".vite": {}, ".yarn": {}, "bower_components": {}, "build": {}, "coverage": {}, "dist": {},
	"jspm_packages": {}, "node_modules": {}, "out": {}, "target": {}, "vendor": {},
}

var ErrNotIndexed = errors.New("project not indexed")
var ErrLocked = errors.New("index already running")

type Lock struct {
	path string
}

type FileRecord struct {
	RelPath     string
	Language    string
	ContentHash string
	MtimeNS     int64
	SizeBytes   int64
	Snapshot    []byte
}

type SymbolRecord struct {
	RelPath   string
	Name      string
	Kind      string
	StartLine int
	EndLine   int
	Signature string
	Language  string
	Evidence  string
}

type CallRecord struct {
	RelPath         string
	CallerName      string
	CallerStartLine int
	CalleeName      string
	CallSiteLine    int
	CallSiteColumn  int
	Snippet         string
}

type CallSiteRecord struct {
	RelPath         string
	Language        string
	CallerName      string
	CallerKind      string
	CallerStartLine int
	CallerEndLine   int
	CalleeName      string
	CallSiteLine    int
	CallSiteColumn  int
	Resolution      string
	Snippet         string
}

var importRE = regexp.MustCompile(`^\s*import\s+.*?from\s+['\"]([^'\"]+)['\"]|^\s*import\s+['\"]([^'\"]+)['\"]`)
var rustUseRE = regexp.MustCompile(`^\s*(pub(\([^)]*\))?\s+)?use\s+([^;]+);`)

var rustUseStartRE = regexp.MustCompile(`^\s*(pub(\([^)]*\))?\s+)?use\b`)

type rustUseStatement struct {
	Module    string
	Signature string
	StartLine int
}

type IndexStats struct {
	FilesScanned     int
	FilesIndexed     int
	SymbolsWritten   int
	RelationsWritten int
}

type Summary struct {
	Version                string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	LastFullReconciliation time.Time
	LastTargetedUpdate     time.Time
	LanguageSet            []string
	LanguageScope          []string
	FileCount              int
	SymbolCount            int
	RelationCount          int
	StaleFiles             int
	DeletedFiles           int
	NewFiles               int
}

func DBPath(projectPath string) string {
	return filepath.Join(projectPath, dbFileName)
}

func LockPath(projectPath string) string {
	return filepath.Join(projectPath, lockFileName)
}

func AcquireLock(projectPath string) (*Lock, error) {
	lockPath := LockPath(projectPath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrLocked
		}
		return nil, err
	}
	_, _ = f.WriteString(time.Now().UTC().Format(time.RFC3339Nano))
	_ = f.Close()
	return &Lock{path: lockPath}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func Open(projectPath string) (*sql.DB, error) {
	dbPath := DBPath(projectPath)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func RecordIndex(db *sql.DB, projectPath string, langs, languageScope []string, mode string, stats IndexStats) error {
	now := time.Now().UTC()
	langCSV := joinCSV(langs)
	scopeCSV := joinCSV(languageScope)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.Exec(`
INSERT INTO projects(root_path, index_version, created_at, updated_at, language_set, language_scope)
VALUES (?, '4', ?, ?, ?, ?)
ON CONFLICT(root_path)
DO UPDATE SET index_version=excluded.index_version, updated_at=excluded.updated_at, language_set=excluded.language_set, language_scope=excluded.language_scope
`, projectPath, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), langCSV, scopeCSV)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
INSERT INTO index_runs(root_path, mode, started_at, ended_at, files_scanned, files_indexed, status)
VALUES (?, ?, ?, ?, ?, ?, 'success')
`, projectPath, mode, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), stats.FilesScanned, stats.FilesIndexed)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func HasIndex(db *sql.DB, projectPath string) (bool, error) {
	var c int
	err := db.QueryRow(`SELECT COUNT(1) FROM projects WHERE root_path = ?`, projectPath).Scan(&c)
	if err != nil {
		return false, err
	}
	return c > 0, nil
}

func GetSummary(db *sql.DB, projectPath string) (Summary, error) {
	s, err := GetIndexSummary(db, projectPath)
	if err != nil {
		return s, err
	}
	recs, err := GetIndexedFiles(db, projectPath)
	if err != nil {
		return s, err
	}
	indexed := make(map[string]struct{}, len(recs))
	for _, rec := range recs {
		indexed[rec.RelPath] = struct{}{}
		full := filepath.Join(projectPath, rec.RelPath)
		st, statErr := os.Stat(full)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				s.DeletedFiles++
			}
			continue
		}
		if st.ModTime().UnixNano() != rec.MtimeNS || st.Size() != rec.SizeBytes {
			s.StaleFiles++
		}
	}
	s.NewFiles, err = countUnindexedSourceFiles(projectPath, indexed, s.LanguageScope)
	if err != nil {
		return s, err
	}
	return s, nil
}

func GetIndexSummary(db *sql.DB, projectPath string) (Summary, error) {
	var s Summary
	var languageSet string
	var languageScope string
	var createdAt string
	var updatedAt string

	err := db.QueryRow(`
SELECT index_version, created_at, updated_at, language_set, language_scope
FROM projects
WHERE root_path = ?
`, projectPath).Scan(&s.Version, &createdAt, &updatedAt, &languageSet, &languageScope)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotIndexed
	}
	if err != nil {
		return s, err
	}

	s.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	s.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	s.LanguageSet = splitCSV(languageSet)
	s.LanguageScope = splitCSV(languageScope)

	if err := db.QueryRow(`SELECT COUNT(1) FROM files WHERE root_path=?`, projectPath).Scan(&s.FileCount); err != nil {
		return s, err
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM symbols WHERE root_path=?`, projectPath).Scan(&s.SymbolCount); err != nil {
		return s, err
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM relations WHERE root_path=?`, projectPath).Scan(&s.RelationCount); err != nil {
		return s, err
	}
	var lastFull, lastTargeted sql.NullString
	if err := db.QueryRow(`SELECT MAX(CASE WHEN mode='full' THEN ended_at END), MAX(CASE WHEN mode='targeted' THEN ended_at END) FROM index_runs WHERE root_path=? AND status='success'`, projectPath).Scan(&lastFull, &lastTargeted); err != nil {
		return s, err
	}
	if lastFull.Valid {
		s.LastFullReconciliation, _ = time.Parse(time.RFC3339Nano, lastFull.String)
	}
	if lastTargeted.Valid {
		s.LastTargetedUpdate, _ = time.Parse(time.RFC3339Nano, lastTargeted.String)
	}

	return s, nil
}

func countUnindexedSourceFiles(root string, indexed map[string]struct{}, languages []string) (int, error) {
	selected := make(map[string]struct{}, len(languages))
	for _, language := range languages {
		selected[language] = struct{}{}
	}
	newFiles := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if _, skip := statusExcludedDirs[strings.ToLower(entry.Name())]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		language, ok := sourceLanguage(filepath.Ext(path))
		if !ok {
			return nil
		}
		if _, selectedLanguage := selected[language]; !selectedLanguage {
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, exists := indexed[filepath.ToSlash(relPath)]; !exists {
			newFiles++
		}
		return nil
	})
	return newFiles, err
}

func sourceLanguage(ext string) (string, bool) {
	switch strings.ToLower(ext) {
	case ".js", ".mjs", ".cjs":
		return "javascript", true
	case ".ts":
		return "typescript", true
	case ".tsx":
		return "tsx", true
	case ".go":
		return "go", true
	case ".java":
		return "java", true
	case ".rs":
		return "rust", true
	case ".css":
		return "css", true
	case ".scss":
		return "scss", true
	case ".sass":
		return "sass", true
	default:
		return "", false
	}
}

func HashFile(path string) (string, int64, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, 0, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		return "", 0, 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), st.ModTime().UnixNano(), st.Size(), nil
}

func GetIndexedFiles(db *sql.DB, projectPath string) ([]FileRecord, error) {
	rows, err := db.Query(`SELECT rel_path, language, content_hash, mtime_ns, size_bytes FROM files WHERE root_path=? ORDER BY rel_path`, projectPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []FileRecord{}
	for rows.Next() {
		var r FileRecord
		if err := rows.Scan(&r.RelPath, &r.Language, &r.ContentHash, &r.MtimeNS, &r.SizeBytes); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func GetIndexedSource(db *sql.DB, projectPath, relPath string) ([]byte, error) {
	var source []byte
	if err := db.QueryRow(`SELECT source_text FROM files WHERE root_path=? AND rel_path=?`, projectPath, relPath).Scan(&source); err != nil {
		return nil, fmt.Errorf("read indexed source snapshot %q: %w", relPath, err)
	}
	if source == nil {
		return nil, fmt.Errorf("indexed source snapshot %q is missing; run a full reconciliation", relPath)
	}
	return source, nil
}

func SyncFiles(db *sql.DB, projectPath string, files []FileRecord) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	current := map[string]FileRecord{}
	existing, err := GetIndexedFiles(db, projectPath)
	if err != nil {
		return 0, err
	}
	for _, e := range existing {
		current[e.RelPath] = e
	}

	indexed := 0
	now := time.Now().UTC().Format(time.RFC3339Nano)
	seen := map[string]struct{}{}
	for _, f := range files {
		seen[f.RelPath] = struct{}{}
		prev, ok := current[f.RelPath]
		if !ok || prev.ContentHash != f.ContentHash {
			indexed++
		}
		_, err = tx.Exec(`
INSERT INTO files(root_path, rel_path, language, content_hash, mtime_ns, size_bytes, indexed_at, source_text)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(root_path, rel_path)
DO UPDATE SET language=excluded.language, content_hash=excluded.content_hash, mtime_ns=excluded.mtime_ns, size_bytes=excluded.size_bytes, indexed_at=excluded.indexed_at,
source_text=CASE WHEN excluded.source_text IS NULL THEN files.source_text ELSE excluded.source_text END
`, projectPath, f.RelPath, f.Language, f.ContentHash, f.MtimeNS, f.SizeBytes, now, nullableSnapshot(f.Snapshot))
		if err != nil {
			return 0, err
		}
	}

	for _, e := range existing {
		if _, ok := seen[e.RelPath]; !ok {
			if err = deleteFileTx(tx, projectPath, e.RelPath); err != nil {
				return 0, err
			}
		}
	}

	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return indexed, nil
}

func UpdateFiles(db *sql.DB, projectPath string, files []FileRecord, deletedPaths []string) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	current := map[string]FileRecord{}
	existing, err := GetIndexedFiles(db, projectPath)
	if err != nil {
		return 0, err
	}
	for _, item := range existing {
		current[item.RelPath] = item
	}
	indexed := 0
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, file := range files {
		prev, ok := current[file.RelPath]
		if !ok || prev.ContentHash != file.ContentHash || prev.Language != file.Language {
			indexed++
		}
		if _, err = tx.Exec(`
INSERT INTO files(root_path, rel_path, language, content_hash, mtime_ns, size_bytes, indexed_at, source_text)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(root_path, rel_path)
DO UPDATE SET language=excluded.language, content_hash=excluded.content_hash, mtime_ns=excluded.mtime_ns, size_bytes=excluded.size_bytes, indexed_at=excluded.indexed_at,
source_text=CASE WHEN excluded.source_text IS NULL THEN files.source_text ELSE excluded.source_text END
`, projectPath, file.RelPath, file.Language, file.ContentHash, file.MtimeNS, file.SizeBytes, now, nullableSnapshot(file.Snapshot)); err != nil {
			return 0, err
		}
	}
	for _, relPath := range deletedPaths {
		if err = deleteFileTx(tx, projectPath, relPath); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return indexed, nil
}

func nullableSnapshot(snapshot []byte) any {
	if snapshot == nil {
		return nil
	}
	if len(snapshot) == 0 {
		return ""
	}
	return snapshot
}

func deleteFileTx(tx *sql.Tx, projectPath, relPath string) error {
	if _, err := tx.Exec(`DELETE FROM symbols WHERE root_path=? AND file_id IN (SELECT id FROM files WHERE root_path=? AND rel_path=?)`, projectPath, projectPath, relPath); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM files WHERE root_path=? AND rel_path=?`, projectPath, relPath)
	return err
}

func ReplaceSymbolsForFile(db *sql.DB, projectPath, relPath string, symbols []SymbolRecord) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var fileID int64
	err = tx.QueryRow(`SELECT id FROM files WHERE root_path=? AND rel_path=?`, projectPath, relPath).Scan(&fileID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`DELETE FROM symbols WHERE root_path=? AND file_id=?`, projectPath, fileID)
	if err != nil {
		return err
	}

	for _, s := range symbols {
		_, err = tx.Exec(`
INSERT INTO symbols(root_path, file_id, name, kind, signature, start_line, end_line, visibility, doc)
VALUES (?, ?, ?, ?, ?, ?, ?, '', '')
`, projectPath, fileID, s.Name, s.Kind, s.Signature, s.StartLine, s.EndLine)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func FindDeclarations(db *sql.DB, projectPath, name string, limit int) ([]SymbolRecord, error) {
	rows, err := db.Query(`
SELECT f.rel_path, s.name, s.kind, s.start_line, s.end_line, s.signature, f.language
FROM symbols s
JOIN files f ON f.id = s.file_id
WHERE s.root_path=? AND s.name LIKE ? ESCAPE '\'
ORDER BY CASE WHEN s.name=? THEN 0 ELSE 1 END, s.name, f.rel_path, s.start_line
LIMIT ?
`, projectPath, "%"+escapeLike(name)+"%", name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []SymbolRecord{}
	for rows.Next() {
		var r SymbolRecord
		if err := rows.Scan(&r.RelPath, &r.Name, &r.Kind, &r.StartLine, &r.EndLine, &r.Signature, &r.Language); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func FileOutline(db *sql.DB, projectPath, relPath string, limit int) ([]SymbolRecord, error) {
	rows, err := db.Query(`
SELECT f.rel_path, s.name, s.kind, s.start_line, s.end_line, s.signature, f.language
FROM symbols s
JOIN files f ON f.id = s.file_id
WHERE s.root_path=? AND f.rel_path=?
ORDER BY s.start_line
LIMIT ?
`, projectPath, relPath, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SymbolRecord{}
	for rows.Next() {
		var r SymbolRecord
		if err := rows.Scan(&r.RelPath, &r.Name, &r.Kind, &r.StartLine, &r.EndLine, &r.Signature, &r.Language); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func LanguageSetFromFiles(files []FileRecord) []string {
	m := map[string]struct{}{}
	for _, f := range files {
		m[f.Language] = struct{}{}
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func RecomputeRelations(db *sql.DB, projectPath string, calls []CallRecord, changedPaths []string) error {
	changed := make(map[string]struct{}, len(changedPaths))
	for _, path := range changedPaths {
		changed[path] = struct{}{}
	}
	existingCalls, err := getCallRecords(db, projectPath)
	if err != nil {
		return err
	}
	for _, call := range existingCalls {
		if _, wasChanged := changed[call.RelPath]; !wasChanged {
			calls = append(calls, call)
		}
	}
	if _, err := db.Exec(`DELETE FROM relations WHERE root_path=?`, projectPath); err != nil {
		return err
	}

	targetByName := map[string][]int64{}
	rows, err := db.Query(`SELECT id, name FROM symbols WHERE root_path=?`, projectPath)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		targetByName[name] = append(targetByName[name], id)
	}
	rows.Close()

	files, err := GetIndexedFiles(db, projectPath)
	if err != nil {
		return err
	}

	for _, f := range files {
		source, err := GetIndexedSource(db, projectPath, f.RelPath)
		if err != nil {
			return err
		}
		sourceText := string(source)
		s := bufio.NewScanner(strings.NewReader(sourceText))
		for s.Scan() {
			line := s.Text()
			for _, mod := range importedModules(line) {
				_, _ = db.Exec(`INSERT INTO relations(root_path, from_symbol_id, to_symbol_id, type) VALUES (?, NULL, NULL, ?)`, projectPath, "imports:"+mod)
			}
		}
		if f.Language == "rust" {
			for _, use := range rustUseStatements(sourceText) {
				_, _ = db.Exec(`INSERT INTO relations(root_path, from_symbol_id, to_symbol_id, type) VALUES (?, NULL, NULL, ?)`, projectPath, "imports:"+use.Module)
			}
		}
		_ = s.Err()
	}
	seenCalls := make(map[[3]int64]struct{}, len(calls))
	for _, call := range calls {
		var fromID int64
		if err := db.QueryRow(`
SELECT s.id FROM symbols s JOIN files f ON f.id=s.file_id
WHERE s.root_path=? AND f.rel_path=? AND s.name=? AND s.start_line=?
LIMIT 1`, projectPath, call.RelPath, call.CallerName, call.CallerStartLine).Scan(&fromID); err != nil {
			continue
		}
		matches := targetByName[call.CalleeName]
		var targetID any
		resolution := "unresolved"
		if len(matches) == 1 {
			targetID = matches[0]
			resolution = "heuristic_name_match"
		} else if len(matches) > 1 {
			resolution = "ambiguous_name_match"
		}
		edge := [3]int64{fromID, int64(call.CallSiteLine), int64(call.CallSiteColumn)}
		if _, exists := seenCalls[edge]; exists {
			continue
		}
		seenCalls[edge] = struct{}{}
		if _, err := db.Exec(`INSERT INTO relations(root_path, from_symbol_id, to_symbol_id, type, call_site_line, call_site_column, target_name, snippet, resolution) VALUES (?, ?, ?, 'calls', ?, ?, ?, ?, ?)`, projectPath, fromID, targetID, call.CallSiteLine, call.CallSiteColumn, call.CalleeName, call.Snippet, resolution); err != nil {
			return err
		}
	}

	return nil
}

func getCallRecords(db *sql.DB, projectPath string) ([]CallRecord, error) {
	rows, err := db.Query(`
SELECT f.rel_path, s.name, s.start_line, r.target_name, r.call_site_line, r.call_site_column, r.snippet
FROM relations r
JOIN symbols s ON s.id=r.from_symbol_id
JOIN files f ON f.id=s.file_id
WHERE r.root_path=? AND r.type='calls' AND r.target_name<>''
ORDER BY f.rel_path, r.call_site_line, r.call_site_column`, projectPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var calls []CallRecord
	for rows.Next() {
		var call CallRecord
		if err := rows.Scan(&call.RelPath, &call.CallerName, &call.CallerStartLine, &call.CalleeName, &call.CallSiteLine, &call.CallSiteColumn, &call.Snippet); err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	return calls, rows.Err()
}

func FindCallers(db *sql.DB, projectPath, symbol string, limit int) ([]CallSiteRecord, error) {
	rows, err := db.Query(`
SELECT sf.rel_path, sf.language, ss.name, ss.kind, ss.start_line, ss.end_line, r.target_name, r.call_site_line, r.call_site_column, r.resolution, r.snippet
FROM relations r
JOIN symbols ss ON ss.id = r.from_symbol_id
JOIN files sf ON sf.id = ss.file_id
WHERE r.root_path=? AND r.type='calls' AND r.target_name=?
ORDER BY sf.rel_path, r.call_site_line, r.call_site_column, ss.start_line
LIMIT ?
`, projectPath, symbol, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := []CallSiteRecord{}
	for rows.Next() {
		var r CallSiteRecord
		if err := rows.Scan(&r.RelPath, &r.Language, &r.CallerName, &r.CallerKind, &r.CallerStartLine, &r.CallerEndLine, &r.CalleeName, &r.CallSiteLine, &r.CallSiteColumn, &r.Resolution, &r.Snippet); err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	return res, rows.Err()
}

func FindCallees(db *sql.DB, projectPath, symbol string, limit int) ([]CallSiteRecord, error) {
	rows, err := db.Query(`
SELECT tf.rel_path, tf.language, ss.name, ss.kind, ss.start_line, ss.end_line, r.target_name, r.call_site_line, r.call_site_column, r.resolution, r.snippet
FROM relations r
JOIN symbols ss ON ss.id = r.from_symbol_id
JOIN files tf ON tf.id = ss.file_id
WHERE r.root_path=? AND r.type='calls' AND ss.name=?
ORDER BY tf.rel_path, r.call_site_line, r.call_site_column, ss.start_line
LIMIT ?
`, projectPath, symbol, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := []CallSiteRecord{}
	for rows.Next() {
		var r CallSiteRecord
		if err := rows.Scan(&r.RelPath, &r.Language, &r.CallerName, &r.CallerKind, &r.CallerStartLine, &r.CallerEndLine, &r.CalleeName, &r.CallSiteLine, &r.CallSiteColumn, &r.Resolution, &r.Snippet); err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	return res, rows.Err()
}

func FindImporters(db *sql.DB, projectPath, module string, limit int) ([]SymbolRecord, error) {
	files, err := GetIndexedFiles(db, projectPath)
	if err != nil {
		return nil, err
	}
	out := []SymbolRecord{}
	for _, f := range files {
		b, err := GetIndexedSource(db, projectPath, f.RelPath)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(b), "\n")
		fileResults := []SymbolRecord{}
		for i, line := range lines {
			if !strings.Contains(line, module) || !strings.Contains(line, "import") {
				continue
			}
			fileResults = append(fileResults, SymbolRecord{RelPath: f.RelPath, Name: module, Kind: "text_match", StartLine: i + 1, EndLine: i + 1, Signature: boundedText(line), Language: f.Language, Evidence: "same-line text match; import syntax is not structurally resolved"})
		}
		if f.Language == "rust" {
			for _, use := range rustUseStatements(string(b)) {
				if strings.Contains(use.Module, module) {
					fileResults = append(fileResults, SymbolRecord{RelPath: f.RelPath, Name: module, Kind: "import", StartLine: use.StartLine, EndLine: use.StartLine, Signature: boundedText(use.Signature), Language: f.Language, Evidence: "Rust use-statement text match; module resolution is not verified"})
				}
			}
		}
		sort.SliceStable(fileResults, func(i, j int) bool {
			return fileResults[i].StartLine < fileResults[j].StartLine
		})
		for _, result := range fileResults {
			out = append(out, result)
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func FindReferences(db *sql.DB, projectPath, symbol string, limit int) ([]SymbolRecord, error) {
	if symbol == "" {
		return []SymbolRecord{}, nil
	}
	declarationRows, err := db.Query(`SELECT f.rel_path, s.start_line FROM symbols s JOIN files f ON f.id=s.file_id WHERE s.root_path=? AND s.name=?`, projectPath, symbol)
	if err != nil {
		return nil, err
	}
	declarations := map[string]struct{}{}
	for declarationRows.Next() {
		var relPath string
		var line int
		if err := declarationRows.Scan(&relPath, &line); err != nil {
			declarationRows.Close()
			return nil, err
		}
		declarations[fmt.Sprintf("%s:%d", relPath, line)] = struct{}{}
	}
	if err := declarationRows.Err(); err != nil {
		declarationRows.Close()
		return nil, err
	}
	declarationRows.Close()
	files, err := GetIndexedFiles(db, projectPath)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
	out := []SymbolRecord{}
	for _, f := range files {
		b, err := GetIndexedSource(db, projectPath, f.RelPath)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				kind := "text_match"
				evidence := "line text match; may include comments or strings"
				if _, isDeclaration := declarations[fmt.Sprintf("%s:%d", f.RelPath, i+1)]; isDeclaration {
					kind = "declaration_match"
					evidence = "line text match at indexed declaration location"
				}
				out = append(out, SymbolRecord{RelPath: f.RelPath, Name: symbol, Kind: kind, StartLine: i + 1, EndLine: i + 1, Signature: boundedText(line), Language: f.Language, Evidence: evidence})
				if len(out) >= limit {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

func rustUseStatements(source string) []rustUseStatement {
	lines := strings.Split(source, "\n")
	statements := []rustUseStatement{}
	current := strings.Builder{}
	startLine := 0

	for i, line := range lines {
		if startLine == 0 {
			if !rustUseStartRE.MatchString(line) {
				continue
			}
			startLine = i + 1
		}
		if current.Len() > 0 {
			current.WriteByte(' ')
		}
		current.WriteString(strings.TrimSpace(line))
		if !strings.Contains(line, ";") {
			continue
		}

		statement := strings.TrimSpace(current.String())
		if match := rustUseRE.FindStringSubmatch(statement); len(match) > 3 && strings.TrimSpace(match[3]) != "" {
			statements = append(statements, rustUseStatement{Module: strings.TrimSpace(match[3]), Signature: statement, StartLine: startLine})
		}
		current.Reset()
		startLine = 0
	}

	return statements
}

func importedModules(line string) []string {
	out := []string{}
	for _, m := range importRE.FindAllStringSubmatch(line, -1) {
		mod := ""
		if len(m) > 1 && m[1] != "" {
			mod = m[1]
		} else if len(m) > 2 {
			mod = m[2]
		}
		if mod != "" {
			out = append(out, mod)
		}
	}
	return out
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func boundedText(value string) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	const maxRunes = 320
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes-3]) + "..."
	}
	return value
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			root_path TEXT PRIMARY KEY,
			index_version TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			language_set TEXT NOT NULL DEFAULT '',
			language_scope TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS files (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			root_path TEXT NOT NULL,
			rel_path TEXT NOT NULL,
			language TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '',
			mtime_ns INTEGER NOT NULL DEFAULT 0,
			size_bytes INTEGER NOT NULL DEFAULT 0,
			indexed_at TEXT NOT NULL DEFAULT '',
			source_text BLOB,
			UNIQUE(root_path, rel_path)
		)`,
		`CREATE TABLE IF NOT EXISTS symbols (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			root_path TEXT NOT NULL,
			file_id INTEGER,
			name TEXT NOT NULL,
			kind TEXT NOT NULL,
			signature TEXT NOT NULL DEFAULT '',
			start_line INTEGER NOT NULL DEFAULT 0,
			start_col INTEGER NOT NULL DEFAULT 0,
			end_line INTEGER NOT NULL DEFAULT 0,
			end_col INTEGER NOT NULL DEFAULT 0,
			exported INTEGER NOT NULL DEFAULT 0,
			visibility TEXT NOT NULL DEFAULT '',
			doc TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS relations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			root_path TEXT NOT NULL,
			from_symbol_id INTEGER,
			to_symbol_id INTEGER,
			type TEXT NOT NULL,
			call_site_line INTEGER NOT NULL DEFAULT 0,
			target_name TEXT NOT NULL DEFAULT '',
			snippet TEXT NOT NULL DEFAULT '',
			resolution TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS index_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			root_path TEXT NOT NULL,
			mode TEXT NOT NULL,
			started_at TEXT NOT NULL,
			ended_at TEXT NOT NULL,
			files_scanned INTEGER NOT NULL DEFAULT 0,
			files_indexed INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT ''
		)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate schema: %w", err)
		}
	}
	for _, col := range []struct{ table, name, definition string }{
		{"projects", "language_scope", "TEXT NOT NULL DEFAULT ''"},
		{"files", "source_text", "BLOB"},
		{"relations", "call_site_line", "INTEGER NOT NULL DEFAULT 0"},
		{"relations", "call_site_column", "INTEGER NOT NULL DEFAULT 0"},
		{"relations", "target_name", "TEXT NOT NULL DEFAULT ''"},
		{"relations", "snippet", "TEXT NOT NULL DEFAULT ''"},
		{"relations", "resolution", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := ensureColumn(db, col.table, col.name, col.definition); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS relations_call_target ON relations(root_path, type, target_name, call_site_line)`); err != nil {
		return fmt.Errorf("create relation index: %w", err)
	}

	return nil
}

func ensureColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if found {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

func joinCSV(items []string) string {
	if len(items) == 0 {
		return ""
	}
	out := items[0]
	for i := 1; i < len(items); i++ {
		out += "," + items[i]
	}
	return out
}

func splitCSV(s string) []string {
	if s == "" {
		return []string{}
	}
	parts := []string{}
	cur := ""
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			if cur != "" {
				parts = append(parts, cur)
			}
			cur = ""
			continue
		}
		cur += string(s[i])
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	return parts
}
