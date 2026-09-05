package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	golang "github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	rustlang "github.com/smacker/go-tree-sitter/rust"
	tsxlang "github.com/smacker/go-tree-sitter/typescript/tsx"
	tslang "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/derekhawker/codesitter/internal/store"
)

type Options struct {
	RootPath string
	Langs    []string
	Include  string
	Exclude  string
	MaxFiles int
}

type Result struct {
	Files   []store.FileRecord
	Symbols map[string][]store.SymbolRecord
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
	allowed := allowedExts(opts.Langs)
	files := []store.FileRecord{}
	symbols := map[string][]store.SymbolRecord{}
	err := filepath.WalkDir(opts.RootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, ok := defaultExcludedDirs[strings.ToLower(d.Name())]; ok {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		lang, ok := extToLanguage(ext)
		if !ok {
			return nil
		}
		if len(allowed) > 0 {
			if _, ok := allowed[ext]; !ok {
				return nil
			}
		}
		rel, err := filepath.Rel(opts.RootPath, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !matchesGlob(opts.Include, rel, true) || matchesGlob(opts.Exclude, rel, false) {
			return nil
		}

		hash, mtime, size, err := store.HashFile(path)
		if err != nil {
			return err
		}
		files = append(files, store.FileRecord{RelPath: rel, Language: lang, ContentHash: hash, MtimeNS: mtime, SizeBytes: size})
		syms, exErr := extractSymbols(path, rel, lang)
		if exErr != nil {
			return exErr
		}
		symbols[rel] = syms

		if opts.MaxFiles > 0 && len(files) >= opts.MaxFiles {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Files: files, Symbols: symbols}, nil
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
	default:
		return "", false
	}
}

func extractSymbols(path, relPath, lang string) ([]store.SymbolRecord, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	parser := sitter.NewParser()
	parser.SetLanguage(languageFor(lang))
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, err
	}

	out := []store.SymbolRecord{}
	walkSymbols(tree.RootNode(), src, relPath, lang, &out)
	return out, nil
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
	default:
		return javascript.GetLanguage()
	}
}

func walkSymbols(n *sitter.Node, src []byte, relPath, lang string, out *[]store.SymbolRecord) {
	if n == nil {
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
		Signature: strings.TrimSpace(nodeText(src, n)),
		Language:  lang,
	})
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
