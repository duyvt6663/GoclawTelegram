package telegrampdfautoreview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func TestUploadHandlerEnabledForChannelRequiresExplicitToolAllow(t *testing.T) {
	agentStore := &testAgentStore{
		byKey: map[string]*store.AgentData{
			"builder-bot": {
				AgentKey:    "builder-bot",
				ToolsConfig: []byte(`{"alsoAllow":["telegram_pdf_auto_review_reprocess"]}`),
			},
			"market-analyst": {
				AgentKey:    "market-analyst",
				ToolsConfig: []byte(`{"allow":["web_search"]}`),
			},
			"fox-spirit": {
				AgentKey:    "fox-spirit",
				ToolsConfig: []byte(`{}`),
			},
		},
	}

	handler := &uploadHandler{feature: &TelegramPDFAutoReviewFeature{agentStore: agentStore}}

	if !handler.EnabledForChannel(testPDFAutoReviewChannel("builder-bot", "builder-bot")) {
		t.Fatal("builder-bot should expose PDF auto review when telegram_pdf_auto_review_reprocess is explicitly allowed")
	}
	if handler.EnabledForChannel(testPDFAutoReviewChannel("stocky_bot", "market-analyst")) {
		t.Fatal("stocky_bot should not expose PDF auto review without telegram_pdf_auto_review_reprocess")
	}
	if handler.EnabledForChannel(testPDFAutoReviewChannel("my-foxy-lady", "fox-spirit")) {
		t.Fatal("my-foxy-lady should not expose PDF auto review when tools_config does not explicitly allow it")
	}
}

func TestSavedPDFPathFallsBackToWorkspaceWhenDataDirIsNotWritable(t *testing.T) {
	workspace := t.TempDir()
	blockedRoot := filepath.Join(t.TempDir(), "blocked-root")
	if err := os.WriteFile(blockedRoot, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatalf("create blocked root: %v", err)
	}

	feature := &TelegramPDFAutoReviewFeature{
		dataDir:   blockedRoot,
		workspace: workspace,
	}

	got := feature.savedPDFPath(context.Background(), "abc123")
	wantPrefix := filepath.Join(workspace, "beta_cache", featureName, "papers") + string(filepath.Separator)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("savedPDFPath fallback = %q, want prefix %q", got, wantPrefix)
	}
}

func TestResolveCurrentPDFRefPrefersLatestPDF(t *testing.T) {
	ctx := tools.WithMediaDocRefs(context.Background(), []providers.MediaRef{
		{ID: "docx-1", MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Path: "/tmp/a.docx"},
		{ID: "pdf-old", MimeType: "application/pdf", Path: "/tmp/old.pdf"},
		{ID: "pdf-new", MimeType: "application/pdf", Path: "/tmp/new.pdf"},
	})

	ref, err := resolveCurrentPDFRef(ctx, "")
	if err != nil {
		t.Fatalf("resolveCurrentPDFRef returned error: %v", err)
	}
	if ref.ID != "pdf-new" {
		t.Fatalf("resolveCurrentPDFRef picked %q, want pdf-new", ref.ID)
	}
}

func TestResolveCurrentPDFRefRejectsNonPDFMediaID(t *testing.T) {
	ctx := tools.WithMediaDocRefs(context.Background(), []providers.MediaRef{
		{ID: "docx-1", MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Path: "/tmp/a.docx"},
		{ID: "pdf-1", MimeType: "application/pdf", Path: "/tmp/a.pdf"},
	})

	_, err := resolveCurrentPDFRef(ctx, "docx-1")
	if err == nil || !strings.Contains(err.Error(), "not a PDF") {
		t.Fatalf("resolveCurrentPDFRef(non-pdf media_id) error = %v, want not a PDF", err)
	}
}

func testPDFAutoReviewChannel(name, agentKey string) *telegramchannel.Channel {
	base := channels.NewBaseChannel(name, nil, nil)
	base.SetAgentID(agentKey)
	base.SetTenantID(uuid.New())
	return &telegramchannel.Channel{BaseChannel: base}
}

type testAgentStore struct {
	byKey map[string]*store.AgentData
}

func (s *testAgentStore) Create(context.Context, *store.AgentData) error { return nil }
func (s *testAgentStore) GetByKey(_ context.Context, key string) (*store.AgentData, error) {
	if agent := s.byKey[key]; agent != nil {
		copyAgent := *agent
		return &copyAgent, nil
	}
	return nil, nil
}
func (s *testAgentStore) GetByID(context.Context, uuid.UUID) (*store.AgentData, error) {
	return nil, nil
}
func (s *testAgentStore) GetByIDUnscoped(context.Context, uuid.UUID) (*store.AgentData, error) {
	return nil, nil
}
func (s *testAgentStore) GetByKeys(context.Context, []string) ([]store.AgentData, error) {
	return nil, nil
}
func (s *testAgentStore) GetByIDs(context.Context, []uuid.UUID) ([]store.AgentData, error) {
	return nil, nil
}
func (s *testAgentStore) GetDefault(context.Context) (*store.AgentData, error) { return nil, nil }
func (s *testAgentStore) Update(context.Context, uuid.UUID, map[string]any) error {
	return nil
}
func (s *testAgentStore) Delete(context.Context, uuid.UUID) error { return nil }
func (s *testAgentStore) List(context.Context, string) ([]store.AgentData, error) {
	return nil, nil
}
func (s *testAgentStore) ShareAgent(context.Context, uuid.UUID, string, string, string) error {
	return nil
}
func (s *testAgentStore) RevokeShare(context.Context, uuid.UUID, string) error { return nil }
func (s *testAgentStore) ListShares(context.Context, uuid.UUID) ([]store.AgentShareData, error) {
	return nil, nil
}
func (s *testAgentStore) CanAccess(context.Context, uuid.UUID, string) (bool, string, error) {
	return false, "", nil
}
func (s *testAgentStore) ListAccessible(context.Context, string) ([]store.AgentData, error) {
	return nil, nil
}
func (s *testAgentStore) GetAgentContextFiles(context.Context, uuid.UUID) ([]store.AgentContextFileData, error) {
	return nil, nil
}
func (s *testAgentStore) SetAgentContextFile(context.Context, uuid.UUID, string, string) error {
	return nil
}
func (s *testAgentStore) GetUserContextFiles(context.Context, uuid.UUID, string) ([]store.UserContextFileData, error) {
	return nil, nil
}
func (s *testAgentStore) SetUserContextFile(context.Context, uuid.UUID, string, string, string) error {
	return nil
}
func (s *testAgentStore) DeleteUserContextFile(context.Context, uuid.UUID, string, string) error {
	return nil
}
func (s *testAgentStore) ListUserContextFilesByName(context.Context, uuid.UUID, string) ([]store.UserContextFileData, error) {
	return nil, nil
}
func (s *testAgentStore) MigrateUserDataOnMerge(context.Context, []string, string) error { return nil }
func (s *testAgentStore) GetUserOverride(context.Context, uuid.UUID, string) (*store.UserAgentOverrideData, error) {
	return nil, nil
}
func (s *testAgentStore) SetUserOverride(context.Context, *store.UserAgentOverrideData) error {
	return nil
}
func (s *testAgentStore) GetOrCreateUserProfile(context.Context, uuid.UUID, string, string, string) (bool, string, error) {
	return false, "", nil
}
func (s *testAgentStore) ListUserInstances(context.Context, uuid.UUID) ([]store.UserInstanceData, error) {
	return nil, nil
}
func (s *testAgentStore) UpdateUserProfileMetadata(context.Context, uuid.UUID, string, map[string]string) error {
	return nil
}
func (s *testAgentStore) EnsureUserProfile(context.Context, uuid.UUID, string) error { return nil }
func (s *testAgentStore) PropagateContextFile(context.Context, uuid.UUID, string) (int, error) {
	return 0, nil
}
