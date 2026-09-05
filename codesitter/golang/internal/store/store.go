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
}

type SymbolRecord struct {
	RelPath   string
	Name      string
	Kind      string
	StartLine int
	EndLine   int
	Signature string
	Language  string
}

var importRE = regexp.MustCompile(`^\s*import\s+.*?from\s+['\"]([^'\"]+)['\"]|^\s*import\s+['\"]([^'\"]+)['\"]`)
var rustUseRE = regexp.MustCompile(`^\s*(pub(\([^)]*\))?\s+)?use\s+([^;]+);`)
var callRE = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*!?\s*\(`)

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
	Version       string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LanguageSet   []string
	FileCount     int
	SymbolCount   int
	RelationCount int
	StaleFiles    int
	DeletedFiles  int
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
	_, _ = f.WriteString(time.Now().UTC().Format(time.RFC3339))
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

func RecordIndex(db *sql.DB, projectPath string, langs []string, mode string, stats IndexStats) error {
	now := time.Now().UTC()
	langCSV := joinCSV(langs)

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
INSERT INTO projects(root_path, index_version, created_at, updated_at, language_set)
VALUES (?, '1', ?, ?, ?)
ON CONFLICT(root_path)
DO UPDATE SET updated_at=excluded.updated_at, language_set=excluded.language_set
`, projectPath, now.Format(time.RFC3339), now.Format(time.RFC3339), langCSV)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
INSERT INTO index_runs(root_path, mode, started_at, ended_at, files_scanned, files_indexed, status)
VALUES (?, ?, ?, ?, ?, ?, 'success')
`, projectPath, mode, now.Format(time.RFC3339), now.Format(time.RFC3339), stats.FilesScanned, stats.FilesIndexed)
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
	var s Summary
	var languageSet string
	var createdAt string
	var updatedAt string

	err := db.QueryRow(`
SELECT index_version, created_at, updated_at, language_set
FROM projects
WHERE root_path = ?
`, projectPath).Scan(&s.Version, &createdAt, &updatedAt, &languageSet)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotIndexed
	}
	if err != nil {
		return s, err
	}

	s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	s.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	s.LanguageSet = splitCSV(languageSet)

	_ = db.QueryRow(`SELECT COUNT(1) FROM files WHERE root_path=?`, projectPath).Scan(&s.FileCount)
	_ = db.QueryRow(`SELECT COUNT(1) FROM symbols WHERE root_path=?`, projectPath).Scan(&s.SymbolCount)
	_ = db.QueryRow(`SELECT COUNT(1) FROM relations WHERE root_path=?`, projectPath).Scan(&s.RelationCount)

	recs, err := GetIndexedFiles(db, projectPath)
	if err == nil {
		for _, rec := range recs {
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
	}

	return s, nil
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
	rows, err := db.Query(`SELECT rel_path, language, content_hash, mtime_ns, size_bytes FROM files WHERE root_path=?`, projectPath)
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
	now := time.Now().UTC().Format(time.RFC3339)
	seen := map[string]struct{}{}
	for _, f := range files {
		seen[f.RelPath] = struct{}{}
		prev, ok := current[f.RelPath]
		if !ok || prev.ContentHash != f.ContentHash {
			indexed++
		}
		_, err = tx.Exec(`
INSERT INTO files(root_path, rel_path, language, content_hash, mtime_ns, size_bytes, indexed_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(root_path, rel_path)
DO UPDATE SET language=excluded.language, content_hash=excluded.content_hash, mtime_ns=excluded.mtime_ns, size_bytes=excluded.size_bytes, indexed_at=excluded.indexed_at
`, projectPath, f.RelPath, f.Language, f.ContentHash, f.MtimeNS, f.SizeBytes, now)
		if err != nil {
			return 0, err
		}
	}

	for _, e := range existing {
		if _, ok := seen[e.RelPath]; !ok {
			_, err = tx.Exec(`DELETE FROM files WHERE root_path=? AND rel_path=?`, projectPath, e.RelPath)
			if err != nil {
				return 0, err
			}
		}
	}

	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return indexed, nil
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
WHERE s.root_path=? AND s.name LIKE ?
ORDER BY s.name, f.rel_path, s.start_line
LIMIT ?
`, projectPath, "%"+name+"%", limit)
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

func RecomputeRelations(db *sql.DB, projectPath string) error {
	if _, err := db.Exec(`DELETE FROM relations WHERE root_path=?`, projectPath); err != nil {
		return err
	}

	fileIDs := map[string]int64{}
	rows, err := db.Query(`SELECT id, rel_path FROM files WHERE root_path=?`, projectPath)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var rel string
		if err := rows.Scan(&id, &rel); err != nil {
			rows.Close()
			return err
		}
		fileIDs[rel] = id
	}
	rows.Close()

	targetByName := map[string]int64{}
	rows, err = db.Query(`SELECT id, name FROM symbols WHERE root_path=?`, projectPath)
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
		if _, ok := targetByName[name]; !ok {
			targetByName[name] = id
		}
	}
	rows.Close()

	files, err := GetIndexedFiles(db, projectPath)
	if err != nil {
		return err
	}

	for _, f := range files {
		full := filepath.Join(projectPath, f.RelPath)
		source, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		sourceText := string(source)
		s := bufio.NewScanner(strings.NewReader(sourceText))
		for s.Scan() {
			line := s.Text()
			for _, mod := range importedModules(line) {
				_, _ = db.Exec(`INSERT INTO relations(root_path, from_symbol_id, to_symbol_id, type) VALUES (?, NULL, NULL, ?)`, projectPath, "imports:"+mod)
			}
			for _, cm := range callRE.FindAllStringSubmatch(line, -1) {
				if len(cm) < 2 {
					continue
				}
				called := cm[1]
				toID, ok := targetByName[called]
				if !ok {
					continue
				}
				fromID := int64(0)
				_ = db.QueryRow(`SELECT id FROM symbols WHERE root_path=? AND file_id=? ORDER BY start_line LIMIT 1`, projectPath, fileIDs[f.RelPath]).Scan(&fromID)
				if fromID == 0 {
					continue
				}
				_, _ = db.Exec(`INSERT INTO relations(root_path, from_symbol_id, to_symbol_id, type) VALUES (?, ?, ?, 'calls')`, projectPath, fromID, toID)
			}
		}
		if f.Language == "rust" {
			for _, use := range rustUseStatements(sourceText) {
				_, _ = db.Exec(`INSERT INTO relations(root_path, from_symbol_id, to_symbol_id, type) VALUES (?, NULL, NULL, ?)`, projectPath, "imports:"+use.Module)
			}
		}
		_ = s.Err()
	}

	return nil
}

func FindCallers(db *sql.DB, projectPath, symbol string, limit int) ([]SymbolRecord, error) {
	rows, err := db.Query(`
SELECT sf.rel_path, ss.name, ss.kind, ss.start_line, ss.end_line, ss.signature, sf.language
FROM relations r
JOIN symbols ss ON ss.id = r.from_symbol_id
JOIN files sf ON sf.id = ss.file_id
JOIN symbols ts ON ts.id = r.to_symbol_id
WHERE r.root_path=? AND r.type='calls' AND ts.name=?
LIMIT ?
`, projectPath, symbol, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := []SymbolRecord{}
	for rows.Next() {
		var r SymbolRecord
		if err := rows.Scan(&r.RelPath, &r.Name, &r.Kind, &r.StartLine, &r.EndLine, &r.Signature, &r.Language); err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	return res, rows.Err()
}

func FindCallees(db *sql.DB, projectPath, symbol string, limit int) ([]SymbolRecord, error) {
	rows, err := db.Query(`
SELECT tf.rel_path, ts.name, ts.kind, ts.start_line, ts.end_line, ts.signature, tf.language
FROM relations r
JOIN symbols ss ON ss.id = r.from_symbol_id
JOIN symbols ts ON ts.id = r.to_symbol_id
JOIN files tf ON tf.id = ts.file_id
WHERE r.root_path=? AND r.type='calls' AND ss.name=?
LIMIT ?
`, projectPath, symbol, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := []SymbolRecord{}
	for rows.Next() {
		var r SymbolRecord
		if err := rows.Scan(&r.RelPath, &r.Name, &r.Kind, &r.StartLine, &r.EndLine, &r.Signature, &r.Language); err != nil {
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
		full := filepath.Join(projectPath, f.RelPath)
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		fileResults := []SymbolRecord{}
		for i, line := range lines {
			if !strings.Contains(line, module) || !strings.Contains(line, "import") {
				continue
			}
			fileResults = append(fileResults, SymbolRecord{RelPath: f.RelPath, Name: module, Kind: "import", StartLine: i + 1, EndLine: i + 1, Signature: strings.TrimSpace(line), Language: f.Language})
		}
		if f.Language == "rust" {
			for _, use := range rustUseStatements(string(b)) {
				if strings.Contains(use.Module, module) {
					fileResults = append(fileResults, SymbolRecord{RelPath: f.RelPath, Name: module, Kind: "import", StartLine: use.StartLine, EndLine: use.StartLine, Signature: use.Signature, Language: f.Language})
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
	files, err := GetIndexedFiles(db, projectPath)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
	out := []SymbolRecord{}
	for _, f := range files {
		full := filepath.Join(projectPath, f.RelPath)
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				out = append(out, SymbolRecord{RelPath: f.RelPath, Name: symbol, Kind: "reference", StartLine: i + 1, EndLine: i + 1, Signature: strings.TrimSpace(line), Language: f.Language})
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

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			root_path TEXT PRIMARY KEY,
			index_version TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			language_set TEXT NOT NULL DEFAULT ''
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
			type TEXT NOT NULL
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
