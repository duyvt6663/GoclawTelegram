package agent

import (
	"strings"
	"testing"
)

func TestBuildSystemPrompt_TelegramReactionMediaHint(t *testing.T) {
	prompt := BuildSystemPrompt(SystemPromptConfig{
		ChannelType: "telegram",
		ToolNames: []string{
			"find_and_post_local_sticker",
			"find_and_post_local_meme",
			"find_and_post_meme",
		},
	})

	wantContains := []string{
		"## Reaction Media",
		"Prefer this order when it fits: `find_and_post_local_sticker` -> `find_and_post_local_meme` -> `find_and_post_meme`.",
		"Prefer reusing learned local media over searching the web.",
		"If the turn itself is a sticker, GIF, or meme-style reaction cue, the best reply may be a reaction item as the main response instead of text.",
		"that moderation action takes priority over reaction media",
		"write the query for the reaction you want to send back, not a literal description of the sticker, GIF, or meme you just received.",
		"Translate the incoming media into a comeback or answering vibe",
		"- find_and_post_local_sticker: Attach a previously learned Telegram sticker from saved libraries",
		"- find_and_post_local_meme: Attach a local meme GIF, video, or image from configured libraries",
		"- find_and_post_meme: Find an online meme or reaction image and attach it to the current reply",
	}
	for _, want := range wantContains {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected %q in prompt, got:\n%s", want, prompt)
		}
	}
}

func TestBuildSystemPrompt_NoTelegramReactionMediaHintWithoutTelegram(t *testing.T) {
	prompt := BuildSystemPrompt(SystemPromptConfig{
		ChannelType: "discord",
		ToolNames: []string{
			"find_and_post_local_sticker",
			"find_and_post_local_meme",
			"find_and_post_meme",
		},
	})

	if strings.Contains(prompt, "## Reaction Media") {
		t.Fatalf("unexpected reaction media hint in non-Telegram prompt:\n%s", prompt)
	}
}

func TestBuildSystemPrompt_PrefersLiveToolDescriptions(t *testing.T) {
	prompt := BuildSystemPrompt(SystemPromptConfig{
		ToolNames: []string{"linkup_web_search"},
		ToolDescs: map[string]string{
			"linkup_web_search": "Search the web with Linkup and return a concise factual answer plus source links. Use deep mode for slower, broader coverage.",
		},
	})

	if !strings.Contains(prompt, "Use deep mode for slower, broader coverage.") {
		t.Fatalf("expected live tool description in prompt, got:\n%s", prompt)
	}
}
