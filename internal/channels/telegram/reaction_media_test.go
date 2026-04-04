package telegram

import (
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

func TestShouldReplyToReactionMediaWithoutMention(t *testing.T) {
	tests := []struct {
		name string
		msg  *telego.Message
		want bool
	}{
		{
			name: "animation",
			msg: &telego.Message{
				Animation: &telego.Animation{FileID: "anim", MimeType: "video/mp4"},
			},
			want: true,
		},
		{
			name: "photo",
			msg: &telego.Message{
				Photo: []telego.PhotoSize{{FileID: "photo"}},
			},
			want: true,
		},
		{
			name: "image document",
			msg: &telego.Message{
				Document: &telego.Document{FileID: "doc", MimeType: "image/webp"},
			},
			want: true,
		},
		{
			name: "pdf document",
			msg: &telego.Message{
				Document: &telego.Document{FileID: "doc", MimeType: "application/pdf", FileName: "notes.pdf"},
			},
			want: false,
		},
		{
			name: "nil",
			msg:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldReplyToReactionMediaWithoutMention(tt.msg); got != tt.want {
				t.Fatalf("shouldReplyToReactionMediaWithoutMention() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldSampleReaction_DeterministicAndMixed(t *testing.T) {
	msg := &telego.Message{
		MessageID: 42,
		Chat:      telego.Chat{ID: -10012345},
		Sticker:   &telego.Sticker{FileID: "sticker-file-id"},
	}
	first := shouldSampleReaction(msg, 30)
	second := shouldSampleReaction(msg, 30)
	if first != second {
		t.Fatalf("expected deterministic sticker sampling, got %v then %v", first, second)
	}

	sawTrue := false
	sawFalse := false
	for i := 1; i <= 16; i++ {
		msg := &telego.Message{
			MessageID: i,
			Chat:      telego.Chat{ID: -10012345},
			Sticker:   &telego.Sticker{FileID: "sticker-file-id"},
		}
		if shouldSampleReaction(msg, 30) {
			sawTrue = true
		} else {
			sawFalse = true
		}
	}
	if !sawTrue || !sawFalse {
		t.Fatalf("expected sticker sampling to allow and skip across a small sample, got allow=%v skip=%v", sawTrue, sawFalse)
	}
}

func TestReactionWorthCommentRate_AccumulatesKeywordHits(t *testing.T) {
	msg := &telego.Message{
		Text: "dcm m ngu vl",
	}
	if got := reactionWorthCommentRate(msg); got != 60 {
		t.Fatalf("reactionWorthCommentRate() = %d, want 60", got)
	}
}

func TestReactionWorthCommentRate_NormalizesVietnameseD(t *testing.T) {
	msg := &telego.Message{
		Text: "đmm ngu vl",
	}
	if got := reactionWorthCommentRate(msg); got != 60 {
		t.Fatalf("reactionWorthCommentRate() = %d, want 60", got)
	}
}

func TestAppendSystemPrompt(t *testing.T) {
	if got := appendSystemPrompt("group prompt", implicitReactionMediaSystemPrompt); got != "group prompt\n\n"+implicitReactionMediaSystemPrompt {
		t.Fatalf("appendSystemPrompt() = %q", got)
	}
	if got := appendSystemPrompt("", implicitReactionMediaSystemPrompt); got != implicitReactionMediaSystemPrompt {
		t.Fatalf("appendSystemPrompt(empty) = %q", got)
	}
}

func TestImplicitReactionMediaSystemPromptPrefersMedia(t *testing.T) {
	wantContains := []string{
		"reaction-worthy comment",
		"override the usual text-first rule",
		"`find_and_post_local_sticker`",
		"`find_and_post_local_meme`",
		"`find_and_post_meme`",
		"formulate the query as the reaction you want to send back",
		"Convert the incoming meme into a comeback vibe or reply intent",
		"Only fall back to a text-only reply",
		"media be the main reply",
	}
	for _, want := range wantContains {
		if !strings.Contains(implicitReactionMediaSystemPrompt, want) {
			t.Fatalf("expected %q in implicitReactionMediaSystemPrompt", want)
		}
	}
}

func TestDetectExplicitSoDauBaiPollActionRemove(t *testing.T) {
	msg := &telego.Message{
		Text: "@my_foxy_lady_bot mở poll thả Phú Lỉn đi e",
	}
	if got := detectExplicitSoDauBaiPollAction(msg); got != "remove" {
		t.Fatalf("detectExplicitSoDauBaiPollAction(remove) = %q, want remove", got)
	}
}

func TestDetectExplicitSoDauBaiPollActionAdd(t *testing.T) {
	msg := &telego.Message{
		Text: "Mở combat add Phú Lỉnh vào sổ @my_foxy_lady_bot",
	}
	if got := detectExplicitSoDauBaiPollAction(msg); got != "add" {
		t.Fatalf("detectExplicitSoDauBaiPollAction(add) = %q, want add", got)
	}
}

func TestExplicitSoDauBaiPollSystemPromptRemove(t *testing.T) {
	prompt := explicitSoDauBaiPollSystemPrompt("remove")
	wantContains := []string{
		"`create_so_dau_bai_pardon_poll`",
		"Opposite-action polls may coexist",
		"Do not replace it with sticker, GIF, or meme reactions",
	}
	for _, want := range wantContains {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected %q in explicitSoDauBaiPollSystemPrompt(remove)", want)
		}
	}
}
