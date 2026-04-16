package telegrampdfautoreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	researchreviewercodex "github.com/nextlevelbuilder/goclaw/internal/beta/research_reviewer_codex"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	featureName           = "telegram_pdf_auto_review"
	defaultMaxPDFBytes    = 20 << 20
	uploadStatusCompleted = "completed"
	uploadStatusRunning   = "running"
	uploadStatusFailed    = "failed"
)

var (
	captionModeRe  = regexp.MustCompile(`(?im)\bmode\s*[:=]\s*(harsh|collaborative|strict|constructive|mentor)\b`)
	captionFocusRe = regexp.MustCompile(`(?im)^\s*focus\s*[:=]\s*(.+?)\s*$`)
)

// TelegramPDFAutoReviewFeature reviews Telegram PDFs only when an agent
// explicitly calls the feature tool.
//
// Plan:
// 1. Persist explicitly requested PDF reviews under a feature-local cache and dedupe by content hash.
// 2. Reuse research_reviewer_codex prepare/review APIs so PDF uploads inherit the same parsing, indexing, and review behavior.
// 3. Persist upload/review metadata in beta-local tables and expose minimal status + reprocess surfaces through tool, RPC, and HTTP endpoints.
type TelegramPDFAutoReviewFeature struct {
	cfg           *config.Config
	store         *featureStore
	agentStore    store.AgentStore
	systemConfigs store.SystemConfigStore
	workspace     string
	dataDir       string

	backgroundCtx context.Context
	cancel        context.CancelFunc
	workers       sync.WaitGroup
}

type FeatureStatus struct {
	Feature           string     `json:"feature"`
	ReviewerAvailable bool       `json:"reviewer_available"`
	DefaultMode       string     `json:"default_mode"`
	MaxFileSizeBytes  int64      `json:"max_file_size_bytes"`
	CachedFiles       int        `json:"cached_files"`
	UploadCount       int        `json:"upload_count"`
	CompletedUploads  int        `json:"completed_uploads"`
	FailedUploads     int        `json:"failed_uploads"`
	LastUploadID      string     `json:"last_upload_id,omitempty"`
	LastUploadAt      *time.Time `json:"last_upload_at,omitempty"`
}

type UploadResultPayload struct {
	UploadID         string                                     `json:"upload_id"`
	FileHash         string                                     `json:"file_hash"`
	OriginalFileName string                                     `json:"original_file_name,omitempty"`
	SavedPDFPath     string                                     `json:"saved_pdf_path,omitempty"`
	PaperID          string                                     `json:"paper_id,omitempty"`
	ReviewID         string                                     `json:"review_id,omitempty"`
	Mode             string                                     `json:"mode"`
	Focus            string                                     `json:"focus,omitempty"`
	Status           string                                     `json:"status"`
	Error            string                                     `json:"error,omitempty"`
	CachedPaper      bool                                       `json:"cached_paper,omitempty"`
	CachedReview     bool                                       `json:"cached_review,omitempty"`
	FileSizeBytes    int64                                      `json:"file_size_bytes,omitempty"`
	CaptionText      string                                     `json:"caption_text,omitempty"`
	Review           *researchreviewercodex.ReviewResultPayload `json:"review,omitempty"`
	CreatedAt        time.Time                                  `json:"created_at"`
	UpdatedAt        time.Time                                  `json:"updated_at"`
}

type ReprocessRequest struct {
	UploadID     string `json:"upload_id"`
	Mode         string `json:"mode,omitempty"`
	Focus        string `json:"focus,omitempty"`
	ForceRefresh bool   `json:"force_refresh,omitempty"`
}

type uploadProcessInput struct {
	Upload       *uploadRecord
	File         *fileCacheRecord
	UserID       string
	ForceRefresh bool
}

type uploadProcessOutcome struct {
	Upload       *uploadRecord
	Review       *researchreviewercodex.ReviewResultPayload
	CachedPaper  bool
	CachedReview bool
}

type localPDFProcessInput struct {
	SourcePath        string
	OriginalFileName  string
	MIMEType          string
	CaptionText       string
	Channel           string
	ChatID            string
	LocalKey          string
	TelegramMessageID string
	TelegramFileID    string
	TelegramUniqueID  string
	Mode              string
	Focus             string
	UserID            string
	ForceRefresh      bool
}

type uploadHandler struct {
	feature *TelegramPDFAutoReviewFeature
}

func (f *TelegramPDFAutoReviewFeature) Name() string { return featureName }

func (f *TelegramPDFAutoReviewFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	f.cfg = deps.Config
	f.store = &featureStore{db: deps.Stores.DB}
	f.agentStore = deps.Stores.Agents
	f.systemConfigs = deps.Stores.SystemConfigs
	f.workspace = deps.Workspace
	f.dataDir = deps.DataDir
	f.backgroundCtx, f.cancel = context.WithCancel(context.Background())

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&reprocessTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	slog.Info("beta telegram PDF auto review initialized")
	return nil
}

func (f *TelegramPDFAutoReviewFeature) Shutdown(ctx context.Context) error {
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

func (h *uploadHandler) Name() string { return featureName }

func (h *uploadHandler) EnabledForChannel(channel *telegramchannel.Channel) bool {
	if h == nil || h.feature == nil {
		return false
	}
	return h.feature.uploadEnabledForChannel(channel)
}

func (h *uploadHandler) MatchesMessage(_ context.Context, _ *telegramchannel.Channel, message *telego.Message) bool {
	if h == nil || h.feature == nil || message == nil || message.Document == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(message.Document.MimeType), "application/pdf")
}

func (h *uploadHandler) HandleUpload(ctx context.Context, channel *telegramchannel.Channel, uploadCtx telegramchannel.DynamicUploadContext) bool {
	if h == nil || h.feature == nil || channel == nil || uploadCtx.Message == nil || uploadCtx.Message.Document == nil {
		return false
	}
	if !h.MatchesMessage(ctx, channel, uploadCtx.Message) {
		return false
	}

	modeOverride, focusOverride := parseCaptionOverrides(uploadCtx.Message.Caption)
	mode, err := normalizeRequestedMode(modeOverride, h.feature.defaultMode(ctx))
	if err != nil {
		_ = uploadCtx.Reply(ctx, "I can only review PDFs with mode=collaborative or mode=harsh.")
		return true
	}
	focus := strings.TrimSpace(focusOverride)

	maxBytes := h.feature.maxFileSizeBytes(ctx)
	if size := int64(uploadCtx.Message.Document.FileSize); size > 0 && size > maxBytes {
		_ = uploadCtx.Reply(ctx, fmt.Sprintf("That PDF is too large for auto-review. Max size is %d MB.", bytesToWholeMB(maxBytes)))
		return true
	}

	if researchreviewercodex.ActiveFeature() == nil {
		_ = uploadCtx.Reply(ctx, "PDF review is unavailable because research_reviewer_codex is not active.")
		return true
	}

	ack := fmt.Sprintf("Received PDF %q. Starting a %s review.", displayFileName(uploadCtx.Message.Document.FileName), mode)
	if focus != "" {
		ack += " Focus: " + focus + "."
	}
	if err := uploadCtx.Reply(ctx, ack); err != nil {
		slog.Warn("telegram PDF auto review ack failed", "error", err)
	}

	h.feature.workers.Add(1)
	go func() {
		defer h.feature.workers.Done()

		runCtx := inheritFeatureContext(h.feature.backgroundCtx, ctx, uploadCtx.UserID)
		if err := h.feature.processTelegramUpload(runCtx, channel, uploadCtx, mode, focus); err != nil {
			slog.Warn("telegram PDF auto review failed", "error", err, "chat_id", uploadCtx.ChatIDStr)
		}
	}()
	return true
}

func (f *TelegramPDFAutoReviewFeature) processTelegramUpload(ctx context.Context, channel *telegramchannel.Channel, uploadCtx telegramchannel.DynamicUploadContext, mode, focus string) error {
	doc := uploadCtx.Message.Document
	if doc == nil {
		return nil
	}

	tempPath, err := channel.DownloadMediaByFileID(ctx, doc.FileID, f.maxFileSizeBytes(ctx))
	if err != nil {
		_ = uploadCtx.Reply(ctx, "I received the PDF but couldn't download it from Telegram.")
		return err
	}
	defer os.Remove(tempPath)

	payload, err := f.processLocalPDF(ctx, localPDFProcessInput{
		SourcePath:        tempPath,
		OriginalFileName:  displayFileName(doc.FileName),
		MIMEType:          strings.TrimSpace(doc.MimeType),
		CaptionText:       strings.TrimSpace(uploadCtx.Message.Caption),
		Channel:           channel.Name(),
		ChatID:            uploadCtx.ChatIDStr,
		LocalKey:          strings.TrimSpace(uploadCtx.LocalKey),
		TelegramMessageID: fmt.Sprintf("%d", uploadCtx.Message.MessageID),
		TelegramFileID:    strings.TrimSpace(doc.FileID),
		TelegramUniqueID:  strings.TrimSpace(doc.FileUniqueID),
		Mode:              mode,
		Focus:             focus,
		UserID:            uploadCtx.UserID,
	})
	if err != nil {
		_ = uploadCtx.Reply(ctx, "I couldn't review that PDF: "+cleanUserFacingError(err))
		return err
	}

	if err := uploadCtx.Reply(ctx, formatUploadResultForChat(payload)); err != nil {
		return err
	}
	return nil
}

func (f *TelegramPDFAutoReviewFeature) reviewCurrentConversationPDF(ctx context.Context, userID, mediaID string, request ReprocessRequest) (*UploadResultPayload, error) {
	if researchreviewercodex.ActiveFeature() == nil {
		return nil, fmt.Errorf("research_reviewer_codex is not active")
	}

	mode, err := normalizeRequestedMode(request.Mode, f.defaultMode(ctx))
	if err != nil {
		return nil, err
	}
	ref, err := resolveCurrentPDFRef(ctx, mediaID)
	if err != nil {
		return nil, err
	}

	return f.processLocalPDF(ctx, localPDFProcessInput{
		SourcePath:       ref.Path,
		OriginalFileName: filepath.Base(ref.Path),
		MIMEType:         ref.MimeType,
		Channel:          tools.ToolChannelFromCtx(ctx),
		ChatID:           tools.ToolChatIDFromCtx(ctx),
		LocalKey:         tools.ToolLocalKeyFromCtx(ctx),
		Mode:             mode,
		Focus:            strings.TrimSpace(request.Focus),
		UserID:           strings.TrimSpace(userID),
		ForceRefresh:     request.ForceRefresh,
	})
}

func (f *TelegramPDFAutoReviewFeature) processLocalPDF(ctx context.Context, input localPDFProcessInput) (*UploadResultPayload, error) {
	sourcePath := strings.TrimSpace(input.SourcePath)
	if sourcePath == "" {
		return nil, fmt.Errorf("pdf path is required")
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return nil, err
	}

	fileHash, fileSize, err := hashFile(sourcePath)
	if err != nil {
		return nil, err
	}

	tenantID := tenantKey(ctx)
	fileRecord, err := f.store.getFileByHash(tenantID, fileHash)
	if err != nil {
		return nil, err
	}

	savedPath := f.savedPDFPath(ctx, fileHash)
	if fileRecord != nil && strings.TrimSpace(fileRecord.SavedPDFPath) != "" {
		savedPath = fileRecord.SavedPDFPath
	}
	if err := ensureStoredPDF(sourcePath, savedPath); err != nil {
		return nil, err
	}

	originalName := displayFileName(defaultIfEmpty(strings.TrimSpace(input.OriginalFileName), filepath.Base(sourcePath)))
	mimeType := strings.TrimSpace(defaultIfEmpty(input.MIMEType, "application/pdf"))
	if fileRecord == nil {
		fileRecord = &fileCacheRecord{
			TenantID: tenantID,
			FileHash: fileHash,
		}
	}
	fileRecord.TelegramFileID = strings.TrimSpace(input.TelegramFileID)
	fileRecord.TelegramUniqueID = strings.TrimSpace(input.TelegramUniqueID)
	fileRecord.OriginalFileName = originalName
	fileRecord.MIMEType = mimeType
	fileRecord.SavedPDFPath = savedPath
	fileRecord.FileSizeBytes = fileSize
	if err := f.store.upsertFile(fileRecord); err != nil {
		return nil, err
	}

	mode, err := normalizeRequestedMode(input.Mode, f.defaultMode(ctx))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	upload := &uploadRecord{
		ID:                uuid.NewString(),
		TenantID:          tenantID,
		FileHash:          fileHash,
		Channel:           strings.TrimSpace(input.Channel),
		ChatID:            strings.TrimSpace(input.ChatID),
		LocalKey:          strings.TrimSpace(input.LocalKey),
		TelegramMessageID: strings.TrimSpace(input.TelegramMessageID),
		TelegramFileID:    strings.TrimSpace(input.TelegramFileID),
		TelegramUniqueID:  strings.TrimSpace(input.TelegramUniqueID),
		OriginalFileName:  originalName,
		CaptionText:       strings.TrimSpace(input.CaptionText),
		SavedPDFPath:      savedPath,
		PaperID:           strings.TrimSpace(fileRecord.PaperID),
		FileSizeBytes:     fileSize,
		Mode:              mode,
		FocusText:         strings.TrimSpace(input.Focus),
		FocusKey:          focusCacheKey(input.Focus),
		Status:            uploadStatusRunning,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := f.store.insertUpload(upload); err != nil {
		return nil, err
	}

	outcome, err := f.runReview(ctx, uploadProcessInput{
		Upload:       upload,
		File:         fileRecord,
		UserID:       strings.TrimSpace(input.UserID),
		ForceRefresh: input.ForceRefresh,
	})
	if err != nil {
		upload.Status = uploadStatusFailed
		upload.ErrorMessage = cleanUserFacingError(err)
		_ = f.store.updateUpload(upload)
		return nil, err
	}

	return f.buildUploadResult(ctx, outcome.Upload, outcome.CachedPaper, outcome.CachedReview, outcome.Review)
}

func (f *TelegramPDFAutoReviewFeature) runReview(ctx context.Context, input uploadProcessInput) (*uploadProcessOutcome, error) {
	if input.Upload == nil {
		return nil, fmt.Errorf("upload record is required")
	}
	if input.File == nil {
		return nil, fmt.Errorf("file cache record is required")
	}

	reviewer := researchreviewercodex.ActiveFeature()
	if reviewer == nil {
		return nil, fmt.Errorf("research_reviewer_codex is not active")
	}

	upload := input.Upload
	file := input.File
	cachedPaper := strings.TrimSpace(file.PaperID) != "" && !input.ForceRefresh

	if !input.ForceRefresh {
		cachedUpload, err := f.store.getLatestCompletedUploadByCacheKey(upload.TenantID, upload.FileHash, upload.Mode, upload.FocusKey)
		if err != nil {
			return nil, err
		}
		if cachedUpload != nil && cachedUpload.ReviewID != "" {
			review, err := reviewer.GetStoredReview(ctx, cachedUpload.ReviewID)
			if err == nil && review != nil && strings.EqualFold(review.Status, uploadStatusCompleted) {
				file.PaperID = strings.TrimSpace(review.Paper.PaperID)
				upload.PaperID = file.PaperID
				upload.ReviewID = review.ReviewID
				upload.Status = uploadStatusCompleted
				upload.ErrorMessage = ""
				if err := f.store.upsertFile(file); err != nil {
					return nil, err
				}
				if err := f.store.updateUpload(upload); err != nil {
					return nil, err
				}
				return &uploadProcessOutcome{
					Upload:       upload,
					Review:       review,
					CachedPaper:  true,
					CachedReview: true,
				}, nil
			}
		}
	}

	bundle, err := reviewer.PrepareReviewBundle(ctx, researchreviewercodex.ReviewRequest{
		PDFPath:      file.SavedPDFPath,
		Mode:         upload.Mode,
		Focus:        upload.FocusText,
		ForceRefresh: input.ForceRefresh,
	})
	if err != nil {
		return nil, err
	}

	file.PaperID = strings.TrimSpace(bundle.Paper.PaperID)
	upload.PaperID = file.PaperID
	if err := f.store.upsertFile(file); err != nil {
		return nil, err
	}

	review, err := reviewer.Review(ctx, input.UserID, researchreviewercodex.ReviewRequest{
		PaperID: upload.PaperID,
		Mode:    upload.Mode,
		Focus:   upload.FocusText,
	})
	if err != nil {
		return nil, err
	}

	upload.ReviewID = strings.TrimSpace(review.ReviewID)
	upload.Status = strings.TrimSpace(review.Status)
	if upload.Status == "" {
		upload.Status = uploadStatusCompleted
	}
	upload.ErrorMessage = strings.TrimSpace(review.Error)
	if err := f.store.updateUpload(upload); err != nil {
		return nil, err
	}

	return &uploadProcessOutcome{
		Upload:       upload,
		Review:       review,
		CachedPaper:  cachedPaper,
		CachedReview: false,
	}, nil
}

func (f *TelegramPDFAutoReviewFeature) statusSnapshot(ctx context.Context) (*FeatureStatus, error) {
	stats, err := f.store.statusStats(tenantKey(ctx))
	if err != nil {
		return nil, err
	}

	payload := &FeatureStatus{
		Feature:           featureName,
		ReviewerAvailable: researchreviewercodex.ActiveFeature() != nil,
		DefaultMode:       f.defaultMode(ctx),
		MaxFileSizeBytes:  f.maxFileSizeBytes(ctx),
		CachedFiles:       stats.CachedFiles,
		UploadCount:       stats.UploadCount,
		CompletedUploads:  stats.CompletedUploads,
		FailedUploads:     stats.FailedUploads,
		LastUploadID:      stats.LastUploadID,
	}
	if !stats.LastUploadAt.IsZero() {
		last := stats.LastUploadAt
		payload.LastUploadAt = &last
	}
	return payload, nil
}

func (f *TelegramPDFAutoReviewFeature) getUploadDetails(ctx context.Context, uploadID string) (*UploadResultPayload, error) {
	record, err := f.store.getUpload(tenantKey(ctx), strings.TrimSpace(uploadID))
	if err != nil {
		return nil, err
	}
	return f.buildUploadResult(ctx, record, false, false, nil)
}

func (f *TelegramPDFAutoReviewFeature) reprocessUpload(ctx context.Context, userID string, request ReprocessRequest) (*UploadResultPayload, error) {
	if researchreviewercodex.ActiveFeature() == nil {
		return nil, fmt.Errorf("research_reviewer_codex is not active")
	}

	record, err := f.store.getUpload(tenantKey(ctx), strings.TrimSpace(request.UploadID))
	if err != nil {
		return nil, err
	}

	mode, err := normalizeRequestedMode(request.Mode, defaultIfEmpty(record.Mode, f.defaultMode(ctx)))
	if err != nil {
		return nil, err
	}
	focus := strings.TrimSpace(request.Focus)
	if focus == "" {
		focus = strings.TrimSpace(record.FocusText)
	}

	record.Mode = mode
	record.FocusText = focus
	record.FocusKey = focusCacheKey(focus)
	record.Status = uploadStatusRunning
	record.ErrorMessage = ""
	record.ReviewID = ""

	fileRecord, err := f.store.getFileByHash(record.TenantID, record.FileHash)
	if err != nil {
		return nil, err
	}
	if fileRecord == nil {
		fileRecord = &fileCacheRecord{
			TenantID:         record.TenantID,
			FileHash:         record.FileHash,
			TelegramFileID:   record.TelegramFileID,
			TelegramUniqueID: record.TelegramUniqueID,
			OriginalFileName: record.OriginalFileName,
			SavedPDFPath:     record.SavedPDFPath,
			PaperID:          record.PaperID,
			FileSizeBytes:    record.FileSizeBytes,
		}
	}
	if strings.TrimSpace(fileRecord.SavedPDFPath) == "" {
		return nil, fmt.Errorf("saved_pdf_path is missing for upload %s", request.UploadID)
	}

	if err := f.store.updateUpload(record); err != nil {
		return nil, err
	}

	outcome, err := f.runReview(ctx, uploadProcessInput{
		Upload:       record,
		File:         fileRecord,
		UserID:       strings.TrimSpace(userID),
		ForceRefresh: request.ForceRefresh,
	})
	if err != nil {
		record.Status = uploadStatusFailed
		record.ErrorMessage = cleanUserFacingError(err)
		_ = f.store.updateUpload(record)
		return nil, err
	}
	return f.buildUploadResult(ctx, outcome.Upload, outcome.CachedPaper, outcome.CachedReview, outcome.Review)
}

func resolveCurrentPDFRef(ctx context.Context, mediaID string) (providers.MediaRef, error) {
	refs := tools.MediaDocRefsFromCtx(ctx)
	if len(refs) == 0 {
		return providers.MediaRef{}, fmt.Errorf("no PDF is attached to the current conversation")
	}

	if mediaID = strings.TrimSpace(mediaID); mediaID != "" {
		for _, ref := range refs {
			if ref.ID != mediaID {
				continue
			}
			if !mediaRefLooksLikePDF(ref) {
				return providers.MediaRef{}, fmt.Errorf("media_id %q is not a PDF document", mediaID)
			}
			if strings.TrimSpace(ref.Path) == "" {
				return providers.MediaRef{}, fmt.Errorf("media_id %q has no accessible local file path", mediaID)
			}
			return ref, nil
		}
		return providers.MediaRef{}, fmt.Errorf("media_id %q was not found in the current conversation", mediaID)
	}

	for i := len(refs) - 1; i >= 0; i-- {
		ref := refs[i]
		if !mediaRefLooksLikePDF(ref) {
			continue
		}
		if strings.TrimSpace(ref.Path) == "" {
			continue
		}
		return ref, nil
	}
	return providers.MediaRef{}, fmt.Errorf("no PDF with an accessible local path is available in the current conversation")
}

func mediaRefLooksLikePDF(ref providers.MediaRef) bool {
	if strings.EqualFold(strings.TrimSpace(ref.MimeType), "application/pdf") {
		return true
	}
	return strings.EqualFold(filepath.Ext(strings.TrimSpace(ref.Path)), ".pdf")
}

func (f *TelegramPDFAutoReviewFeature) buildUploadResult(ctx context.Context, record *uploadRecord, cachedPaper, cachedReview bool, review *researchreviewercodex.ReviewResultPayload) (*UploadResultPayload, error) {
	if record == nil {
		return nil, fmt.Errorf("upload record is required")
	}
	if review == nil && record.ReviewID != "" {
		if reviewer := researchreviewercodex.ActiveFeature(); reviewer != nil {
			if payload, err := reviewer.GetStoredReview(ctx, record.ReviewID); err == nil {
				review = payload
			}
		}
	}

	payload := &UploadResultPayload{
		UploadID:         record.ID,
		FileHash:         record.FileHash,
		OriginalFileName: record.OriginalFileName,
		SavedPDFPath:     record.SavedPDFPath,
		PaperID:          record.PaperID,
		ReviewID:         record.ReviewID,
		Mode:             record.Mode,
		Focus:            record.FocusText,
		Status:           record.Status,
		Error:            record.ErrorMessage,
		CachedPaper:      cachedPaper,
		CachedReview:     cachedReview,
		FileSizeBytes:    record.FileSizeBytes,
		CaptionText:      record.CaptionText,
		Review:           review,
		CreatedAt:        record.CreatedAt,
		UpdatedAt:        record.UpdatedAt,
	}
	if review != nil {
		if payload.PaperID == "" {
			payload.PaperID = review.Paper.PaperID
		}
		if payload.ReviewID == "" {
			payload.ReviewID = review.ReviewID
		}
		if payload.Status == "" {
			payload.Status = review.Status
		}
		if payload.Error == "" {
			payload.Error = review.Error
		}
	}
	return payload, nil
}

func (f *TelegramPDFAutoReviewFeature) maxFileSizeBytes(ctx context.Context) int64 {
	if raw := strings.TrimSpace(os.Getenv("GOCLAW_BETA_TELEGRAM_PDF_AUTO_REVIEW_MAX_BYTES")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	if raw := strings.TrimSpace(f.systemConfig(ctx, "beta."+featureName+".max_bytes")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultMaxPDFBytes
}

func (f *TelegramPDFAutoReviewFeature) defaultMode(ctx context.Context) string {
	if raw := strings.TrimSpace(os.Getenv("GOCLAW_BETA_TELEGRAM_PDF_AUTO_REVIEW_DEFAULT_MODE")); raw != "" {
		if mode := normalizeKnownMode(raw); mode != "" {
			return mode
		}
	}
	if raw := strings.TrimSpace(f.systemConfig(ctx, "beta."+featureName+".default_mode")); raw != "" {
		if mode := normalizeKnownMode(raw); mode != "" {
			return mode
		}
	}
	return "collaborative"
}

func (f *TelegramPDFAutoReviewFeature) systemConfig(ctx context.Context, key string) string {
	if f == nil || f.systemConfigs == nil {
		return ""
	}
	value, err := f.systemConfigs.Get(ctx, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func (f *TelegramPDFAutoReviewFeature) uploadEnabledForChannel(channel *telegramchannel.Channel) bool {
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

	spec := agent.ParseToolsConfig()
	return toolPolicyExplicitlyAllows(spec, (&reprocessTool{}).Name())
}

func (f *TelegramPDFAutoReviewFeature) savedPDFPath(ctx context.Context, fileHash string) string {
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		tenantID = store.MasterTenantID
	}
	tenantSlug := store.TenantSlugFromContext(ctx)
	root := f.resolvedStorageRoot(tenantID, tenantSlug)
	return filepath.Join(root, "papers", fileHash+".pdf")
}

func (f *TelegramPDFAutoReviewFeature) resolvedStorageRoot(tenantID uuid.UUID, tenantSlug string) string {
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
		if storageDirWritable(filepath.Join(candidate, "papers")) {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

func inheritFeatureContext(base, source context.Context, userID string) context.Context {
	if base == nil {
		base = context.Background()
	}
	tenantID := store.TenantIDFromContext(source)
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

func ensureStoredPDF(srcPath, dstPath string) error {
	if strings.TrimSpace(srcPath) == "" || strings.TrimSpace(dstPath) == "" {
		return fmt.Errorf("source and destination paths are required")
	}
	if info, err := os.Stat(dstPath); err == nil && info.Size() > 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}

	tmpPath := dstPath + ".tmp-" + uuid.NewString()
	if err := copyFile(srcPath, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func storageDirWritable(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}

	probePath := filepath.Join(dir, ".write-probe-"+uuid.NewString())
	if err := os.WriteFile(probePath, []byte("ok"), 0o600); err != nil {
		return false
	}
	_ = os.Remove(probePath)
	return true
}

func toolPolicyExplicitlyAllows(spec *config.ToolPolicySpec, toolName string) bool {
	if spec == nil {
		return false
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return false
	}
	return toolPolicyListContains(spec.Allow, toolName) || toolPolicyListContains(spec.AlsoAllow, toolName)
}

func toolPolicyListContains(values []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func copyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}

	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func parseCaptionOverrides(caption string) (string, string) {
	caption = strings.TrimSpace(caption)
	if caption == "" {
		return "", ""
	}

	mode := ""
	if matches := captionModeRe.FindStringSubmatch(caption); len(matches) >= 2 {
		mode = strings.TrimSpace(matches[1])
	}

	focus := ""
	for _, rawLine := range strings.Split(caption, "\n") {
		line := strings.TrimSpace(strings.TrimLeft(rawLine, "-*"))
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "focus="):
			focus = strings.TrimSpace(line[len("focus="):])
		case strings.HasPrefix(lower, "focus:"):
			focus = strings.TrimSpace(line[len("focus:"):])
		}
		if focus != "" {
			break
		}
	}
	if focus == "" {
		if matches := captionFocusRe.FindStringSubmatch(caption); len(matches) >= 2 {
			focus = strings.TrimSpace(matches[1])
		}
	}

	return mode, focus
}

func normalizeRequestedMode(value, fallback string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if normalized := normalizeKnownMode(fallback); normalized != "" {
			return normalized, nil
		}
		return "collaborative", nil
	}
	if normalized := normalizeKnownMode(value); normalized != "" {
		return normalized, nil
	}
	return "", fmt.Errorf("mode must be collaborative or harsh")
}

func normalizeKnownMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "collaborative", "constructive", "mentor":
		return "collaborative"
	case "harsh", "strict", "harsh reviewer":
		return "harsh"
	default:
		return ""
	}
}

func focusCacheKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func formatUploadResultForChat(payload *UploadResultPayload) string {
	if payload == nil {
		return "Research review finished."
	}

	var out strings.Builder
	if payload.Status == uploadStatusFailed || (payload.Review != nil && strings.EqualFold(payload.Review.Status, uploadStatusFailed)) {
		out.WriteString("Research review failed.\n\n")
	} else {
		out.WriteString("Research review ready.\n\n")
	}
	out.WriteString("Paper: ")
	out.WriteString(defaultIfEmpty(payload.resolveTitle(), displayFileName(payload.OriginalFileName)))
	out.WriteString("\nMode: ")
	out.WriteString(defaultIfEmpty(payload.Mode, "collaborative"))
	if payload.Focus != "" {
		out.WriteString("\nFocus: ")
		out.WriteString(payload.Focus)
	}
	if payload.CachedReview {
		out.WriteString("\nCache: reused prior completed review")
	} else if payload.CachedPaper {
		out.WriteString("\nCache: reused parsed paper")
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
	if payload.Review != nil && strings.TrimSpace(payload.Review.Report) != "" {
		out.WriteString("\n\n")
		out.WriteString(strings.TrimSpace(payload.Review.Report))
	}
	return strings.TrimSpace(out.String())
}

func (p *UploadResultPayload) resolveTitle() string {
	if p == nil || p.Review == nil {
		return ""
	}
	return strings.TrimSpace(p.Review.Paper.Title)
}

func cleanUserFacingError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 280 {
		text = text[:280] + "..."
	}
	return text
}

func displayFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "paper.pdf"
	}
	return name
}

func defaultIfEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func bytesToWholeMB(value int64) int64 {
	if value <= 0 {
		return 0
	}
	return (value + (1 << 20) - 1) >> 20
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

func isInputError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "is required") ||
		strings.Contains(text, "mode must") ||
		strings.Contains(text, "missing for upload")
}
