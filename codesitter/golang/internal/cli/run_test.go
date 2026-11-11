package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derekhawker/codesitter/internal/model"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

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

func TestOutputWriteFailureReturnsErrorExit(t *testing.T) {
	root := t.TempDir()
	if code := Run([]string{"index", "--path", root, "--json"}, failingWriter{}, io.Discard); code != ExitError {
		t.Fatalf("expected output failure to return ExitError, got %d", code)
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
	if status.Index == nil || len(status.Index.LanguageSet) != 1 || status.Index.LanguageSet[0] != "typescript" {
		t.Fatalf("expected actual detected language coverage, got %#v", status.Index)
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

func TestQueryRequiresIntentOperandsAndPositiveLimit(t *testing.T) {
	root := t.TempDir()
	checks := [][]string{
		{"query", "find_declarations", "--path", root, "--json"},
		{"query", "get_file_outline", "--path", root, "--json"},
		{"query", "find_importers", "--path", root, "--json"},
		{"query", "find_declarations", "--path", root, "--symbol", "x", "--limit", "0", "--json"},
	}
	for _, args := range checks {
		var out, errOut bytes.Buffer
		if code := Run(args, &out, &errOut); code != ExitInvalidInput {
			t.Errorf("expected invalid input for %v, got %d", args, code)
		}
	}
}

func TestQueryReportsHasMoreAndPlainOutputShowsResults(t *testing.T) {
	root := t.TempDir()
	source := "function alpha() {}\nfunction alpine() {}\n"
	if err := os.WriteFile(filepath.Join(root, "a.ts"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := Run([]string{"index", "--path", root, "--force", "--lang", "ts", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("index got %d; stderr=%s", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"query", "find_declarations", "--path", root, "--symbol", "alp", "--limit", "1", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("query got %d; stderr=%s", code, errOut.String())
	}
	var response model.QueryResponse
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Meta.HasMore || response.Meta.IndexVersion == "" || response.Meta.DurationMS < 0 {
		t.Fatalf("query metadata should report completeness and measured fields, got %#v", response.Meta)
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"query", "find_declarations", "--path", root, "--symbol", "alpha"}, &out, &errOut); code != ExitOK {
		t.Fatalf("plain query got %d; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "a.ts:1: alpha") || strings.Contains(out.String(), "Query complete.") {
		t.Fatalf("plain query should show located results, got %q", out.String())
	}
}

func TestCallQueriesReturnEnclosingFunctionsOnly(t *testing.T) {
	root := t.TempDir()
	source := `function target() {}
function firstCaller() {
  target(); target()
}

function secondCaller() {
  target()
}
function unrelated() {
  other()
  // target()
  const example = "target()"
}
function other() {}
`
	if err := os.WriteFile(filepath.Join(root, "calls.ts"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := Run([]string{"index", "--path", root, "--force", "--lang", "ts", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("index expected code %d, got %d, stderr=%s", ExitOK, code, errOut.String())
	}

	query := func(intent, symbol string) model.QueryResponse {
		t.Helper()
		out.Reset()
		errOut.Reset()
		if code := Run([]string{"query", intent, "--path", root, "--symbol", symbol, "--json"}, &out, &errOut); code != ExitOK {
			t.Fatalf("%s %s expected code %d, got %d, stderr=%s", intent, symbol, ExitOK, code, errOut.String())
		}
		var response model.QueryResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatalf("decode %s response: %v", intent, err)
		}
		return response
	}

	callers := query("find_callers", "target")
	gotCallers := map[string]bool{}
	callLines := map[int]bool{}
	columnsOnLine3 := map[int]bool{}
	for _, result := range callers.Results {
		gotCallers[result.Name] = true
		callLines[result.CallSiteLine] = true
		if result.CallSiteLine == 3 {
			columnsOnLine3[result.CallSiteColumn] = true
		}
		if result.Callee != "target" || result.Resolution != "heuristic_name_match" || result.Snippet == "" {
			t.Errorf("caller result missing honest call-site evidence: %#v", result)
		}
	}
	if len(gotCallers) != 2 || !gotCallers["firstCaller"] || !gotCallers["secondCaller"] {
		t.Fatalf("expected only firstCaller and secondCaller, got %#v", callers.Results)
	}
	if len(callers.Results) != 3 || len(callLines) != 2 || !callLines[3] || !callLines[7] || len(columnsOnLine3) != 2 {
		t.Fatalf("expected separate call sites on lines 3 and 7, got %#v", callers.Results)
	}

	callees := query("find_callees", "firstCaller")
	if len(callees.Results) != 2 || callees.Results[0].Name != "target" || callees.Results[0].StartLine != 3 || callees.Results[0].Caller != "firstCaller" {
		t.Fatalf("expected firstCaller to call only target, got %#v", callees.Results)
	}
}

func TestCallQueriesLabelAmbiguousAndUnresolvedTargets(t *testing.T) {
	root := t.TempDir()
	source := `function repeated() {}
function repeated() {}
function caller() {
  repeated()
  external()
}
`
	if err := os.WriteFile(filepath.Join(root, "calls.ts"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := Run([]string{"index", "--path", root, "--force", "--lang", "ts", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("index got %d; stderr=%s", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"query", "find_callees", "--path", root, "--symbol", "caller", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("query got %d; stderr=%s", code, errOut.String())
	}
	var response model.QueryResponse
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	resolutions := map[string]string{}
	for _, result := range response.Results {
		resolutions[result.Callee] = result.Resolution
	}
	if resolutions["repeated"] != "ambiguous_name_match" || resolutions["external"] != "unresolved" {
		t.Fatalf("expected explicit ambiguous/unresolved call results, got %#v", response.Results)
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

func TestRustSupportsAllSixQueryIntents(t *testing.T) {
	root := t.TempDir()
	source := `use crate::service::Service;

pub fn target() {}

pub fn caller() {
    target();
    external();
}
`
	if err := os.WriteFile(filepath.Join(root, "lib.rs"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := Run([]string{"index", "--path", root, "--force", "--lang", "rust", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("Rust index got %d; stderr=%s", code, errOut.String())
	}
	query := func(intent, operandFlag, operand string) model.QueryResponse {
		t.Helper()
		out.Reset()
		errOut.Reset()
		args := []string{"query", intent, "--path", root, operandFlag, operand, "--json"}
		if code := Run(args, &out, &errOut); code != ExitOK {
			t.Fatalf("Rust %s query got %d; stderr=%s", intent, code, errOut.String())
		}
		var response model.QueryResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatalf("decode Rust %s response: %v", intent, err)
		}
		return response
	}
	if got := query("find_declarations", "--symbol", "target"); len(got.Results) != 1 || got.Results[0].Language != "rust" {
		t.Fatalf("Rust declarations failed: %#v", got.Results)
	}
	if got := query("find_references", "--symbol", "target"); len(got.Results) < 2 || got.Results[0].Evidence == "" {
		t.Fatalf("Rust text references should be labeled: %#v", got.Results)
	}
	if got := query("find_callers", "--symbol", "target"); len(got.Results) != 1 || got.Results[0].CallSiteLine != 6 {
		t.Fatalf("Rust callers should include call-site evidence: %#v", got.Results)
	}
	if got := query("find_callees", "--symbol", "caller"); len(got.Results) != 2 {
		t.Fatalf("Rust callees should include resolved and unresolved calls: %#v", got.Results)
	}
	if got := query("get_file_outline", "--file", "lib.rs"); len(got.Results) < 2 {
		t.Fatalf("Rust outline failed: %#v", got.Results)
	}
	if got := query("find_importers", "--module", "service::Service"); len(got.Results) != 1 || got.Results[0].Language != "rust" {
		t.Fatalf("Rust importers failed: %#v", got.Results)
	}
}

func TestTextQueriesUseIndexedSourceSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "snapshot.ts")
	original := "import { oldModule } from \"./oldModule\";\nfunction originalName() {}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "empty.ts"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := Run([]string{"index", "--path", root, "--force", "--lang", "ts", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("index got %d; stderr=%s", code, errOut.String())
	}
	if err := os.WriteFile(path, []byte("import { newModule } from \"./newModule\";\nfunction changedName() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	query := func(intent, flag, value string) model.QueryResponse {
		t.Helper()
		out.Reset()
		errOut.Reset()
		args := []string{"query", intent, "--path", root, flag, value, "--json"}
		if code := Run(args, &out, &errOut); code != ExitOK {
			t.Fatalf("query %s got %d; stderr=%s", intent, code, errOut.String())
		}
		var response model.QueryResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	importers := query("find_importers", "--module", "./oldModule")
	if len(importers.Results) != 1 || importers.Results[0].StartLine != 1 {
		t.Fatalf("importer query should reflect indexed source snapshot: %#v", importers.Results)
	}
	if currentImporters := query("find_importers", "--module", "./newModule"); len(currentImporters.Results) != 0 {
		t.Fatalf("unindexed filesystem changes must not leak into query results: %#v", currentImporters.Results)
	}
	if references := query("find_references", "--symbol", "originalName"); len(references.Results) != 1 {
		t.Fatalf("reference query should reflect indexed source snapshot: %#v", references.Results)
	}
	if references := query("find_references", "--symbol", "changedName"); len(references.Results) != 0 {
		t.Fatalf("unindexed filesystem changes must not leak into query results: %#v", references.Results)
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"index", "--path", root, "--force", "--file", "snapshot.ts", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("targeted snapshot update got %d; stderr=%s", code, errOut.String())
	}
	if currentImporters := query("find_importers", "--module", "./newModule"); len(currentImporters.Results) != 1 {
		t.Fatalf("targeted update should refresh the source snapshot: %#v", currentImporters.Results)
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
	code := Run([]string{"query", "find_declarations", "--path", root, "--symbol", "missing", "--json"}, &out, &errOut)
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

func TestIndexRejectsUnsupportedLanguageBeforeCreatingIndex(t *testing.T) {
	root := t.TempDir()
	var out, errOut bytes.Buffer
	code := Run([]string{"index", "--path", root, "--force", "--lang", "rustish", "--json"}, &out, &errOut)
	if code != ExitInvalidInput {
		t.Fatalf("expected invalid input, got %d; stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".codesitter", "index.db")); !os.IsNotExist(err) {
		t.Fatalf("unsupported language should not create an index, stat err=%v", err)
	}
}

func TestIndexRejectsPartialScanFlags(t *testing.T) {
	for _, flag := range []string{"--include", "--exclude", "--max-files"} {
		root := t.TempDir()
		var out, errOut bytes.Buffer
		code := Run([]string{"index", "--path", root, "--force", flag, "x", "--json"}, &out, &errOut)
		if code != ExitInvalidInput {
			t.Errorf("%s should be rejected, got %d", flag, code)
		}
		if _, err := os.Stat(filepath.Join(root, ".codesitter", "index.db")); !os.IsNotExist(err) {
			t.Errorf("%s should not create an index, stat err=%v", flag, err)
		}
	}
}

func TestIndexFullReconciliationAndTargetedFileUpdates(t *testing.T) {
	root := t.TempDir()
	for name, source := range map[string]string{"a.ts": "export function a() {}\n", "b.ts": "export function b() {}\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	if code := Run([]string{"index", "--path", root, "--force", "--lang", "ts", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("full index got %d; stderr=%s", code, errOut.String())
	}
	var full model.IndexResponse
	if err := json.Unmarshal(out.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if full.Mode != "full" || full.Stats == nil || full.Stats.FilesScanned != 2 || full.Stats.FilesChanged != 2 {
		t.Fatalf("unexpected full reconciliation response: %#v", full)
	}

	if err := os.WriteFile(filepath.Join(root, "a.ts"), []byte("export function changedA() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"index", "--path", root, "--force", "--file", "a.ts", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("targeted update got %d; stderr=%s", code, errOut.String())
	}
	var targeted model.IndexResponse
	if err := json.Unmarshal(out.Bytes(), &targeted); err != nil {
		t.Fatal(err)
	}
	if targeted.Mode != "targeted" || targeted.Stats == nil || targeted.Stats.FilesScanned != 1 || targeted.Stats.FilesChanged != 1 {
		t.Fatalf("unexpected targeted response: %#v", targeted)
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"index", "--path", root, "--force", "--deleted", "b.ts", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("targeted deletion got %d; stderr=%s", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"status", "--path", root, "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("status got %d; stderr=%s", code, errOut.String())
	}
	var status model.StatusResponse
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Index == nil || status.Index.FileCount != 1 {
		t.Fatalf("expected b.ts to be removed, got %#v", status.Index)
	}
}

func TestTargetedIndexRequiresExistingUsableIndex(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.ts"), []byte("function a() {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"index", "--path", root, "--force", "--file", "a.ts", "--json"}, &out, &errOut)
	if code != ExitNotIndexed {
		t.Fatalf("expected full reconciliation requirement, got %d; stderr=%s", code, errOut.String())
	}
}

func TestEmptyFullIndexPreservesSelectedLanguageScope(t *testing.T) {
	root := t.TempDir()
	var out, errOut bytes.Buffer
	if code := Run([]string{"index", "--path", root, "--force", "--lang", "rust", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("empty full index got %d; stderr=%s", code, errOut.String())
	}
	var indexed model.IndexResponse
	if err := json.Unmarshal(out.Bytes(), &indexed); err != nil {
		t.Fatal(err)
	}
	if indexed.Usable || len(indexed.LanguageSet) != 0 || len(indexed.LanguageScope) != 1 || indexed.LanguageScope[0] != "rust" {
		t.Fatalf("expected empty but scoped unusable index, got %#v", indexed)
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"status", "--path", root, "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("status got %d; stderr=%s", code, errOut.String())
	}
	var status model.StatusResponse
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Index == nil || len(status.Index.LanguageSet) != 0 || len(status.Index.LanguageScope) != 1 || status.Index.Usable {
		t.Fatalf("status should distinguish detected languages from selected scope: %#v", status.Index)
	}
}

func TestTargetedIndexCannotExpandExistingLanguageScope(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.ts"), []byte("function a() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.rs"), []byte("fn a() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := Run([]string{"index", "--path", root, "--force", "--lang", "ts", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("full index got %d; stderr=%s", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code := Run([]string{"index", "--path", root, "--force", "--lang", "rust", "--file", "a.rs", "--json"}, &out, &errOut)
	if code != ExitInvalidInput || !strings.Contains(errOut.String(), "outside existing index scope") {
		t.Fatalf("expected targeted language outside scope to be rejected, got %d; stderr=%s", code, errOut.String())
	}
	code = Run([]string{"index", "--path", root, "--force", "--file", "a.rs", "--json"}, &out, &errOut)
	if code != ExitError || !strings.Contains(errOut.String(), "not among selected languages") {
		t.Fatalf("expected targeted path outside stored scope to be rejected, got %d; stderr=%s", code, errOut.String())
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

func TestStatusDetectsNewFilesAndReportsReconciliationScope(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.ts"), []byte("function a() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := Run([]string{"index", "--path", root, "--force", "--lang", "ts", "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("index got %d; stderr=%s", code, errOut.String())
	}
	if err := os.WriteFile(filepath.Join(root, "new.ts"), []byte("function newFile() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"status", "--path", root, "--json"}, &out, &errOut); code != ExitOK {
		t.Fatalf("status got %d; stderr=%s", code, errOut.String())
	}
	var status model.StatusResponse
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Index == nil || status.Index.LastFullReconciliation == "" || status.Index.LastTargetedUpdate != "" {
		t.Fatalf("expected full reconciliation time and no targeted update, got %#v", status.Index)
	}
	if status.Freshness == nil || status.Freshness.NewFiles != 1 || !status.Freshness.NeedsReindex {
		t.Fatalf("expected one new file and reindex recommendation, got %#v", status.Freshness)
	}
}
