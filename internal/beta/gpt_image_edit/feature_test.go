package gptimageedit

import (
	"context"
	"database/sql"
	"encoding/base64"
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

func TestImageRefFromTelegramMessageSupportsStaticSticker(t *testing.T) {
	message := &telego.Message{
		Sticker: &telego.Sticker{
			FileID:   "sticker-file",
			FileSize: 1234,
		},
	}

	ref, err := imageRefFromTelegramMessage(message)
	if err != nil {
		t.Fatalf("imageRefFromTelegramMessage() error = %v", err)
	}
	if ref.FileID != "sticker-file" {
		t.Fatalf("FileID = %q, want sticker-file", ref.FileID)
	}
	if ref.MIME != "image/webp" {
		t.Fatalf("MIME = %q, want image/webp", ref.MIME)
	}
	if ref.FileName != "telegram-sticker.webp" {
		t.Fatalf("FileName = %q, want telegram-sticker.webp", ref.FileName)
	}
	if ref.Source != "telegram:current_message:sticker" {
		t.Fatalf("Source = %q, want sticker source", ref.Source)
	}
}

func TestImageRefFromTelegramMessageUsesAnimatedStickerThumbnail(t *testing.T) {
	message := &telego.Message{
		Sticker: &telego.Sticker{
			FileID:     "animated-sticker-file",
			FileSize:   1234,
			IsAnimated: true,
			Thumbnail: &telego.PhotoSize{
				FileID:   "thumb-file",
				FileSize: 345,
			},
		},
	}

	ref, err := imageRefFromTelegramMessage(message)
	if err != nil {
		t.Fatalf("imageRefFromTelegramMessage() error = %v", err)
	}
	if ref.FileID != "thumb-file" {
		t.Fatalf("FileID = %q, want thumbnail file", ref.FileID)
	}
	if ref.FileName != "telegram-sticker-preview.webp" {
		t.Fatalf("FileName = %q, want preview file", ref.FileName)
	}
	if ref.Source != "telegram:current_message:sticker_preview" {
		t.Fatalf("Source = %q, want sticker preview source", ref.Source)
	}
}

func TestImageRefFromTelegramMessageSupportsReplyToSticker(t *testing.T) {
	message := &telego.Message{
		ReplyToMessage: &telego.Message{
			Sticker: &telego.Sticker{
				FileID:   "reply-sticker-file",
				FileSize: 1234,
			},
		},
	}

	ref, err := imageRefFromTelegramMessage(message)
	if err != nil {
		t.Fatalf("imageRefFromTelegramMessage() error = %v", err)
	}
	if ref.FileID != "reply-sticker-file" {
		t.Fatalf("FileID = %q, want reply sticker file", ref.FileID)
	}
	if ref.Source != "telegram:reply_to_message:sticker" {
		t.Fatalf("Source = %q, want reply sticker source", ref.Source)
	}
}

func TestImageRefFromTelegramMessageRejectsAnimatedStickerWithoutThumbnail(t *testing.T) {
	_, err := imageRefFromTelegramMessage(&telego.Message{
		Sticker: &telego.Sticker{
			FileID:     "animated-sticker-file",
			IsAnimated: true,
		},
	})
	if err == nil {
		t.Fatal("animated sticker without thumbnail should be rejected")
	}
	if !strings.Contains(err.Error(), "static sticker") {
		t.Fatalf("error = %q, want static sticker guidance", err.Error())
	}
}

func TestParseImageRefCommandArgsSupportsProfilePhotoMode(t *testing.T) {
	got := parseImageRefCommandArgs("avatar phu")
	if !got.profilePhoto {
		t.Fatal("profilePhoto = false, want true")
	}
	if got.label != "phu" {
		t.Fatalf("label = %q, want phu", got.label)
	}

	got = parseImageRefCommandArgs("face")
	if got.profilePhoto {
		t.Fatal("plain image ref should not force profile photo mode")
	}
	if got.label != "face" {
		t.Fatalf("label = %q, want face", got.label)
	}
}

func TestProfilePhotoUserFromTelegramMessageUsesReplyAuthor(t *testing.T) {
	message := &telego.Message{
		From: &telego.User{ID: 1, FirstName: "sender"},
		ReplyToMessage: &telego.Message{
			From: &telego.User{ID: 2, FirstName: "target"},
		},
	}
	user := profilePhotoUserFromTelegramMessage(message)
	if user == nil {
		t.Fatal("profilePhotoUserFromTelegramMessage() returned nil")
	}
	if user.ID != 2 {
		t.Fatalf("user.ID = %d, want reply author 2", user.ID)
	}
}

func TestProfilePhotoUserFromTelegramMessageSkipsBots(t *testing.T) {
	message := &telego.Message{
		From: &telego.User{ID: 1, FirstName: "sender"},
		ReplyToMessage: &telego.Message{
			From: &telego.User{ID: 2, FirstName: "bot", IsBot: true},
		},
	}
	user := profilePhotoUserFromTelegramMessage(message)
	if user == nil {
		t.Fatal("profilePhotoUserFromTelegramMessage() returned nil")
	}
	if user.ID != 1 {
		t.Fatalf("user.ID = %d, want sender fallback 1", user.ID)
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
			output, statusCode, err := feature.callOpenAIEditOnce(context.Background(), []*imageInput{{
				Data:     []byte("input image"),
				MIME:     "image/png",
				FileName: "input.png",
				Size:     int64(len("input image")),
			}}, EditRequest{
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

func TestCallOpenAIEditUsesImageArrayForMultipleInputs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(maxImageBytes); err != nil {
			t.Errorf("ParseMultipartForm() error = %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		assertFormValue(t, r, "model", defaultModel)
		assertFormValue(t, r, "prompt", "combine refs")
		if got := len(r.MultipartForm.File["image"]); got != 0 {
			t.Errorf("multipart image file count = %d, want 0", got)
		}
		files := r.MultipartForm.File["image[]"]
		if got := len(files); got != 2 {
			t.Fatalf("multipart image[] file count = %d, want 2", got)
		}
		if got := files[0].Header.Get("Content-Type"); got != "image/png" {
			t.Errorf("first image Content-Type = %q, want image/png", got)
		}
		if got := files[1].Header.Get("Content-Type"); got != "image/webp" {
			t.Errorf("second image Content-Type = %q, want image/webp", got)
		}
		fmt.Fprintf(w, `{"data":[{"b64_json":%q}]}`, base64.StdEncoding.EncodeToString([]byte("generated image")))
	}))
	defer server.Close()

	feature := &GPTImageEditFeature{
		apiKey:  "test-key",
		apiBase: server.URL,
	}
	output, statusCode, err := feature.callOpenAIEditOnce(context.Background(), []*imageInput{
		{
			Data:     []byte("primary"),
			MIME:     "image/png",
			FileName: "primary.png",
			Size:     int64(len("primary")),
		},
		{
			Data:     []byte("style"),
			MIME:     "image/webp",
			FileName: "style.webp",
			Size:     int64(len("style")),
		},
	}, EditRequest{Prompt: "combine refs"})
	if err != nil {
		t.Fatalf("callOpenAIEditOnce() error = %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("statusCode = %d, want 200", statusCode)
	}
	if string(output.Data) != "generated image" {
		t.Fatalf("output data = %q, want generated image", string(output.Data))
	}
}

func TestParseImageRefGenerationPrompt(t *testing.T) {
	labels, prompt, ok := parseImageRefGenerationPrompt("using face, hair_style: cho bác mọc thêm tóc")
	if !ok {
		t.Fatal("parseImageRefGenerationPrompt() ok = false")
	}
	if got, want := strings.Join(labels, ","), "face,hair_style"; got != want {
		t.Fatalf("labels = %q, want %q", got, want)
	}
	if prompt != "cho bác mọc thêm tóc" {
		t.Fatalf("prompt = %q", prompt)
	}

	if _, _, ok := parseImageRefGenerationPrompt("make this sticker realistic"); ok {
		t.Fatal("parseImageRefGenerationPrompt() should only accept explicit using syntax")
	}
}

func TestParseImageGenerationPromptSupportsDirectMediaPrompt(t *testing.T) {
	labels, prompt, ok := parseImageGenerationPrompt("make this sticker realistic")
	if !ok {
		t.Fatal("parseImageGenerationPrompt() ok = false")
	}
	if len(labels) != 0 {
		t.Fatalf("labels = %v, want none", labels)
	}
	if prompt != "make this sticker realistic" {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestImageGenMessageRefsSupportsReplySticker(t *testing.T) {
	refs := imageGenMessageRefs(&telego.Message{
		ReplyToMessage: &telego.Message{
			Sticker: &telego.Sticker{
				FileID:   "reply-sticker-file",
				FileSize: 1234,
			},
		},
	})
	if len(refs) != 1 {
		t.Fatalf("len(refs) = %d, want 1", len(refs))
	}
	if refs[0].FileID != "reply-sticker-file" {
		t.Fatalf("FileID = %q, want reply-sticker-file", refs[0].FileID)
	}
	if refs[0].MIME != "image/webp" {
		t.Fatalf("MIME = %q, want image/webp", refs[0].MIME)
	}
}

func TestImageReferenceStoreReturnsRequestedOrder(t *testing.T) {
	store := newTestImageFeatureStore(t)
	scope := telegramRefScope{tenantID: uuid.NewString(), chatID: "-100123", threadID: 42}
	for _, ref := range []imageReference{
		{TenantID: scope.tenantID, ChatID: scope.chatID, ThreadID: scope.threadID, Label: "face", FileID: "face-file", MIME: "image/jpeg", FileName: "face.jpg"},
		{TenantID: scope.tenantID, ChatID: scope.chatID, ThreadID: scope.threadID, Label: "style", FileID: "style-file", MIME: "image/webp", FileName: "style.webp"},
	} {
		if err := store.upsertImageRef(&ref); err != nil {
			t.Fatalf("upsertImageRef(%s): %v", ref.Label, err)
		}
	}

	refs, err := store.getImageRefsByLabels(scope.tenantID, scope.chatID, scope.threadID, []string{"style", "face"})
	if err != nil {
		t.Fatalf("getImageRefsByLabels() error = %v", err)
	}
	if got, want := refs[0].Label+","+refs[1].Label, "style,face"; got != want {
		t.Fatalf("ref order = %q, want %q", got, want)
	}
}

func assertFormValue(t *testing.T, r *http.Request, key, want string) {
	t.Helper()
	if got := r.FormValue(key); got != want {
		t.Errorf("form %s = %q, want %q", key, got, want)
	}
}

func newTestImageFeatureStore(t *testing.T) *featureStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "gpt-image-edit.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &featureStore{db: db}
	if err := store.migrate(); err != nil {
		t.Fatalf("migrate feature store: %v", err)
	}
	return store
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
