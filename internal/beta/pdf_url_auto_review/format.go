package pdfurlautoreview

import (
	"strings"

	telegrampdfautoreview "github.com/nextlevelbuilder/goclaw/internal/beta/telegram_pdf_auto_review"
)

func formatFetchReviewForChat(payload *FetchReviewPayload) string {
	if payload == nil {
		return "URL review finished."
	}
	if payload.PDFReview != nil {
		return strings.TrimSpace("Fetched URL: " + payload.SourceURL + "\n\n" + telegrampdfautoreview.FormatUploadResultForChat(payload.PDFReview))
	}

	var out strings.Builder
	if payload.Status == fetchStatusFailed {
		out.WriteString("URL review failed.\n\n")
	} else {
		out.WriteString("URL review ready.\n\n")
	}
	out.WriteString("Source: ")
	out.WriteString(payload.SourceURL)
	if payload.ResolvedURL != "" && payload.ResolvedURL != payload.SourceURL {
		out.WriteString("\nResolved: ")
		out.WriteString(payload.ResolvedURL)
	}
	out.WriteString("\nMode: ")
	out.WriteString(defaultIfEmpty(payload.Mode, defaultReviewMode))
	if payload.Focus != "" {
		out.WriteString("\nFocus: ")
		out.WriteString(payload.Focus)
	}
	if payload.ReviewID != "" {
		out.WriteString("\nReview ID: ")
		out.WriteString(payload.ReviewID)
	}
	if payload.PaperID != "" {
		out.WriteString("\nPaper ID: ")
		out.WriteString(payload.PaperID)
	}
	if payload.Error != "" {
		out.WriteString("\nError: ")
		out.WriteString(payload.Error)
	}
	if payload.TextReview != nil && strings.TrimSpace(payload.TextReview.Report) != "" {
		out.WriteString("\n\n")
		out.WriteString(strings.TrimSpace(payload.TextReview.Report))
	}
	return strings.TrimSpace(out.String())
}

func defaultIfEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}
