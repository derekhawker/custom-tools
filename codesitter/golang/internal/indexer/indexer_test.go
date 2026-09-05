package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanSkipsDependencyAndGeneratedDirectories(t *testing.T) {
	root := t.TempDir()

	files := map[string]string{
		"src/keep.ts":                          "export function keep() {}\n",
		"node_modules/package/index.ts":        "export function dependency() {}\n",
		"vendor/library/index.ts":              "export function vendored() {}\n",
		"bower_components/library/index.ts":    "export function bowerDependency() {}\n",
		"jspm_packages/library/index.ts":       "export function jspmDependency() {}\n",
		"packages/app/dist/bundle.ts":          "export function bundled() {}\n",
		"packages/app/out/generated.ts":        "export function output() {}\n",
		"packages/app/build/generated.ts":      "export function built() {}\n",
		"packages/app/target/classes/index.ts": "export function targeted() {}\n",
	}
	for relPath, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := Scan(Options{RootPath: root, Langs: []string{"ts"}})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(result.Files) != 1 || result.Files[0].RelPath != "src/keep.ts" {
		t.Fatalf("expected only src/keep.ts to be indexed, got %#v", result.Files)
	}
}
