package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreIndexLifecycle(t *testing.T) {
	root := t.TempDir()

	db, err := Open(root)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	has, err := HasIndex(db, root)
	if err != nil {
		t.Fatalf("has index: %v", err)
	}
	if has {
		t.Fatalf("expected no index")
	}

	if err := RecordIndex(db, root, []string{"javascript", "typescript"}, []string{"javascript", "typescript", "tsx"}, "full", IndexStats{}); err != nil {
		t.Fatalf("record index: %v", err)
	}

	has, err = HasIndex(db, root)
	if err != nil {
		t.Fatalf("has index after record: %v", err)
	}
	if !has {
		t.Fatalf("expected index")
	}

	summary, err := GetSummary(db, root)
	if err != nil {
		t.Fatalf("get summary: %v", err)
	}
	if summary.Version != CurrentIndexVersion {
		t.Fatalf("expected current version %q, got %q", CurrentIndexVersion, summary.Version)
	}
	if len(summary.LanguageSet) != 2 || len(summary.LanguageScope) != 3 {
		t.Fatalf("expected detected languages and selected scope, got %#v", summary)
	}
}

func TestSyncFilesIncrementalAndDelete(t *testing.T) {
	root := t.TempDir()
	db, err := Open(root)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	if err := os.WriteFile(filepath.Join(root, "a.ts"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	h1, m1, s1, err := HashFile(filepath.Join(root, "a.ts"))
	if err != nil {
		t.Fatal(err)
	}

	indexed, err := SyncFiles(db, root, []FileRecord{{RelPath: "a.ts", Language: "typescript", ContentHash: h1, MtimeNS: m1, SizeBytes: s1}})
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("expected 1 indexed file, got %d", indexed)
	}

	indexed, err = SyncFiles(db, root, []FileRecord{{RelPath: "a.ts", Language: "typescript", ContentHash: h1, MtimeNS: m1, SizeBytes: s1}})
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 0 {
		t.Fatalf("expected 0 indexed files on unchanged sync, got %d", indexed)
	}

	if err := os.WriteFile(filepath.Join(root, "a.ts"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	h2, m2, s2, err := HashFile(filepath.Join(root, "a.ts"))
	if err != nil {
		t.Fatal(err)
	}
	indexed, err = SyncFiles(db, root, []FileRecord{{RelPath: "a.ts", Language: "typescript", ContentHash: h2, MtimeNS: m2, SizeBytes: s2}})
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("expected 1 indexed file after change, got %d", indexed)
	}

	indexed, err = SyncFiles(db, root, []FileRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 0 {
		t.Fatalf("expected 0 indexed files when deleting entries, got %d", indexed)
	}
	recs, err := GetIndexedFiles(db, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("expected empty indexed files after delete sync, got %d", len(recs))
	}
}

func TestLockAcquireRelease(t *testing.T) {
	root := t.TempDir()
	lock, err := AcquireLock(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = AcquireLock(root)
	if err != ErrLocked {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireLockRecoversDeadOwner(t *testing.T) {
	root := t.TempDir()
	lockPath := LockPath(root)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(lockOwner{PID: 1 << 30, Host: hostnameOrEmpty(), ProcessBorn: "1", Token: "stale", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("expected stale lock recovery: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireLockPreservesLegacyLock(t *testing.T) {
	root := t.TempDir()
	lockPath := LockPath(root)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte(time.Now().Add(-24*time.Hour).Format(time.RFC3339Nano)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(root); err != ErrLocked {
		t.Fatalf("expected ambiguous legacy lock to remain locked, got %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("legacy lock was unexpectedly removed: %v", err)
	}
}

func TestConcurrentStaleLockRecoveryHasOneWinner(t *testing.T) {
	root := t.TempDir()
	lockPath := LockPath(root)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(lockOwner{PID: 1 << 30, Host: hostnameOrEmpty(), ProcessBorn: "1", Token: "stale", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wins := make(chan *Lock, 8)
	errs := make(chan error, 8)
	for i := 0; i < cap(wins); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock, err := AcquireLock(root)
			if err != nil {
				errs <- err
				return
			}
			wins <- lock
		}()
	}
	wg.Wait()
	close(wins)
	close(errs)
	var acquired []*Lock
	for lock := range wins {
		acquired = append(acquired, lock)
	}
	if len(acquired) != 1 {
		t.Fatalf("expected exactly one recovery winner, got %d", len(acquired))
	}
	for err := range errs {
		if err != ErrLocked {
			t.Errorf("expected ErrLocked for losing acquirer, got %v", err)
		}
	}
	if err := acquired[0].Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFindSyntaxImporters(t *testing.T) {
	tests := []struct {
		name, lang, source, module string
		want                       int
	}{
		{
			name: "multiline typescript import and re-export", lang: "typescript",
			source: "import {\n  value,\n} from \"@scope/pkg\";\nexport { other } from \"@scope/pkg\";",
			module: "@scope/pkg", want: 2,
		},
		{
			name: "ignores comments strings and identifier substrings", lang: "javascript",
			source: "// import 'target'\nconst text = \"import target\";\nimport 'target-extra';",
			module: "target", want: 0,
		},
		{
			name: "multiple go imports", lang: "go",
			source: "package p\nimport (\n \"fmt\"\n x \"example/x\"\n)",
			module: "example/x", want: 1,
		},
		{
			name: "java qualified import", lang: "java",
			source: "import com.example.Service;", module: "com.example.Service", want: 1,
		},
		{
			name: "rust grouped use", lang: "rust",
			source: "use service::{Service, Other};", module: "service::Service", want: 1,
		},
		{
			name: "css import", lang: "css",
			source: "@import\n  url(\"theme.css\");", module: "theme.css", want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findSyntaxImporters("sample."+tt.lang, tt.lang, []byte(tt.source), tt.module)
			if err != nil {
				t.Fatalf("findSyntaxImporters: %v", err)
			}
			if len(got) != tt.want {
				t.Fatalf("got %d matches, want %d: %#v", len(got), tt.want, got)
			}
			for _, result := range got {
				if !strings.Contains(result.Evidence, "syntax-aware") {
					t.Errorf("expected syntax-aware evidence, got %q", result.Evidence)
				}
			}
		})
	}
}

func TestGetSummaryNotIndexed(t *testing.T) {
	root := t.TempDir()
	db, err := Open(root)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	_, err = GetSummary(db, root)
	if err != ErrNotIndexed {
		t.Fatalf("expected ErrNotIndexed, got %v", err)
	}
}
