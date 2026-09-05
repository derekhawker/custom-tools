package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/derekhawker/codesitter/internal/model"
)

func TestIndexRequiresForce(t *testing.T) {
	root := t.TempDir()

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := Run([]string{"index", "--path", root, "--json"}, &out, &errOut)
	if code != ExitForceRequired {
		t.Fatalf("expected code %d, got %d", ExitForceRequired, code)
	}

	var resp model.IndexResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.RequiresForce {
		t.Fatalf("expected requires_force=true")
	}
	if resp.Error == nil || resp.Error.Code != "FORCE_REQUIRED" {
		t.Fatalf("expected FORCE_REQUIRED error")
	}
}

func TestIndexStatusQueryFlow(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "home.ts"), []byte("import { util } from './util'\nexport function renderPage() {\n  helper()\n}\nconst helper = () => { util() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "util.ts"), []byte("export function util() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer

	code := Run([]string{"index", "--path", root, "--force", "--lang", "js,ts", "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("index expected code %d, got %d, stderr=%s", ExitOK, code, errOut.String())
	}

	var idx model.IndexResponse
	if err := json.Unmarshal(out.Bytes(), &idx); err != nil {
		t.Fatalf("unmarshal index response: %v", err)
	}
	if !idx.OK || !idx.Started {
		t.Fatalf("expected successful index response")
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"status", "--path", root, "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("status expected code %d, got %d", ExitOK, code)
	}

	var status model.StatusResponse
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if !status.Indexed {
		t.Fatalf("expected indexed=true")
	}
	if status.Index == nil || len(status.Index.LanguageSet) != 2 {
		t.Fatalf("expected language set from index")
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"query", "find_declarations", "--path", root, "--symbol", "renderPage", "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("query expected code %d, got %d", ExitOK, code)
	}

	var queryResp model.QueryResponse
	if err := json.Unmarshal(out.Bytes(), &queryResp); err != nil {
		t.Fatalf("unmarshal query response: %v", err)
	}
	if !queryResp.OK {
		t.Fatalf("expected query ok=true after indexing")
	}
	if len(queryResp.Results) == 0 {
		t.Fatalf("expected declaration results")
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"query", "find_references", "--path", root, "--symbol", "helper", "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("find_references expected %d got %d", ExitOK, code)
	}
	out.Reset()
	errOut.Reset()
	code = Run([]string{"query", "find_callers", "--path", root, "--symbol", "helper", "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("find_callers expected %d got %d", ExitOK, code)
	}
	out.Reset()
	errOut.Reset()
	code = Run([]string{"query", "find_callees", "--path", root, "--symbol", "renderPage", "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("find_callees expected %d got %d", ExitOK, code)
	}
	out.Reset()
	errOut.Reset()
	code = Run([]string{"query", "get_file_outline", "--path", root, "--file", "home.ts", "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("get_file_outline expected %d got %d", ExitOK, code)
	}
	out.Reset()
	errOut.Reset()
	code = Run([]string{"query", "find_importers", "--path", root, "--module", "./util", "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("find_importers expected %d got %d", ExitOK, code)
	}
}

func TestIndexGoDeclarations(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\ntype Server struct{}\n\nfunc Run() {}\n\nfunc (s Server) Start() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := Run([]string{"index", "--path", root, "--force", "--lang", "go", "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("index expected code %d, got %d, stderr=%s", ExitOK, code, errOut.String())
	}

	var idx model.IndexResponse
	if err := json.Unmarshal(out.Bytes(), &idx); err != nil {
		t.Fatalf("unmarshal index response: %v", err)
	}
	if idx.Stats == nil || idx.Stats.FilesScanned != 1 {
		t.Fatalf("expected one Go file scanned, got %#v", idx.Stats)
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"query", "find_declarations", "--path", root, "--symbol", "Run", "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("query expected code %d, got %d", ExitOK, code)
	}

	var queryResp model.QueryResponse
	if err := json.Unmarshal(out.Bytes(), &queryResp); err != nil {
		t.Fatalf("unmarshal query response: %v", err)
	}
	if len(queryResp.Results) != 1 || queryResp.Results[0].Language != "go" {
		t.Fatalf("expected one Go declaration result, got %#v", queryResp.Results)
	}
}

func TestIndexRustDeclarations(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "lib.rs"), []byte("use crate::{\n    service::Service,\n};\n\npub struct Server {}\n\nimpl Server {\n    pub fn start(&self) {}\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := Run([]string{"index", "--path", root, "--force", "--lang", "rust", "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("index expected code %d, got %d, stderr=%s", ExitOK, code, errOut.String())
	}

	var idx model.IndexResponse
	if err := json.Unmarshal(out.Bytes(), &idx); err != nil {
		t.Fatalf("unmarshal index response: %v", err)
	}
	if idx.Stats == nil || idx.Stats.FilesScanned != 1 {
		t.Fatalf("expected one Rust file scanned, got %#v", idx.Stats)
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"query", "find_declarations", "--path", root, "--symbol", "Server", "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("query expected code %d, got %d, stderr=%s", ExitOK, code, errOut.String())
	}

	var queryResp model.QueryResponse
	if err := json.Unmarshal(out.Bytes(), &queryResp); err != nil {
		t.Fatalf("unmarshal query response: %v", err)
	}
	if len(queryResp.Results) != 1 || queryResp.Results[0].Language != "rust" || queryResp.Results[0].Kind != "struct" {
		t.Fatalf("expected one Rust struct declaration, got %#v", queryResp.Results)
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"query", "find_importers", "--path", root, "--module", "service::Service", "--json"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("importer query expected code %d, got %d, stderr=%s", ExitOK, code, errOut.String())
	}

	var importResp model.QueryResponse
	if err := json.Unmarshal(out.Bytes(), &importResp); err != nil {
		t.Fatalf("unmarshal importer response: %v", err)
	}
	if len(importResp.Results) != 1 || importResp.Results[0].Language != "rust" || importResp.Results[0].Kind != "import" {
		t.Fatalf("expected one Rust importer result, got %#v", importResp.Results)
	}
}

func TestFindImportersPreservesJavaImports(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Main.java"), []byte("import com.example.Service;\n\nclass Main {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	if code := Run([]string{"index", "--path", root, "--force", "--lang", "java", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("index expected code %d, got %d, stderr=%s", ExitOK, code, errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"query", "find_importers", "--path", root, "--module", "com.example.Service", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("importer query expected code %d, got %d, stderr=%s", ExitOK, code, errOut.String())
	}

	var resp model.QueryResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal importer response: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Language != "java" {
		t.Fatalf("expected one Java importer result, got %#v", resp.Results)
	}
}

func TestQueryNotIndexed(t *testing.T) {
	root := t.TempDir()

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := Run([]string{"query", "find_declarations", "--path", root, "--json"}, &out, &errOut)
	if code != ExitNotIndexed {
		t.Fatalf("expected code %d, got %d", ExitNotIndexed, code)
	}

	var resp model.QueryResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != "NOT_INDEXED" {
		t.Fatalf("expected NOT_INDEXED error")
	}
}

func TestInvalidIntent(t *testing.T) {
	root := t.TempDir()

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := Run([]string{"query", "bad_intent", "--path", root}, &out, &errOut)
	if code != ExitInvalidInput {
		t.Fatalf("expected code %d, got %d", ExitInvalidInput, code)
	}
}

func TestIndexMissingPath(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	code := Run([]string{"index", "--force"}, &out, &errOut)
	if code != ExitInvalidInput {
		t.Fatalf("expected code %d, got %d", ExitInvalidInput, code)
	}
}

func TestCreatesDbArtifact(t *testing.T) {
	root := t.TempDir()

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := Run([]string{"index", "--path", root, "--force"}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("expected code %d, got %d", ExitOK, code)
	}

	dbPath := filepath.Join(root, ".codesitter", "index.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected index db artifact at %s: %v", dbPath, err)
	}
}

func TestIndexLocked(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codesitter"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codesitter", "index.lock"), []byte("locked"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := Run([]string{"index", "--path", root, "--force", "--json"}, &out, &errOut)
	if code != ExitLocked {
		t.Fatalf("expected code %d, got %d", ExitLocked, code)
	}

	var resp model.IndexResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != "LOCKED" {
		t.Fatalf("expected LOCKED error")
	}
}

func TestStatusFreshnessDetectsStaleFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.ts"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	if code := Run([]string{"index", "--path", root, "--force", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("index expected code %d, got %d", ExitOK, code)
	}

	if err := os.WriteFile(filepath.Join(root, "x.ts"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"status", "--path", root, "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("status expected code %d, got %d", ExitOK, code)
	}

	var resp model.StatusResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if resp.Freshness == nil || resp.Freshness.StaleFiles < 1 {
		t.Fatalf("expected stale file detection")
	}
}
