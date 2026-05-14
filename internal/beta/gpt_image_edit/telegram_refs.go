package gptimageedit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
)

var imageRefLabelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

type imageRefCommand struct {
	feature *GPTImageEditFeature
}

type imageRefsCommand struct {
	feature *GPTImageEditFeature
}

type imageGenCommand struct {
	feature *GPTImageEditFeature
}

func (c *imageRefCommand) Command() string { return imageRefCommandName }

func (c *imageRefCommand) Description() string {
	return "Save an image ref"
}

func (c *imageRefCommand) EnabledForChannel(channel *telegramchannel.Channel) bool {
	return telegramImageFeatureEnabledForChannel(c.feature, channel)
}

func (c *imageRefCommand) EnabledForContext(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	return telegramImageFeatureEnabledForContext(ctx, c.feature, channel, cmdCtx)
}

func (c *imageRefCommand) Handle(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if c == nil || c.feature == nil || c.feature.store == nil || channel == nil {
		return false
	}

	args := parseImageRefCommandArgs(promptFromCommandText(cmdCtx.Text, cmdCtx.Command))
	label := args.label
	if label == "" {
		cmdCtx.Reply(ctx, "Usage: reply to an image/sticker with /image_ref <name>\nFor profile photos: reply to a user with /image_ref avatar <name>")
		return true
	}
	if !validImageRefLabel(label) {
		cmdCtx.Reply(ctx, "Image ref names must be 1-32 chars: lowercase letters, numbers, underscore, or dash.\nExample: /image_ref face")
		return true
	}

	ref, err := c.resolveImageReference(ctx, channel, cmdCtx, args.profilePhoto)
	if err != nil {
		cmdCtx.Reply(ctx, err.Error())
		return true
	}
	if ref.Size > maxImageBytes {
		cmdCtx.Reply(ctx, fmt.Sprintf("That image is too large for GPT Image editing. Max size is %d MB.", maxImageBytes>>20))
		return true
	}

	scope := telegramImageRefScope(ctx, channel, cmdCtx)
	if err := c.feature.store.upsertImageRef(&imageReference{
		TenantID: scope.tenantID,
		ChatID:   scope.chatID,
		ThreadID: scope.threadID,
		OwnerID:  cmdCtx.SenderID,
		Label:    label,
		FileID:   ref.FileID,
		MIME:     ref.MIME,
		FileName: ref.FileName,
		FileSize: ref.Size,
		Source:   ref.Source,
	}); err != nil {
		cmdCtx.Reply(ctx, "I could not save that image reference.")
		slog.Warn("GPT image edit Telegram ref save failed", "error", err, "chat_id", cmdCtx.ChatIDStr, "label", label)
		return true
	}

	cmdCtx.Reply(ctx, fmt.Sprintf("Saved image ref %q for this topic.\nUse: /image_gen using %s: <prompt>", label, label))
	return true
}

func (c *imageRefCommand) resolveImageReference(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext, forceProfilePhoto bool) (telegramImageRef, error) {
	if forceProfilePhoto {
		return profilePhotoRefFromTelegramMessage(ctx, channel, cmdCtx.Message)
	}
	ref, err := imageRefFromTelegramMessage(cmdCtx.Message)
	if err == nil {
		return ref, nil
	}
	if user := profilePhotoUserFromTelegramMessage(cmdCtx.Message); user != nil {
		return profilePhotoRefFromUser(ctx, channel, user)
	}
	return telegramImageRef{}, err
}

func (c *imageRefsCommand) Command() string { return imageRefsCommandName }

func (c *imageRefsCommand) Description() string {
	return "List image refs"
}

func (c *imageRefsCommand) EnabledForChannel(channel *telegramchannel.Channel) bool {
	return telegramImageFeatureEnabledForChannel(c.feature, channel)
}

func (c *imageRefsCommand) EnabledForContext(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	return telegramImageFeatureEnabledForContext(ctx, c.feature, channel, cmdCtx)
}

func (c *imageRefsCommand) Handle(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if c == nil || c.feature == nil || c.feature.store == nil || channel == nil {
		return false
	}

	scope := telegramImageRefScope(ctx, channel, cmdCtx)
	action := strings.ToLower(strings.TrimSpace(promptFromCommandText(cmdCtx.Text, cmdCtx.Command)))
	if action == "clear" {
		if err := c.feature.store.clearImageRefs(scope.tenantID, scope.chatID, scope.threadID); err != nil {
			cmdCtx.Reply(ctx, "I could not clear image refs for this topic.")
			slog.Warn("GPT image edit Telegram refs clear failed", "error", err, "chat_id", cmdCtx.ChatIDStr)
			return true
		}
		cmdCtx.Reply(ctx, "Cleared image refs for this topic.")
		return true
	}

	refs, err := c.feature.store.listImageRefs(scope.tenantID, scope.chatID, scope.threadID, maxInputImages)
	if err != nil {
		cmdCtx.Reply(ctx, "I could not list image refs for this topic.")
		slog.Warn("GPT image edit Telegram refs list failed", "error", err, "chat_id", cmdCtx.ChatIDStr)
		return true
	}
	if len(refs) == 0 {
		cmdCtx.Reply(ctx, "No image refs saved for this topic yet.\nReply to an image with /image_ref face, or reply to a user with /image_ref avatar face")
		return true
	}
	cmdCtx.Reply(ctx, formatImageRefList(refs))
	return true
}

func (c *imageGenCommand) Command() string { return imageGenCommandName }

func (c *imageGenCommand) Description() string {
	return "Generate from image refs"
}

func (c *imageGenCommand) EnabledForChannel(channel *telegramchannel.Channel) bool {
	return telegramImageFeatureEnabledForChannel(c.feature, channel)
}

func (c *imageGenCommand) EnabledForContext(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	return telegramImageFeatureEnabledForContext(ctx, c.feature, channel, cmdCtx)
}

func (c *imageGenCommand) Handle(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if c == nil || c.feature == nil || c.feature.store == nil || channel == nil {
		return false
	}

	text := promptFromCommandText(cmdCtx.Text, cmdCtx.Command)
	labels, prompt, ok := parseImageGenerationPrompt(text)
	if !ok {
		cmdCtx.Reply(ctx, "Usage: /image_gen using face,hair_style: <prompt>\nYou can also attach/reply to an image or sticker and run /image_gen <prompt>.")
		return true
	}
	extraRefs := imageGenMessageRefs(cmdCtx.Message)
	if len(labels) == 0 && len(extraRefs) == 0 {
		cmdCtx.Reply(ctx, "Attach/reply to an image or sticker, or use saved refs: /image_gen using face,hair_style: <prompt>")
		return true
	}
	c.handleGenerateFromRefs(ctx, channel, cmdCtx, labels, extraRefs, prompt)
	return true
}

func (c *imageGenCommand) handleGenerateFromRefs(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext, labels []string, extraRefs []telegramImageRef, prompt string) {
	scope := telegramImageRefScope(ctx, channel, cmdCtx)
	refs, err := c.feature.store.getImageRefsByLabels(scope.tenantID, scope.chatID, scope.threadID, labels)
	if errors.Is(err, sql.ErrNoRows) {
		cmdCtx.Reply(ctx, "One or more image refs were not found. Run /image_refs to see saved refs for this topic.")
		return
	}
	if err != nil {
		cmdCtx.Reply(ctx, "I could not load image refs for this topic.")
		slog.Warn("GPT image edit Telegram refs load failed", "error", err, "chat_id", cmdCtx.ChatIDStr)
		return
	}
	for i, ref := range extraRefs {
		refs = append(refs, imageReference{
			TenantID: scope.tenantID,
			ChatID:   scope.chatID,
			ThreadID: scope.threadID,
			Label:    fmt.Sprintf("telegram_media_%d", i+1),
			FileID:   ref.FileID,
			MIME:     ref.MIME,
			FileName: ref.FileName,
			FileSize: ref.Size,
			Source:   ref.Source,
		})
	}
	if len(refs) == 0 {
		cmdCtx.Reply(ctx, "No image refs selected. Use saved refs or attach/reply to an image or sticker.")
		return
	}
	if len(refs) > maxInputImages {
		cmdCtx.Reply(ctx, fmt.Sprintf("Too many image refs selected. Max is %d.", maxInputImages))
		return
	}

	cmdCtx.Reply(ctx, fmt.Sprintf("Generating image with %s from %d reference image(s)...", defaultModel, len(refs)))

	c.feature.workers.Add(1)
	go func() {
		defer c.feature.workers.Done()

		runCtx := inheritFeatureContext(c.feature.backgroundCtx, ctx)
		paths, mimes, cleanup, err := downloadTelegramImageRefs(runCtx, channel, refs)
		defer cleanup()
		if err != nil {
			cmdCtx.Reply(runCtx, "I received the image refs but could not download them from Telegram: "+cleanUserFacingError(err))
			slog.Warn("GPT image edit Telegram refs download failed", "error", err, "chat_id", cmdCtx.ChatIDStr, "refs", labels)
			return
		}

		payload, err := c.feature.edit(runCtx, EditRequest{
			Prompt:       prompt,
			Operation:    "auto",
			ImagePaths:   paths,
			ImageMIMEs:   mimes,
			OutputFormat: defaultOutputFormat,
			Source:       "telegram:refs:" + strings.Join(labels, ","),
			Channel:      channel.Name(),
			ChatID:       cmdCtx.ChatIDStr,
		}, false)
		if err != nil {
			cmdCtx.Reply(runCtx, "I could not generate that image: "+cleanUserFacingError(err))
			slog.Warn("GPT image edit Telegram refs generation failed", "error", err, "chat_id", cmdCtx.ChatIDStr, "refs", labels)
			return
		}
		if err := sendEditedImage(runCtx, channel, cmdCtx, payload); err != nil {
			cmdCtx.Reply(runCtx, "The image was generated, but I could not send it back to Telegram.")
			slog.Warn("GPT image edit Telegram refs send failed", "error", err, "chat_id", cmdCtx.ChatIDStr)
		}
	}()
}

type telegramRefScope struct {
	tenantID string
	chatID   string
	threadID int
}

func telegramImageRefScope(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) telegramRefScope {
	tenantID := tenantKeyFromCtx(ctx)
	if channel != nil {
		if channelTenantID := channel.TenantID(); channelTenantID != uuid.Nil {
			tenantID = channelTenantID.String()
		}
	}
	return telegramRefScope{
		tenantID: tenantID,
		chatID:   cmdCtx.ChatIDStr,
		threadID: cmdCtx.MessageThreadID,
	}
}

func telegramImageFeatureEnabledForChannel(feature *GPTImageEditFeature, channel *telegramchannel.Channel) bool {
	return feature != nil && feature.commandEnabledForChannel(channel)
}

func telegramImageFeatureEnabledForContext(ctx context.Context, feature *GPTImageEditFeature, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if feature == nil {
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

func parseImageRefGenerationPrompt(text string) ([]string, string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, "", false
	}
	lower := strings.ToLower(text)
	if !strings.HasPrefix(lower, "using ") {
		return nil, "", false
	}
	rest := strings.TrimSpace(text[len("using "):])
	separator := strings.Index(rest, ":")
	if separator < 0 {
		return nil, "", false
	}
	labels := normalizeImageRefLabels(strings.Split(rest[:separator], ","))
	prompt := strings.TrimSpace(rest[separator+1:])
	if len(labels) == 0 || prompt == "" || len(labels) > maxInputImages {
		return nil, "", false
	}
	return labels, prompt, true
}

func parseImageGenerationPrompt(text string) ([]string, string, bool) {
	labels, prompt, ok := parseImageRefGenerationPrompt(text)
	if ok {
		return labels, prompt, true
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, "", false
	}
	return nil, text, true
}

func imageGenMessageRefs(message *telego.Message) []telegramImageRef {
	ref, err := imageRefFromTelegramMessage(message)
	if err != nil {
		return nil
	}
	return []telegramImageRef{ref}
}

type imageRefCommandArgs struct {
	label        string
	profilePhoto bool
}

func parseImageRefCommandArgs(text string) imageRefCommandArgs {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) >= 2 {
		switch strings.ToLower(fields[0]) {
		case "avatar", "profile", "pfp":
			return imageRefCommandArgs{
				label:        normalizeImageRefLabel(fields[1]),
				profilePhoto: true,
			}
		}
	}
	return imageRefCommandArgs{label: normalizeImageRefLabel(text)}
}

func profilePhotoRefFromTelegramMessage(ctx context.Context, channel *telegramchannel.Channel, message *telego.Message) (telegramImageRef, error) {
	user := profilePhotoUserFromTelegramMessage(message)
	if user == nil {
		return telegramImageRef{}, fmt.Errorf("reply to a user's message with /image_ref avatar <name>")
	}
	return profilePhotoRefFromUser(ctx, channel, user)
}

func profilePhotoUserFromTelegramMessage(message *telego.Message) *telego.User {
	if message == nil {
		return nil
	}
	if message.ReplyToMessage != nil && message.ReplyToMessage.From != nil && !message.ReplyToMessage.From.IsBot {
		return message.ReplyToMessage.From
	}
	if message.From != nil && !message.From.IsBot {
		return message.From
	}
	return nil
}

func profilePhotoRefFromUser(ctx context.Context, channel *telegramchannel.Channel, user *telego.User) (telegramImageRef, error) {
	if channel == nil {
		return telegramImageRef{}, fmt.Errorf("telegram channel is unavailable")
	}
	if user == nil || user.ID == 0 {
		return telegramImageRef{}, fmt.Errorf("target user is unavailable")
	}
	profileRef, err := channel.LatestUserProfilePhotoRef(ctx, user.ID)
	if err != nil {
		return telegramImageRef{}, fmt.Errorf("could not read that user's profile photo: %w", err)
	}
	return telegramImageRef{
		FileID:   profileRef.FileID,
		MIME:     "image/jpeg",
		FileName: "telegram-profile-photo.jpg",
		Size:     profileRef.FileSize,
		Source:   fmt.Sprintf("telegram:profile_photo:user:%d", user.ID),
	}, nil
}

func normalizeImageRefLabels(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		label := normalizeImageRefLabel(value)
		if label == "" || !validImageRefLabel(label) {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	return out
}

func normalizeImageRefLabel(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "#")
	value = strings.Trim(value, " ,;:")
	return value
}

func validImageRefLabel(label string) bool {
	return imageRefLabelPattern.MatchString(label)
}

func formatImageRefList(refs []imageReference) string {
	var b strings.Builder
	b.WriteString("Image refs for this topic:\n")
	for _, ref := range refs {
		fmt.Fprintf(&b, "- %s", ref.Label)
		if ref.FileName != "" {
			fmt.Fprintf(&b, " (%s)", ref.FileName)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nUse: /image_gen using ")
	for i, ref := range refs {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(ref.Label)
		if i == 2 {
			break
		}
	}
	b.WriteString(": <prompt>")
	return b.String()
}

func downloadTelegramImageRefs(ctx context.Context, channel *telegramchannel.Channel, refs []imageReference) ([]string, []string, func(), error) {
	cleanupPaths := make([]string, 0, len(refs))
	cleanup := func() {
		for _, path := range cleanupPaths {
			_ = os.Remove(path)
		}
	}
	if channel == nil {
		return nil, nil, cleanup, fmt.Errorf("telegram channel is unavailable")
	}
	paths := make([]string, 0, len(refs))
	mimes := make([]string, 0, len(refs))
	downloadLimit := int64(maxImageBytes)
	if telegramLimit := channel.MediaDownloadMaxBytes(); telegramLimit > 0 && telegramLimit < downloadLimit {
		downloadLimit = telegramLimit
	}
	for _, ref := range refs {
		if ref.FileSize > downloadLimit {
			return nil, nil, cleanup, fmt.Errorf("%s: too large to download from Telegram (%d MB max)", ref.Label, downloadLimit>>20)
		}
		path, err := channel.DownloadMediaByFileID(ctx, ref.FileID, downloadLimit)
		if err != nil {
			return nil, nil, cleanup, fmt.Errorf("%s: %w", ref.Label, err)
		}
		cleanupPaths = append(cleanupPaths, path)
		paths = append(paths, path)
		mimes = append(mimes, ref.MIME)
	}
	return paths, mimes, cleanup, nil
}
