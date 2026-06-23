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
	Started       bool       `json:"started"`
	RequiresForce bool       `json:"requires_force"`
	Mode          string     `json:"mode,omitempty"`
	Stats         *IndexStats `json:"stats,omitempty"`
}

type IndexStats struct {
	FilesScanned     int   `json:"files_scanned"`
	FilesIndexed     int   `json:"files_indexed"`
	SymbolsWritten   int   `json:"symbols_written"`
	RelationsWritten int   `json:"relations_written"`
	DurationMS       int64 `json:"duration_ms"`
}

type StatusResponse struct {
	Envelope
	Indexed   bool       `json:"indexed"`
	Index     *IndexMeta `json:"index,omitempty"`
	Freshness *Freshness `json:"freshness,omitempty"`
}

type IndexMeta struct {
	Version       string   `json:"version"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	LanguageSet   []string `json:"language_set"`
	FileCount     int      `json:"file_count"`
	SymbolCount   int      `json:"symbol_count"`
	RelationCount int      `json:"relation_count"`
}

type Freshness struct {
	StaleFiles   int  `json:"stale_files"`
	DeletedFiles int  `json:"deleted_files"`
	NeedsReindex bool `json:"needs_reindex"`
}

type QueryResponse struct {
	Envelope
	Intent  string        `json:"intent"`
	Results []QueryResult `json:"results"`
	Meta    QueryMeta     `json:"meta"`
}

type QueryResult struct {
	SymbolID  string  `json:"symbol_id"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	File      string  `json:"file"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Signature string  `json:"signature,omitempty"`
	Snippet   string  `json:"snippet,omitempty"`
	Score     float64 `json:"score"`
	Relation  string  `json:"relation"`
	Language  string  `json:"language"`
}

type QueryMeta struct {
	Limit        int   `json:"limit"`
	Returned     int   `json:"returned"`
	IndexVersion string `json:"index_version"`
	DurationMS   int64 `json:"duration_ms"`
}
