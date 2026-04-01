package telegram

import (
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

// --- buildMediaTags tests ---

// TestBuildMediaTags_NoTranscript_Legacy verifies that the pre-patch behaviour is
// preserved: audio/voice items without a transcript still produce plain tags,
// and all other media types are unaffected.
func TestBuildMediaTags_NoTranscript_Legacy(t *testing.T) {
	tests := []struct {
		name  string
		items []MediaInfo
		want  string
	}{
		{
			name:  "image",
			items: []MediaInfo{{Type: "image"}},
			want:  "<media:image>",
		},
		{
			name:  "video",
			items: []MediaInfo{{Type: "video"}},
			want:  "<media:video>",
		},
		{
			name:  "animation",
			items: []MediaInfo{{Type: "animation"}},
			want:  "<media:video>",
		},
		{
			name:  "audio without transcript",
			items: []MediaInfo{{Type: "audio"}},
			want:  "<media:audio>",
		},
		{
			name:  "voice without transcript",
			items: []MediaInfo{{Type: "voice"}},
			want:  "<media:voice>",
		},
		{
			name:  "document",
			items: []MediaInfo{{Type: "document"}},
			want:  "<media:document>",
		},
		{
			name:  "empty list",
			items: []MediaInfo{},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildMediaTags(tt.items)
			if got != tt.want {
				t.Errorf("buildMediaTags(%v) = %q, want %q", tt.items, got, tt.want)
			}
		})
	}
}

// TestBuildMediaTags_VoiceWithTranscript verifies that a voice item with a
// populated Transcript field generates the <transcript> sub-block.
func TestBuildMediaTags_VoiceWithTranscript(t *testing.T) {
	items := []MediaInfo{{Type: "voice", Transcript: "xin chào"}}
	got := buildMediaTags(items)

	if !strings.HasPrefix(got, "<media:voice>") {
		t.Errorf("expected output to start with <media:voice>, got: %q", got)
	}
	if !strings.Contains(got, "<transcript>") {
		t.Errorf("expected <transcript> block, got: %q", got)
	}
	if !strings.Contains(got, "xin chào") {
		t.Errorf("expected transcript text in output, got: %q", got)
	}
	if !strings.Contains(got, "</transcript>") {
		t.Errorf("expected closing </transcript>, got: %q", got)
	}
}

// TestBuildMediaTags_AudioWithTranscript verifies the same for audio type.
func TestBuildMediaTags_AudioWithTranscript(t *testing.T) {
	items := []MediaInfo{{Type: "audio", Transcript: "hello world"}}
	got := buildMediaTags(items)

	if !strings.HasPrefix(got, "<media:audio>") {
		t.Errorf("expected output to start with <media:audio>, got: %q", got)
	}
	if !strings.Contains(got, "<transcript>hello world</transcript>") {
		t.Errorf("expected transcript content, got: %q", got)
	}
}

// TestBuildMediaTags_TranscriptHTMLEscaping verifies that special HTML characters
// in the transcript are properly escaped to prevent XML injection.
func TestBuildMediaTags_TranscriptHTMLEscaping(t *testing.T) {
	items := []MediaInfo{{Type: "voice", Transcript: `<script>alert("xss")</script>`}}
	got := buildMediaTags(items)

	// Raw angle brackets must NOT appear inside the transcript block.
	if strings.Contains(got, "<script>") {
		t.Errorf("unescaped <script> tag found in output — XSS risk: %q", got)
	}
	// Escaped form must be present.
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected HTML-escaped content, got: %q", got)
	}
}

// TestBuildMediaTags_MultipleItems verifies correct handling of mixed media lists,
// including one voice with transcript and others without.
func TestBuildMediaTags_MultipleItems(t *testing.T) {
	items := []MediaInfo{
		{Type: "image"},
		{Type: "voice", Transcript: "hey there"},
		{Type: "document"},
	}
	got := buildMediaTags(items)
	parts := strings.Split(got, "\n")

	// Should have 3 top-level entries (image, voice block [2 lines], document)
	// but since voice produces 2 lines the split will have 4 parts.
	if !strings.Contains(parts[0], "<media:image>") {
		t.Errorf("first part should be image tag, got: %q", parts[0])
	}
	if !strings.Contains(got, "<media:voice>") {
		t.Errorf("expected voice tag, not found in: %q", got)
	}
	if !strings.Contains(got, "hey there") {
		t.Errorf("expected transcript text, not found in: %q", got)
	}
	if !strings.Contains(got, "<media:document>") {
		t.Errorf("expected document tag, not found in: %q", got)
	}
}

// TestBuildMediaTags_UnknownType verifies that an unrecognised media type is
// silently ignored (no panic, no output).
func TestBuildMediaTags_UnknownType(t *testing.T) {
	items := []MediaInfo{{Type: "sticker"}}
	got := buildMediaTags(items)
	if got != "" {
		t.Errorf("expected empty string for unknown type, got: %q", got)
	}
}

func TestBuildMediaTags_WithNote(t *testing.T) {
	items := []MediaInfo{{Type: "image", Note: "Telegram sticker (emoji 😼)."}}
	got := buildMediaTags(items)
	if !strings.Contains(got, "<media:image>") {
		t.Fatalf("expected image tag, got %q", got)
	}
	if !strings.Contains(got, "<note>Telegram sticker (emoji 😼).</note>") {
		t.Fatalf("expected note block, got %q", got)
	}
}

func TestLightweightMediaTags_Sticker(t *testing.T) {
	msg := &telego.Message{
		Sticker: &telego.Sticker{
			Emoji:      "😼",
			SetName:    "cat_pack",
			IsAnimated: true,
		},
	}
	got := lightweightMediaTags(msg)
	if got != "[sent an animated sticker 😼 from set cat_pack]" {
		t.Fatalf("lightweightMediaTags(sticker) = %q", got)
	}
}

func TestExtractMediaRefs_StickerUsesPreviewForAnimated(t *testing.T) {
	msg := &telego.Message{
		Sticker: &telego.Sticker{
			FileID:     "sticker-file",
			FileSize:   1234,
			IsAnimated: true,
			Emoji:      "😼",
			SetName:    "cat_pack",
			Thumbnail:  &telego.PhotoSize{FileID: "thumb-file", FileSize: 321},
		},
	}

	refs := extractMediaRefs(msg)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Type != "image" {
		t.Fatalf("expected animated sticker preview to resolve as image, got %q", refs[0].Type)
	}
	if refs[0].FileID != "thumb-file" {
		t.Fatalf("expected thumbnail file id, got %q", refs[0].FileID)
	}
	if refs[0].Note == "" || !strings.Contains(refs[0].Note, "animated sticker preview") {
		t.Fatalf("expected animated sticker note, got %q", refs[0].Note)
	}
}

func TestExtractMediaRefs_VideoStickerIncludesPreviewAndVideo(t *testing.T) {
	msg := &telego.Message{
		Sticker: &telego.Sticker{
			FileID:    "video-sticker",
			FileSize:  2048,
			IsVideo:   true,
			Emoji:     "🐟",
			SetName:   "fish_pack",
			Thumbnail: &telego.PhotoSize{FileID: "video-thumb", FileSize: 256},
		},
	}

	refs := extractMediaRefs(msg)
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	if refs[0].Type != "image" || refs[0].FileID != "video-thumb" {
		t.Fatalf("expected preview image first, got %#v", refs[0])
	}
	if refs[1].Type != "video" || refs[1].FileID != "video-sticker" {
		t.Fatalf("expected video sticker second, got %#v", refs[1])
	}
	if !strings.Contains(refs[0].Note, "preview frame") {
		t.Fatalf("expected preview note, got %q", refs[0].Note)
	}
	if !strings.Contains(refs[1].Note, "read_video") {
		t.Fatalf("expected video note to mention read_video, got %q", refs[1].Note)
	}
}

func TestLightweightTagForType_Sticker(t *testing.T) {
	msg := &telego.Message{
		Sticker: &telego.Sticker{
			Emoji:   "😼",
			SetName: "cat_pack",
		},
	}
	got := lightweightTagForType("sticker", msg)
	if got != "[sent a sticker 😼 from set cat_pack]" {
		t.Fatalf("lightweightTagForType(sticker) = %q", got)
	}
}
