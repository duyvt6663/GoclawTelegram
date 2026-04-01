package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/memory"
	"github.com/nextlevelbuilder/goclaw/internal/stickers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type localStickerCandidate struct {
	Library        string
	Path           string
	Base           string
	Score          float64
	SemanticScore  float64
	TextScore      int
	Description    string
	Keywords       []string
	ContentType    string
	TelegramFileID string
}

type FindAndPostLocalStickerTool struct {
	embProvider store.EmbeddingProvider
}

func NewFindAndPostLocalStickerTool() *FindAndPostLocalStickerTool {
	return &FindAndPostLocalStickerTool{}
}

func (t *FindAndPostLocalStickerTool) SetEmbeddingProvider(provider store.EmbeddingProvider) {
	t.embProvider = provider
}

func (t *FindAndPostLocalStickerTool) Name() string { return stickers.LocalStickerToolName }

func (t *FindAndPostLocalStickerTool) RequiredChannelTypes() []string { return []string{"telegram"} }

func (t *FindAndPostLocalStickerTool) Description() string {
	return "Pick a previously captured Telegram sticker from configured libraries using semantic search over stored sticker metadata and attach it to the current reply. Prefer this for quick reaction stickers, callbacks, and recurring favorites in Telegram chats."
}

func (t *FindAndPostLocalStickerTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Optional short reaction, mood, or callback to match against stored sticker metadata. Keep it short and expressive, for example: 'side eye', 'smug cat', 'applause', 'dead inside'. Omit or use 'random' for a random saved sticker.",
			},
			"library": map[string]any{
				"type":        "string",
				"description": "Optional configured sticker library name to restrict the search.",
			},
		},
	}
}

func (t *FindAndPostLocalStickerTool) Execute(ctx context.Context, args map[string]any) *Result {
	settings, err := resolveLocalStickerSettings(ctx)
	if err != nil {
		return ErrorResult(err.Error())
	}

	query := strings.TrimSpace(GetParamString(args, "query", ""))
	library := strings.TrimSpace(GetParamString(args, "library", ""))

	candidates, err := collectLocalStickerCandidates(ctx, t.embProvider, settings, library, query)
	if err != nil {
		return ErrorResult(err.Error())
	}
	if len(candidates) == 0 {
		return ErrorResult(localStickerNoMatchMessage(query, library, settings))
	}

	chosen := chooseLocalStickerCandidate(candidates)
	mediaPath := localStickerMediaPath(ctx, chosen)
	mimeType := strings.TrimSpace(chosen.ContentType)
	if mimeType == "" {
		mimeType = mimeFromPath(chosen.Path)
	}

	queryLabel := query
	if strings.TrimSpace(queryLabel) == "" {
		queryLabel = "random"
	}

	forLLM := fmt.Sprintf(
		"Attached a saved sticker from library %q for query %q.\nFilename: %s\nPath: %s",
		chosen.Library,
		queryLabel,
		filepath.Base(chosen.Path),
		mediaPath,
	)
	if chosen.Description != "" {
		forLLM += "\nDescription: " + chosen.Description
	}
	if len(chosen.Keywords) > 0 {
		forLLM += "\nKeywords: " + strings.Join(chosen.Keywords, ", ")
	}
	forLLM += "\nWrite only the caption or short context you want the user to see."

	return &Result{
		ForLLM: forLLM,
		Media: []bus.MediaFile{{
			Path:     mediaPath,
			MimeType: mimeType,
		}},
		Deliverable: fmt.Sprintf("[Attached saved sticker: %s]\nLibrary: %s", filepath.Base(chosen.Path), chosen.Library),
	}
}

func resolveLocalStickerSettings(ctx context.Context) (stickers.Settings, error) {
	settings := BuiltinToolSettingsFromCtx(ctx)
	if settings == nil {
		return stickers.Settings{}, fmt.Errorf("no local sticker libraries are configured for %s", stickers.LocalStickerToolName)
	}
	raw, ok := settings[stickers.LocalStickerToolName]
	if !ok || len(raw) == 0 {
		return stickers.Settings{}, fmt.Errorf("no local sticker libraries are configured for %s", stickers.LocalStickerToolName)
	}
	return stickers.ParseSettings(raw)
}

func collectLocalStickerCandidates(
	ctx context.Context,
	embProvider store.EmbeddingProvider,
	cfg stickers.Settings,
	libraryFilter, query string,
) ([]localStickerCandidate, error) {
	allowedExts := normalizeLocalStickerExts(cfg.AllowedExtensions)
	excludeTerms := normalizeLocalMemeTerms(cfg.ExcludeTerms)
	queryEmbedding, err := buildLocalMemeQueryEmbedding(ctx, embProvider, query)
	if err != nil {
		// Fall back to text matching only.
		queryEmbedding = nil
	}

	var candidates []localStickerCandidate
	var matchedLibrary bool
	for _, lib := range cfg.Libraries {
		if !lib.IsEnabled() {
			continue
		}
		if !matchesLocalStickerLibrary(libraryFilter, lib.Name) {
			continue
		}
		matchedLibrary = true

		libraryName := strings.TrimSpace(lib.Name)
		if libraryName == "" {
			libraryName = defaultLocalMemeLibraryKey
		}

		metadataPath := resolveLocalStickerMetadataPath(cfg, lib)
		if metadataPath != "" {
			metadataRoot := resolveLocalStickerMetadataRoot(cfg, lib, metadataPath)
			entries, err := stickers.LoadMetadata(metadataPath)
			if err == nil {
				candidates = append(candidates, collectLocalStickerMetadataCandidates(query, libraryName, metadataRoot, entries, queryEmbedding, excludeTerms, allowedExts)...)
			}
		}

		// Last-resort fallback: scan files by basename if metadata is absent or incomplete.
		root := cleanLocalMemePath(lib.Path)
		if root == "" {
			continue
		}
		for _, file := range collectLocalStickerFileFallback(root, libraryName, excludeTerms, allowedExts) {
			if score := float64(scoreLocalMemeCandidate(query, file.Base)); score > 0 {
				candidates = append(candidates, localStickerCandidate{
					Library:     file.Library,
					Path:        file.Path,
					Base:        file.Base,
					Score:       score,
					ContentType: mimeFromPath(file.Path),
				})
			}
		}
	}
	if strings.TrimSpace(libraryFilter) != "" && !matchedLibrary {
		return nil, fmt.Errorf("local sticker library %q is not configured", libraryFilter)
	}
	return dedupeLocalStickerCandidates(candidates), nil
}

func collectLocalStickerMetadataCandidates(
	query, libraryName, metadataRoot string,
	entries []stickers.MetadataEntry,
	queryEmbedding []float32,
	excludeTerms []string,
	allowedExts map[string]bool,
) []localStickerCandidate {
	bestByPath := make(map[string]localStickerCandidate)
	for _, entry := range entries {
		path := stickers.ResolveEntryPath(entry.Path, metadataRoot)
		if path == "" {
			continue
		}
		base := filepath.Base(path)
		if !allowedExts[strings.ToLower(filepath.Ext(base))] {
			continue
		}
		if shouldExcludeLocalMeme(strings.Join(localStickerMetadataTexts(entry), " "), excludeTerms) {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		textScore := scoreLocalMemeText(query, localStickerMetadataTexts(entry)...)
		semanticScore := 0.0
		if len(queryEmbedding) > 0 && len(entry.Embedding) > 0 {
			semanticScore = memory.CosineSimilarity(queryEmbedding, entry.Embedding)
		}
		score := computeLocalMemeCandidateScore(query, semanticScore, textScore)
		if score <= 0 {
			continue
		}
		candidate := localStickerCandidate{
			Library:        libraryName,
			Path:           path,
			Base:           base,
			Score:          score,
			SemanticScore:  semanticScore,
			TextScore:      textScore,
			Description:    strings.TrimSpace(entry.Description),
			Keywords:       append([]string(nil), entry.Keywords...),
			ContentType:    entry.ContentType,
			TelegramFileID: strings.TrimSpace(entry.TelegramFileID),
		}
		prev, exists := bestByPath[path]
		if !exists || localStickerCandidateRanksAbove(candidate, prev) {
			bestByPath[path] = candidate
		}
	}
	candidates := make([]localStickerCandidate, 0, len(bestByPath))
	for _, candidate := range bestByPath {
		candidates = append(candidates, candidate)
	}
	return candidates
}

func collectLocalStickerFileFallback(
	root, libraryName string,
	excludeTerms []string,
	allowedExts map[string]bool,
) []localMemeFileRecord {
	files, err := collectLocalMemeFiles(root, true, allowedExts, defaultTelegramMediaMax, excludeTerms, libraryName)
	if err != nil {
		return nil
	}
	return files
}

func dedupeLocalStickerCandidates(candidates []localStickerCandidate) []localStickerCandidate {
	bestByPath := make(map[string]localStickerCandidate)
	for _, candidate := range candidates {
		if prev, exists := bestByPath[candidate.Path]; !exists || localStickerCandidateRanksAbove(candidate, prev) {
			bestByPath[candidate.Path] = candidate
		}
	}
	out := make([]localStickerCandidate, 0, len(bestByPath))
	for _, candidate := range bestByPath {
		out = append(out, candidate)
	}
	return out
}

func localStickerCandidateRanksAbove(a, b localStickerCandidate) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.SemanticScore != b.SemanticScore {
		return a.SemanticScore > b.SemanticScore
	}
	if a.TextScore != b.TextScore {
		return a.TextScore > b.TextScore
	}
	return a.Base < b.Base
}

func chooseLocalStickerCandidate(candidates []localStickerCandidate) localStickerCandidate {
	sort.SliceStable(candidates, func(i, j int) bool {
		return localStickerCandidateRanksAbove(candidates[i], candidates[j])
	})
	if len(candidates) == 1 {
		return candidates[0]
	}
	topScore := candidates[0].Score
	topCount := 1
	for topCount < len(candidates) && candidates[topCount].Score == topScore {
		topCount++
	}
	if topCount == 1 {
		return candidates[0]
	}
	wrapped := make([]localMemeCandidate, 0, topCount)
	for _, c := range candidates[:topCount] {
		wrapped = append(wrapped, localMemeCandidate{
			Base:          c.Base,
			Score:         c.Score,
			SemanticScore: c.SemanticScore,
			TextScore:     c.TextScore,
		})
	}
	chosen := chooseLocalMemeCandidate(wrapped)
	for _, candidate := range candidates[:topCount] {
		if candidate.Base == chosen.Base && candidate.Score == chosen.Score && candidate.SemanticScore == chosen.SemanticScore {
			return candidate
		}
	}
	return candidates[0]
}

func localStickerMetadataTexts(entry stickers.MetadataEntry) []string {
	texts := []string{
		entry.Description,
		entry.SearchText,
		entry.Note,
		entry.Emoji,
		entry.SetName,
		entry.StickerType,
		entry.Filename,
	}
	texts = append(texts, entry.Keywords...)
	return texts
}

func resolveLocalStickerMetadataPath(cfg stickers.Settings, lib stickers.Library) string {
	if path := cleanLocalMemePath(lib.MetadataPath); path != "" {
		return path
	}
	if path := cleanLocalMemePath(cfg.AutoCapture.MetadataPath); path != "" {
		return path
	}
	root := cleanLocalMemePath(lib.Path)
	if root == "" {
		return ""
	}
	return filepath.Join(root, "metadata_with_embeddings.json")
}

func resolveLocalStickerMetadataRoot(cfg stickers.Settings, lib stickers.Library, metadataPath string) string {
	if root := cleanLocalMemePath(lib.MetadataRoot); root != "" {
		return root
	}
	if root := cleanLocalMemePath(cfg.AutoCapture.MetadataRoot); root != "" {
		return root
	}
	if root := cleanLocalMemePath(lib.Path); root != "" {
		return root
	}
	if metadataPath == "" {
		return ""
	}
	return filepath.Dir(metadataPath)
}

func normalizeLocalStickerExts(exts []string) map[string]bool {
	if len(exts) == 0 {
		return map[string]bool{
			".tgs":  true,
			".webm": true,
			".webp": true,
		}
	}
	return normalizeLocalMemeExts(exts)
}

func matchesLocalStickerLibrary(filter, name string) bool {
	return matchesLocalMemeLibrary(filter, name)
}

func localStickerNoMatchMessage(query, library string, cfg stickers.Settings) string {
	var hints []string
	for _, lib := range cfg.Libraries {
		if !lib.IsEnabled() {
			continue
		}
		name := strings.TrimSpace(lib.Name)
		if name == "" {
			name = defaultLocalMemeLibraryKey
		}
		hints = append(hints, name)
	}
	sort.Strings(hints)
	queryLabel := strings.TrimSpace(query)
	if queryLabel == "" {
		queryLabel = "random"
	}
	if library != "" {
		return fmt.Sprintf("no saved sticker matched %q in library %q", queryLabel, library)
	}
	if len(hints) == 0 {
		return fmt.Sprintf("no saved sticker matched %q", queryLabel)
	}
	return fmt.Sprintf("no saved sticker matched %q. Configured libraries: %s", queryLabel, strings.Join(hints, ", "))
}

func localStickerMediaPath(ctx context.Context, candidate localStickerCandidate) string {
	if ToolChannelTypeFromCtx(ctx) == "telegram" && candidate.TelegramFileID != "" {
		return telegramStickerFileIDURL(candidate.TelegramFileID)
	}
	return candidate.Path
}

func telegramStickerFileIDURL(fileID string) string {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return ""
	}
	return "telegram-file-id:" + fileID
}

func withLocalStickerSettings(tctx context.Context, settings stickers.Settings) context.Context {
	raw, _ := json.Marshal(settings)
	return WithBuiltinToolSettings(tctx, BuiltinToolSettings{
		stickers.LocalStickerToolName: raw,
	})
}
