package loppho

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestVoteCommandEnabledForChannelRequiresExplicitToolAllow(t *testing.T) {
	agentStore := &testAgentStore{
		byKey: map[string]*store.AgentData{
			"builder-bot": {
				AgentKey:    "builder-bot",
				ToolsConfig: []byte(`{"alsoAllow":["vote_lop_pho_open"]}`),
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
	cmd := &voteCommand{feature: &LopPhoFeature{agentStore: agentStore}}

	if !cmd.EnabledForChannel(testLopPhoChannel("builder-bot", "builder-bot")) {
		t.Fatal("builder-bot should expose /vote_lp when vote_lop_pho_open is explicitly allowed")
	}
	if cmd.EnabledForChannel(testLopPhoChannel("market-analyst-bot", "market-analyst")) {
		t.Fatal("market-analyst-bot should not expose /vote_lp without vote_lop_pho_open")
	}
	if cmd.EnabledForChannel(testLopPhoChannel("my-foxy-lady", "fox-spirit")) {
		t.Fatal("my-foxy-lady should not expose /vote_lp when tools_config does not explicitly allow it")
	}
}

func TestStableVoteTargetKeyPrefersRawTargetForCrossBotConsistency(t *testing.T) {
	rawMention := "@psycholog1st"
	builderResolved := telegramIdentity{
		UserID:   "1565106682",
		SenderID: "1565106682|psycholog1st",
		Label:    "@psycholog1st",
	}
	foxyResolved := telegramIdentity{
		UserID:   "group:my-foxy-lady:-1003865644303",
		SenderID: "group:my-foxy-lady:-1003865644303",
		Label:    "@psycholog1st",
	}

	gotBuilder := stableVoteTargetKey(rawMention, builderResolved)
	gotFoxy := stableVoteTargetKey(rawMention, foxyResolved)
	if gotBuilder != "sender:psycholog1st" {
		t.Fatalf("stableVoteTargetKey(%q, builder) = %q, want sender:psycholog1st", rawMention, gotBuilder)
	}
	if gotFoxy != gotBuilder {
		t.Fatalf("stableVoteTargetKey(%q) mismatch across bots: builder=%q foxy=%q", rawMention, gotBuilder, gotFoxy)
	}

	rawDisplay := "Psycholog1st"
	gotBuilder = stableVoteTargetKey(rawDisplay, builderResolved)
	gotFoxy = stableVoteTargetKey(rawDisplay, foxyResolved)
	if gotBuilder != "input:psycholog1st" {
		t.Fatalf("stableVoteTargetKey(%q, builder) = %q, want input:psycholog1st", rawDisplay, gotBuilder)
	}
	if gotFoxy != gotBuilder {
		t.Fatalf("stableVoteTargetKey(%q) mismatch across bots: builder=%q foxy=%q", rawDisplay, gotBuilder, gotFoxy)
	}

	rawNumeric := "1565106682"
	if got := stableVoteTargetKey(rawNumeric, foxyResolved); got != "user:1565106682" {
		t.Fatalf("stableVoteTargetKey(%q) = %q, want user:1565106682", rawNumeric, got)
	}
}

func testLopPhoChannel(name, agentKey string) *telegramchannel.Channel {
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
