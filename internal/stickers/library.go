package stickers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/channels/media"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	LocalStickerToolName = "find_and_post_local_sticker"
	defaultMetadataName  = "metadata_with_embeddings.json"
)

type Library struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Enabled      *bool  `json:"enabled,omitempty"`
	Recursive    bool   `json:"recursive,omitempty"`
	MetadataPath string `json:"metadata_path,omitempty"`
	MetadataRoot string `json:"metadata_root,omitempty"`
}

type AutoCaptureSettings struct {
	Enabled             *bool  `json:"enabled,omitempty"`
	Library             string `json:"library,omitempty"`
	Path                string `json:"path,omitempty"`
	MetadataPath        string `json:"metadata_path,omitempty"`
	MetadataRoot        string `json:"metadata_root,omitempty"`
	DescriptionProvider string `json:"description_provider,omitempty"`
	DescriptionModel    string `json:"description_model,omitempty"`
}

type Settings struct {
	Libraries         []Library           `json:"libraries,omitempty"`
	AllowedExtensions []string            `json:"allowed_extensions,omitempty"`
	ExcludeTerms      []string            `json:"exclude_terms,omitempty"`
	AutoCapture       AutoCaptureSettings `json:"auto_capture,omitempty"`
}

type MetadataEntry struct {
	ID                string    `json:"id"`
	Path              string    `json:"path"`
	PreviewPath       string    `json:"preview_path,omitempty"`
	Filename          string    `json:"filename"`
	PreviewFilename   string    `json:"preview_filename,omitempty"`
	ContentType       string    `json:"content_type"`
	PreviewMimeType   string    `json:"preview_mime_type,omitempty"`
	StickerType       string    `json:"sticker_type"`
	TelegramFileID    string    `json:"telegram_file_id,omitempty"`
	TelegramPreviewID string    `json:"telegram_preview_id,omitempty"`
	Emoji             string    `json:"emoji,omitempty"`
	SetName           string    `json:"set_name,omitempty"`
	Note              string    `json:"note,omitempty"`
	Description       string    `json:"description,omitempty"`
	Keywords          []string  `json:"keywords,omitempty"`
	SearchText        string    `json:"search_text,omitempty"`
	Embedding         []float32 `json:"embedding,omitempty"`
	AssetHash         string    `json:"asset_hash"`
	PreviewHash       string    `json:"preview_hash,omitempty"`
	SourceChannel     string    `json:"source_channel,omitempty"`
	SourceChannelType string    `json:"source_channel_type,omitempty"`
	SourceChatID      string    `json:"source_chat_id,omitempty"`
	SourceMessageID   string    `json:"source_message_id,omitempty"`
	CapturedAt        int64     `json:"captured_at"`
	LastSeenAt        int64     `json:"last_seen_at"`
	CaptureCount      int       `json:"capture_count"`
}

type MetadataEnvelope struct {
	Stickers []MetadataEntry `json:"stickers"`
}

type CaptureInput struct {
	TenantID           uuid.UUID
	ChannelName        string
	ChannelType        string
	ChatID             string
	MessageID          string
	StickerType        string
	Emoji              string
	SetName            string
	Note               string
	AssetPath          string
	AssetContentType   string
	AssetFileID        string
	PreviewPath        string
	PreviewContentType string
	PreviewFileID      string
}

type CaptureService struct {
	builtinTools store.BuiltinToolStore
	embProvider  store.EmbeddingProvider
	providers    *providers.Registry
	mu           sync.Mutex
}

func NewCaptureService(builtinTools store.BuiltinToolStore, providerReg *providers.Registry) *CaptureService {
	return &CaptureService{builtinTools: builtinTools, providers: providerReg}
}

func (s *CaptureService) SetEmbeddingProvider(provider store.EmbeddingProvider) {
	s.embProvider = provider
}

func (s *CaptureService) CaptureTelegramSticker(ctx context.Context, input CaptureInput) error {
	if s == nil || s.builtinTools == nil {
		return nil
	}
	if strings.TrimSpace(input.AssetPath) == "" && strings.TrimSpace(input.PreviewPath) == "" {
		return fmt.Errorf("no sticker asset or preview to capture")
	}

	settings, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	lib, metadataPath, metadataRoot, err := resolveCaptureLibrary(settings)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := LoadMetadata(metadataPath)
	if err != nil {
		return err
	}

	assetPath, assetHash, err := s.copyIntoLibrary(lib.Path, input.AssetPath, input.AssetContentType, "sticker")
	if err != nil {
		return err
	}
	previewPath := ""
	previewHash := ""
	if strings.TrimSpace(input.PreviewPath) != "" {
		previewPath, previewHash, err = s.copyIntoLibrary(lib.Path, input.PreviewPath, input.PreviewContentType, "sticker_preview")
		if err != nil {
			return err
		}
	}

	now := time.Now().Unix()
	for i := range entries {
		if assetHash != "" && entries[i].AssetHash == assetHash {
			entries[i].LastSeenAt = now
			entries[i].CaptureCount++
			if entries[i].TelegramFileID == "" && strings.TrimSpace(input.AssetFileID) != "" {
				entries[i].TelegramFileID = strings.TrimSpace(input.AssetFileID)
			}
			if previewPath != "" && entries[i].PreviewPath == "" {
				entries[i].PreviewPath = relativeMetadataPath(previewPath, metadataRoot)
				entries[i].PreviewFilename = filepath.Base(previewPath)
				entries[i].PreviewMimeType = input.PreviewContentType
				entries[i].PreviewHash = previewHash
			}
			if entries[i].TelegramPreviewID == "" && strings.TrimSpace(input.PreviewFileID) != "" {
				entries[i].TelegramPreviewID = strings.TrimSpace(input.PreviewFileID)
			}
			if entries[i].Description == "" || len(entries[i].Embedding) == 0 {
				s.enrichEntry(ctx, settings, &entries[i], input, assetPath, previewPath)
			}
			return writeMetadata(metadataPath, entries)
		}
	}

	entry := MetadataEntry{
		ID:                uuid.New().String(),
		Path:              relativeMetadataPath(assetPath, metadataRoot),
		Filename:          filepath.Base(assetPath),
		ContentType:       normalizeMIME(input.AssetContentType, assetPath),
		StickerType:       normalizeStickerType(input.StickerType, input.AssetContentType),
		TelegramFileID:    strings.TrimSpace(input.AssetFileID),
		TelegramPreviewID: strings.TrimSpace(input.PreviewFileID),
		Emoji:             strings.TrimSpace(input.Emoji),
		SetName:           strings.TrimSpace(input.SetName),
		Note:              strings.TrimSpace(input.Note),
		AssetHash:         assetHash,
		PreviewHash:       previewHash,
		SourceChannel:     input.ChannelName,
		SourceChannelType: input.ChannelType,
		SourceChatID:      input.ChatID,
		SourceMessageID:   input.MessageID,
		CapturedAt:        now,
		LastSeenAt:        now,
		CaptureCount:      1,
	}
	if previewPath != "" {
		entry.PreviewPath = relativeMetadataPath(previewPath, metadataRoot)
		entry.PreviewFilename = filepath.Base(previewPath)
		entry.PreviewMimeType = normalizeMIME(input.PreviewContentType, previewPath)
	}
	s.enrichEntry(ctx, settings, &entry, input, assetPath, previewPath)
	entries = append(entries, entry)

	return writeMetadata(metadataPath, entries)
}

func (s *CaptureService) loadSettings(ctx context.Context) (Settings, error) {
	raw, err := s.builtinTools.GetSettings(ctx, LocalStickerToolName)
	if err != nil {
		return Settings{}, err
	}
	return ParseSettings(raw)
}

func ParseSettings(raw json.RawMessage) (Settings, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
		return Settings{}, fmt.Errorf("no local sticker libraries are configured")
	}
	var cfg Settings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Settings{}, fmt.Errorf("invalid %s settings: %w", LocalStickerToolName, err)
	}
	if len(cfg.Libraries) == 0 && cleanPath(cfg.AutoCapture.Path) != "" {
		cfg.Libraries = append(cfg.Libraries, Library{
			Name:         strings.TrimSpace(cfg.AutoCapture.Library),
			Path:         cfg.AutoCapture.Path,
			Enabled:      boolPtr(true),
			MetadataPath: cfg.AutoCapture.MetadataPath,
			MetadataRoot: cfg.AutoCapture.MetadataRoot,
		})
	}
	return cfg, nil
}

func resolveCaptureLibrary(cfg Settings) (Library, string, string, error) {
	if !cfg.AutoCapture.IsEnabled() {
		return Library{}, "", "", fmt.Errorf("local sticker auto capture is disabled")
	}
	targetName := strings.TrimSpace(cfg.AutoCapture.Library)
	for _, lib := range cfg.Libraries {
		if !lib.IsEnabled() {
			continue
		}
		if targetName == "" || matchesLibrary(targetName, lib.Name) {
			metadataPath := resolveMetadataPath(cfg, lib)
			metadataRoot := resolveMetadataRoot(cfg, lib, metadataPath)
			if cleanPath(lib.Path) == "" {
				continue
			}
			return lib, metadataPath, metadataRoot, nil
		}
	}
	return Library{}, "", "", fmt.Errorf("no local sticker library is configured for auto capture")
}

func (s *CaptureService) copyIntoLibrary(root, srcPath, contentType, prefix string) (string, string, error) {
	srcPath = cleanPath(srcPath)
	if srcPath == "" {
		return "", "", fmt.Errorf("missing source sticker path")
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", "", fmt.Errorf("read sticker source: %w", err)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	root = cleanPath(root)
	if root == "" {
		return "", "", fmt.Errorf("missing sticker library path")
	}
	dayDir := filepath.Join(root, "captured", time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create sticker library dir: %w", err)
	}
	ext := extFromMime(contentType)
	if ext == "" {
		ext = filepath.Ext(srcPath)
	}
	if ext == "" {
		ext = ".bin"
	}
	dstPath := filepath.Join(dayDir, fmt.Sprintf("%s_%s%s", prefix, hash[:12], ext))
	if _, err := os.Stat(dstPath); err == nil {
		return dstPath, hash, nil
	}
	if err := os.WriteFile(dstPath, data, 0o644); err != nil {
		return "", "", fmt.Errorf("write sticker library file: %w", err)
	}
	return dstPath, hash, nil
}

func (s *CaptureService) enrichEntry(ctx context.Context, settings Settings, entry *MetadataEntry, input CaptureInput, assetPath, previewPath string) {
	description := strings.TrimSpace(entry.Description)
	keywords := append([]string(nil), entry.Keywords...)
	sourcePath := preferredPreviewPath(previewPath, assetPath)
	sourceMime := entry.PreviewMimeType
	if sourcePath == assetPath || sourceMime == "" {
		sourceMime = entry.ContentType
	}
	if sourcePath != "" {
		if desc, kws, err := s.describeSticker(ctx, input.TenantID, settings.AutoCapture, sourcePath, sourceMime, input); err == nil {
			if desc != "" {
				description = desc
			}
			if len(kws) > 0 {
				keywords = kws
			}
		}
	}
	if description == "" {
		description = fallbackDescription(input, entry.StickerType)
	}
	entry.Description = description
	entry.Keywords = uniqueKeywords(append(keywords, fallbackKeywords(input)...))
	entry.SearchText = buildSearchText(*entry)

	if s.embProvider == nil || strings.TrimSpace(entry.SearchText) == "" {
		return
	}
	if vectors, err := s.embProvider.Embed(ctx, []string{entry.SearchText}); err == nil && len(vectors) > 0 {
		entry.Embedding = append([]float32(nil), vectors[0]...)
	}
}

func (s *CaptureService) describeSticker(ctx context.Context, tenantID uuid.UUID, captureCfg AutoCaptureSettings, imagePath, mime string, input CaptureInput) (string, []string, error) {
	if s.providers == nil {
		return "", nil, fmt.Errorf("no provider registry configured")
	}
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return "", nil, err
	}
	if mime == "" {
		mime = normalizeMIME("", imagePath)
	}
	providerName := strings.TrimSpace(captureCfg.DescriptionProvider)
	model := strings.TrimSpace(captureCfg.DescriptionModel)
	candidates := []string{}
	if providerName != "" {
		candidates = append(candidates, providerName)
	} else {
		candidates = append(candidates, "openai-compat", "openrouter", "gemini")
	}

	prompt := "Describe this sticker for later retrieval in one short sentence. Then write a second line that starts with 'Keywords:' followed by 3 to 8 short lowercase keywords about the visible subject, mood, and action."
	callCtx := store.WithTenantID(ctx, tenantID)
	for _, name := range candidates {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		p, err := s.providers.Get(callCtx, name)
		if err != nil {
			continue
		}
		resp, err := p.Chat(callCtx, providers.ChatRequest{
			Model: model,
			Messages: []providers.Message{{
				Role:    "user",
				Content: prompt,
				Images: []providers.ImageContent{{
					MimeType: mime,
					Data:     base64.StdEncoding.EncodeToString(data),
				}},
			}},
			Options: map[string]any{
				providers.OptMaxTokens:   200,
				providers.OptTemperature: 0.2,
			},
		})
		if err != nil {
			continue
		}
		return parseCaptionResponse(resp.Content), parseCaptionKeywords(resp.Content), nil
	}
	return "", nil, fmt.Errorf("no vision provider available for sticker capture")
}

func parseCaptionResponse(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return s
	}
	first := strings.TrimSpace(lines[0])
	first = strings.TrimPrefix(first, "-")
	first = strings.TrimSpace(first)
	return first
}

func parseCaptionKeywords(s string) []string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "keywords:") {
			continue
		}
		raw := strings.TrimSpace(line[len("keywords:"):])
		parts := strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == '|'
		})
		return uniqueKeywords(parts)
	}
	return nil
}

func fallbackDescription(input CaptureInput, stickerType string) string {
	parts := []string{}
	switch stickerType {
	case "video":
		parts = append(parts, "video sticker")
	case "animated":
		parts = append(parts, "animated sticker")
	default:
		parts = append(parts, "sticker")
	}
	if strings.TrimSpace(input.Emoji) != "" {
		parts = append(parts, "emoji "+strings.TrimSpace(input.Emoji))
	}
	if strings.TrimSpace(input.SetName) != "" {
		parts = append(parts, "from set "+strings.TrimSpace(input.SetName))
	}
	return strings.Join(parts, " ")
}

func fallbackKeywords(input CaptureInput) []string {
	var out []string
	if v := normalizeText(input.Emoji); v != "" {
		out = append(out, v)
	}
	if v := normalizeText(input.SetName); v != "" {
		out = append(out, strings.Fields(v)...)
	}
	return out
}

func buildSearchText(entry MetadataEntry) string {
	parts := []string{
		entry.Description,
		entry.Note,
		entry.StickerType,
		entry.Emoji,
		entry.SetName,
		strings.Join(entry.Keywords, " "),
	}
	var nonEmpty []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}
	return strings.Join(nonEmpty, "\n")
}

func LoadMetadata(path string) ([]MetadataEntry, error) {
	path = cleanPath(path)
	if path == "" {
		return nil, fmt.Errorf("missing sticker metadata path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []MetadataEntry
	trimmed := strings.TrimSpace(string(data))
	switch {
	case trimmed == "":
		return nil, nil
	case strings.HasPrefix(trimmed, "["):
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, err
		}
	default:
		var env MetadataEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, err
		}
		entries = env.Stickers
	}
	return entries, nil
}

func writeMetadata(path string, entries []MetadataEntry) error {
	path = cleanPath(path)
	if path == "" {
		return fmt.Errorf("missing sticker metadata path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(MetadataEnvelope{Stickers: entries}, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func resolveMetadataPath(cfg Settings, lib Library) string {
	if path := cleanPath(lib.MetadataPath); path != "" {
		return path
	}
	if path := cleanPath(cfg.AutoCapture.MetadataPath); path != "" {
		return path
	}
	root := cleanPath(lib.Path)
	if root == "" {
		root = cleanPath(cfg.AutoCapture.Path)
	}
	if root == "" {
		return ""
	}
	return filepath.Join(root, defaultMetadataName)
}

func resolveMetadataRoot(cfg Settings, lib Library, metadataPath string) string {
	if root := cleanPath(lib.MetadataRoot); root != "" {
		return root
	}
	if root := cleanPath(cfg.AutoCapture.MetadataRoot); root != "" {
		return root
	}
	if root := cleanPath(lib.Path); root != "" {
		return root
	}
	if root := cleanPath(cfg.AutoCapture.Path); root != "" {
		return root
	}
	if metadataPath == "" {
		return ""
	}
	return filepath.Dir(metadataPath)
}

func relativeMetadataPath(path, root string) string {
	path = cleanPath(path)
	root = cleanPath(root)
	if path == "" || root == "" {
		return path
	}
	if rel, err := filepath.Rel(root, path); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return path
}

func ResolveEntryPath(entryPath, metadataRoot string) string {
	entryPath = strings.TrimSpace(entryPath)
	if entryPath == "" {
		return ""
	}
	if filepath.IsAbs(entryPath) {
		return cleanPath(entryPath)
	}
	return cleanPath(filepath.Join(metadataRoot, filepath.FromSlash(entryPath)))
}

func preferredPreviewPath(previewPath, assetPath string) string {
	if cleanPath(previewPath) != "" {
		return cleanPath(previewPath)
	}
	return cleanPath(assetPath)
}

func normalizeStickerType(kind, mime string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "video", "animated", "static":
		if kind == "static" {
			return "image"
		}
		return kind
	}
	switch {
	case strings.Contains(strings.ToLower(mime), "tgsticker"):
		return "animated"
	case strings.HasPrefix(strings.ToLower(mime), "video/"):
		return "video"
	default:
		return "image"
	}
}

func normalizeMIME(mimeType, path string) string {
	mimeType = strings.TrimSpace(mimeType)
	if mimeType != "" {
		return mimeType
	}
	if path == "" {
		return "application/octet-stream"
	}
	if ct := media.DetectMIMEType(path); ct != "" && ct != "application/octet-stream" {
		return ct
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".webp":
		return "image/webp"
	case ".webm":
		return "video/webm"
	case ".tgs":
		return "application/x-tgsticker"
	default:
		return "application/octet-stream"
	}
}

func extFromMime(mimeType string) string {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case strings.HasPrefix(mimeType, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(mimeType, "image/png"):
		return ".png"
	case strings.HasPrefix(mimeType, "image/gif"):
		return ".gif"
	case strings.HasPrefix(mimeType, "image/webp"):
		return ".webp"
	case strings.HasPrefix(mimeType, "video/mp4"):
		return ".mp4"
	case strings.HasPrefix(mimeType, "video/webm"):
		return ".webm"
	case strings.HasPrefix(mimeType, "audio/ogg"), strings.HasPrefix(mimeType, "audio/opus"):
		return ".ogg"
	case strings.HasPrefix(mimeType, "audio/mpeg"):
		return ".mp3"
	case strings.HasPrefix(mimeType, "audio/wav"):
		return ".wav"
	case mimeType == "application/x-tgsticker", mimeType == "application/x-telegram-sticker", mimeType == "application/x-tgs":
		return ".tgs"
	default:
		return ""
	}
}

func normalizeText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	return strings.Join(fields, " ")
}

func uniqueKeywords(items []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, item := range items {
		norm := normalizeText(item)
		if norm == "" {
			continue
		}
		for _, field := range strings.Fields(norm) {
			if field == "" || seen[field] {
				continue
			}
			seen[field] = true
			out = append(out, field)
		}
	}
	return out
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, path[2:])
			}
		}
	}
	return filepath.Clean(path)
}

func matchesLibrary(filter, name string) bool {
	filter = normalizeText(filter)
	name = normalizeText(name)
	if filter == "" {
		return true
	}
	return name == filter || strings.Contains(name, filter)
}

func (lib Library) IsEnabled() bool {
	return lib.Enabled == nil || *lib.Enabled
}

func (cfg AutoCaptureSettings) IsEnabled() bool {
	return cfg.Enabled != nil && *cfg.Enabled
}

func boolPtr(v bool) *bool { return &v }
