package zlibrarymcp

import "time"

type PublicKeyPayload struct {
	Algorithm string `json:"algorithm"`
	Format    string `json:"format"`
	PublicKey string `json:"public_key"`
}

type SearchBooksRequest struct {
	Query        string   `json:"query"`
	Exact        bool     `json:"exact,omitempty"`
	FromYear     int      `json:"from_year,omitempty"`
	ToYear       int      `json:"to_year,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	Extensions   []string `json:"extensions,omitempty"`
	ContentTypes []string `json:"content_types,omitempty"`
	Count        int      `json:"count,omitempty"`
}

type DownloadBookRequest struct {
	BookDetails           map[string]any `json:"book_details"`
	OutputSubdir          string         `json:"output_subdir,omitempty"`
	ProcessForRAG         bool           `json:"process_for_rag,omitempty"`
	ProcessedOutputFormat string         `json:"processed_output_format,omitempty"`
}

type MCPToolPayload struct {
	Tool              string    `json:"tool"`
	Text              string    `json:"text"`
	StructuredContent any       `json:"structured_content,omitempty"`
	IsError           bool      `json:"is_error"`
	CalledAt          time.Time `json:"called_at"`
}

type RuntimeStatus struct {
	Installed    bool     `json:"installed"`
	Connected    bool     `json:"connected"`
	ServerDir    string   `json:"server_dir,omitempty"`
	DownloadRoot string   `json:"download_root,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	LastError    string   `json:"last_error,omitempty"`
}

type StatusPayload struct {
	Feature      string        `json:"feature"`
	StorageRoot  string        `json:"storage_root,omitempty"`
	PublicKeySet bool          `json:"public_key_set"`
	Runtime      RuntimeStatus `json:"runtime"`
}
