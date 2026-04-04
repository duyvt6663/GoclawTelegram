package telegram

import (
	"strings"

	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/sodaubai"
)

const explicitSoDauBaiPollSystemPromptBase = "The user is explicitly asking for a sổ đầu bài moderation action in this chat.\n" +
	"- This is an action request, not casual banter.\n" +
	"- Do not replace it with sticker, GIF, or meme reactions.\n" +
	"- Do not invent extra rules that are not returned by the tool.\n" +
	"- If the person is referred to by nickname or display name, resolve them to the real Telegram contact or username if you know it from the chat context.\n" +
	"- Use the matching sổ đầu bài tool on this turn.\n"

func detectExplicitSoDauBaiPollAction(message *telego.Message) string {
	text := normalizeSoDauBaiIntentText(strings.TrimSpace(messageTextAndCaption(message)))
	if text == "" {
		return ""
	}

	if !looksLikeSoDauBaiActionRequest(text) {
		return ""
	}

	switch {
	case looksLikeSoDauBaiRemoveRequest(text):
		return sodaubai.PollActionRemove
	case looksLikeSoDauBaiAddRequest(text):
		return sodaubai.PollActionAdd
	default:
		return ""
	}
}

func explicitSoDauBaiPollSystemPrompt(action string) string {
	switch sodaubai.NormalizePollAction(action) {
	case sodaubai.PollActionRemove:
		return explicitSoDauBaiPollSystemPromptBase +
			"- Call `create_so_dau_bai_pardon_poll` on this turn.\n" +
			"- Opposite-action polls may coexist. An active add/xử poll does not by itself block opening a pardon poll.\n" +
			"- If the target is already in today's sổ đầu bài, opening the pardon poll is allowed.\n" +
			"- Only fall back to text if the tool itself reports a real blocking condition.\n"
	case sodaubai.PollActionAdd:
		return explicitSoDauBaiPollSystemPromptBase +
			"- Call `create_so_dau_bai_poll` on this turn.\n" +
			"- Use the vote flow instead of narrating what people should do.\n" +
			"- Only fall back to text if the tool itself reports a real blocking condition.\n"
	default:
		return ""
	}
}

func messageTextAndCaption(message *telego.Message) string {
	if message == nil {
		return ""
	}
	text := strings.TrimSpace(message.Text)
	caption := strings.TrimSpace(message.Caption)
	switch {
	case text == "":
		return caption
	case caption == "":
		return text
	default:
		return text + "\n" + caption
	}
}

func normalizeSoDauBaiIntentText(text string) string {
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

func looksLikeSoDauBaiActionRequest(text string) bool {
	return containsAny(text,
		"so dau bai",
		"sổ đầu bài",
		"vao so",
		"vào sổ",
		"khoi so",
		"khỏi sổ",
		"an xa",
		"ân xá",
		"tha ",
		"thả ",
		"xoa ",
		"xóa ",
		"gach ten",
		"gạch tên",
		"pard",
		"poll",
		"vote",
		"bieu quyet",
		"biểu quyết",
	)
}

func looksLikeSoDauBaiRemoveRequest(text string) bool {
	if containsAny(text,
		"mo poll tha",
		"mở poll thả",
		"mo poll an xa",
		"mở poll ân xá",
		"poll tha",
		"poll thả",
		"poll an xa",
		"poll ân xá",
		"pardon poll",
		"poll pardon",
		"vote tha",
		"vote thả",
		"vote an xa",
		"vote ân xá",
	) {
		return true
	}
	if containsAny(text,
		"tha khoi so",
		"thả khỏi sổ",
		"an xa",
		"ân xá",
		"tha ra",
		"thả ra",
		"xoa khoi so",
		"xóa khỏi sổ",
		"gach ten",
		"gạch tên",
		"remove khoi so",
		"remove khỏi sổ",
		"xoa ",
		"xóa ",
		"tha ",
		"thả ",
		"pard",
	) && containsAny(text, "so dau bai", "sổ đầu bài", "khoi so", "khỏi sổ", "khoi lo", "khỏi lọ", "khoi lo", "khỏi lo", "poll", "vote", "bieu quyet", "biểu quyết") {
		return true
	}
	return false
}

func looksLikeSoDauBaiAddRequest(text string) bool {
	if containsAny(text,
		"mo poll cho",
		"mở poll cho",
		"mo poll tong",
		"mở poll tống",
		"poll vao so",
		"poll vào sổ",
		"poll len so",
		"poll lên sổ",
		"vote vao so",
		"vote vào sổ",
		"vote len so",
		"vote lên sổ",
	) {
		return true
	}
	return containsAny(text,
		"vao so",
		"vào sổ",
		"vao so dau bai",
		"vào sổ đầu bài",
		"len so",
		"lên sổ",
		"len so dau bai",
		"lên sổ đầu bài",
		"cho vao so",
		"cho vào sổ",
		"tong vao so",
		"tống vào sổ",
	) && containsAny(text, "poll", "vote", "bieu quyet", "biểu quyết", "mo", "mở", "lap", "lập", "combat")
}

func containsAny(text string, parts ...string) bool {
	for _, part := range parts {
		if part != "" && strings.Contains(text, part) {
			return true
		}
	}
	return false
}
