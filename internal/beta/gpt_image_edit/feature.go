package gptimageedit

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	featureName         = "gpt_image_edit"
	toolName            = "gpt_image_edit"
	commandName         = "/image_edit"
	defaultModel        = "gpt-image-2"
	defaultOpenAIBase   = "https://api.openai.com/v1"
	defaultOutputFormat = "png"

	maxPromptRunes = 32000
	maxImageBytes  = 50 << 20
)

// GPTImageEditFeature adds GPT Image edits for attached chat images.
//
// Plan:
// 1. Reuse the existing tool media-return path so edited images are attached to assistant chat replies.
// 2. Expose the same edit service through a scoped Telegram /image_edit command, RPC, and HTTP route.
// 3. Keep only lightweight run metadata in beta-local tables while storing output files in a probed feature cache.
type GPTImageEditFeature struct {
	cfg        *config.Config
	store      *featureStore
	agentStore store.AgentStore
	channelMgr *channels.Manager
	workspace  string
	dataDir    string

	apiKey  string
	apiBase string

	backgroundCtx context.Context
	cancel        context.CancelFunc
	workers       sync.WaitGroup
}

type EditRequest struct {
	Prompt       string `json:"prompt"`
	Operation    string `json:"operation,omitempty"`
	ImagePath    string `json:"image_path,omitempty"`
	ImageBase64  string `json:"image_base64,omitempty"`
	ImageMIME    string `json:"image_mime,omitempty"`
	OutputFormat string `json:"output_format,omitempty"`
	Size         string `json:"size,omitempty"`
	Quality      string `json:"quality,omitempty"`
	Source       string `json:"source,omitempty"`
	Channel      string `json:"channel,omitempty"`
	ChatID       string `json:"chat_id,omitempty"`
}

type EditPayload struct {
	RunID        string         `json:"run_id"`
	Model        string         `json:"model"`
	Provider     string         `json:"provider"`
	Operation    string         `json:"operation"`
	OutputPath   string         `json:"output_path,omitempty"`
	OutputMIME   string         `json:"output_mime"`
	OutputBase64 string         `json:"output_base64,omitempty"`
	OutputBytes  int64          `json:"output_bytes"`
	LatencyMS    int64          `json:"latency_ms"`
	Usage        map[string]any `json:"usage,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

type imageInput struct {
	Data     []byte
	MIME     string
	FileName string
	Source   string
	Size     int64
}

func (f *GPTImageEditFeature) Name() string { return featureName }

func (f *GPTImageEditFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	f.cfg = deps.Config
	f.store = &featureStore{db: deps.Stores.DB}
	f.agentStore = deps.Stores.Agents
	f.channelMgr = deps.ChannelManager
	f.workspace = deps.Workspace
	f.dataDir = deps.DataDir
	f.apiKey = resolveOpenAIAPIKey(deps.Config)
	f.apiBase = resolveOpenAIBase(deps.Config)
	f.backgroundCtx, f.cancel = context.WithCancel(context.Background())

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	topicrouting.RegisterTopicFeatureTools(featureName, toolName)

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&editTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}
	if deps.ChannelManager != nil {
		telegramchannel.RegisterDynamicCommand(&imageEditCommand{feature: f})
		f.syncTelegramMenus()
	}

	slog.Info("beta GPT image edit initialized", "model", defaultModel)
	return nil
}

func (f *GPTImageEditFeature) Shutdown(ctx context.Context) error {
	telegramchannel.UnregisterDynamicCommand(commandName)
	topicrouting.UnregisterTopicFeatureTools(featureName)
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

func (f *GPTImageEditFeature) edit(ctx context.Context, request EditRequest, includeBase64 bool) (*EditPayload, error) {
	if f == nil {
		return nil, fmt.Errorf("%s is unavailable", featureName)
	}

	normalized, err := normalizeEditRequest(request)
	if err != nil {
		return nil, err
	}
	input, err := f.resolveImageInput(ctx, normalized)
	if err != nil {
		return nil, err
	}

	tenantID := tenantKeyFromCtx(ctx)
	start := time.Now()
	runID := uuid.NewString()
	output, err := f.callOpenAIEdit(ctx, input, normalized)
	latency := time.Since(start)
	if err != nil {
		f.persistRunBestEffort(&runRecord{
			ID:           runID,
			TenantID:     tenantID,
			Prompt:       normalized.Prompt,
			Operation:    normalized.Operation,
			InputSource:  input.Source,
			InputMIME:    input.MIME,
			InputBytes:   input.Size,
			OutputFormat: normalized.OutputFormat,
			Status:       runStatusFailed,
			ErrorMessage: trimForStorage(err.Error(), 1200),
			LatencyMS:    latency.Milliseconds(),
			CreatedAt:    start.UTC(),
		})
		return nil, err
	}

	outputPath, err := f.saveOutputImage(ctx, output.Data, normalized.OutputFormat)
	if err != nil {
		return nil, err
	}

	payload := &EditPayload{
		RunID:       runID,
		Model:       defaultModel,
		Provider:    "openai",
		Operation:   normalized.Operation,
		OutputPath:  outputPath,
		OutputMIME:  outputMime(normalized.OutputFormat),
		OutputBytes: int64(len(output.Data)),
		LatencyMS:   latency.Milliseconds(),
		Usage:       output.Usage,
		CreatedAt:   start.UTC(),
	}
	if includeBase64 {
		payload.OutputBase64 = encodeBase64(output.Data)
	}

	f.persistRunBestEffort(&runRecord{
		ID:           runID,
		TenantID:     tenantID,
		Prompt:       normalized.Prompt,
		Operation:    normalized.Operation,
		InputSource:  input.Source,
		InputMIME:    input.MIME,
		InputBytes:   input.Size,
		OutputPath:   outputPath,
		OutputMIME:   payload.OutputMIME,
		OutputBytes:  payload.OutputBytes,
		OutputFormat: normalized.OutputFormat,
		Status:       runStatusCompleted,
		LatencyMS:    payload.LatencyMS,
		CreatedAt:    payload.CreatedAt,
	})

	return payload, nil
}

func (f *GPTImageEditFeature) persistRunBestEffort(record *runRecord) {
	if f == nil || f.store == nil || record == nil {
		return
	}
	if err := f.store.insertRun(record); err != nil {
		slog.Warn("beta GPT image edit run persist failed", "error", err)
	}
}

func (f *GPTImageEditFeature) saveOutputImage(ctx context.Context, data []byte, format string) (string, error) {
	root := f.resolvedStorageRoot(ctx)
	dir := filepath.Join(root, "outputs", time.Now().UTC().Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	path := filepath.Join(dir, "image-edit-"+uuid.NewString()+"."+outputExtension(format))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write edited image: %w", err)
	}
	return path, nil
}

func (f *GPTImageEditFeature) resolvedStorageRoot(ctx context.Context) string {
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		tenantID = store.MasterTenantID
	}
	tenantSlug := store.TenantSlugFromContext(ctx)
	if tenantSlug == "" {
		tenantSlug = tenantID.String()
	}

	candidates := make([]string, 0, 3)
	if root := strings.TrimSpace(config.ExpandHome(f.dataDir)); root != "" {
		candidates = append(candidates, filepath.Join(config.TenantDataDir(root, tenantID, tenantSlug), "beta_cache", featureName))
	}
	if root := strings.TrimSpace(f.workspace); root != "" {
		candidates = append(candidates, filepath.Join(config.TenantWorkspace(root, tenantID, tenantSlug), "beta_cache", featureName))
	}
	candidates = append(candidates, filepath.Join(os.TempDir(), "goclaw", "beta_cache", featureName, tenantID.String()))

	for _, candidate := range candidates {
		if storageDirWritable(filepath.Join(candidate, "outputs")) {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

func (f *GPTImageEditFeature) syncTelegramMenus() {
	if f == nil || f.channelMgr == nil {
		return
	}
	for _, name := range f.channelMgr.GetEnabledChannels() {
		rawChannel, ok := f.channelMgr.GetChannel(name)
		if !ok || rawChannel.Type() != channels.TypeTelegram {
			continue
		}
		channel, ok := rawChannel.(*telegramchannel.Channel)
		if !ok {
			continue
		}
		tg, ok := rawChannel.(interface {
			SyncMenuCommands(ctx context.Context, commands []telego.BotCommand) error
		})
		if !ok {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := tg.SyncMenuCommands(ctx, telegramchannel.DefaultMenuCommandsForChannel(channel)); err != nil {
			slog.Warn("beta GPT image edit menu sync failed", "channel", name, "error", err)
		}
		cancel()
	}
}

func inheritFeatureContext(base, source context.Context) context.Context {
	if base == nil {
		base = context.Background()
	}
	ctx := base
	if tenantID := store.TenantIDFromContext(source); tenantID != uuid.Nil {
		ctx = store.WithTenantID(ctx, tenantID)
	}
	if tenantSlug := store.TenantSlugFromContext(source); tenantSlug != "" {
		ctx = store.WithTenantSlug(ctx, tenantSlug)
	}
	return ctx
}

func sendEditedImage(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext, payload *EditPayload) error {
	if channel == nil || payload == nil || payload.OutputPath == "" {
		return nil
	}
	meta := map[string]string{}
	if cmdCtx.LocalKey != "" && cmdCtx.LocalKey != cmdCtx.ChatIDStr {
		meta["local_key"] = cmdCtx.LocalKey
	}
	if cmdCtx.Message != nil && cmdCtx.Message.MessageID > 0 {
		meta["reply_to_message_id"] = fmt.Sprintf("%d", cmdCtx.Message.MessageID)
	}
	if cmdCtx.MessageThreadID > 0 {
		meta["message_thread_id"] = fmt.Sprintf("%d", cmdCtx.MessageThreadID)
	}
	return channel.Send(ctx, bus.OutboundMessage{
		Channel: channel.Name(),
		ChatID:  cmdCtx.ChatIDStr,
		Content: "Edited image",
		Media: []bus.MediaAttachment{{
			URL:         payload.OutputPath,
			ContentType: payload.OutputMIME,
			Caption:     "Edited image",
		}},
		Metadata: meta,
	})
}
