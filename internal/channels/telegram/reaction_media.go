package telegram

import (
	"hash/fnv"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
)

const (
	stickerReactionBaseRatePct = 20
	commentReactionHitRatePct  = 20
	maxReactionRatePct         = 100
)

var reactionWorthCommentKeywords = map[string]bool{
	"cc":      true,
	"clm":     true,
	"damn":    true,
	"dcm":     true,
	"dm":      true,
	"dmm":     true,
	"dumb":    true,
	"fuck":    true,
	"fucking": true,
	"idiot":   true,
	"ngu":     true,
	"shit":    true,
	"stupid":  true,
	"vcl":     true,
	"vl":      true,
	"vkl":     true,
	"wtf":     true,
	"cặc":     true,
	"lz":      true,
	"sủa":     true,
	"vãi":     true,
}

const implicitReactionMediaSystemPrompt = "An unmentioned sticker, GIF, meme, media post, or reaction-worthy comment just arrived in the group.\n" +
	"- Treat it as an implicit reaction cue only if it clearly reads like a sticker, meme, reaction image, GIF, short reaction clip, or a spicy/banter-heavy comment that deserves a comeback.\n" +
	"- Inspect the actual media before replying when media is present. Use read_image/read_video when needed. Do not guess from filenames or sticker metadata alone.\n" +
	"- Reply to the mood, intention, or subtext behind the media, not just a literal description.\n" +
	"- When you call a reaction-media tool, formulate the query as the reaction you want to send back, not as a description of the incoming media.\n" +
	"- Convert the incoming meme into a comeback vibe or reply intent such as 'not impressed', 'dead inside', 'caught in 4k', 'bro please', 'evil grin', or 'applause', instead of describing objects or characters you just saw.\n" +
	"- For this kind of turn, override the usual text-first rule: do not default to a plain-text reaction when a matching media reaction is available.\n" +
	"- Your first attempt should be a reaction-media tool, in this order when available: `find_and_post_local_sticker`, then `find_and_post_local_meme`, then `find_and_post_meme`.\n" +
	"- Only fall back to a text-only reply if those reaction-media tools are unavailable or you cannot find a fitting match after trying.\n" +
	"- Let the media be the main reply. Add at most one short caption line if needed.\n" +
	"- Keep it brief and natural.\n" +
	"- If no fitting reaction media exists, a very short text reply is acceptable.\n" +
	"- If the media looks like an ordinary photo/file rather than a reaction post, reply with NO_REPLY."

func shouldReplyToReactionMediaWithoutMention(message *telego.Message) bool {
	if message == nil {
		return false
	}
	if message.Sticker != nil {
		rate := stickerReactionBaseRatePct + reactionWorthCommentRate(message)
		return shouldSampleReaction(message, rate)
	}
	if message.Animation != nil || message.VideoNote != nil || message.Photo != nil || message.Video != nil {
		return true
	}
	if message.Document != nil {
		mime := strings.ToLower(strings.TrimSpace(message.Document.MimeType))
		if strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "video/") {
			return true
		}

		switch strings.ToLower(filepath.Ext(strings.TrimSpace(message.Document.FileName))) {
		case ".gif", ".jpg", ".jpeg", ".png", ".mp4", ".mov", ".m4v", ".webm", ".webp":
			return true
		default:
		}
	}

	commentRate := reactionWorthCommentRate(message)
	if commentRate <= 0 {
		return false
	}
	return shouldSampleReaction(message, commentRate)
}

func shouldSampleReaction(message *telego.Message, ratePct int) bool {
	if message == nil || ratePct <= 0 {
		return false
	}
	if ratePct >= maxReactionRatePct {
		return true
	}

	h := fnv.New32a()
	_, _ = h.Write([]byte(strconv.FormatInt(message.Chat.ID, 10)))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(strconv.Itoa(message.MessageID)))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(strings.TrimSpace(message.Text)))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(strings.TrimSpace(message.Caption)))
	if message.Sticker != nil {
		_, _ = h.Write([]byte(":"))
		_, _ = h.Write([]byte(strings.TrimSpace(message.Sticker.FileID)))
	}
	return int(h.Sum32()%100) < ratePct
}

func reactionWorthCommentRate(message *telego.Message) int {
	hits := reactionWorthCommentHitCount(message)
	if hits <= 0 {
		return 0
	}
	rate := hits * commentReactionHitRatePct
	if rate > maxReactionRatePct {
		return maxReactionRatePct
	}
	return rate
}

func reactionWorthCommentHitCount(message *telego.Message) int {
	if message == nil {
		return 0
	}

	text := normalizeReactionWorthCommentText(strings.TrimSpace(message.Text + "\n" + message.Caption))
	if text == "" {
		return 0
	}

	hits := 0
	for _, token := range strings.FieldsFunc(text, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z':
			return false
		case r >= '0' && r <= '9':
			return false
		default:
			return true
		}
	}) {
		if reactionWorthCommentKeywords[token] {
			hits++
		}
	}
	return hits
}

func normalizeReactionWorthCommentText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"đ", "d",
		"Đ", "d",
	)
	return replacer.Replace(text)
}

func appendSystemPrompt(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "\n\n" + extra
	}
}
