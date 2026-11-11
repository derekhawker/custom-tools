package model

type Envelope struct {
	OK      bool       `json:"ok"`
	Command string     `json:"command"`
	Path    string     `json:"path"`
	Message string     `json:"message"`
	Error   *ErrDetail `json:"error,omitempty"`
}

type ErrDetail struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

type IndexResponse struct {
	Envelope
	Started       bool        `json:"started"`
	RequiresForce bool        `json:"requires_force"`
	Usable        bool        `json:"usable"`
	LanguageSet   []string    `json:"language_set"`
	LanguageScope []string    `json:"language_scope"`
	Coverage      string      `json:"coverage"`
	Mode          string      `json:"mode,omitempty"`
	Stats         *IndexStats `json:"stats,omitempty"`
}

type IndexStats struct {
	FilesScanned          int      `json:"files_scanned"`
	FilesChanged          int      `json:"files_changed"`
	FilesParsed           int      `json:"files_parsed"`
	FilesDeleted          int      `json:"files_deleted"`
	DurationMS            int64    `json:"duration_ms"`
	SkippedDirectoryCount int      `json:"skipped_directory_count,omitempty"`
	SkippedDirectories    []string `json:"skipped_directories,omitempty"`
}

type StatusResponse struct {
	Envelope
	Indexed   bool       `json:"indexed"`
	Index     *IndexMeta `json:"index,omitempty"`
	Freshness *Freshness `json:"freshness,omitempty"`
}

type IndexMeta struct {
	Version                string   `json:"version"`
	CreatedAt              string   `json:"created_at"`
	UpdatedAt              string   `json:"updated_at"`
	LastFullReconciliation string   `json:"last_full_reconciliation,omitempty"`
	LastTargetedUpdate     string   `json:"last_targeted_update,omitempty"`
	LanguageSet            []string `json:"language_set"`
	LanguageScope          []string `json:"language_scope"`
	FileCount              int      `json:"file_count"`
	SymbolCount            int      `json:"symbol_count"`
	RelationCount          int      `json:"relation_count"`
	Usable                 bool     `json:"usable"`
	Coverage               string   `json:"coverage"`
}

type Freshness struct {
	StaleFiles   int      `json:"stale_files"`
	DeletedFiles int      `json:"deleted_files"`
	NewFiles     int      `json:"new_files"`
	NeedsReindex bool     `json:"needs_reindex"`
	CheckMethod  string   `json:"check_method"`
	Limitations  []string `json:"limitations,omitempty"`
}

type QueryResponse struct {
	Envelope
	Intent  string        `json:"intent"`
	Results []QueryResult `json:"results"`
	Meta    QueryMeta     `json:"meta"`
}

type QueryResult struct {
	SymbolID       string `json:"symbol_id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	File           string `json:"file"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	Signature      string `json:"signature,omitempty"`
	Snippet        string `json:"snippet,omitempty"`
	Relation       string `json:"relation"`
	Language       string `json:"language"`
	Caller         string `json:"caller,omitempty"`
	Callee         string `json:"callee,omitempty"`
	CallSiteLine   int    `json:"call_site_line,omitempty"`
	CallSiteColumn int    `json:"call_site_column,omitempty"`
	Resolution     string `json:"resolution,omitempty"`
	Evidence       string `json:"evidence,omitempty"`
}

type QueryMeta struct {
	Limit        int      `json:"limit"`
	Returned     int      `json:"returned"`
	IndexVersion string   `json:"index_version"`
	DurationMS   int64    `json:"duration_ms"`
	HasMore      bool     `json:"has_more"`
	Warnings     []string `json:"warnings,omitempty"`
}
