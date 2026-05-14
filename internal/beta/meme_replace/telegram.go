package memereplace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type replaceCommand struct {
	feature *MemeReplaceFeature
}

type telegramImageRef struct {
	FileID   string
	MIME     string
	FileName string
	Size     int64
	Source   string
}

func (c *replaceCommand) Command() string { return commandName }

func (c *replaceCommand) Description() string {
	return "Replace meme green screen"
}

func (c *replaceCommand) EnabledForChannel(channel *telegramchannel.Channel) bool {
	if c == nil || c.feature == nil {
		return false
	}
	return c.feature.commandEnabledForChannel(channel)
}

func (c *replaceCommand) EnabledForContext(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if c == nil || c.feature == nil {
		return false
	}
	if channel == nil {
		return true
	}
	decision, err := topicrouting.ResolveTopicToolDecision(ctx, topicrouting.TopicToolScope{
		Channel:  channel.Name(),
		ChatID:   cmdCtx.ChatIDStr,
		ThreadID: cmdCtx.MessageThreadID,
		LocalKey: cmdCtx.LocalKey,
	})
	if err != nil {
		slog.Warn("meme replace topic routing check failed", "error", err, "channel", channel.Name(), "chat_id", cmdCtx.ChatIDStr)
		return true
	}
	if decision == nil || !decision.Matched {
		return true
	}
	return featureListContains(decision.EnabledFeatures, featureName)
}

func (c *replaceCommand) Handle(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if c == nil || c.feature == nil || channel == nil {
		return false
	}

	ref, err := imageRefFromTelegramMessage(cmdCtx.Message)
	if err != nil {
		cmdCtx.Reply(ctx, "Usage: /meme_replace as the caption of an image, or reply to an image with /meme_replace.")
		return true
	}
	if ref.Size > maxInputImageSize {
		cmdCtx.Reply(ctx, fmt.Sprintf("That image is too large for meme replacement. Max size is %d MB.", maxInputImageSize>>20))
		return true
	}
	downloadLimit := int64(maxInputImageSize)
	if telegramLimit := channel.MediaDownloadMaxBytes(); telegramLimit > 0 && telegramLimit < downloadLimit {
		downloadLimit = telegramLimit
	}
	if ref.Size > downloadLimit {
		cmdCtx.Reply(ctx, fmt.Sprintf("That image is too large to download from Telegram. Max size is %d MB.", downloadLimit>>20))
		return true
	}

	cmdCtx.Reply(ctx, "Replacing meme green screen...")

	c.feature.workers.Add(1)
	go func() {
		defer c.feature.workers.Done()

		runCtx := inheritFeatureContext(c.feature.backgroundCtx, ctx)
		tempPath, err := channel.DownloadMediaByFileID(runCtx, ref.FileID, downloadLimit)
		if err != nil {
			cmdCtx.Reply(runCtx, "I received the image but could not download it from Telegram.")
			slog.Warn("meme replace Telegram download failed", "error", err, "chat_id", cmdCtx.ChatIDStr)
			return
		}
		defer os.Remove(tempPath)

		payload, err := c.feature.replace(runCtx, ReplaceRequest{
			ImagePath: tempPath,
			ImageMIME: ref.MIME,
			FitMode:   fitModeFromCommandText(cmdCtx.Text),
			Source:    ref.Source,
			Channel:   channel.Name(),
			ChatID:    cmdCtx.ChatIDStr,
		}, false)
		if err != nil {
			cmdCtx.Reply(runCtx, "I could not replace that meme: "+cleanUserFacingError(err))
			slog.Warn("meme replace Telegram command failed", "error", err, "chat_id", cmdCtx.ChatIDStr)
			return
		}
		if err := sendReplacedMeme(runCtx, channel, cmdCtx, payload); err != nil {
			cmdCtx.Reply(runCtx, "The meme was rendered, but I could not send it back to Telegram.")
			slog.Warn("meme replace Telegram send failed", "error", err, "chat_id", cmdCtx.ChatIDStr)
		}
	}()
	return true
}

func (f *MemeReplaceFeature) commandEnabledForChannel(channel *telegramchannel.Channel) bool {
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
	return toolPolicyExplicitlyAllows(agent.ParseToolsConfig(), toolName)
}

func imageRefFromTelegramMessage(message *telego.Message) (telegramImageRef, error) {
	if message == nil {
		return telegramImageRef{}, fmt.Errorf("attach an image or reply to one with /meme_replace")
	}
	if ref, ok := currentTelegramImageRef(message, "current_message"); ok {
		return ref, nil
	}
	if message.ReplyToMessage != nil {
		if ref, ok := currentTelegramImageRef(message.ReplyToMessage, "reply_to_message"); ok {
			return ref, nil
		}
	}
	return telegramImageRef{}, fmt.Errorf("attach a png, jpg, webp, gif, or static sticker, or reply to one with /meme_replace")
}

func currentTelegramImageRef(message *telego.Message, source string) (telegramImageRef, bool) {
	if message == nil {
		return telegramImageRef{}, false
	}
	if len(message.Photo) > 0 {
		photo := message.Photo[len(message.Photo)-1]
		return telegramImageRef{
			FileID:   photo.FileID,
			MIME:     "image/jpeg",
			FileName: "telegram-photo.jpg",
			Size:     int64(photo.FileSize),
			Source:   "telegram:" + source + ":photo",
		}, true
	}
	if message.Document != nil {
		mimeType := strings.TrimSpace(message.Document.MimeType)
		if !isSupportedImageMIME(strings.ToLower(strings.Split(mimeType, ";")[0])) {
			return telegramImageRef{}, false
		}
		return telegramImageRef{
			FileID:   message.Document.FileID,
			MIME:     mimeType,
			FileName: message.Document.FileName,
			Size:     int64(message.Document.FileSize),
			Source:   "telegram:" + source + ":document",
		}, true
	}
	if message.Sticker != nil && !message.Sticker.IsAnimated && !message.Sticker.IsVideo {
		return telegramImageRef{
			FileID:   message.Sticker.FileID,
			MIME:     "image/webp",
			FileName: "telegram-sticker.webp",
			Size:     int64(message.Sticker.FileSize),
			Source:   "telegram:" + source + ":sticker",
		}, true
	}
	return telegramImageRef{}, false
}

func fitModeFromCommandText(text string) string {
	text = strings.ToLower(text)
	if strings.Contains(text, " contain") || strings.Contains(text, " fit=contain") || strings.Contains(text, "fit_mode=contain") {
		return fitModeContain
	}
	return fitModeCover
}

func sendReplacedMeme(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext, payload *ReplacePayload) error {
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
		Content: "Meme replacement",
		Media: []bus.MediaAttachment{{
			URL:         payload.OutputPath,
			ContentType: payload.OutputMIME,
			Caption:     "Meme replacement",
		}},
		Metadata: meta,
	})
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
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func featureListContains(values []string, featureName string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(featureName)) {
			return true
		}
	}
	return false
}
