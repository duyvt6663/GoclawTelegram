package telegram

import (
	"testing"

	"github.com/mymmrac/telego"
)

func TestResolveTelegramSenderInfoUsesUser(t *testing.T) {
	msg := &telego.Message{
		From: &telego.User{
			ID:        123,
			Username:  "duy",
			FirstName: "Duy",
			LastName:  "Vo",
			IsBot:     true,
		},
	}

	got := resolveTelegramSenderInfo(msg)
	if got == nil {
		t.Fatal("resolveTelegramSenderInfo returned nil")
	}
	if got.userID != "123" {
		t.Fatalf("userID = %q, want %q", got.userID, "123")
	}
	if got.senderID != "123|duy" {
		t.Fatalf("senderID = %q, want %q", got.senderID, "123|duy")
	}
	if got.label != "@duy" {
		t.Fatalf("label = %q, want %q", got.label, "@duy")
	}
	if got.contactType != "user" {
		t.Fatalf("contactType = %q, want %q", got.contactType, "user")
	}
	if !got.isBot {
		t.Fatal("expected bot sender")
	}
}

func TestResolveTelegramSenderInfoFallsBackToSenderChat(t *testing.T) {
	msg := &telego.Message{
		SenderChat: &telego.Chat{
			ID:       -1001234567890,
			Type:     telego.ChatTypeChannel,
			Title:    "Linked Announcements",
			Username: "linked_announcements",
		},
	}

	got := resolveTelegramSenderInfo(msg)
	if got == nil {
		t.Fatal("resolveTelegramSenderInfo returned nil")
	}
	if got.userID != "sender_chat:-1001234567890" {
		t.Fatalf("userID = %q, want %q", got.userID, "sender_chat:-1001234567890")
	}
	if got.senderID != "sender_chat:-1001234567890|linked_announcements" {
		t.Fatalf("senderID = %q, want %q", got.senderID, "sender_chat:-1001234567890|linked_announcements")
	}
	if got.label != "Linked Announcements" {
		t.Fatalf("label = %q, want %q", got.label, "Linked Announcements")
	}
	if got.contactType != telego.ChatTypeChannel {
		t.Fatalf("contactType = %q, want %q", got.contactType, telego.ChatTypeChannel)
	}
	if got.isBot {
		t.Fatal("expected non-bot sender")
	}
}
