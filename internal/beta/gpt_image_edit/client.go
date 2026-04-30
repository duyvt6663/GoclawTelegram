package gptimageedit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/config"
)

const openAIEditAttempts = 3

type openAIEditOutput struct {
	Data  []byte
	Usage map[string]any
}

func (f *GPTImageEditFeature) callOpenAIEdit(ctx context.Context, input *imageInput, request EditRequest) (*openAIEditOutput, error) {
	if strings.TrimSpace(f.apiKey) == "" {
		return nil, fmt.Errorf("OpenAI API key is required for %s", featureName)
	}
	if input == nil || len(input.Data) == 0 {
		return nil, fmt.Errorf("image is required")
	}

	var lastErr error
	for attempt := 1; attempt <= openAIEditAttempts; attempt++ {
		output, statusCode, err := f.callOpenAIEditOnce(ctx, input, request)
		if err == nil {
			return output, nil
		}
		lastErr = err
		if ctx.Err() != nil || !shouldRetryOpenAIStatus(statusCode) || attempt == openAIEditAttempts {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func (f *GPTImageEditFeature) callOpenAIEditOnce(ctx context.Context, input *imageInput, request EditRequest) (*openAIEditOutput, int, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	fileName := input.FileName
	if strings.TrimSpace(fileName) == "" {
		fileName = "input." + extensionForMIME(input.MIME)
	}
	part, err := createImageFormFile(writer, "image", fileName, input.MIME)
	if err != nil {
		return nil, 0, fmt.Errorf("create image form file: %w", err)
	}
	if _, err := part.Write(input.Data); err != nil {
		return nil, 0, fmt.Errorf("write image form file: %w", err)
	}

	fields := map[string]string{
		"model":  defaultModel,
		"prompt": request.Prompt,
		"n":      "1",
	}
	if request.OutputFormat != "" && request.OutputFormat != defaultOutputFormat {
		fields["output_format"] = request.OutputFormat
	}
	if request.Size != "" {
		fields["size"] = request.Size
	}
	if request.Quality != "" {
		fields["quality"] = request.Quality
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, 0, fmt.Errorf("write form field %s: %w", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, 0, fmt.Errorf("close multipart writer: %w", err)
	}

	apiBase := strings.TrimRight(f.apiBase, "/")
	if apiBase == "" {
		apiBase = defaultOpenAIBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/images/edits", &body)
	if err != nil {
		return nil, 0, fmt.Errorf("create OpenAI request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("OpenAI image edit request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read OpenAI response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("OpenAI image edit API error %d: %s", resp.StatusCode, truncateBytes(respBody, 800))
	}

	var decoded struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("parse OpenAI image edit response: %w", err)
	}
	if len(decoded.Data) == 0 || strings.TrimSpace(decoded.Data[0].B64JSON) == "" {
		return nil, resp.StatusCode, fmt.Errorf("OpenAI image edit response did not include image data")
	}

	imageData, err := decodeBase64Image(decoded.Data[0].B64JSON)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decode edited image: %w", err)
	}
	return &openAIEditOutput{Data: imageData, Usage: decoded.Usage}, resp.StatusCode, nil
}

func createImageFormFile(writer *multipart.Writer, fieldName, fileName, mimeType string) (io.Writer, error) {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeMultipartQuote(fieldName), escapeMultipartQuote(fileName)))
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	header.Set("Content-Type", mimeType)
	return writer.CreatePart(header)
}

func escapeMultipartQuote(value string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, `\"`).Replace(value)
}

func shouldRetryOpenAIStatus(statusCode int) bool {
	if statusCode == 0 {
		return true
	}
	return statusCode == http.StatusRequestTimeout ||
		statusCode == http.StatusConflict ||
		statusCode == http.StatusTooManyRequests ||
		statusCode >= 500
}

func resolveOpenAIAPIKey(cfg *config.Config) string {
	if cfg != nil {
		if key := strings.TrimSpace(cfg.Providers.OpenAI.APIKey); key != "" {
			return key
		}
	}
	if key := strings.TrimSpace(getenv("GOCLAW_OPENAI_API_KEY")); key != "" {
		return key
	}
	return strings.TrimSpace(getenv("OPENAI_API_KEY"))
}

func resolveOpenAIBase(cfg *config.Config) string {
	if cfg != nil {
		if base := strings.TrimSpace(cfg.Providers.OpenAI.APIBase); base != "" {
			return strings.TrimRight(base, "/")
		}
	}
	if base := strings.TrimSpace(getenv("GOCLAW_OPENAI_BASE_URL")); base != "" {
		return strings.TrimRight(base, "/")
	}
	if base := strings.TrimSpace(getenv("OPENAI_BASE_URL")); base != "" {
		return strings.TrimRight(base, "/")
	}
	return defaultOpenAIBase
}
