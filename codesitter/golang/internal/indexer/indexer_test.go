package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derekhawker/codesitter/internal/store"
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

func TestScanSupportsJavaScriptModuleExtensions(t *testing.T) {
	root := t.TempDir()

	files := map[string]string{
		"common.cjs": "function loadCommon() {}\nmodule.exports = { loadCommon }\n",
		"module.mjs": "export function loadModule() {}\n",
		"legacy.js":  "function loadLegacy() {}\n",
		"types.ts":   "export function notJavaScript() {}\n",
	}
	for relPath, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relPath))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := Scan(Options{RootPath: root, Langs: []string{"js"}})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(result.Files) != 3 {
		t.Fatalf("expected three JavaScript files, got %#v", result.Files)
	}

	wantSymbols := map[string]string{
		"common.cjs": "loadCommon",
		"module.mjs": "loadModule",
		"legacy.js":  "loadLegacy",
	}
	for _, file := range result.Files {
		if file.Language != "javascript" {
			t.Errorf("expected %s to be identified as JavaScript, got %q", file.RelPath, file.Language)
		}
		if len(result.Symbols[file.RelPath]) != 1 || result.Symbols[file.RelPath][0].Name != wantSymbols[file.RelPath] {
			t.Errorf("expected JavaScript symbol %q in %s, got %#v", wantSymbols[file.RelPath], file.RelPath, result.Symbols[file.RelPath])
		}
	}
}

func TestScanSkipsParsingUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "same.ts")
	if err := os.WriteFile(path, []byte("export function same() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, mtime, size, err := store.HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Scan(Options{RootPath: root, Langs: []string{"ts"}, Previous: map[string]store.FileRecord{
		"same.ts": {RelPath: "same.ts", Language: "typescript", ContentHash: hash, MtimeNS: mtime, SizeBytes: size},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesScanned != 1 || len(result.ChangedPaths) != 0 || len(result.Symbols) != 0 {
		t.Fatalf("unchanged scan should retain coverage without reparsing, got %#v", result)
	}
}

func TestScanTargetsOnlySuppliedPaths(t *testing.T) {
	root := t.TempDir()
	for name, source := range map[string]string{"a.ts": "function a() {}", "b.ts": "function b() {}"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Scan(Options{RootPath: root, Langs: []string{"ts"}, Targeted: true, Paths: []string{"b.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].RelPath != "b.ts" || result.FilesScanned != 1 {
		t.Fatalf("targeted scan should only visit b.ts, got %#v", result)
	}
}

func TestTargetedScanRejectsPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := Scan(Options{RootPath: root, Targeted: true, Paths: []string{"../outside.ts"}}); err == nil {
		t.Fatal("expected path outside project root to be rejected")
	}
}

func TestScanSupportsStylesheetExtensions(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"styles.css": `:root {
  --primary: #336699;
}

.button {
  color: var(--primary);
}`,
		"theme.scss": `$primary: #336699;

@mixin rounded($radius) {
  border-radius: $radius;
}

.card {
  @include rounded(4px);
}`,
		"legacy.sass": `$spacing: 1rem
.card
  padding: $spacing
`,
		"ignored.ts": "export function ignored() {}\n",
	}
	for relPath, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relPath))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := Scan(Options{RootPath: root, Langs: []string{"css", "scss", "sass"}})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(result.Files) != 3 {
		t.Fatalf("expected three stylesheet files, got %#v", result.Files)
	}

	wantLanguages := map[string]string{
		"styles.css":  "css",
		"theme.scss":  "scss",
		"legacy.sass": "sass",
	}
	for _, file := range result.Files {
		if file.Language != wantLanguages[file.RelPath] {
			t.Errorf("expected %s to be identified as %s, got %q", file.RelPath, wantLanguages[file.RelPath], file.Language)
		}
	}

	wantSymbols := map[string]map[string]string{
		"styles.css":  {".button": "selector", "--primary": "variable"},
		"theme.scss":  {".card": "selector", "$primary": "variable", "rounded": "mixin"},
		"legacy.sass": {".card": "selector", "$spacing": "variable"},
	}
	for file, symbols := range wantSymbols {
		got := map[string]string{}
		for _, symbol := range result.Symbols[file] {
			got[symbol.Name] = symbol.Kind
		}
		for name, kind := range symbols {
			if got[name] != kind {
				t.Errorf("expected stylesheet symbol %q in %s to have kind %q, got %q", name, file, kind, got[name])
			}
		}
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

func TestValidateLanguagesRejectsUnknownAndEmptyNames(t *testing.T) {
	for _, langs := range [][]string{{"rustish"}, {"js", "unknown"}, {""}, {"js", " "}} {
		if err := ValidateLanguages(langs); err == nil {
			t.Errorf("expected invalid language list %#v to be rejected", langs)
		}
	}
}

func TestValidateLanguagesAcceptsSupportedAliases(t *testing.T) {
	if err := ValidateLanguages([]string{"js", "typescript", "rust", "css", "sass"}); err != nil {
		t.Fatalf("supported languages rejected: %v", err)
	}
}

func TestDeclarationSignaturesOmitFunctionBodies(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.ts"), []byte("export function save(item: string) {\n  persist(item)\n  return item\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Scan(Options{RootPath: root, Langs: []string{"ts"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Symbols["a.ts"]) != 1 {
		t.Fatalf("expected one declaration, got %#v", result.Symbols["a.ts"])
	}
	header := result.Symbols["a.ts"][0].Signature
	if strings.Contains(header, "persist") || strings.Contains(header, "return item") || !strings.Contains(header, "save(item: string)") {
		t.Fatalf("expected bounded declaration header without body, got %q", header)
	}
}
