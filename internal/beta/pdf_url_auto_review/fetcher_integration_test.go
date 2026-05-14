//go:build integration

package pdfurlautoreview

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLiveArxivPDFEndpointReachesProviderAndHonorsSizeLimit(t *testing.T) {
	t.Setenv("GOCLAW_BETA_PDF_URL_AUTO_REVIEW_MAX_BYTES", "64")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	feature := &PDFURLAutoReviewFeature{}
	_, err := feature.fetchDocument(ctx, "https://arxiv.org/pdf/1706.03762")
	if err == nil {
		t.Fatal("fetchDocument unexpectedly succeeded with a 64-byte live PDF limit")
	}
	if !strings.Contains(err.Error(), "response exceeded 64 bytes") {
		t.Fatalf("error = %q, want live size-limit validation", err.Error())
	}
}
