package gptimageedit

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/beta/topicrouting"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	telegramchannel "github.com/nextlevelbuilder/goclaw/internal/channels/telegram"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestImageEditCommandEnabledForChannelRequiresExplicitToolAllow(t *testing.T) {
	agentStore := &testAgentStore{
		byKey: map[string]*store.AgentData{
			"builder-bot": {
				AgentKey:    "builder-bot",
				ToolsConfig: []byte(`{"alsoAllow":["gpt_image_edit"]}`),
			},
			"market-analyst": {
				AgentKey:    "market-analyst",
				ToolsConfig: []byte(`{"allow":["web_search"]}`),
			},
		},
	}
	cmd := &imageEditCommand{feature: &GPTImageEditFeature{agentStore: agentStore}}

	if !cmd.EnabledForChannel(testTelegramChannel("builder-telegram", "builder-bot")) {
		t.Fatal("builder-bot should expose /image_edit when gpt_image_edit is explicitly allowed")
	}
	if cmd.EnabledForChannel(testTelegramChannel("market-telegram", "market-analyst")) {
		t.Fatal("market-analyst should not expose /image_edit without gpt_image_edit")
	}
}

func TestImageEditCommandEnabledForContextRespectsTopicRouting(t *testing.T) {
	defer topicrouting.SetTopicToolResolver(nil)

	cmd := &imageEditCommand{feature: &GPTImageEditFeature{}}
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
		t.Fatal("/image_edit should be hidden when a matched topic route does not enable gpt_image_edit")
	}

	topicrouting.SetTopicToolResolver(testTopicResolver{decision: &topicrouting.TopicToolDecision{
		Matched:         true,
		EnabledFeatures: []string{"gpt_image_edit"},
	}})
	if !cmd.EnabledForContext(context.Background(), channel, cmdCtx) {
		t.Fatal("/image_edit should be visible when a matched topic route enables gpt_image_edit")
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

	feature := &GPTImageEditFeature{
		dataDir:   blockedDataDir,
		workspace: workspace,
	}
	got := feature.resolvedStorageRoot(ctx)
	wantPrefix := filepath.Join(workspace, "tenants", tenantID.String(), "beta_cache", featureName)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("storage root = %q, want workspace fallback prefix %q", got, wantPrefix)
	}
	if !storageDirWritable(filepath.Join(got, "outputs")) {
		t.Fatalf("storage root is not writable: %q", got)
	}
}

func TestResolveAllowedImagePathAllowsSymlinkedTempDir(t *testing.T) {
	realTemp := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "tmp-link")
	if err := os.Symlink(realTemp, linkRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Setenv("TMPDIR", linkRoot)

	imagePath := filepath.Join(linkRoot, "telegram-photo.jpg")
	if err := os.WriteFile(imagePath, []byte("fake image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

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

func TestNormalizeEditRequestSupportsCoreEditTypes(t *testing.T) {
	cases := map[string]string{
		"object_removal":    "remove_object",
		"replace_object":    "replace_object",
		"style transfer":    "style_transfer",
		"background_change": "background_change",
		"text edits":        "text_edit",
		"upscaling":         "upscale",
	}
	for input, want := range cases {
		got, err := normalizeEditRequest(EditRequest{Prompt: "edit the image", Operation: input})
		if err != nil {
			t.Fatalf("normalizeEditRequest(%q) error: %v", input, err)
		}
		if got.Operation != want {
			t.Fatalf("normalizeEditRequest(%q) operation = %q, want %q", input, got.Operation, want)
		}
	}
}

func TestValidateImageDataRejectsInvalidInputs(t *testing.T) {
	if _, err := validateImageData(nil, "image/png", "empty.png", "test"); err == nil {
		t.Fatal("empty image should fail validation")
	}
	if _, err := validateImageData([]byte("not an image"), "text/plain", "note.txt", "test"); err == nil {
		t.Fatal("unsupported image MIME should fail validation")
	}
}

func TestCallOpenAIEditUsesOfficialMultipartFields(t *testing.T) {
	cases := []struct {
		name               string
		outputFormat       string
		wantOutputFormat   string
		wantOutputFormatOK bool
	}{
		{
			name:         "default png relies on API default",
			outputFormat: defaultOutputFormat,
		},
		{
			name:               "non default format is sent with snake case",
			outputFormat:       "webp",
			wantOutputFormat:   "webp",
			wantOutputFormatOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %s, want POST", r.Method)
				}
				if r.URL.Path != "/images/edits" {
					t.Errorf("path = %s, want /images/edits", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
					t.Errorf("Authorization = %q, want Bearer test-key", got)
				}
				if err := r.ParseMultipartForm(maxImageBytes); err != nil {
					t.Errorf("ParseMultipartForm() error = %v", err)
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}

				assertFormValue(t, r, "model", defaultModel)
				assertFormValue(t, r, "prompt", "remove the icons")
				assertFormValue(t, r, "n", "1")
				assertNoFormValue(t, r, "outputformat")

				if tc.wantOutputFormatOK {
					assertFormValue(t, r, "output_format", tc.wantOutputFormat)
				} else {
					assertNoFormValue(t, r, "output_format")
				}
				if got := len(r.MultipartForm.File["image"]); got != 1 {
					t.Errorf("multipart image file count = %d, want 1", got)
				} else if contentType := r.MultipartForm.File["image"][0].Header.Get("Content-Type"); contentType != "image/png" {
					t.Errorf("multipart image Content-Type = %q, want image/png", contentType)
				}
				if got := len(r.MultipartForm.File["image[]"]); got != 0 {
					t.Errorf("multipart image[] file count = %d, want 0", got)
				}

				fmt.Fprintf(w, `{"data":[{"b64_json":%q}],"usage":{"total_tokens":1}}`, base64.StdEncoding.EncodeToString([]byte("edited image")))
			}))
			defer server.Close()

			feature := &GPTImageEditFeature{
				apiKey:  "test-key",
				apiBase: server.URL,
			}
			output, statusCode, err := feature.callOpenAIEditOnce(context.Background(), &imageInput{
				Data:     []byte("input image"),
				MIME:     "image/png",
				FileName: "input.png",
				Size:     int64(len("input image")),
			}, EditRequest{
				Prompt:       "remove the icons",
				OutputFormat: tc.outputFormat,
			})
			if err != nil {
				t.Fatalf("callOpenAIEditOnce() error = %v", err)
			}
			if statusCode != http.StatusOK {
				t.Fatalf("statusCode = %d, want 200", statusCode)
			}
			if string(output.Data) != "edited image" {
				t.Fatalf("output data = %q, want edited image", string(output.Data))
			}
		})
	}
}

func assertFormValue(t *testing.T, r *http.Request, key, want string) {
	t.Helper()
	if got := r.FormValue(key); got != want {
		t.Errorf("form %s = %q, want %q", key, got, want)
	}
}

func assertNoFormValue(t *testing.T, r *http.Request, key string) {
	t.Helper()
	if values, ok := r.MultipartForm.Value[key]; ok && len(values) > 0 {
		t.Errorf("form %s should be absent, got %q", key, values)
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
