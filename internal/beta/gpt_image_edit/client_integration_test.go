//go:build integration

package gptimageedit

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOpenAIImageEditLiveIntegrationRejectsOnlyAfterModelAndParameterValidation(t *testing.T) {
	apiKey := resolveOpenAIAPIKey(nil)
	if strings.TrimSpace(apiKey) == "" {
		t.Fatal("OPENAI_API_KEY or GOCLAW_OPENAI_API_KEY is required for live image edit integration verification")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	feature := &GPTImageEditFeature{
		apiKey:  apiKey,
		apiBase: resolveOpenAIBase(nil),
	}
	_, statusCode, err := feature.callOpenAIEditOnce(ctx, &imageInput{
		Data:     []byte("not-an-image"),
		MIME:     "image/png",
		FileName: "invalid.png",
		Size:     int64(len("not-an-image")),
	}, EditRequest{
		Prompt:       "integration compatibility probe; this should fail before image generation",
		OutputFormat: defaultOutputFormat,
	})
	if err == nil {
		t.Fatal("live compatibility probe unexpectedly succeeded with invalid image bytes")
	}
	if statusCode != 400 {
		t.Fatalf("live compatibility probe status = %d, want 400 invalid image response; error=%v", statusCode, err)
	}

	errText := err.Error()
	rejectedBeforeImageValidation := []string{
		"Invalid value: '" + defaultModel + "'",
		"Value must be 'dall-e-2'",
		"Unknown parameter",
		"outputformat",
	}
	for _, marker := range rejectedBeforeImageValidation {
		if strings.Contains(errText, marker) {
			t.Fatalf("live compatibility probe failed before image validation (%q): %v", marker, err)
		}
	}
	if !strings.Contains(errText, "invalid_image_file") && !strings.Contains(errText, "Invalid image file") {
		t.Fatalf("live compatibility probe error = %v, want invalid image after model/parameter validation", err)
	}
}
