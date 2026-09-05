package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/derekhawker/codesitter/internal/indexer"
	"github.com/derekhawker/codesitter/internal/model"
	"github.com/derekhawker/codesitter/internal/store"
)

const (
	ExitOK            = 0
	ExitError         = 1
	ExitForceRequired = 2
	ExitInvalidInput  = 3
	ExitLocked        = 4
	ExitNotIndexed    = 5
)

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return ExitInvalidInput
	}

	cmd := args[0]
	switch cmd {
	case "index":
		return runIndex(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "query":
		return runQuery(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", cmd)
		printUsage(stderr)
		return ExitInvalidInput
	}
}

func runIndex(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	fs.SetOutput(stderr)

	pathFlag := fs.String("path", "", "project path")
	forceFlag := fs.Bool("force", false, "required to run indexing")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	_ = fs.Bool("incremental", true, "incremental indexing")
	langFlag := fs.String("lang", "", "language filter list (js, ts, tsx, go, java, rust, css, scss, sass)")
	includeFlag := fs.String("include", "", "include glob")
	excludeFlag := fs.String("exclude", "", "exclude glob")
	maxFilesFlag := fs.Int("max-files", 0, "max files to index")

	if err := fs.Parse(args); err != nil {
		return ExitInvalidInput
	}

	if *pathFlag == "" {
		writeErr(stderr, "index requires --path")
		return ExitInvalidInput
	}

	absPath, err := filepath.Abs(*pathFlag)
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitInvalidInput
	}

	if !*forceFlag {
		resp := model.IndexResponse{
			Envelope: model.Envelope{
				OK:      false,
				Command: "index",
				Path:    absPath,
				Message: "Indexing can take a while on large repositories and may use significant CPU. Re-run with --force to proceed.",
				Error:   &model.ErrDetail{Code: "FORCE_REQUIRED"},
			},
			Started:       false,
			RequiresForce: true,
		}
		writeOutput(stdout, *jsonFlag, resp)
		return ExitForceRequired
	}
	start := time.Now()

	if _, err := os.Stat(absPath); err != nil {
		writeErr(stderr, err.Error())
		return ExitInvalidInput
	}

	lock, err := store.AcquireLock(absPath)
	if err != nil {
		if err == store.ErrLocked {
			resp := model.IndexResponse{
				Envelope: model.Envelope{OK: false, Command: "index", Path: absPath, Message: "Index is already running.", Error: &model.ErrDetail{Code: "LOCKED"}},
				Started:  false,
			}
			writeOutput(stdout, *jsonFlag, resp)
			return ExitLocked
		}
		writeErr(stderr, err.Error())
		return ExitError
	}
	defer lock.Release()

	db, err := store.Open(absPath)
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	defer db.Close()

	langs := parseLangs(*langFlag)
	scanResult, err := indexer.Scan(indexer.Options{RootPath: absPath, Langs: langs, Include: *includeFlag, Exclude: *excludeFlag, MaxFiles: *maxFilesFlag})
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	filesIndexed, err := store.SyncFiles(db, absPath, scanResult.Files)
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	for relPath, syms := range scanResult.Symbols {
		if err := store.ReplaceSymbolsForFile(db, absPath, relPath, syms); err != nil {
			writeErr(stderr, err.Error())
			return ExitError
		}
	}
	if err := store.RecomputeRelations(db, absPath); err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	if len(langs) == 0 {
		langs = store.LanguageSetFromFiles(scanResult.Files)
	}
	stats := store.IndexStats{FilesScanned: len(scanResult.Files), FilesIndexed: filesIndexed}
	if err := store.RecordIndex(db, absPath, langs, "incremental", stats); err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	durationMS := time.Since(start).Milliseconds()

	resp := model.IndexResponse{
		Envelope: model.Envelope{
			OK:      true,
			Command: "index",
			Path:    absPath,
			Message: "Index completed.",
		},
		Started:       true,
		RequiresForce: false,
		Mode:          "incremental",
		Stats:         &model.IndexStats{FilesScanned: stats.FilesScanned, FilesIndexed: stats.FilesIndexed, DurationMS: durationMS},
	}
	writeOutput(stdout, *jsonFlag, resp)
	return ExitOK
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)

	pathFlag := fs.String("path", "", "project path")
	jsonFlag := fs.Bool("json", false, "output as JSON")

	if err := fs.Parse(args); err != nil {
		return ExitInvalidInput
	}
	if *pathFlag == "" {
		writeErr(stderr, "status requires --path")
		return ExitInvalidInput
	}
	absPath, err := filepath.Abs(*pathFlag)
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitInvalidInput
	}

	if _, err := os.Stat(absPath); err != nil {
		writeErr(stderr, err.Error())
		return ExitInvalidInput
	}

	db, err := store.Open(absPath)
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	defer db.Close()

	summary, err := store.GetSummary(db, absPath)
	if err == store.ErrNotIndexed {
		resp := model.StatusResponse{
			Envelope: model.Envelope{OK: true, Command: "status", Path: absPath, Message: "No index found yet. Run index --force."},
			Indexed:  false,
		}
		writeOutput(stdout, *jsonFlag, resp)
		return ExitOK
	}
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}

	resp := model.StatusResponse{
		Envelope: model.Envelope{
			OK:      true,
			Command: "status",
			Path:    absPath,
			Message: "Index exists.",
		},
		Indexed: true,
		Index: &model.IndexMeta{
			Version:       summary.Version,
			CreatedAt:     summary.CreatedAt.Format(time.RFC3339),
			UpdatedAt:     summary.UpdatedAt.Format(time.RFC3339),
			LanguageSet:   summary.LanguageSet,
			FileCount:     summary.FileCount,
			SymbolCount:   summary.SymbolCount,
			RelationCount: summary.RelationCount,
		},
		Freshness: &model.Freshness{StaleFiles: summary.StaleFiles, DeletedFiles: summary.DeletedFiles, NeedsReindex: false},
	}

	writeOutput(stdout, *jsonFlag, resp)
	return ExitOK
}

func runQuery(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeErr(stderr, "query requires an intent")
		return ExitInvalidInput
	}

	intent := args[0]
	if !validIntent(intent) {
		writeErr(stderr, "invalid intent")
		return ExitInvalidInput
	}
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	fs.SetOutput(stderr)

	pathFlag := fs.String("path", "", "project path")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	limitFlag := fs.Int("limit", 200, "max results")
	symbolFlag := fs.String("symbol", "", "symbol name")
	fileFlag := fs.String("file", "", "file path")
	moduleFlag := fs.String("module", "", "module name")

	if err := fs.Parse(args[1:]); err != nil {
		return ExitInvalidInput
	}
	if *pathFlag == "" {
		writeErr(stderr, "query requires --path")
		return ExitInvalidInput
	}
	absPath, err := filepath.Abs(*pathFlag)
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitInvalidInput
	}

	if _, err := os.Stat(absPath); err != nil {
		writeErr(stderr, err.Error())
		return ExitInvalidInput
	}

	db, err := store.Open(absPath)
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	defer db.Close()

	hasIndex, err := store.HasIndex(db, absPath)
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	if !hasIndex {
		resp := model.QueryResponse{
			Envelope: model.Envelope{
				OK:      false,
				Command: "query",
				Path:    absPath,
				Message: "Project is not indexed. Run index --force first.",
				Error:   &model.ErrDetail{Code: "NOT_INDEXED"},
			},
			Intent:  intent,
			Results: []model.QueryResult{},
			Meta:    model.QueryMeta{Limit: *limitFlag, Returned: 0},
		}
		writeOutput(stdout, *jsonFlag, resp)
		return ExitNotIndexed
	}

	results := []model.QueryResult{}
	relation := "declares"
	if intent == "find_declarations" {
		decls, qErr := store.FindDeclarations(db, absPath, *symbolFlag, *limitFlag)
		if qErr != nil {
			writeErr(stderr, qErr.Error())
			return ExitError
		}
		results = toQueryResults(decls)
		relation = "declares"
	} else if intent == "find_references" {
		refs, qErr := store.FindReferences(db, absPath, *symbolFlag, *limitFlag)
		if qErr != nil {
			writeErr(stderr, qErr.Error())
			return ExitError
		}
		results = toQueryResults(refs)
		relation = "references"
	} else if intent == "find_callers" {
		callers, qErr := store.FindCallers(db, absPath, *symbolFlag, *limitFlag)
		if qErr != nil {
			writeErr(stderr, qErr.Error())
			return ExitError
		}
		results = toQueryResults(callers)
		relation = "calls"
	} else if intent == "find_callees" {
		callees, qErr := store.FindCallees(db, absPath, *symbolFlag, *limitFlag)
		if qErr != nil {
			writeErr(stderr, qErr.Error())
			return ExitError
		}
		results = toQueryResults(callees)
		relation = "calls"
	} else if intent == "get_file_outline" {
		outline, qErr := store.FileOutline(db, absPath, filepath.ToSlash(*fileFlag), *limitFlag)
		if qErr != nil {
			writeErr(stderr, qErr.Error())
			return ExitError
		}
		results = toQueryResults(outline)
		relation = "declares"
	} else if intent == "find_importers" {
		importers, qErr := store.FindImporters(db, absPath, *moduleFlag, *limitFlag)
		if qErr != nil {
			writeErr(stderr, qErr.Error())
			return ExitError
		}
		results = toQueryResults(importers)
		relation = "imports"
	}
	setRelation(results, relation)

	resp := model.QueryResponse{
		Envelope: model.Envelope{
			OK:      true,
			Command: "query",
			Path:    absPath,
			Message: "Query complete.",
		},
		Intent:  intent,
		Results: results,
		Meta: model.QueryMeta{
			Limit:    *limitFlag,
			Returned: len(results),
		},
	}

	writeOutput(stdout, *jsonFlag, resp)
	return ExitOK
}

func parseLangs(raw string) []string {
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trim := strings.TrimSpace(p)
		if trim != "" {
			out = append(out, trim)
		}
	}
	return out
}

func validIntent(intent string) bool {
	switch intent {
	case "find_declarations", "find_references", "find_callers", "find_callees", "get_file_outline", "find_importers":
		return true
	default:
		return false
	}
}

func toQueryResults(items []store.SymbolRecord) []model.QueryResult {
	out := make([]model.QueryResult, 0, len(items))
	for _, it := range items {
		out = append(out, model.QueryResult{
			SymbolID:  fmt.Sprintf("%s:%s:%d", it.RelPath, it.Name, it.StartLine),
			Name:      it.Name,
			Kind:      it.Kind,
			File:      it.RelPath,
			StartLine: it.StartLine,
			EndLine:   it.EndLine,
			Signature: it.Signature,
			Score:     1.0,
			Relation:  "declares",
			Language:  it.Language,
		})
	}
	return out
}

func setRelation(items []model.QueryResult, relation string) {
	for i := range items {
		items[i].Relation = relation
	}
}

func writeOutput(w io.Writer, asJSON bool, v any) {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
		return
	}

	switch t := v.(type) {
	case model.IndexResponse:
		fmt.Fprintf(w, "%s\n", t.Message)
	case model.StatusResponse:
		fmt.Fprintf(w, "%s\n", t.Message)
	case model.QueryResponse:
		fmt.Fprintf(w, "%s\n", t.Message)
	default:
		fmt.Fprintln(w, "ok")
	}
}

func writeErr(w io.Writer, msg string) {
	fmt.Fprintln(w, msg)
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "codesitter <command> [flags]")
	fmt.Fprintln(w, "commands: index, status, query")
}
