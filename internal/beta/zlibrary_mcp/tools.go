package zlibrarymcp

import (
	"context"
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type publicKeyTool struct {
	feature *ZLibraryMCPFeature
}

func (t *publicKeyTool) Name() string { return toolPublicKeyName }

func (t *publicKeyTool) Description() string {
	return "Return the ZLibrary MCP beta feature RSA public key in PEM format. The private key is never exposed."
}

func (t *publicKeyTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *publicKeyTool) Execute(_ context.Context, _ map[string]any) *tools.Result {
	payload, err := t.feature.publicKey()
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewResult(fmt.Sprintf("ZLibrary MCP RSA public key (%s):\n%s", payload.Format, payload.PublicKey))
}

type searchBooksTool struct {
	feature *ZLibraryMCPFeature
}

func (t *searchBooksTool) Name() string { return toolSearchBooksName }

func (t *searchBooksTool) Description() string {
	return "Search Z-Library books through the ZLibrary MCP server. Returns external MCP search results with book metadata for follow-up download calls."
}

func (t *searchBooksTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Book title, author, topic, or keywords to search.",
			},
			"exact": map[string]any{
				"type":        "boolean",
				"description": "Use exact matching when searching.",
			},
			"from_year": map[string]any{
				"type":        "integer",
				"description": "Optional minimum publication year.",
			},
			"to_year": map[string]any{
				"type":        "integer",
				"description": "Optional maximum publication year.",
			},
			"languages": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional language filters such as english or russian.",
			},
			"extensions": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional file extension filters such as pdf or epub.",
			},
			"content_types": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional content type filters such as book or article.",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "Number of results to return. Defaults to 10, maximum 25.",
				"minimum":     1,
				"maximum":     25,
			},
		},
		"required": []string{"query"},
	}
}

func (t *searchBooksTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("zlibrary MCP feature is unavailable")
	}
	payload, err := t.feature.searchBooks(ctx, tenantKeyFromCtx(ctx), SearchBooksRequest{
		Query:        stringArg(args, "query"),
		Exact:        boolArg(args, "exact"),
		FromYear:     intArg(args, "from_year"),
		ToYear:       intArg(args, "to_year"),
		Languages:    stringSliceArg(args, "languages"),
		Extensions:   stringSliceArg(args, "extensions"),
		ContentTypes: stringSliceArg(args, "content_types"),
		Count:        intArg(args, "count"),
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return resultFromMCPPayload(payload, "ZLibrary MCP search_books")
}

type downloadBookTool struct {
	feature *ZLibraryMCPFeature
}

func (t *downloadBookTool) Name() string { return toolDownloadBookName }

func (t *downloadBookTool) Description() string {
	return "Download a Z-Library book through the ZLibrary MCP server. Pass a full book_details object returned by zlibrary_mcp_search_books."
}

func (t *downloadBookTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"book_details": map[string]any{
				"type":        "object",
				"description": "Full book object from zlibrary_mcp_search_books.",
			},
			"output_subdir": map[string]any{
				"type":        "string",
				"description": "Optional relative subdirectory under the feature download directory.",
			},
			"process_for_rag": map[string]any{
				"type":        "boolean",
				"description": "Process the downloaded document into a RAG text bundle.",
			},
			"processed_output_format": map[string]any{
				"type":        "string",
				"description": "Optional RAG output format, such as text or markdown.",
			},
		},
		"required": []string{"book_details"},
	}
}

func (t *downloadBookTool) Execute(ctx context.Context, args map[string]any) *tools.Result {
	if t == nil || t.feature == nil {
		return tools.ErrorResult("zlibrary MCP feature is unavailable")
	}
	payload, err := t.feature.downloadBook(ctx, tenantKeyFromCtx(ctx), DownloadBookRequest{
		BookDetails:           mapArg(args, "book_details", "bookDetails"),
		OutputSubdir:          stringArg(args, "output_subdir"),
		ProcessForRAG:         boolArg(args, "process_for_rag"),
		ProcessedOutputFormat: stringArg(args, "processed_output_format"),
	})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return resultFromMCPPayload(payload, "ZLibrary MCP download_book_to_file")
}

func resultFromMCPPayload(payload *MCPToolPayload, source string) *tools.Result {
	if payload == nil {
		return tools.ErrorResult("zlibrary MCP returned no payload")
	}
	text := tools.WrapExternalContent(payload.Text, source, false)
	if payload.IsError {
		return tools.ErrorResult(text)
	}
	return tools.NewResult(text)
}
