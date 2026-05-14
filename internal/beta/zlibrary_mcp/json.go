package zlibrarymcp

import (
	"encoding/json"
)

func decodeDownloadRequest(data []byte) (DownloadBookRequest, error) {
	if len(data) == 0 {
		return DownloadBookRequest{}, nil
	}
	var raw struct {
		BookDetailsCamel      map[string]any `json:"bookDetails"`
		BookDetailsSnake      map[string]any `json:"book_details"`
		OutputSubdir          string         `json:"output_subdir"`
		ProcessForRAG         bool           `json:"process_for_rag"`
		ProcessedOutputFormat string         `json:"processed_output_format"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return DownloadBookRequest{}, err
	}
	bookDetails := raw.BookDetailsSnake
	if len(bookDetails) == 0 {
		bookDetails = raw.BookDetailsCamel
	}
	return DownloadBookRequest{
		BookDetails:           bookDetails,
		OutputSubdir:          raw.OutputSubdir,
		ProcessForRAG:         raw.ProcessForRAG,
		ProcessedOutputFormat: raw.ProcessedOutputFormat,
	}, nil
}
