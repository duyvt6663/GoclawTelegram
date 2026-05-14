package memereplace

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	featureName       = "meme_replace"
	toolName          = "meme_replace"
	commandName       = "/meme_replace"
	templateID        = "single_green_screen_panel_v1"
	defaultOutputMIME = "image/png"
	defaultFitMode    = "cover"
	maxInputImageSize = 20 << 20
)

// MemeReplaceFeature adds one hardcoded green-screen meme replacement flow.
//
// Plan:
// 1. Keep the beta boundary narrow: one built-in template, one fixed green-screen region, one output format.
// 2. Reuse the same deterministic compositor from tool, Telegram command, RPC, and HTTP routes.
// 3. Persist only run metadata while storing rendered files in a probed feature-local cache.
type MemeReplaceFeature struct {
	store      *featureStore
	agentStore store.AgentStore
	channelMgr *channels.Manager
	workspace  string
	dataDir    string

	backgroundCtx context.Context
	cancel        context.CancelFunc
	workers       sync.WaitGroup
}

type ReplaceRequest struct {
	ImagePath   string `json:"image_path,omitempty"`
	ImageBase64 string `json:"image_base64,omitempty"`
	ImageMIME   string `json:"image_mime,omitempty"`
	FitMode     string `json:"fit_mode,omitempty"`
	Source      string `json:"source,omitempty"`
	Channel     string `json:"channel,omitempty"`
	ChatID      string `json:"chat_id,omitempty"`
}

type ReplacePayload struct {
	RunID        string    `json:"run_id"`
	TemplateID   string    `json:"template_id"`
	FitMode      string    `json:"fit_mode"`
	OutputPath   string    `json:"output_path,omitempty"`
	OutputMIME   string    `json:"output_mime"`
	OutputBase64 string    `json:"output_base64,omitempty"`
	OutputBytes  int64     `json:"output_bytes"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	Region       RectSpec  `json:"region"`
	LatencyMS    int64     `json:"latency_ms"`
	CreatedAt    time.Time `json:"created_at"`
}

func (f *MemeReplaceFeature) Name() string { return featureName }

func (f *MemeReplaceFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}

	f.store = &featureStore{db: deps.Stores.DB}
	f.workspace = deps.Workspace
	f.dataDir = deps.DataDir
	f.backgroundCtx, f.cancel = context.WithCancel(context.Background())
	if deps.Stores != nil {
		f.agentStore = deps.Stores.Agents
	}
	f.channelMgr = deps.ChannelManager

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	topicrouting.RegisterTopicFeatureTools(featureName, toolName)

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&replaceTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}
	if deps.ChannelManager != nil {
		telegramchannel.RegisterDynamicCommand(&replaceCommand{feature: f})
		f.syncTelegramMenus()
	}

	slog.Info("beta meme replace initialized", "template", templateID)
	return nil
}

func (f *MemeReplaceFeature) Shutdown(ctx context.Context) error {
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

func (f *MemeReplaceFeature) replace(ctx context.Context, request ReplaceRequest, includeBase64 bool) (*ReplacePayload, error) {
	if f == nil {
		return nil, fmt.Errorf("%s is unavailable", featureName)
	}

	normalized, err := normalizeReplaceRequest(request)
	if err != nil {
		return nil, err
	}
	input, err := f.resolveImageInput(ctx, normalized)
	if err != nil {
		return nil, err
	}

	runID := newRunID()
	start := time.Now()
	rendered, stats, err := renderReplacement(input.Image, normalized.FitMode)
	latency := time.Since(start)
	tenantID := tenantKeyFromCtx(ctx)
	if err != nil {
		f.persistRunBestEffort(&runRecord{
			ID:           runID,
			TenantID:     tenantID,
			TemplateID:   templateID,
			FitMode:      normalized.FitMode,
			InputSource:  input.Source,
			InputMIME:    input.MIME,
			InputBytes:   input.Size,
			Status:       runStatusFailed,
			ErrorMessage: trimForStorage(err.Error(), 1200),
			LatencyMS:    latency.Milliseconds(),
			CreatedAt:    start.UTC(),
		})
		return nil, err
	}

	outputPath, err := f.saveOutput(ctx, rendered)
	if err != nil {
		return nil, err
	}

	payload := &ReplacePayload{
		RunID:       runID,
		TemplateID:  templateID,
		FitMode:     normalized.FitMode,
		OutputPath:  outputPath,
		OutputMIME:  defaultOutputMIME,
		OutputBytes: int64(len(rendered)),
		Width:       stats.Width,
		Height:      stats.Height,
		Region:      stats.Region,
		LatencyMS:   latency.Milliseconds(),
		CreatedAt:   start.UTC(),
	}
	if includeBase64 {
		payload.OutputBase64 = base64.StdEncoding.EncodeToString(rendered)
	}

	f.persistRunBestEffort(&runRecord{
		ID:          runID,
		TenantID:    tenantID,
		TemplateID:  templateID,
		FitMode:     normalized.FitMode,
		InputSource: input.Source,
		InputMIME:   input.MIME,
		InputBytes:  input.Size,
		OutputPath:  outputPath,
		OutputMIME:  payload.OutputMIME,
		OutputBytes: payload.OutputBytes,
		Status:      runStatusCompleted,
		LatencyMS:   payload.LatencyMS,
		CreatedAt:   payload.CreatedAt,
	})

	return payload, nil
}

func (f *MemeReplaceFeature) persistRunBestEffort(record *runRecord) {
	if f == nil || f.store == nil || record == nil {
		return
	}
	if err := f.store.insertRun(record); err != nil {
		slog.Warn("beta meme replace run persist failed", "error", err)
	}
}

func (f *MemeReplaceFeature) syncTelegramMenus() {
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
			slog.Warn("beta meme replace menu sync failed", "channel", name, "error", err)
		}
		cancel()
	}
}
