package telegram

import (
	"hash/fnv"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
)

const implicitReactionMediaSystemPrompt = "An unmentioned sticker, GIF, meme, or media post just arrived in the group.\n" +
	"- Treat it as an implicit reaction cue only if the media clearly reads like a sticker, meme, reaction image, GIF, or short reaction clip.\n" +
	"- Inspect the actual media before replying. Use read_image/read_video when needed. Do not guess from filenames or sticker metadata alone.\n" +
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
		return shouldSampleStickerReaction(message)
	}
	if message.Animation != nil || message.VideoNote != nil || message.Photo != nil || message.Video != nil {
		return true
	}
	if message.Document == nil {
		return false
	}

	mime := strings.ToLower(strings.TrimSpace(message.Document.MimeType))
	if strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "video/") {
		return true
	}

	switch strings.ToLower(filepath.Ext(strings.TrimSpace(message.Document.FileName))) {
	case ".gif", ".jpg", ".jpeg", ".png", ".mp4", ".mov", ".m4v", ".webm", ".webp":
		return true
	default:
		return false
	}
}

func shouldSampleStickerReaction(message *telego.Message) bool {
	if message == nil || message.Sticker == nil {
		return false
	}

	h := fnv.New32a()
	_, _ = h.Write([]byte(strconv.FormatInt(message.Chat.ID, 10)))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(strconv.Itoa(message.MessageID)))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(strings.TrimSpace(message.Sticker.FileID)))
	return h.Sum32()%10 < 3
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
