package store

import (
	"os"
	"path/filepath"
	"testing"
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
