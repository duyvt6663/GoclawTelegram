package pdfurlautoreview

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mymmrac/telego"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	_ "modernc.org/sqlite"
)

func TestURLMessageHandlerEnabledForChannelRequiresExplicitToolAllow(t *testing.T) {
	agentStore := &testAgentStore{
		byKey: map[string]*store.AgentData{
			"builder-bot": {
				AgentKey:    "builder-bot",
				ToolsConfig: []byte(`{"alsoAllow":["pdf_url_auto_review_fetch"]}`),
			},
			"market-analyst": {
				AgentKey:    "market-analyst",
				ToolsConfig: []byte(`{"allow":["web_search"]}`),
			},
		},
	}
	handler := &urlMessageHandler{feature: &PDFURLAutoReviewFeature{agentStore: agentStore}}

	if !handler.EnabledForChannel(testTelegramChannel("builder-telegram", "builder-bot")) {
		t.Fatal("builder-bot should expose URL auto review when pdf_url_auto_review_fetch is explicitly allowed")
	}
	if handler.EnabledForChannel(testTelegramChannel("market-telegram", "market-analyst")) {
		t.Fatal("market-analyst should not expose URL auto review without pdf_url_auto_review_fetch")
	}
}

func TestURLMessageHandlerEnabledForContextRespectsTopicRouting(t *testing.T) {
	defer topicrouting.SetTopicToolResolver(nil)

	handler := &urlMessageHandler{feature: &PDFURLAutoReviewFeature{}}
	channel := testTelegramChannel("builder-telegram", "builder-bot")
	msgCtx := telegramchannel.DynamicMessageContext{
		ChatIDStr:       "-100123",
		MessageThreadID: 42,
		LocalKey:        "-100123:topic:42",
	}

	topicrouting.SetTopicToolResolver(testTopicResolver{decision: &topicrouting.TopicToolDecision{
		Matched:         true,
		EnabledFeatures: []string{"job_crawler"},
	}})
	if handler.EnabledForContext(context.Background(), channel, msgCtx) {
		t.Fatal("URL auto review should be hidden when a matched topic route does not enable pdf_url_auto_review")
	}

	topicrouting.SetTopicToolResolver(testTopicResolver{decision: &topicrouting.TopicToolDecision{
		Matched:         true,
		EnabledFeatures: []string{"pdf_url_auto_review"},
	}})
	if !handler.EnabledForContext(context.Background(), channel, msgCtx) {
		t.Fatal("URL auto review should be visible when a matched topic route enables pdf_url_auto_review")
	}
}

func TestResolvedStorageRootFallsBackFromUnwritableDataDirToWorkspace(t *testing.T) {
	dataRoot := t.TempDir()
	blockedDataDir := filepath.Join(dataRoot, "not-a-dir")
	if err := os.WriteFile(blockedDataDir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	tenantID := uuid.New()
	ctx := store.WithTenantID(context.Background(), tenantID)

	feature := &PDFURLAutoReviewFeature{
		dataDir:   blockedDataDir,
		workspace: workspace,
	}
	got := feature.resolvedStorageRoot(ctx)
	wantPrefix := filepath.Join(workspace, "tenants", tenantID.String(), "beta_cache", featureName)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("storage root = %q, want workspace fallback prefix %q", got, wantPrefix)
	}
	if !storageDirWritable(filepath.Join(got, "downloads")) {
		t.Fatalf("storage root is not writable: %q", got)
	}
}

func TestFetchDocumentMockedServerFollowsHTMLPDFLinkAndValidatesRequest(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("User-Agent"); got != fetchUserAgent {
			t.Errorf("User-Agent = %q, want %q", got, fetchUserAgent)
		}
		if got := r.Header.Get("Accept"); !strings.Contains(got, "application/pdf") || !strings.Contains(got, "text/html") {
			t.Errorf("Accept = %q, want PDF and HTML accept header", got)
		}
		switch r.URL.Path {
		case "/article":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<html><head><meta name="citation_pdf_url" content="/paper.pdf"><title>Mock Paper</title></head><body>paper page</body></html>`)
		case "/paper.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.4\nmock pdf bytes\n%%EOF"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	feature := &PDFURLAutoReviewFeature{httpClient: server.Client()}
	doc, err := feature.fetchDocument(context.Background(), server.URL+"/article")
	if err != nil {
		t.Fatalf("fetchDocument returned error: %v", err)
	}
	if doc.Kind != sourceKindPDF {
		t.Fatalf("kind = %q, want %q", doc.Kind, sourceKindPDF)
	}
	if doc.FinalURL != server.URL+"/paper.pdf" {
		t.Fatalf("final URL = %q, want linked PDF", doc.FinalURL)
	}
	if !strings.HasPrefix(string(doc.Data), "%PDF-") {
		t.Fatalf("downloaded body does not look like PDF: %q", string(doc.Data))
	}
	wantSeen := []string{"GET /article", "GET /paper.pdf"}
	if strings.Join(seen, ",") != strings.Join(wantSeen, ",") {
		t.Fatalf("requests = %v, want %v", seen, wantSeen)
	}
}

func TestFetchDocumentHTTPErrorIncludesProviderBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bad" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"provider down"}`)
	}))
	defer server.Close()

	feature := &PDFURLAutoReviewFeature{httpClient: server.Client()}
	_, err := feature.fetchDocument(context.Background(), server.URL+"/bad")
	if err == nil {
		t.Fatal("fetchDocument returned nil error for HTTP 503")
	}
	text := err.Error()
	if !strings.Contains(text, "unexpected status 503") || !strings.Contains(text, "provider down") {
		t.Fatalf("error = %q, want status and response body", text)
	}
}

func TestStoreMigrationAndStatus(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "feature.db"))
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	defer db.Close()

	store := &featureStore{db: db}
	if err := store.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	record := &fetchRecord{
		ID:        "fetch-1",
		TenantID:  "tenant-1",
		SourceURL: "https://example.com/paper.pdf",
		Mode:      reviewModeCollaborative,
		Status:    fetchStatusCompleted,
	}
	if err := store.insertFetch(record); err != nil {
		t.Fatalf("insertFetch: %v", err)
	}
	stats, err := store.statusStats("tenant-1")
	if err != nil {
		t.Fatalf("statusStats: %v", err)
	}
	if stats.FetchCount != 1 || stats.CompletedFetches != 1 || stats.LastFetchID != "fetch-1" {
		t.Fatalf("stats = %+v, want one completed fetch", stats)
	}
}

func TestURLMessageHandlerMatchesOnlyMessagesWithURL(t *testing.T) {
	handler := &urlMessageHandler{feature: &PDFURLAutoReviewFeature{}}
	if !handler.MatchesMessage(context.Background(), nil, telegramchannel.DynamicMessageContext{
		Message: &telego.Message{MessageID: 1},
		Text:    "review https://arxiv.org/abs/1706.03762",
	}) {
		t.Fatal("message with URL should match")
	}
	if handler.MatchesMessage(context.Background(), nil, telegramchannel.DynamicMessageContext{
		Message: &telego.Message{MessageID: 1},
		Text:    "no link here",
	}) {
		t.Fatal("message without URL should not match")
	}
}

func testTelegramChannel(name, agentKey string) *telegramchannel.Channel {
	base := channels.NewBaseChannel(name, nil, nil)
	base.SetAgentID(agentKey)
	base.SetTenantID(uuid.New())
	return &telegramchannel.Channel{BaseChannel: base}
}

type testTopicResolver struct {
	decision *topicrouting.TopicToolDecision
	err      error
}

func (r testTopicResolver) ResolveTopicToolDecision(context.Context, topicrouting.TopicToolScope) (*topicrouting.TopicToolDecision, error) {
	return r.decision, r.err
}

type testAgentStore struct {
	store.AgentStore
	byKey map[string]*store.AgentData
}

func (s *testAgentStore) GetByKey(_ context.Context, key string) (*store.AgentData, error) {
	if agent := s.byKey[key]; agent != nil {
		copyAgent := *agent
		return &copyAgent, nil
	}
	return nil, nil
}
