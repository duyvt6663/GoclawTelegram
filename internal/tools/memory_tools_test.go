package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type mockSearchMemoryStore struct {
	*mockMemoryStore
	results map[string][]store.MemorySearchResult
}

func newMockSearchMemoryStore() *mockSearchMemoryStore {
	return &mockSearchMemoryStore{
		mockMemoryStore: newMockMemoryStore(),
		results:         make(map[string][]store.MemorySearchResult),
	}
}

func (m *mockSearchMemoryStore) Search(_ context.Context, query string, agentID, userID string, _ store.MemorySearchOptions) ([]store.MemorySearchResult, error) {
	return append([]store.MemorySearchResult(nil), m.results[fmt.Sprintf("%s|%s|%s", agentID, userID, query)]...), nil
}

type mockEpisodicStore struct {
	items   map[string]*store.EpisodicSummary
	results map[string][]store.EpisodicSearchResult
}

func newMockEpisodicStore() *mockEpisodicStore {
	return &mockEpisodicStore{
		items:   make(map[string]*store.EpisodicSummary),
		results: make(map[string][]store.EpisodicSearchResult),
	}
}

func (m *mockEpisodicStore) Create(_ context.Context, ep *store.EpisodicSummary) error {
	m.items[ep.ID.String()] = ep
	return nil
}

func (m *mockEpisodicStore) Get(_ context.Context, id string) (*store.EpisodicSummary, error) {
	if ep, ok := m.items[id]; ok {
		return ep, nil
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockEpisodicStore) Delete(_ context.Context, id string) error {
	delete(m.items, id)
	return nil
}

func (m *mockEpisodicStore) List(_ context.Context, _, _ string, _, _ int) ([]store.EpisodicSummary, error) {
	return nil, nil
}

func (m *mockEpisodicStore) Search(_ context.Context, query, agentID, userID string, _ store.EpisodicSearchOptions) ([]store.EpisodicSearchResult, error) {
	return append([]store.EpisodicSearchResult(nil), m.results[fmt.Sprintf("%s|%s|%s", agentID, userID, query)]...), nil
}

func (m *mockEpisodicStore) ExistsBySourceID(_ context.Context, _, _, _ string) (bool, error) {
	return false, nil
}

func (m *mockEpisodicStore) SetEmbeddingProvider(_ store.EmbeddingProvider) {}

func (m *mockEpisodicStore) Close() error { return nil }

func TestMemorySearch_CombinesDocumentAndEpisodicResults(t *testing.T) {
	memStore := newMockSearchMemoryStore()
	epStore := newMockEpisodicStore()
	tool := NewMemorySearchTool()
	tool.SetMemoryStore(memStore)
	tool.SetEpisodicStore(epStore)

	agentID := uuid.New()
	query := "release plan"
	memStore.results[fmt.Sprintf("%s|%s|%s", agentID.String(), "user1", query)] = []store.MemorySearchResult{
		{Path: "MEMORY.md", StartLine: 1, EndLine: 3, Score: 0.91, Snippet: "Document hit", Source: "memory"},
	}
	epStore.results[fmt.Sprintf("%s|%s|%s", agentID.String(), "user1", query)] = []store.EpisodicSearchResult{
		{EpisodicID: "ep-1", L0Abstract: "Discussed the release timeline with blockers.", Score: 0.72, SessionKey: "session-1", CreatedAt: time.Now()},
	}

	result := tool.Execute(memCtx(agentID, "user1", ""), map[string]any{"query": query})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, `"tier": "document"`) {
		t.Fatalf("expected document tier in result: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, `"tier": "episodic"`) {
		t.Fatalf("expected episodic tier in result: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, `"episodic_id": "ep-1"`) {
		t.Fatalf("expected episodic id in result: %s", result.ForLLM)
	}
}

func TestMemorySearch_EpisodicLeaderFallback(t *testing.T) {
	memStore := newMockSearchMemoryStore()
	epStore := newMockEpisodicStore()
	tool := NewMemorySearchTool()
	tool.SetMemoryStore(memStore)
	tool.SetEpisodicStore(epStore)

	memberID := uuid.New()
	leaderID := uuid.New()
	query := "migration"
	epStore.results[fmt.Sprintf("%s|%s|%s", leaderID.String(), "user1", query)] = []store.EpisodicSearchResult{
		{EpisodicID: "ep-leader", L0Abstract: "Leader discussed the migration rollout.", Score: 0.8, SessionKey: "leader-session", CreatedAt: time.Now()},
	}

	result := tool.Execute(memCtx(memberID, "user1", leaderID.String()), map[string]any{"query": query})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, `"episodic_id": "ep-leader"`) {
		t.Fatalf("expected leader episodic fallback result: %s", result.ForLLM)
	}
}

func TestMemoryExpand_ReturnsSummary(t *testing.T) {
	epStore := newMockEpisodicStore()
	tool := NewMemoryExpandTool()
	tool.SetEpisodicStore(epStore)

	id := uuid.New()
	epStore.items[id.String()] = &store.EpisodicSummary{
		ID:         id,
		SessionKey: "agent:demo:ws:direct:user1",
		L0Abstract: "Shipped the memory update.",
		Summary:    "We merged the new memory update after validating the migration plan.",
		TurnCount:  6,
		CreatedAt:  time.Date(2026, 4, 16, 9, 30, 0, 0, time.UTC),
	}

	result := tool.Execute(context.Background(), map[string]any{"id": id.String()})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "Shipped the memory update.") {
		t.Fatalf("expected L0 abstract in expand result: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "We merged the new memory update") {
		t.Fatalf("expected summary in expand result: %s", result.ForLLM)
	}
}
