package memereplace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
	_ "modernc.org/sqlite"
)

func TestRenderReplacementChromaKeysTemplateRegion(t *testing.T) {
	input := image.NewRGBA(image.Rect(0, 0, 80, 80))
	fill(input, input.Bounds(), color.RGBA{R: 230, A: 255})

	data, stats, err := renderReplacement(input, fitModeCover)
	if err != nil {
		t.Fatalf("renderReplacement() error = %v", err)
	}
	if stats.TemplateRegionEmpty() {
		t.Fatalf("render stats region is empty: %+v", stats)
	}

	output, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	center := rgbaAt(output, templateRegion.X+templateRegion.W/2, templateRegion.Y+templateRegion.H/2)
	if center.R < 200 || center.G > 80 || center.B > 80 {
		t.Fatalf("center pixel = %+v, want replacement red", center)
	}
	outside := rgbaAt(output, 20, 20)
	if isGreenKey(outside) {
		t.Fatalf("outside pixel unexpectedly green-keyed: %+v", outside)
	}
}

func TestReplaceToolRejectsMissingImage(t *testing.T) {
	result := (&replaceTool{feature: &MemeReplaceFeature{}}).Execute(context.Background(), nil)
	if result == nil || !result.IsError {
		t.Fatalf("Execute() should reject missing image, got %#v", result)
	}
	if !strings.Contains(result.ForLLM, "no image provided") {
		t.Fatalf("error = %q, want no image guidance", result.ForLLM)
	}
}

func TestReplaceUsesWorkspaceFallbackFromUnwritableDataDir(t *testing.T) {
	db := openFeatureDB(t)
	workspace := t.TempDir()
	blockedDataDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blockedDataDir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}

	imagePath := filepath.Join(workspace, "input.png")
	writeSolidPNG(t, imagePath, color.RGBA{R: 40, G: 80, B: 220, A: 255})

	tenantID := uuid.New()
	ctx := tools.WithToolWorkspace(store.WithTenantID(context.Background(), tenantID), workspace)
	feature := &MemeReplaceFeature{
		store:     &featureStore{db: db},
		workspace: workspace,
		dataDir:   blockedDataDir,
	}
	if err := feature.store.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	payload, err := feature.replace(ctx, ReplaceRequest{ImagePath: imagePath, Source: "test"}, false)
	if err != nil {
		t.Fatalf("replace() error = %v", err)
	}
	if _, err := os.Stat(payload.OutputPath); err != nil {
		t.Fatalf("output was not written: %v", err)
	}
	wantPrefix := filepath.Join(workspace, "tenants", tenantID.String(), "beta_cache", featureName)
	if !strings.HasPrefix(payload.OutputPath, wantPrefix) {
		t.Fatalf("output path = %q, want workspace fallback prefix %q", payload.OutputPath, wantPrefix)
	}
	runs, err := feature.store.listRecentRuns(tenantID.String(), 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != runStatusCompleted {
		t.Fatalf("runs = %+v, want one completed run", runs)
	}
}

func TestResolveAllowedImagePathAllowsSymlinkedTempDir(t *testing.T) {
	realTemp := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "tmp-link")
	if err := os.Symlink(realTemp, linkRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Setenv("TMPDIR", linkRoot)

	imagePath := filepath.Join(linkRoot, "telegram-photo.png")
	writeSolidPNG(t, imagePath, color.RGBA{R: 10, G: 20, B: 30, A: 255})

	resolved, err := resolveAllowedImagePath(context.Background(), imagePath, t.TempDir())
	if err != nil {
		t.Fatalf("resolveAllowedImagePath() error = %v", err)
	}
	want, err := filepath.EvalSymlinks(imagePath)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", imagePath, err)
	}
	if resolved != want {
		t.Fatalf("resolved path = %q, want %q", resolved, want)
	}
}

func TestReplaceCommandEnabledForChannelRequiresExplicitToolAllow(t *testing.T) {
	agentStore := &testAgentStore{
		byKey: map[string]*store.AgentData{
			"builder-bot": {
				AgentKey:    "builder-bot",
				ToolsConfig: []byte(`{"alsoAllow":["meme_replace"]}`),
			},
			"market-analyst": {
				AgentKey:    "market-analyst",
				ToolsConfig: []byte(`{"allow":["web_search"]}`),
			},
		},
	}
	cmd := &replaceCommand{feature: &MemeReplaceFeature{agentStore: agentStore}}

	if !cmd.EnabledForChannel(testTelegramChannel("builder-telegram", "builder-bot")) {
		t.Fatal("builder-bot should expose /meme_replace when meme_replace is explicitly allowed")
	}
	if cmd.EnabledForChannel(testTelegramChannel("market-telegram", "market-analyst")) {
		t.Fatal("market-analyst should not expose /meme_replace without meme_replace")
	}
}

func TestReplaceCommandEnabledForContextRespectsTopicRouting(t *testing.T) {
	defer topicrouting.SetTopicToolResolver(nil)

	cmd := &replaceCommand{feature: &MemeReplaceFeature{}}
	channel := testTelegramChannel("builder-telegram", "builder-bot")
	cmdCtx := telegramchannel.DynamicCommandContext{
		ChatIDStr:       "-100123",
		MessageThreadID: 42,
		LocalKey:        "-100123:topic:42",
	}

	topicrouting.SetTopicToolResolver(testTopicResolver{decision: &topicrouting.TopicToolDecision{
		Matched:         true,
		EnabledFeatures: []string{"job_crawler"},
	}})
	if cmd.EnabledForContext(context.Background(), channel, cmdCtx) {
		t.Fatal("/meme_replace should be hidden when a matched topic route does not enable meme_replace")
	}

	topicrouting.SetTopicToolResolver(testTopicResolver{decision: &topicrouting.TopicToolDecision{
		Matched:         true,
		EnabledFeatures: []string{"meme_replace"},
	}})
	if !cmd.EnabledForContext(context.Background(), channel, cmdCtx) {
		t.Fatal("/meme_replace should be visible when a matched topic route enables meme_replace")
	}
}

func TestValidateImageDataAcceptsDataURLBase64(t *testing.T) {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	fill(img, img.Bounds(), color.RGBA{R: 1, G: 2, B: 3, A: 255})
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	decoded, err := decodeBase64Image(dataURL)
	if err != nil {
		t.Fatalf("decodeBase64Image() error = %v", err)
	}
	input, err := validateImageData(decoded, "image/png", "input.png", "test")
	if err != nil {
		t.Fatalf("validateImageData() error = %v", err)
	}
	if input.MIME != "image/png" {
		t.Fatalf("MIME = %q, want image/png", input.MIME)
	}
}

func openFeatureDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "feature.db"))
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func writeSolidPNG(t *testing.T, path string, c color.RGBA) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	fill(img, img.Bounds(), c)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encodeErr := png.Encode(f, img)
	closeErr := f.Close()
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func testTelegramChannel(name, agentKey string) *telegramchannel.Channel {
	base := channels.NewBaseChannel(name, nil, nil)
	base.SetAgentID(agentKey)
	base.SetTenantID(uuid.New())
	return &telegramchannel.Channel{BaseChannel: base}
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

type testTopicResolver struct {
	decision *topicrouting.TopicToolDecision
}

func (r testTopicResolver) ResolveTopicToolDecision(context.Context, topicrouting.TopicToolScope) (*topicrouting.TopicToolDecision, error) {
	return r.decision, nil
}

func (s renderStats) TemplateRegionEmpty() bool {
	return s.Region.W <= 0 || s.Region.H <= 0
}
