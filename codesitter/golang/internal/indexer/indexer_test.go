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

func TestScanExtractsRustSymbols(t *testing.T) {
	root := t.TempDir()
	source := `mod api {
    pub fn nested() {}
}

pub struct Server {
    port: u16,
}

pub enum State {
    Ready,
    Busy,
}

pub trait Service {
    fn serve(&self);
}

impl Server {
    pub fn start(&self) {}
}

pub type Result = std::result::Result<(), ()>;
pub const MAX_RETRIES: usize = 3;
pub static LOGGER: &str = "server";
fn helper() {}

macro_rules! log_message {
    ($value:expr) => { println!("{}", $value) };
}`
	if err := os.WriteFile(filepath.Join(root, "main.rs"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Scan(Options{RootPath: root, Langs: []string{"rs"}})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0].RelPath != "main.rs" || result.Files[0].Language != "rust" {
		t.Fatalf("expected one Rust file, got %#v", result.Files)
	}

	want := map[string]string{
		"api":         "module",
		"nested":      "function",
		"Server":      "struct",
		"State":       "enum",
		"Service":     "trait",
		"start":       "method",
		"serve":       "method",
		"Result":      "type",
		"MAX_RETRIES": "constant",
		"LOGGER":      "static",
		"helper":      "function",
		"log_message": "macro",
	}
	got := map[string]string{}
	for _, symbol := range result.Symbols["main.rs"] {
		got[symbol.Name] = symbol.Kind
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("expected Rust symbol %q to have kind %q, got %q", name, kind, got[name])
		}
	}
}
