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
