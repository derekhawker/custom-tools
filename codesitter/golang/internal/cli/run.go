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
	defaultQueryLimit = 20
	maxQueryLimit     = 200
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
	langFlag := fs.String("lang", "", "languages to index (js, ts, tsx, go, java, rust, css, scss, sass)")
	var filePaths, deletedPaths stringListFlag
	fs.Var(&filePaths, "file", "changed file path relative to project root; repeat for multiple files; omit for full reconciliation")
	fs.Var(&deletedPaths, "deleted", "deleted file path relative to project root; repeat for multiple files")

	if err := fs.Parse(args); err != nil {
		return ExitInvalidInput
	}
	langs := parseLangs(*langFlag)
	if err := indexer.ValidateLanguages(langs); err != nil {
		writeErr(stderr, err.Error())
		return ExitInvalidInput
	}
	languageScope := indexer.LanguageNames(langs)
	if err := validateRelativePaths(append(append([]string{}, filePaths...), deletedPaths...)); err != nil {
		writeErr(stderr, err.Error())
		return ExitInvalidInput
	}
	filePaths = normalizeRelativePaths(filePaths)
	deletedPaths = normalizeRelativePaths(deletedPaths)
	deletedSet := make(map[string]struct{}, len(deletedPaths))
	for _, path := range deletedPaths {
		deletedSet[path] = struct{}{}
	}
	for _, path := range filePaths {
		if _, alsoDeleted := deletedSet[path]; alsoDeleted {
			writeErr(stderr, fmt.Sprintf("path %q cannot be both changed and deleted", path))
			return ExitInvalidInput
		}
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
		return writeAndExit(stdout, stderr, *jsonFlag, resp, ExitForceRequired)
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
			return writeAndExit(stdout, stderr, *jsonFlag, resp, ExitLocked)
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

	targeted := len(filePaths)+len(deletedPaths) > 0
	hasIndex, err := store.HasIndex(db, absPath)
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	previousRecords, err := store.GetIndexedFiles(db, absPath)
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	var previous map[string]store.FileRecord
	if hasIndex {
		summary, summaryErr := store.GetIndexSummary(db, absPath)
		if summaryErr != nil {
			writeErr(stderr, summaryErr.Error())
			return ExitError
		}
		if targeted && (summary.Version != store.CurrentIndexVersion || len(previousRecords) == 0) {
			writeErr(stderr, "targeted updates require a usable index; run index --force without --file/--deleted to reconcile the full project")
			return ExitInvalidInput
		}
		if targeted {
			languageScope = summary.LanguageScope
			if len(langs) > 0 {
				scopedLanguages := make(map[string]struct{}, len(languageScope))
				for _, language := range languageScope {
					scopedLanguages[language] = struct{}{}
				}
				for _, language := range indexer.LanguageNames(langs) {
					if _, allowed := scopedLanguages[language]; !allowed {
						writeErr(stderr, fmt.Sprintf("targeted language %q is outside existing index scope; run a full reconciliation to change scope", language))
						return ExitInvalidInput
					}
				}
			}
		}
		if summary.Version == store.CurrentIndexVersion {
			previous = make(map[string]store.FileRecord, len(previousRecords))
			for _, record := range previousRecords {
				previous[record.RelPath] = record
			}
		}
	} else if targeted {
		writeErr(stderr, "targeted updates require an existing index; run index --force without --file/--deleted to reconcile the full project")
		return ExitNotIndexed
	}
	scanLangs := langs
	if targeted && len(scanLangs) == 0 {
		scanLangs = languageScope
	}
	scanResult, err := indexer.Scan(indexer.Options{RootPath: absPath, Langs: scanLangs, Paths: filePaths, Targeted: targeted, Previous: previous})
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	var filesIndexed int
	if targeted {
		filesIndexed, err = store.UpdateFiles(db, absPath, scanResult.Files, deletedPaths)
	} else {
		filesIndexed, err = store.SyncFiles(db, absPath, scanResult.Files)
	}
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	filesDeleted := 0
	if targeted {
		existing := make(map[string]struct{}, len(previousRecords))
		for _, record := range previousRecords {
			existing[record.RelPath] = struct{}{}
		}
		for _, path := range deletedPaths {
			if _, ok := existing[path]; ok {
				filesDeleted++
			}
		}
	} else {
		present := make(map[string]struct{}, len(scanResult.Files))
		for _, record := range scanResult.Files {
			present[record.RelPath] = struct{}{}
		}
		filesDeleted = 0
		for _, previous := range previousRecords {
			if _, exists := present[previous.RelPath]; !exists {
				filesDeleted++
			}
		}
	}
	for relPath, syms := range scanResult.Symbols {
		if err := store.ReplaceSymbolsForFile(db, absPath, relPath, syms); err != nil {
			writeErr(stderr, err.Error())
			return ExitError
		}
	}
	changedPaths := append([]string{}, scanResult.ChangedPaths...)
	changedPaths = append(changedPaths, deletedPaths...)
	if err := store.RecomputeRelations(db, absPath, scanResult.Calls, changedPaths); err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	allIndexedFiles, err := store.GetIndexedFiles(db, absPath)
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}
	langs = store.LanguageSetFromFiles(allIndexedFiles)
	usable := len(allIndexedFiles) > 0
	mode := "full"
	if targeted {
		mode = "targeted"
	}
	stats := store.IndexStats{FilesScanned: scanResult.FilesScanned, FilesIndexed: filesIndexed}
	if err := store.RecordIndex(db, absPath, langs, languageScope, mode, stats); err != nil {
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
		Usable:        usable,
		LanguageSet:   langs,
		LanguageScope: languageScope,
		Coverage:      "all supported files in these languages, excluding standard dependency/cache/generated directories",
		Mode:          mode,
		Stats: &model.IndexStats{
			FilesScanned: stats.FilesScanned, FilesChanged: stats.FilesIndexed, FilesParsed: len(scanResult.Symbols), FilesDeleted: filesDeleted, DurationMS: durationMS,
			SkippedDirectoryCount: scanResult.SkippedDirectoryCount, SkippedDirectories: scanResult.SkippedDirectories,
		},
	}
	if !usable {
		resp.Message = "Index completed, but no supported source files were found; queries require a usable index."
	}
	return writeAndExit(stdout, stderr, *jsonFlag, resp, ExitOK)
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
		return writeAndExit(stdout, stderr, *jsonFlag, resp, ExitOK)
	}
	if err != nil {
		writeErr(stderr, err.Error())
		return ExitError
	}

	usable := summary.FileCount > 0 && summary.Version == store.CurrentIndexVersion
	message := "Index exists and covers indexed files."
	if summary.FileCount == 0 {
		message = "Index exists but has no supported files; run a full reconciliation after selecting a supported language."
	} else if summary.Version != store.CurrentIndexVersion {
		message = "Index requires a full reconciliation before its results are trustworthy."
	}
	resp := model.StatusResponse{
		Envelope: model.Envelope{
			OK:      true,
			Command: "status",
			Path:    absPath,
			Message: message,
		},
		Indexed: true,
		Index: &model.IndexMeta{
			Version:                summary.Version,
			CreatedAt:              summary.CreatedAt.Format(time.RFC3339),
			UpdatedAt:              summary.UpdatedAt.Format(time.RFC3339),
			LastFullReconciliation: formatTime(summary.LastFullReconciliation),
			LastTargetedUpdate:     formatTime(summary.LastTargetedUpdate),
			LanguageSet:            summary.LanguageSet,
			LanguageScope:          summary.LanguageScope,
			FileCount:              summary.FileCount,
			SymbolCount:            summary.SymbolCount,
			RelationCount:          summary.RelationCount,
			Usable:                 usable,
			Coverage:               "all supported files in selected languages, excluding standard dependency/cache/generated directories",
		},
		Freshness: &model.Freshness{
			StaleFiles: summary.StaleFiles, DeletedFiles: summary.DeletedFiles, NewFiles: summary.NewFiles,
			NeedsReindex: !usable || summary.StaleFiles > 0 || summary.DeletedFiles > 0 || summary.NewFiles > 0,
			CheckMethod:  "filesystem metadata and supported-file path scan",
			Limitations:  []string{"same-size content changes with preserved modification time may not be detected until full reconciliation"},
		},
	}

	return writeAndExit(stdout, stderr, *jsonFlag, resp, ExitOK)
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func runQuery(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()
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
	limitFlag := fs.Int("limit", defaultQueryLimit, "max results (1-200)")
	symbolFlag := fs.String("symbol", "", "symbol name")
	fileFlag := fs.String("file", "", "file path")
	moduleFlag := fs.String("module", "", "module name")

	if err := fs.Parse(args[1:]); err != nil {
		return ExitInvalidInput
	}
	if *limitFlag <= 0 || *limitFlag > maxQueryLimit {
		writeErr(stderr, "query --limit must be between 1 and 200")
		return ExitInvalidInput
	}
	switch intent {
	case "find_declarations", "find_references", "find_callers", "find_callees":
		if strings.TrimSpace(*symbolFlag) == "" {
			writeErr(stderr, intent+" requires --symbol")
			return ExitInvalidInput
		}
	case "get_file_outline":
		if strings.TrimSpace(*fileFlag) == "" {
			writeErr(stderr, "get_file_outline requires --file")
			return ExitInvalidInput
		}
		if err := validateRelativePaths([]string{*fileFlag}); err != nil {
			writeErr(stderr, err.Error())
			return ExitInvalidInput
		}
		*fileFlag = filepath.ToSlash(filepath.Clean(filepath.FromSlash(*fileFlag)))
	case "find_importers":
		if strings.TrimSpace(*moduleFlag) == "" {
			writeErr(stderr, "find_importers requires --module")
			return ExitInvalidInput
		}
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
		return writeAndExit(stdout, stderr, *jsonFlag, resp, ExitNotIndexed)
	}
	summary, summaryErr := store.GetIndexSummary(db, absPath)
	if summaryErr != nil {
		writeErr(stderr, summaryErr.Error())
		return ExitError
	}
	if summary.Version != store.CurrentIndexVersion || summary.FileCount == 0 {
		resp := model.QueryResponse{
			Envelope: model.Envelope{OK: false, Command: "query", Path: absPath, Message: "Index is not usable; run a full reconciliation with index --force.", Error: &model.ErrDetail{Code: "FULL_RECONCILIATION_REQUIRED"}},
			Intent:   intent, Results: []model.QueryResult{}, Meta: model.QueryMeta{Limit: *limitFlag, Returned: 0, IndexVersion: summary.Version},
		}
		return writeAndExit(stdout, stderr, *jsonFlag, resp, ExitNotIndexed)
	}
	queryLimit := *limitFlag + 1

	results := []model.QueryResult{}
	relation := "declares"
	if intent == "find_declarations" {
		decls, qErr := store.FindDeclarations(db, absPath, *symbolFlag, queryLimit)
		if qErr != nil {
			writeErr(stderr, qErr.Error())
			return ExitError
		}
		results = toQueryResults(decls)
		relation = "declares"
	} else if intent == "find_references" {
		refs, qErr := store.FindReferences(db, absPath, *symbolFlag, queryLimit)
		if qErr != nil {
			writeErr(stderr, qErr.Error())
			return ExitError
		}
		results = toQueryResults(refs)
		relation = "references"
	} else if intent == "find_callers" {
		callers, qErr := store.FindCallers(db, absPath, *symbolFlag, queryLimit)
		if qErr != nil {
			writeErr(stderr, qErr.Error())
			return ExitError
		}
		results = toCallQueryResults(callers, true)
		relation = "calls"
	} else if intent == "find_callees" {
		callees, qErr := store.FindCallees(db, absPath, *symbolFlag, queryLimit)
		if qErr != nil {
			writeErr(stderr, qErr.Error())
			return ExitError
		}
		results = toCallQueryResults(callees, false)
		relation = "calls"
	} else if intent == "get_file_outline" {
		outline, qErr := store.FileOutline(db, absPath, filepath.ToSlash(*fileFlag), queryLimit)
		if qErr != nil {
			writeErr(stderr, qErr.Error())
			return ExitError
		}
		results = toQueryResults(outline)
		relation = "declares"
	} else if intent == "find_importers" {
		importers, qErr := store.FindImporters(db, absPath, *moduleFlag, queryLimit)
		if qErr != nil {
			writeErr(stderr, qErr.Error())
			return ExitError
		}
		results = toQueryResults(importers)
		relation = "imports"
	}
	hasMore := len(results) > *limitFlag
	if hasMore {
		results = results[:*limitFlag]
	}
	setRelation(results, relation)
	warnings := []string{}
	if summary.LastTargetedUpdate.After(summary.LastFullReconciliation) {
		warnings = append(warnings, "Only caller-reported paths were updated since the last full reconciliation; other changes were not checked.")
	}
	switch intent {
	case "find_references":
		warnings = append(warnings, "Reference matches are based on line text and may include comments/strings or miss syntax-spanning matches.")
	case "find_importers":
		warnings = append(warnings, "Importer matches are heuristic text matches; imports are not resolved structurally.")
	case "find_callers", "find_callees":
		warnings = append(warnings, "Call relationships use syntax call sites and same-name matching, not semantic resolution.")
	}

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
			Limit:        *limitFlag,
			Returned:     len(results),
			IndexVersion: summary.Version,
			DurationMS:   time.Since(startedAt).Milliseconds(),
			HasMore:      hasMore,
			Warnings:     warnings,
		},
	}

	return writeAndExit(stdout, stderr, *jsonFlag, resp, ExitOK)
}

func parseLangs(raw string) []string {
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func validateRelativePaths(paths []string) error {
	for _, path := range paths {
		clean := filepath.Clean(filepath.FromSlash(path))
		if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("index file paths must be relative to project root: %q", path)
		}
	}
	return nil
}

func normalizeRelativePaths(paths []string) stringListFlag {
	out := make(stringListFlag, 0, len(paths))
	for _, path := range paths {
		out = append(out, filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))))
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
			Signature: boundText(it.Signature),
			Relation:  "declares",
			Language:  it.Language,
			Evidence:  it.Evidence,
		})
	}
	return out
}

func boundText(value string) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	const maxRunes = 320
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes-3]) + "..."
	}
	return value
}

func toCallQueryResults(items []store.CallSiteRecord, callers bool) []model.QueryResult {
	out := make([]model.QueryResult, 0, len(items))
	for _, item := range items {
		result := model.QueryResult{
			File:           item.RelPath,
			Snippet:        item.Snippet,
			Language:       item.Language,
			Caller:         item.CallerName,
			Callee:         item.CalleeName,
			CallSiteLine:   item.CallSiteLine,
			CallSiteColumn: item.CallSiteColumn,
			Resolution:     item.Resolution,
			Relation:       "calls",
		}
		if callers {
			result.SymbolID = fmt.Sprintf("%s:%s:%d", item.RelPath, item.CallerName, item.CallerStartLine)
			result.Name = item.CallerName
			result.Kind = item.CallerKind
			result.StartLine = item.CallerStartLine
			result.EndLine = item.CallerEndLine
		} else {
			result.Name = item.CalleeName
			result.Kind = "call"
			result.StartLine = item.CallSiteLine
			result.EndLine = item.CallSiteLine
		}
		out = append(out, result)
	}
	return out
}

func setRelation(items []model.QueryResult, relation string) {
	for i := range items {
		items[i].Relation = relation
	}
}

func writeAndExit(stdout, stderr io.Writer, asJSON bool, value any, code int) int {
	if err := writeOutput(stdout, asJSON, value); err != nil {
		_, _ = fmt.Fprintf(stderr, "write output: %v\n", err)
		return ExitError
	}
	return code
}

func writeOutput(w io.Writer, asJSON bool, v any) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}

	switch t := v.(type) {
	case model.IndexResponse:
		if _, err := fmt.Fprintf(w, "%s\n", t.Message); err != nil {
			return err
		}
		if t.Stats != nil {
			if _, err := fmt.Fprintf(w, "Detected languages: %s; selected languages: %s; coverage: %s\n", strings.Join(t.LanguageSet, ", "), strings.Join(t.LanguageScope, ", "), t.Coverage); err != nil {
				return err
			}
			_, err := fmt.Fprintf(w, "Mode: %s; files scanned: %d; files changed: %d; files parsed: %d; files deleted: %d; duration: %dms\n", t.Mode, t.Stats.FilesScanned, t.Stats.FilesChanged, t.Stats.FilesParsed, t.Stats.FilesDeleted, t.Stats.DurationMS)
			if err != nil {
				return err
			}
			if t.Stats.SkippedDirectoryCount > 0 {
				_, err = fmt.Fprintf(w, "Skipped %d standard dependency/cache/generated directories: %s\n", t.Stats.SkippedDirectoryCount, strings.Join(t.Stats.SkippedDirectories, ", "))
			}
			return err
		}
	case model.StatusResponse:
		if _, err := fmt.Fprintf(w, "%s\n", t.Message); err != nil {
			return err
		}
		if t.Index != nil {
			if _, err := fmt.Fprintf(w, "Detected languages: %s; selected languages: %s; files: %d; symbols: %d; relations: %d\nCoverage: %s\n", strings.Join(t.Index.LanguageSet, ", "), strings.Join(t.Index.LanguageScope, ", "), t.Index.FileCount, t.Index.SymbolCount, t.Index.RelationCount, t.Index.Coverage); err != nil {
				return err
			}
			if t.Index.LastFullReconciliation != "" {
				if _, err := fmt.Fprintf(w, "Last full reconciliation: %s\n", t.Index.LastFullReconciliation); err != nil {
					return err
				}
			}
			if t.Index.LastTargetedUpdate != "" {
				if _, err := fmt.Fprintf(w, "Last targeted update: %s\n", t.Index.LastTargetedUpdate); err != nil {
					return err
				}
			}
		}
		if t.Freshness != nil {
			_, err := fmt.Fprintf(w, "Freshness check (%s): %d changed, %d deleted, %d new; needs reindex: %t\n", t.Freshness.CheckMethod, t.Freshness.StaleFiles, t.Freshness.DeletedFiles, t.Freshness.NewFiles, t.Freshness.NeedsReindex)
			if err != nil {
				return err
			}
			for _, limitation := range t.Freshness.Limitations {
				if _, err := fmt.Fprintf(w, "Limitation: %s\n", limitation); err != nil {
					return err
				}
			}
		}
	case model.QueryResponse:
		if _, err := fmt.Fprintf(w, "%s: %d result(s)\n", t.Intent, len(t.Results)); err != nil {
			return err
		}
		for _, warning := range t.Meta.Warnings {
			if _, err := fmt.Fprintf(w, "Warning: %s\n", warning); err != nil {
				return err
			}
		}
		for _, result := range t.Results {
			line := result.StartLine
			if result.CallSiteLine > 0 {
				line = result.CallSiteLine
			}
			if _, err := fmt.Fprintf(w, "%s:%d: %s (%s)\n", result.File, line, result.Name, result.Kind); err != nil {
				return err
			}
			if result.Caller != "" || result.Callee != "" {
				if _, err := fmt.Fprintf(w, "  %s → %s [%s]\n", result.Caller, result.Callee, result.Resolution); err != nil {
					return err
				}
			}
			if result.Signature != "" {
				if _, err := fmt.Fprintf(w, "  %s\n", result.Signature); err != nil {
					return err
				}
			}
			if result.Snippet != "" {
				if _, err := fmt.Fprintf(w, "  %s\n", result.Snippet); err != nil {
					return err
				}
			}
			if result.Evidence != "" {
				if _, err := fmt.Fprintf(w, "  Evidence: %s\n", result.Evidence); err != nil {
					return err
				}
			}
		}
		if t.Meta.HasMore {
			if _, err := fmt.Fprintln(w, "More results are available; increase --limit to see them."); err != nil {
				return err
			}
		}
	default:
		_, err := fmt.Fprintln(w, "ok")
		return err
	}
	return nil
}

func writeErr(w io.Writer, msg string) {
	fmt.Fprintln(w, msg)
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "codesitter <command> [flags]")
	fmt.Fprintln(w, "commands: index, status, query")
}
