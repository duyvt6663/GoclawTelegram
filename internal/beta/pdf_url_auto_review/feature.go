package pdfurlautoreview

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	researchreviewercodex "github.com/nextlevelbuilder/goclaw/internal/beta/research_reviewer_codex"
	telegrampdfautoreview "github.com/nextlevelbuilder/goclaw/internal/beta/telegram_pdf_auto_review"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	featureName             = "pdf_url_auto_review"
	fetchToolName           = "pdf_url_auto_review_fetch"
	commandName             = "/review_url"
	defaultMaxFetchBytes    = 20 << 20
	defaultFetchTimeout     = 45 * time.Second
	fetchStatusCompleted    = "completed"
	fetchStatusRunning      = "running"
	fetchStatusFailed       = "failed"
	sourceKindPDF           = "pdf"
	sourceKindArxivPDF      = "arxiv_pdf"
	sourceKindHTMLText      = "html_text"
	sourceKindPlainText     = "plain_text"
	defaultReviewMode       = "collaborative"
	reviewModeHarsh         = "harsh"
	reviewModeCollaborative = "collaborative"
)

// PDFURLAutoReviewFeature fetches paper URLs and routes them into the reviewer.
//
// Plan:
// 1. Resolve arXiv, direct PDF, and simple HTML/text URLs with strict size and timeout limits.
// 2. Save fetched PDFs locally, then hand them to telegram_pdf_auto_review so storage, dedupe, parsing, and review behavior stay shared.
// 3. Expose the same flow through a deploy-discoverable tool, scoped Telegram URL detection, RPC methods, and HTTP routes.
type PDFURLAutoReviewFeature struct {
	cfg           *config.Config
	store         *featureStore
	agentStore    store.AgentStore
	systemConfigs store.SystemConfigStore
	workspace     string
	dataDir       string
	httpClient    *http.Client

	backgroundCtx context.Context
	cancel        context.CancelFunc
	workers       sync.WaitGroup
}

type FetchReviewRequest struct {
	URL          string `json:"url"`
	Mode         string `json:"mode,omitempty"`
	Focus        string `json:"focus,omitempty"`
	ForceRefresh bool   `json:"force_refresh,omitempty"`
}

type FetchReviewPayload struct {
	FetchID       string                                     `json:"fetch_id"`
	SourceURL     string                                     `json:"source_url"`
	ResolvedURL   string                                     `json:"resolved_url,omitempty"`
	ContentType   string                                     `json:"content_type,omitempty"`
	SourceKind    string                                     `json:"source_kind,omitempty"`
	SavedPath     string                                     `json:"saved_path,omitempty"`
	FileHash      string                                     `json:"file_hash,omitempty"`
	FileSizeBytes int64                                      `json:"file_size_bytes,omitempty"`
	UploadID      string                                     `json:"upload_id,omitempty"`
	PaperID       string                                     `json:"paper_id,omitempty"`
	ReviewID      string                                     `json:"review_id,omitempty"`
	Mode          string                                     `json:"mode"`
	Focus         string                                     `json:"focus,omitempty"`
	Status        string                                     `json:"status"`
	Error         string                                     `json:"error,omitempty"`
	PDFReview     *telegrampdfautoreview.UploadResultPayload `json:"pdf_review,omitempty"`
	TextReview    *researchreviewercodex.ReviewResultPayload `json:"text_review,omitempty"`
	CreatedAt     time.Time                                  `json:"created_at"`
	UpdatedAt     time.Time                                  `json:"updated_at"`
}

type FeatureStatus struct {
	Feature              string     `json:"feature"`
	ReviewerAvailable    bool       `json:"reviewer_available"`
	PDFPipelineAvailable bool       `json:"pdf_pipeline_available"`
	DefaultMode          string     `json:"default_mode"`
	MaxFetchBytes        int64      `json:"max_fetch_bytes"`
	FetchCount           int        `json:"fetch_count"`
	CompletedFetches     int        `json:"completed_fetches"`
	FailedFetches        int        `json:"failed_fetches"`
	LastFetchID          string     `json:"last_fetch_id,omitempty"`
	LastFetchAt          *time.Time `json:"last_fetch_at,omitempty"`
}

type requestSource struct {
	Channel           string
	ChatID            string
	LocalKey          string
	TelegramMessageID string
}

func (f *PDFURLAutoReviewFeature) Name() string { return featureName }

func (f *PDFURLAutoReviewFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	f.cfg = deps.Config
	f.store = &featureStore{db: deps.Stores.DB}
	f.agentStore = deps.Stores.Agents
	f.systemConfigs = deps.Stores.SystemConfigs
	f.workspace = deps.Workspace
	f.dataDir = deps.DataDir
	f.httpClient = &http.Client{}
	f.backgroundCtx, f.cancel = context.WithCancel(context.Background())

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}
	topicrouting.RegisterTopicFeatureTools(featureName, fetchToolName)

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&fetchTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	telegramchannel.RegisterDynamicCommand(&reviewURLCommand{feature: f})
	telegramchannel.RegisterDynamicMessageHandler(&urlMessageHandler{feature: f})

	slog.Info("beta PDF URL auto review initialized")
	return nil
}

func (f *PDFURLAutoReviewFeature) Shutdown(ctx context.Context) error {
	topicrouting.UnregisterTopicFeatureTools(featureName)
	telegramchannel.UnregisterDynamicCommand(commandName)
	telegramchannel.UnregisterDynamicMessageHandler(featureName)
	if f.cancel != nil {
		f.cancel()
	}

	done := make(chan struct{})
	go func() {
		f.workers.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return fmt.Errorf("%s workers did not stop before timeout", featureName)
	}
}

func (f *PDFURLAutoReviewFeature) FetchAndReview(ctx context.Context, userID string, request FetchReviewRequest) (*FetchReviewPayload, error) {
	return f.fetchAndReview(ctx, userID, request, requestSource{
		Channel:  tools.ToolChannelFromCtx(ctx),
		ChatID:   tools.ToolChatIDFromCtx(ctx),
		LocalKey: tools.ToolLocalKeyFromCtx(ctx),
	})
}

func (f *PDFURLAutoReviewFeature) fetchAndReview(ctx context.Context, userID string, request FetchReviewRequest, source requestSource) (*FetchReviewPayload, error) {
	if f == nil || f.store == nil {
		return nil, fmt.Errorf("%s is unavailable", featureName)
	}

	mode, err := normalizeReviewMode(request.Mode, f.defaultMode(ctx))
	if err != nil {
		return nil, err
	}
	sourceURL, err := normalizeHTTPURL(request.URL)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	record := &fetchRecord{
		ID:                uuid.NewString(),
		TenantID:          tenantKey(ctx),
		SourceURL:         sourceURL,
		Channel:           strings.TrimSpace(source.Channel),
		ChatID:            strings.TrimSpace(source.ChatID),
		LocalKey:          strings.TrimSpace(source.LocalKey),
		TelegramMessageID: strings.TrimSpace(source.TelegramMessageID),
		Mode:              mode,
		FocusText:         strings.TrimSpace(request.Focus),
		FocusKey:          focusCacheKey(request.Focus),
		Status:            fetchStatusRunning,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := f.store.insertFetch(record); err != nil {
		return nil, err
	}

	payload, err := f.runFetchAndReview(ctx, userID, request, source, record)
	if err != nil {
		record.Status = fetchStatusFailed
		record.ErrorMessage = cleanUserFacingError(err)
		_ = f.store.updateFetch(record)
		return nil, err
	}
	return payload, nil
}

func (f *PDFURLAutoReviewFeature) runFetchAndReview(ctx context.Context, userID string, request FetchReviewRequest, source requestSource, record *fetchRecord) (*FetchReviewPayload, error) {
	doc, err := f.fetchDocument(ctx, record.SourceURL)
	if err != nil {
		return nil, err
	}

	record.ResolvedURL = doc.FinalURL
	record.ContentType = doc.ContentType
	record.SourceKind = doc.Kind
	record.FileSizeBytes = int64(len(doc.Data))

	switch doc.Kind {
	case sourceKindPDF, sourceKindArxivPDF:
		return f.reviewFetchedPDF(ctx, userID, request, source, record, doc)
	case sourceKindHTMLText, sourceKindPlainText:
		return f.reviewFetchedText(ctx, userID, request, record, doc)
	default:
		return nil, fmt.Errorf("unsupported content type for %s", record.SourceURL)
	}
}

func (f *PDFURLAutoReviewFeature) reviewFetchedPDF(ctx context.Context, userID string, request FetchReviewRequest, source requestSource, record *fetchRecord, doc fetchedDocument) (*FetchReviewPayload, error) {
	pdfPipeline := telegrampdfautoreview.ActiveFeature()
	if pdfPipeline == nil {
		return nil, fmt.Errorf("telegram_pdf_auto_review is not active")
	}
	if len(doc.Data) == 0 {
		return nil, fmt.Errorf("fetched PDF is empty")
	}

	fileHash := hashBytes(doc.Data)
	savedPath := filepath.Join(f.resolvedStorageRoot(ctx), "downloads", fileHash+".pdf")
	if err := writeFileOnce(savedPath, doc.Data); err != nil {
		return nil, err
	}

	record.FileHash = fileHash
	record.SavedPath = savedPath
	record.FileSizeBytes = int64(len(doc.Data))

	pdfPayload, err := pdfPipeline.ProcessLocalPDF(ctx, telegrampdfautoreview.LocalPDFProcessRequest{
		SourcePath:        savedPath,
		OriginalFileName:  fileNameFromURL(doc.FinalURL, "paper.pdf"),
		MIMEType:          "application/pdf",
		CaptionText:       "URL: " + record.SourceURL,
		Channel:           strings.TrimSpace(source.Channel),
		ChatID:            strings.TrimSpace(source.ChatID),
		LocalKey:          strings.TrimSpace(source.LocalKey),
		TelegramMessageID: strings.TrimSpace(source.TelegramMessageID),
		Mode:              record.Mode,
		Focus:             record.FocusText,
		UserID:            strings.TrimSpace(userID),
		ForceRefresh:      request.ForceRefresh,
	})
	if err != nil {
		return nil, err
	}

	record.UploadID = strings.TrimSpace(pdfPayload.UploadID)
	record.PaperID = strings.TrimSpace(pdfPayload.PaperID)
	record.ReviewID = strings.TrimSpace(pdfPayload.ReviewID)
	record.Status = strings.TrimSpace(pdfPayload.Status)
	if record.Status == "" {
		record.Status = fetchStatusCompleted
	}
	record.ErrorMessage = strings.TrimSpace(pdfPayload.Error)
	if err := f.store.updateFetch(record); err != nil {
		return nil, err
	}
	return f.buildFetchPayload(record, pdfPayload, nil), nil
}

func (f *PDFURLAutoReviewFeature) reviewFetchedText(ctx context.Context, userID string, request FetchReviewRequest, record *fetchRecord, doc fetchedDocument) (*FetchReviewPayload, error) {
	reviewer := researchreviewercodex.ActiveFeature()
	if reviewer == nil {
		return nil, fmt.Errorf("research_reviewer_codex is not active")
	}
	text := strings.TrimSpace(doc.Text)
	if text == "" {
		return nil, fmt.Errorf("no readable text extracted from %s", record.SourceURL)
	}

	textHash := hashBytes([]byte(text))
	ext := ".txt"
	savedPath := filepath.Join(f.resolvedStorageRoot(ctx), "texts", textHash+ext)
	if err := writeFileOnce(savedPath, []byte(text)); err != nil {
		return nil, err
	}

	bundle, err := reviewer.PrepareReviewBundle(ctx, researchreviewercodex.ReviewRequest{
		Title:        doc.Title,
		PaperText:    text,
		Mode:         record.Mode,
		Focus:        record.FocusText,
		ForceRefresh: request.ForceRefresh,
	})
	if err != nil {
		return nil, err
	}
	review, err := reviewer.Review(ctx, strings.TrimSpace(userID), researchreviewercodex.ReviewRequest{
		PaperID: bundle.Paper.PaperID,
		Mode:    record.Mode,
		Focus:   record.FocusText,
	})
	if err != nil {
		return nil, err
	}

	record.FileHash = textHash
	record.SavedPath = savedPath
	record.FileSizeBytes = int64(len(text))
	record.PaperID = strings.TrimSpace(review.Paper.PaperID)
	record.ReviewID = strings.TrimSpace(review.ReviewID)
	record.Status = strings.TrimSpace(review.Status)
	if record.Status == "" {
		record.Status = fetchStatusCompleted
	}
	record.ErrorMessage = strings.TrimSpace(review.Error)
	if err := f.store.updateFetch(record); err != nil {
		return nil, err
	}
	return f.buildFetchPayload(record, nil, review), nil
}

func (f *PDFURLAutoReviewFeature) statusSnapshot(ctx context.Context) (*FeatureStatus, error) {
	stats, err := f.store.statusStats(tenantKey(ctx))
	if err != nil {
		return nil, err
	}
	payload := &FeatureStatus{
		Feature:              featureName,
		ReviewerAvailable:    researchreviewercodex.ActiveFeature() != nil,
		PDFPipelineAvailable: telegrampdfautoreview.ActiveFeature() != nil,
		DefaultMode:          f.defaultMode(ctx),
		MaxFetchBytes:        f.maxFetchBytes(ctx),
		FetchCount:           stats.FetchCount,
		CompletedFetches:     stats.CompletedFetches,
		FailedFetches:        stats.FailedFetches,
		LastFetchID:          stats.LastFetchID,
	}
	if !stats.LastFetchAt.IsZero() {
		last := stats.LastFetchAt
		payload.LastFetchAt = &last
	}
	return payload, nil
}

func (f *PDFURLAutoReviewFeature) getFetchDetails(ctx context.Context, fetchID string) (*FetchReviewPayload, error) {
	record, err := f.store.getFetch(tenantKey(ctx), strings.TrimSpace(fetchID))
	if err != nil {
		return nil, err
	}
	return f.buildFetchPayload(record, nil, nil), nil
}

func (f *PDFURLAutoReviewFeature) buildFetchPayload(record *fetchRecord, pdfReview *telegrampdfautoreview.UploadResultPayload, textReview *researchreviewercodex.ReviewResultPayload) *FetchReviewPayload {
	if record == nil {
		return nil
	}
	return &FetchReviewPayload{
		FetchID:       record.ID,
		SourceURL:     record.SourceURL,
		ResolvedURL:   record.ResolvedURL,
		ContentType:   record.ContentType,
		SourceKind:    record.SourceKind,
		SavedPath:     record.SavedPath,
		FileHash:      record.FileHash,
		FileSizeBytes: record.FileSizeBytes,
		UploadID:      record.UploadID,
		PaperID:       record.PaperID,
		ReviewID:      record.ReviewID,
		Mode:          record.Mode,
		Focus:         record.FocusText,
		Status:        record.Status,
		Error:         record.ErrorMessage,
		PDFReview:     pdfReview,
		TextReview:    textReview,
		CreatedAt:     record.CreatedAt,
		UpdatedAt:     record.UpdatedAt,
	}
}

func (f *PDFURLAutoReviewFeature) maxFetchBytes(ctx context.Context) int64 {
	if raw := strings.TrimSpace(os.Getenv("GOCLAW_BETA_PDF_URL_AUTO_REVIEW_MAX_BYTES")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	if raw := strings.TrimSpace(f.systemConfig(ctx, "beta."+featureName+".max_bytes")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultMaxFetchBytes
}

func (f *PDFURLAutoReviewFeature) fetchTimeout(ctx context.Context) time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GOCLAW_BETA_PDF_URL_AUTO_REVIEW_TIMEOUT_SECONDS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			return time.Duration(parsed) * time.Second
		}
	}
	if raw := strings.TrimSpace(f.systemConfig(ctx, "beta."+featureName+".timeout_seconds")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			return time.Duration(parsed) * time.Second
		}
	}
	return defaultFetchTimeout
}

func (f *PDFURLAutoReviewFeature) defaultMode(ctx context.Context) string {
	if raw := strings.TrimSpace(os.Getenv("GOCLAW_BETA_PDF_URL_AUTO_REVIEW_DEFAULT_MODE")); raw != "" {
		if mode, err := normalizeReviewMode(raw, ""); err == nil {
			return mode
		}
	}
	if raw := strings.TrimSpace(f.systemConfig(ctx, "beta."+featureName+".default_mode")); raw != "" {
		if mode, err := normalizeReviewMode(raw, ""); err == nil {
			return mode
		}
	}
	return defaultReviewMode
}

func (f *PDFURLAutoReviewFeature) systemConfig(ctx context.Context, key string) string {
	if f == nil || f.systemConfigs == nil {
		return ""
	}
	value, err := f.systemConfigs.Get(ctx, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func (f *PDFURLAutoReviewFeature) resolvedStorageRoot(ctx context.Context) string {
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		tenantID = store.MasterTenantID
	}
	tenantSlug := store.TenantSlugFromContext(ctx)
	if tenantSlug == "" && tenantID != store.MasterTenantID {
		tenantSlug = tenantID.String()
	}

	candidates := make([]string, 0, 3)
	if root := strings.TrimSpace(config.ExpandHome(f.dataDir)); root != "" {
		candidates = append(candidates, config.TenantDataDir(root, tenantID, tenantSlug))
	}
	if root := strings.TrimSpace(f.workspace); root != "" {
		candidates = append(candidates, filepath.Join(
			config.TenantWorkspace(root, tenantID, tenantSlug),
			"beta_cache",
			featureName,
		))
	}
	candidates = append(candidates, filepath.Join(
		os.TempDir(),
		"goclaw",
		"beta_cache",
		featureName,
		tenantID.String(),
	))

	for _, candidate := range candidates {
		if storageDirWritable(filepath.Join(candidate, "downloads")) {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

func (f *PDFURLAutoReviewFeature) enabledForChannel(channel *telegramchannel.Channel) bool {
	if f == nil || f.agentStore == nil || channel == nil {
		return false
	}
	agentKey := strings.TrimSpace(channel.AgentID())
	if agentKey == "" {
		return false
	}
	ctx := store.WithTenantID(context.Background(), channel.TenantID())
	agent, err := f.agentStore.GetByKey(ctx, agentKey)
	if err != nil || agent == nil {
		return false
	}
	return toolPolicyExplicitlyAllows(agent.ParseToolsConfig(), fetchToolName)
}

func (f *PDFURLAutoReviewFeature) enabledForTopicContext(ctx context.Context, channel *telegramchannel.Channel, chatID string, threadID int, localKey string) bool {
	if f == nil {
		return false
	}
	if channel == nil {
		return true
	}
	decision, err := topicrouting.ResolveTopicToolDecision(ctx, topicrouting.TopicToolScope{
		Channel:  channel.Name(),
		ChatID:   chatID,
		ThreadID: threadID,
		LocalKey: localKey,
	})
	if err != nil {
		slog.Warn("PDF URL auto review topic routing check failed", "error", err, "channel", channel.Name(), "chat_id", chatID)
		return true
	}
	if decision == nil || !decision.Matched {
		return true
	}
	return stringListContains(decision.EnabledFeatures, featureName)
}

func inheritFeatureContext(base, source context.Context, userID string, channel *telegramchannel.Channel) context.Context {
	if base == nil {
		base = context.Background()
	}
	tenantID := store.TenantIDFromContext(source)
	if tenantID == uuid.Nil && channel != nil && channel.TenantID() != uuid.Nil {
		tenantID = channel.TenantID()
	}
	if tenantID == uuid.Nil {
		tenantID = store.MasterTenantID
	}
	ctx := store.WithTenantID(base, tenantID)
	if slug := store.TenantSlugFromContext(source); slug != "" {
		ctx = store.WithTenantSlug(ctx, slug)
	}
	if strings.TrimSpace(userID) != "" {
		ctx = store.WithUserID(ctx, strings.TrimSpace(userID))
	}
	return ctx
}

func tenantKey(ctx context.Context) string {
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		tenantID = store.MasterTenantID
	}
	return tenantID.String()
}
