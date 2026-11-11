package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	csslang "github.com/smacker/go-tree-sitter/css"
	golang "github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	rustlang "github.com/smacker/go-tree-sitter/rust"
	tsxlang "github.com/smacker/go-tree-sitter/typescript/tsx"
	tslang "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/derekhawker/codesitter/internal/store"
)

var stylesheetVariableRE = regexp.MustCompile(`^[\t ]*(\$[-_a-zA-Z][-\w]*|--[-_a-zA-Z][-\w]*)[\t ]*:`)
var stylesheetMixinRE = regexp.MustCompile(`^[\t ]*@mixin[\t ]+([-_a-zA-Z][-\w]*)`)
var stylesheetFunctionRE = regexp.MustCompile(`^[\t ]*@function[\t ]+([-_a-zA-Z][-\w]*)`)

type Options struct {
	RootPath string
	Langs    []string
	Paths    []string
	Targeted bool
	Previous map[string]store.FileRecord
}

type Result struct {
	Files                 []store.FileRecord
	Symbols               map[string][]store.SymbolRecord
	Calls                 []store.CallRecord
	ChangedPaths          []string
	FilesScanned          int
	SkippedDirectories    []string
	SkippedDirectoryCount int
}

// Prune dependency, metadata, cache, and generated-output trees before walking them.
var defaultExcludedDirs = map[string]struct{}{
	".angular":         {},
	".cache":           {},
	".codesitter":      {},
	".git":             {},
	".hg":              {},
	".next":            {},
	".nuxt":            {},
	".parcel-cache":    {},
	".pnpm":            {},
	".svelte-kit":      {},
	".svn":             {},
	".turbo":           {},
	".vite":            {},
	".yarn":            {},
	"bower_components": {},
	"build":            {},
	"coverage":         {},
	"dist":             {},
	"jspm_packages":    {},
	"node_modules":     {},
	"out":              {},
	"target":           {},
	"vendor":           {},
}

func Scan(opts Options) (Result, error) {
	if err := ValidateLanguages(opts.Langs); err != nil {
		return Result{}, err
	}
	allowed := allowedExts(opts.Langs)
	result := Result{Files: []store.FileRecord{}, Symbols: map[string][]store.SymbolRecord{}}
	process := func(path, rel string, requireLanguage bool) error {
		ext := strings.ToLower(filepath.Ext(path))
		lang, ok := extToLanguage(ext)
		if !ok {
			if requireLanguage {
				return fmt.Errorf("unsupported source file %q", rel)
			}
			return nil
		}
		if len(allowed) > 0 {
			if _, ok := allowed[ext]; !ok {
				if requireLanguage {
					return fmt.Errorf("file %q is not among selected languages", rel)
				}
				return nil
			}
		}
		hash, mtime, size, err := store.HashFile(path)
		if err != nil {
			return err
		}
		record := store.FileRecord{RelPath: rel, Language: lang, ContentHash: hash, MtimeNS: mtime, SizeBytes: size}
		result.Files = append(result.Files, record)
		result.FilesScanned++
		previous, hadPrevious := opts.Previous[rel]
		changed := !hadPrevious || previous.ContentHash != hash || previous.Language != lang
		if changed {
			result.ChangedPaths = append(result.ChangedPaths, rel)
			syms, fileCalls, source, parseErr := extractFile(path, rel, lang)
			if parseErr != nil {
				return parseErr
			}
			record.Snapshot = source
			result.Files[len(result.Files)-1] = record
			result.Symbols[rel] = syms
			result.Calls = append(result.Calls, fileCalls...)
		}
		return nil
	}

	if opts.Targeted {
		seen := map[string]struct{}{}
		for _, path := range opts.Paths {
			clean := filepath.Clean(filepath.FromSlash(path))
			if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return Result{}, fmt.Errorf("targeted path must be relative to project root: %q", path)
			}
			rel := filepath.ToSlash(clean)
			if _, duplicate := seen[rel]; duplicate {
				continue
			}
			seen[rel] = struct{}{}
			full := filepath.Join(opts.RootPath, clean)
			st, err := os.Lstat(full)
			if err != nil {
				return Result{}, fmt.Errorf("changed path %q: %w (use deleted-path input for removals)", rel, err)
			}
			if !st.Mode().IsRegular() {
				return Result{}, fmt.Errorf("changed path %q is not a regular file", rel)
			}
			rootResolved, err := filepath.EvalSymlinks(opts.RootPath)
			if err != nil {
				return Result{}, err
			}
			fileResolved, err := filepath.EvalSymlinks(full)
			if err != nil {
				return Result{}, err
			}
			withinRoot, err := filepath.Rel(rootResolved, fileResolved)
			if err != nil || withinRoot == ".." || strings.HasPrefix(withinRoot, ".."+string(filepath.Separator)) {
				return Result{}, fmt.Errorf("changed path %q resolves outside project root", rel)
			}
			if err := process(full, rel, true); err != nil {
				return Result{}, err
			}
		}
		return result, nil
	}

	err := filepath.WalkDir(opts.RootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, ok := defaultExcludedDirs[strings.ToLower(d.Name())]; ok {
				rel, relErr := filepath.Rel(opts.RootPath, path)
				if relErr != nil {
					return relErr
				}
				result.SkippedDirectoryCount++
				if len(result.SkippedDirectories) < 20 {
					result.SkippedDirectories = append(result.SkippedDirectories, filepath.ToSlash(rel))
				}
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(opts.RootPath, path)
		if err != nil {
			return err
		}
		return process(path, filepath.ToSlash(rel), false)
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func ValidateLanguages(langs []string) error {
	for _, lang := range langs {
		switch strings.ToLower(strings.TrimSpace(lang)) {
		case "javascript", "js", "typescript", "ts", "tsx", "go", "golang", "java", "rust", "rs", "css", "scss", "sass":
		default:
			return fmt.Errorf("unsupported language %q; supported languages: js, ts, tsx, go, java, rust, css, scss, sass", lang)
		}
	}
	return nil
}

func LanguageNames(langs []string) []string {
	if len(langs) == 0 {
		return []string{"css", "go", "java", "javascript", "rust", "sass", "scss", "tsx", "typescript"}
	}
	names := map[string]struct{}{}
	for _, language := range langs {
		switch strings.ToLower(strings.TrimSpace(language)) {
		case "javascript", "js":
			names["javascript"] = struct{}{}
		case "typescript", "ts":
			names["typescript"] = struct{}{}
		case "tsx":
			names["tsx"] = struct{}{}
		case "go", "golang":
			names["go"] = struct{}{}
		case "java":
			names["java"] = struct{}{}
		case "rust", "rs":
			names["rust"] = struct{}{}
		case "css":
			names["css"] = struct{}{}
		case "scss":
			names["scss"] = struct{}{}
		case "sass":
			names["sass"] = struct{}{}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func extractFile(path, relPath, lang string) ([]store.SymbolRecord, []store.CallRecord, []byte, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(languageFor(lang))
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, nil, nil, err
	}
	defer tree.Close()
	symbols := []store.SymbolRecord{}
	walkSymbols(tree.RootNode(), src, relPath, lang, &symbols)
	var calls []store.CallRecord
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Type() == "call_expression" {
			function := node.ChildByFieldName("function")
			callee := lastIdentifier(nodeText(src, function))
			if callee != "" {
				callLine := int(node.StartPoint().Row) + 1
				owner := enclosingSymbol(symbols, callLine)
				if owner != nil {
					snippet := strings.Join(strings.Fields(nodeText(src, node)), " ")
					if runes := []rune(snippet); len(runes) > 240 {
						snippet = string(runes[:237]) + "..."
					}
					calls = append(calls, store.CallRecord{
						RelPath: relPath, CallerName: owner.Name,
						CallerStartLine: owner.StartLine, CalleeName: callee,
						CallSiteLine: callLine, CallSiteColumn: int(node.StartPoint().Column), Snippet: snippet,
					})
				}
			}
		}
		for i := 0; i < int(node.NamedChildCount()); i++ {
			walk(node.NamedChild(i))
		}
	}
	walk(tree.RootNode())
	return symbols, calls, src, nil
}

var identifierRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

func lastIdentifier(expression string) string {
	identifiers := identifierRE.FindAllString(expression, -1)
	if len(identifiers) == 0 {
		return ""
	}
	return identifiers[len(identifiers)-1]
}

func enclosingSymbol(symbols []store.SymbolRecord, line int) *store.SymbolRecord {
	var owner *store.SymbolRecord
	ownerSpan := int(^uint(0) >> 1)
	for i := range symbols {
		symbol := &symbols[i]
		if line < symbol.StartLine || line > symbol.EndLine {
			continue
		}
		span := symbol.EndLine - symbol.StartLine
		if span < ownerSpan {
			owner = symbol
			ownerSpan = span
		}
	}
	return owner
}

func allowedExts(langs []string) map[string]struct{} {
	if len(langs) == 0 {
		return nil
	}
	out := map[string]struct{}{}
	for _, lang := range langs {
		switch strings.ToLower(strings.TrimSpace(lang)) {
		case "js", "javascript":
			out[".js"] = struct{}{}
			out[".mjs"] = struct{}{}
			out[".cjs"] = struct{}{}
		case "ts", "typescript":
			out[".ts"] = struct{}{}
		case "tsx":
			out[".tsx"] = struct{}{}
		case "go", "golang":
			out[".go"] = struct{}{}
		case "java":
			out[".java"] = struct{}{}
		case "rs", "rust":
			out[".rs"] = struct{}{}
		case "css":
			out[".css"] = struct{}{}
		case "scss":
			out[".scss"] = struct{}{}
		case "sass":
			out[".sass"] = struct{}{}
		}
	}
	return out
}

func extToLanguage(ext string) (string, bool) {
	switch ext {
	case ".js", ".mjs", ".cjs":
		return "javascript", true
	case ".ts":
		return "typescript", true
	case ".tsx":
		return "tsx", true
	case ".go":
		return "go", true
	case ".java":
		return "java", true
	case ".rs":
		return "rust", true
	case ".css":
		return "css", true
	case ".scss":
		return "scss", true
	case ".sass":
		return "sass", true
	default:
		return "", false
	}
}

func languageFor(lang string) *sitter.Language {
	switch lang {
	case "go":
		return golang.GetLanguage()
	case "java":
		return java.GetLanguage()
	case "typescript":
		return tslang.GetLanguage()
	case "tsx":
		return tsxlang.GetLanguage()
	case "rs", "rust":
		return rustlang.GetLanguage()
	case "css", "scss", "sass":
		return csslang.GetLanguage()
	default:
		return javascript.GetLanguage()
	}
}

func walkSymbols(n *sitter.Node, src []byte, relPath, lang string, out *[]store.SymbolRecord) {
	if n == nil {
		return
	}
	if isStylesheetLanguage(lang) {
		walkStylesheetSymbols(n, src, relPath, lang, out)
		return
	}

	switch n.Type() {
	case "function_declaration":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "function", n, src)
	case "method_declaration":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "method", n, src)
	case "type_spec":
		name := nodeText(src, n.ChildByFieldName("name"))
		kind := "type"
		if value := n.ChildByFieldName("type"); value != nil {
			switch value.Type() {
			case "interface_type":
				kind = "interface"
			case "struct_type":
				kind = "struct"
			}
		}
		addSymbol(*&out, relPath, lang, name, kind, n, src)
	case "class_declaration":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "class", n, src)
	case "method_definition":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "method", n, src)
	case "interface_declaration":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "interface", n, src)
	case "enum_declaration":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "enum", n, src)
	case "constructor_declaration":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "constructor", n, src)
	case "variable_declarator":
		name := nodeText(src, n.ChildByFieldName("name"))
		val := n.ChildByFieldName("value")
		if val != nil {
			vt := val.Type()
			if vt == "arrow_function" || vt == "function" || vt == "function_expression" {
				addSymbol(*&out, relPath, lang, name, "variable_function", n, src)
			}
		}
	case "function_item", "function_signature_item":
		name := nodeText(src, n.ChildByFieldName("name"))
		kind := "function"
		if lang == "rust" && rustMethodNode(n) {
			kind = "method"
		}
		addSymbol(*&out, relPath, lang, name, kind, n, src)
	case "struct_item":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "struct", n, src)
	case "union_item":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "union", n, src)
	case "enum_item":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "enum", n, src)
	case "trait_item":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "trait", n, src)
	case "type_item":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "type", n, src)
	case "const_item":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "constant", n, src)
	case "static_item":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "static", n, src)
	case "mod_item":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "module", n, src)
	case "macro_definition":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "macro", n, src)
	case "associated_type":
		name := nodeText(src, n.ChildByFieldName("name"))
		addSymbol(*&out, relPath, lang, name, "type", n, src)
	}

	for i := 0; i < int(n.NamedChildCount()); i++ {
		walkSymbols(n.NamedChild(i), src, relPath, lang, out)
	}
}

func isStylesheetLanguage(lang string) bool {
	switch lang {
	case "css", "scss", "sass":
		return true
	default:
		return false
	}
}

func walkStylesheetSymbols(n *sitter.Node, src []byte, relPath, lang string, out *[]store.SymbolRecord) {
	if n == nil {
		return
	}

	switch n.Type() {
	case "rule_set":
		if selectors := stylesheetChildByType(n, "selectors"); selectors != nil {
			addSymbol(*&out, relPath, lang, strings.TrimSpace(nodeText(src, selectors)), "selector", n, src)
		}
	case "declaration":
		if property := stylesheetChildByType(n, "property_name"); property != nil {
			name := strings.TrimSpace(nodeText(src, property))
			if strings.HasPrefix(name, "--") {
				addSymbol(*&out, relPath, lang, name, "variable", n, src)
			}
		}
	case "keyframes_statement":
		if name := stylesheetChildByType(n, "keyframes_name"); name != nil {
			addSymbol(*&out, relPath, lang, strings.TrimSpace(nodeText(src, name)), "keyframes", n, src)
		}
	case "mixin_statement":
		addStylesheetNamedSymbol(n, src, relPath, lang, "mixin", out)
	case "function_statement":
		addStylesheetNamedSymbol(n, src, relPath, lang, "function", out)
	}

	for i := 0; i < int(n.NamedChildCount()); i++ {
		walkStylesheetSymbols(n.NamedChild(i), src, relPath, lang, out)
	}

	if n.Type() == "stylesheet" {
		addStylesheetSourceSymbols(src, relPath, lang, out)
	}
}

func stylesheetChildByType(n *sitter.Node, nodeType string) *sitter.Node {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child != nil && child.Type() == nodeType {
			return child
		}
	}
	return nil
}

func addStylesheetNamedSymbol(n *sitter.Node, src []byte, relPath, lang, kind string, out *[]store.SymbolRecord) {
	name := n.ChildByFieldName("name")
	if name == nil {
		name = stylesheetChildByType(n, "identifier")
	}
	if name == nil {
		return
	}
	addSymbol(*&out, relPath, lang, strings.TrimSpace(nodeText(src, name)), kind, n, src)
}

func addStylesheetSourceSymbols(src []byte, relPath, lang string, out *[]store.SymbolRecord) {
	for lineNumber, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if match := stylesheetVariableRE.FindStringSubmatch(line); len(match) > 1 {
			addSourceSymbol(out, relPath, lang, match[1], "variable", lineNumber+1, trimmed)
		}
		if match := stylesheetMixinRE.FindStringSubmatch(line); len(match) > 1 {
			addSourceSymbol(out, relPath, lang, match[1], "mixin", lineNumber+1, trimmed)
		}
		if match := stylesheetFunctionRE.FindStringSubmatch(line); len(match) > 1 {
			addSourceSymbol(out, relPath, lang, match[1], "function", lineNumber+1, trimmed)
		}
	}

	if lang == "sass" {
		addIndentedSassSelectors(src, relPath, lang, out)
	}
}

func addIndentedSassSelectors(src []byte, relPath, lang string, out *[]store.SymbolRecord) {
	for lineNumber, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if strings.HasPrefix(trimmed, "$") || strings.HasPrefix(trimmed, "@") || strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "=") {
			continue
		}
		if sassDeclarationLine(trimmed) {
			continue
		}
		name := strings.TrimSuffix(trimmed, ",")
		if name == "" || strings.ContainsAny(name, "{};") {
			continue
		}
		addSourceSymbol(out, relPath, lang, name, "selector", lineNumber+1, trimmed)
	}
}

func sassDeclarationLine(line string) bool {
	colon := strings.IndexByte(line, ':')
	if colon < 1 {
		return false
	}
	if colon == len(line)-1 {
		return true
	}
	next := line[colon+1]
	return next == ' ' || next == '\t'
}

func addSourceSymbol(out *[]store.SymbolRecord, relPath, lang, name, kind string, line int, signature string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	for _, existing := range *out {
		if existing.Name == name && existing.Kind == kind && existing.StartLine == line {
			return
		}
	}
	*out = append(*out, store.SymbolRecord{
		RelPath:   relPath,
		Name:      name,
		Kind:      kind,
		StartLine: line,
		EndLine:   line,
		Signature: signature,
		Language:  lang,
	})
}

func rustMethodNode(n *sitter.Node) bool {
	for parent := n.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Type() {
		case "impl_item", "trait_item":
			return true
		case "source_file":
			return false
		}
	}
	return false
}

func matchesGlob(pattern, relPath string, emptyMatches bool) bool {
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	if pattern == "" {
		return emptyMatches
	}
	for _, part := range strings.Split(pattern, ",") {
		part = strings.TrimSpace(filepath.ToSlash(part))
		if part == "" {
			continue
		}
		if globMatch(part, relPath) {
			return true
		}
	}
	return false
}

func globMatch(pattern, relPath string) bool {
	if ok, _ := filepath.Match(pattern, relPath); ok {
		return true
	}
	if strings.HasPrefix(pattern, "**/") {
		trimmed := strings.TrimPrefix(pattern, "**/")
		if ok, _ := filepath.Match(trimmed, relPath); ok {
			return true
		}
	}
	if strings.Contains(pattern, "/**/") {
		parts := strings.SplitN(pattern, "/**/", 2)
		return strings.HasPrefix(relPath, parts[0]+"/") && globMatch(parts[1], filepath.Base(relPath))
	}
	return false
}

func addSymbol(out *[]store.SymbolRecord, relPath, lang, name, kind string, n *sitter.Node, src []byte) {
	if name == "" || n == nil {
		return
	}
	start := int(n.StartPoint().Row) + 1
	end := int(n.EndPoint().Row) + 1
	*out = append(*out, store.SymbolRecord{
		RelPath:   relPath,
		Name:      name,
		Kind:      kind,
		StartLine: start,
		EndLine:   end,
		Signature: declarationHeader(src, n),
		Language:  lang,
	})
}

func declarationHeader(src []byte, n *sitter.Node) string {
	if n == nil {
		return ""
	}
	start, end := n.StartByte(), n.EndByte()
	if body := n.ChildByFieldName("body"); body != nil && body.StartByte() >= start {
		end = body.StartByte()
	} else if newline := strings.IndexByte(string(src[start:end]), '\n'); newline >= 0 {
		end = start + uint32(newline)
	}
	header := strings.TrimSpace(string(src[start:end]))
	header = strings.TrimSpace(strings.TrimSuffix(header, "{"))
	const maxHeaderRunes = 320
	runes := []rune(header)
	if len(runes) > maxHeaderRunes {
		header = string(runes[:maxHeaderRunes-3]) + "..."
	}
	return header
}

func nodeText(src []byte, n *sitter.Node) string {
	if n == nil {
		return ""
	}
	start := n.StartByte()
	end := n.EndByte()
	if int(end) > len(src) || int(start) >= len(src) || end <= start {
		return ""
	}
	return string(src[start:end])
}
