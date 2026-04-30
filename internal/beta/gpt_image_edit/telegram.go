package gptimageedit

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type imageEditCommand struct {
	feature *GPTImageEditFeature
}

type telegramImageRef struct {
	FileID   string
	MIME     string
	FileName string
	Size     int64
	Source   string
}

func (c *imageEditCommand) Command() string { return commandName }

func (c *imageEditCommand) Description() string {
	return "Edit an attached image"
}

func (c *imageEditCommand) EnabledForChannel(channel *telegramchannel.Channel) bool {
	if c == nil || c.feature == nil {
		return false
	}
	return c.feature.commandEnabledForChannel(channel)
}

func (c *imageEditCommand) EnabledForContext(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
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
		slog.Warn("GPT image edit topic routing check failed", "error", err, "channel", channel.Name(), "chat_id", cmdCtx.ChatIDStr)
		return true
	}
	if decision == nil || !decision.Matched {
		return true
	}
	return featureListContains(decision.EnabledFeatures, featureName)
}

func (c *imageEditCommand) Handle(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if c == nil || c.feature == nil || channel == nil {
		return false
	}

	prompt := promptFromCommandText(cmdCtx.Text, cmdCtx.Command)
	if prompt == "" {
		cmdCtx.Reply(ctx, "Usage: /image_edit <edit instruction> as the caption of a png, jpg, or webp image, or reply to an image with that command.")
		return true
	}
	ref, err := imageRefFromTelegramMessage(cmdCtx.Message)
	if err != nil {
		cmdCtx.Reply(ctx, err.Error())
		return true
	}
	if ref.Size > maxImageBytes {
		cmdCtx.Reply(ctx, fmt.Sprintf("That image is too large for GPT Image editing. Max size is %d MB.", maxImageBytes>>20))
		return true
	}

	cmdCtx.Reply(ctx, "Editing image with "+defaultModel+"...")

	c.feature.workers.Add(1)
	go func() {
		defer c.feature.workers.Done()

		runCtx := inheritFeatureContext(c.feature.backgroundCtx, ctx)
		tempPath, err := channel.DownloadMediaByFileID(runCtx, ref.FileID, maxImageBytes)
		if err != nil {
			cmdCtx.Reply(runCtx, "I received the image but could not download it from Telegram.")
			slog.Warn("GPT image edit Telegram download failed", "error", err, "chat_id", cmdCtx.ChatIDStr)
			return
		}
		defer os.Remove(tempPath)

		payload, err := c.feature.edit(runCtx, EditRequest{
			Prompt:       prompt,
			Operation:    "auto",
			ImagePath:    tempPath,
			ImageMIME:    ref.MIME,
			OutputFormat: defaultOutputFormat,
			Source:       ref.Source,
			Channel:      channel.Name(),
			ChatID:       cmdCtx.ChatIDStr,
		}, false)
		if err != nil {
			cmdCtx.Reply(runCtx, "I could not edit that image: "+cleanUserFacingError(err))
			slog.Warn("GPT image edit Telegram command failed", "error", err, "chat_id", cmdCtx.ChatIDStr)
			return
		}
		if err := sendEditedImage(runCtx, channel, cmdCtx, payload); err != nil {
			cmdCtx.Reply(runCtx, "The image was edited, but I could not send it back to Telegram.")
			slog.Warn("GPT image edit Telegram send failed", "error", err, "chat_id", cmdCtx.ChatIDStr)
		}
	}()
	return true
}

func (f *GPTImageEditFeature) commandEnabledForChannel(channel *telegramchannel.Channel) bool {
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

func promptFromCommandText(text, command string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	first := strings.ToLower(strings.SplitN(fields[0], "@", 2)[0])
	if first != strings.ToLower(command) {
		return text
	}
	return strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
}

func imageRefFromTelegramMessage(message *telego.Message) (telegramImageRef, error) {
	if message == nil {
		return telegramImageRef{}, fmt.Errorf("attach an image or reply to an image with /image_edit")
	}
	if ref, ok := currentTelegramImageRef(message, "current_message"); ok {
		return ref, nil
	}
	if message.ReplyToMessage != nil {
		if ref, ok := currentTelegramImageRef(message.ReplyToMessage, "reply_to_message"); ok {
			return ref, nil
		}
	}
	return telegramImageRef{}, fmt.Errorf("attach a png, jpg, or webp image, or reply to one with /image_edit")
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
		if !isAllowedImageMIME(normalizeImageMIME(mimeType, message.Document.FileName, nil)) {
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
	return telegramImageRef{}, false
}

func cleanUserFacingError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len([]rune(msg)) > 220 {
		msg = string([]rune(msg)[:220]) + "..."
	}
	return msg
}

func toolPolicyExplicitlyAllows(spec *config.ToolPolicySpec, toolName string) bool {
	if spec == nil {
		return false
	}
	for _, value := range append(spec.Allow, spec.AlsoAllow...) {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(toolName)) {
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
