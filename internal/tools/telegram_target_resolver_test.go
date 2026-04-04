package tools

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type fakeContactStore struct {
	results []store.ChannelContact
}

func (f *fakeContactStore) UpsertContact(context.Context, string, string, string, string, string, string, string, string) error {
	return nil
}

func (f *fakeContactStore) ListContacts(_ context.Context, opts store.ContactListOpts) ([]store.ChannelContact, error) {
	return f.results, nil
}

func (f *fakeContactStore) CountContacts(context.Context, store.ContactListOpts) (int, error) {
	return len(f.results), nil
}

func (f *fakeContactStore) GetContactsBySenderIDs(context.Context, []string) (map[string]store.ChannelContact, error) {
	return nil, nil
}

func (f *fakeContactStore) GetContactByID(context.Context, uuid.UUID) (*store.ChannelContact, error) {
	return nil, nil
}

func (f *fakeContactStore) GetSenderIDsByContactIDs(context.Context, []uuid.UUID) ([]string, error) {
	return nil, nil
}

func (f *fakeContactStore) MergeContacts(context.Context, []uuid.UUID, uuid.UUID) error {
	return nil
}

func (f *fakeContactStore) UnmergeContacts(context.Context, []uuid.UUID) error {
	return nil
}

func (f *fakeContactStore) GetContactsByMergedID(context.Context, uuid.UUID) ([]store.ChannelContact, error) {
	return nil, nil
}

func (f *fakeContactStore) ResolveTenantUserID(context.Context, string, string) (string, error) {
	return "", nil
}

func TestResolveTelegramTargetPrefersMatchingDisplayNameInSameChannel(t *testing.T) {
	channelA := "telegram-main"
	channelB := "telegram-other"
	usernameA := "Phamhphu"
	displayA := "Phú Lỉn"
	usernameB := "someoneelse"
	displayB := "Phú Lỉn"
	now := time.Now()

	contacts := &fakeContactStore{
		results: []store.ChannelContact{
			{
				ChannelType:     "telegram",
				ChannelInstance: &channelB,
				SenderID:        "200|someoneelse",
				Username:        &usernameB,
				DisplayName:     &displayB,
				ContactType:     "user",
				FirstSeenAt:     now,
				LastSeenAt:      now,
			},
			{
				ChannelType:     "telegram",
				ChannelInstance: &channelA,
				SenderID:        "1565106682|Phamhphu",
				Username:        &usernameA,
				DisplayName:     &displayA,
				ContactType:     "user",
				FirstSeenAt:     now,
				LastSeenAt:      now,
			},
		},
	}

	ctx := WithToolChannel(context.Background(), channelA)
	got := resolveTelegramTarget(ctx, contacts, "Phú Lỉn")
	if got != "@Phamhphu" {
		t.Fatalf("resolveTelegramTarget() = %q, want @Phamhphu", got)
	}
}

func TestResolveTelegramTargetKeepsCanonicalUsername(t *testing.T) {
	got := resolveTelegramTarget(context.Background(), &fakeContactStore{}, "@Phamhphu")
	if got != "@Phamhphu" {
		t.Fatalf("resolveTelegramTarget() = %q, want canonical username unchanged", got)
	}
}
