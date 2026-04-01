package telegram

import "testing"

func TestIsAnimationMedia(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		path        string
		want        bool
	}{
		{name: "gif content type", contentType: "image/gif", path: "/tmp/cat.bin", want: true},
		{name: "gif extension", contentType: "", path: "/tmp/cat.gif", want: true},
		{name: "mp4 video", contentType: "video/mp4", path: "/tmp/cat.mp4", want: false},
		{name: "png image", contentType: "image/png", path: "/tmp/cat.png", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAnimationMedia(tt.contentType, tt.path); got != tt.want {
				t.Fatalf("isAnimationMedia(%q, %q) = %v, want %v", tt.contentType, tt.path, got, tt.want)
			}
		})
	}
}

func TestIsStickerMedia(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		path        string
		want        bool
	}{
		{name: "tgs extension", contentType: "", path: "/tmp/cat.tgs", want: true},
		{name: "sticker webp path", contentType: "image/webp", path: "/tmp/goclaw_sticker_123.webp", want: true},
		{name: "sticker webm path", contentType: "video/webm", path: "/tmp/reaction_sticker.webm", want: true},
		{name: "telegram file id", contentType: "video/webm", path: "telegram-file-id:abc123", want: true},
		{name: "ordinary webp image", contentType: "image/webp", path: "/tmp/cat.webp", want: false},
		{name: "ordinary webm video", contentType: "video/webm", path: "/tmp/cat.webm", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStickerMedia(tt.contentType, tt.path); got != tt.want {
				t.Fatalf("isStickerMedia(%q, %q) = %v, want %v", tt.contentType, tt.path, got, tt.want)
			}
		})
	}
}

func TestTelegramFileIDFromMediaURL(t *testing.T) {
	if got := telegramFileIDFromMediaURL("telegram-file-id:abc123"); got != "abc123" {
		t.Fatalf("telegramFileIDFromMediaURL() = %q, want %q", got, "abc123")
	}
	if got := telegramFileIDFromMediaURL("/tmp/sticker.webp"); got != "" {
		t.Fatalf("telegramFileIDFromMediaURL() = %q, want empty", got)
	}
}
