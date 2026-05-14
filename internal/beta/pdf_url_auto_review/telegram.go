package pdfurlautoreview

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/mymmrac/telego"

	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
)

type reviewURLCommand struct {
	feature *PDFURLAutoReviewFeature
}

type urlMessageHandler struct {
	feature *PDFURLAutoReviewFeature
}

func (c *reviewURLCommand) Command() string { return commandName }

func (c *reviewURLCommand) Description() string {
	return "Review a paper URL"
}

func (c *reviewURLCommand) EnabledForChannel(channel *telegramchannel.Channel) bool {
	return c != nil && c.feature != nil && c.feature.enabledForChannel(channel)
}

func (c *reviewURLCommand) EnabledForContext(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	return c != nil && c.feature != nil && c.feature.enabledForTopicContext(ctx, channel, cmdCtx.ChatIDStr, cmdCtx.MessageThreadID, cmdCtx.LocalKey)
}

func (c *reviewURLCommand) Handle(ctx context.Context, channel *telegramchannel.Channel, cmdCtx telegramchannel.DynamicCommandContext) bool {
	if c == nil || c.feature == nil || channel == nil {
		return false
	}
	request := parseURLReviewText(cmdCtx.Text)
	if strings.TrimSpace(request.URL) == "" {
		cmdCtx.Reply(ctx, "Usage: /review_url <arXiv, PDF, or web URL>")
		return true
	}

	c.feature.startTelegramReview(ctx, channel, telegramReviewRequest{
		Request:           request,
		ChatIDStr:         cmdCtx.ChatIDStr,
		LocalKey:          cmdCtx.LocalKey,
		UserID:            cmdCtx.SenderID,
		MessageThreadID:   cmdCtx.MessageThreadID,
		TelegramMessageID: telegramMessageID(cmdCtx.Message),
		Reply: func(replyCtx context.Context, text string) error {
			cmdCtx.Reply(replyCtx, text)
			return nil
		},
	})
	return true
}

func (h *urlMessageHandler) Name() string { return featureName }

func (h *urlMessageHandler) EnabledForChannel(channel *telegramchannel.Channel) bool {
	return h != nil && h.feature != nil && h.feature.enabledForChannel(channel)
}

func (h *urlMessageHandler) EnabledForContext(ctx context.Context, channel *telegramchannel.Channel, msgCtx telegramchannel.DynamicMessageContext) bool {
	return h != nil && h.feature != nil && h.feature.enabledForTopicContext(ctx, channel, msgCtx.ChatIDStr, msgCtx.MessageThreadID, msgCtx.LocalKey)
}

func (h *urlMessageHandler) MatchesMessage(_ context.Context, _ *telegramchannel.Channel, msgCtx telegramchannel.DynamicMessageContext) bool {
	if h == nil || h.feature == nil {
		return false
	}
	return extractFirstURL(msgCtx.Text) != ""
}

func (h *urlMessageHandler) HandleMessage(ctx context.Context, channel *telegramchannel.Channel, msgCtx telegramchannel.DynamicMessageContext) bool {
	if h == nil || h.feature == nil || channel == nil {
		return false
	}
	request := parseURLReviewText(msgCtx.Text)
	if strings.TrimSpace(request.URL) == "" {
		return false
	}
	h.feature.startTelegramReview(ctx, channel, telegramReviewRequest{
		Request:           request,
		ChatIDStr:         msgCtx.ChatIDStr,
		LocalKey:          msgCtx.LocalKey,
		UserID:            msgCtx.UserID,
		MessageThreadID:   msgCtx.MessageThreadID,
		TelegramMessageID: telegramMessageID(msgCtx.Message),
		Reply:             msgCtx.Reply,
	})
	return true
}

type telegramReviewRequest struct {
	Request           FetchReviewRequest
	ChatIDStr         string
	LocalKey          string
	UserID            string
	MessageThreadID   int
	TelegramMessageID string
	Reply             func(context.Context, string) error
}

func (f *PDFURLAutoReviewFeature) startTelegramReview(ctx context.Context, channel *telegramchannel.Channel, req telegramReviewRequest) {
	mode, err := normalizeReviewMode(req.Request.Mode, f.defaultMode(ctx))
	if err != nil {
		replyTelegram(ctx, req.Reply, "I can only review URLs with mode=collaborative or mode=harsh.")
		return
	}
	req.Request.Mode = mode

	ack := fmt.Sprintf("Fetching %s and starting a %s review.", req.Request.URL, mode)
	if strings.TrimSpace(req.Request.Focus) != "" {
		ack += " Focus: " + strings.TrimSpace(req.Request.Focus) + "."
	}
	replyTelegram(ctx, req.Reply, ack)

	f.workers.Add(1)
	go func() {
		defer f.workers.Done()

		runCtx := inheritFeatureContext(f.backgroundCtx, ctx, req.UserID, channel)
		payload, err := f.fetchAndReview(runCtx, req.UserID, req.Request, requestSource{
			Channel:           channel.Name(),
			ChatID:            req.ChatIDStr,
			LocalKey:          req.LocalKey,
			TelegramMessageID: req.TelegramMessageID,
		})
		if err != nil {
			replyTelegram(runCtx, req.Reply, "I couldn't review that URL: "+cleanUserFacingError(err))
			slog.Warn("PDF URL auto review failed", "error", err, "chat_id", req.ChatIDStr)
			return
		}
		replyTelegram(runCtx, req.Reply, formatFetchReviewForChat(payload))
	}()
}

func replyTelegram(ctx context.Context, reply func(context.Context, string) error, text string) {
	if reply == nil || strings.TrimSpace(text) == "" {
		return
	}
	if err := reply(ctx, text); err != nil {
		slog.Warn("PDF URL auto review reply failed", "error", err)
	}
}

func telegramMessageID(message *telego.Message) string {
	if message == nil || message.MessageID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", message.MessageID)
}
