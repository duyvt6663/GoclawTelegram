package tools

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/memory"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	localMemeToolName          = "find_and_post_local_meme"
	defaultTelegramMediaMax    = 20 * 1024 * 1024
	defaultGenericMediaMax     = 50 * 1024 * 1024
	defaultLocalMemeLibraryKey = "default"
)

var (
	defaultLocalMemeExts = map[string]bool{
		".gif":  true,
		".jpeg": true,
		".jpg":  true,
		".mov":  true,
		".mp4":  true,
		".png":  true,
		".webm": true,
		".webp": true,
	}
	localMemeStopwords = map[string]bool{
		"a": true, "an": true, "any": true, "clip": true, "clips": true,
		"find": true, "for": true, "gif": true, "gifs": true, "green": true,
		"greenscreen": true, "local": true, "me": true, "meme": true, "memes": true,
		"of": true, "please": true, "post": true, "screen": true, "send": true,
		"show": true, "some": true, "the": true, "to": true, "video": true, "videos": true,
	}
)

type localMemeLibrary struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Enabled      *bool  `json:"enabled,omitempty"`
	Recursive    bool   `json:"recursive,omitempty"`
	MetadataPath string `json:"metadata_path,omitempty"`
	MetadataRoot string `json:"metadata_root,omitempty"`
}

type localMemeSettings struct {
	// Legacy single-library shorthand for quick setup.
	Name         string `json:"name,omitempty"`
	Path         string `json:"path,omitempty"`
	Recursive    bool   `json:"recursive,omitempty"`
	MetadataPath string `json:"metadata_path,omitempty"`
	MetadataRoot string `json:"metadata_root,omitempty"`

	Libraries         []localMemeLibrary `json:"libraries,omitempty"`
	AllowedExtensions []string           `json:"allowed_extensions,omitempty"`
	MaxBytes          int64              `json:"max_bytes,omitempty"`
	ExcludeTerms      []string           `json:"exclude_terms,omitempty"`
}

type localMemeMetadataEntry struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	Animal      string    `json:"animal"`
	Path        string    `json:"path"`
	Description string    `json:"description"`
	Scenarios   []string  `json:"scenarios"`
	Embedding   []float32 `json:"embedding"`
}

type localMemeMetadataEnvelope struct {
	Memes []localMemeMetadataEntry `json:"memes"`
}

type localMemeFileRecord struct {
	Library string
	Path    string
	Base    string
	Size    int64
}

type localMemeCandidate struct {
	Library       string
	Path          string
	Base          string
	Size          int64
	Score         float64
	SemanticScore float64
	TextScore     int
	Description   string
	Scenarios     []string
}

// FindAndPostLocalMemeTool picks a meme clip/image from configured local libraries
// and attaches it to the current reply.
type FindAndPostLocalMemeTool struct {
	embProvider store.EmbeddingProvider
}

func NewFindAndPostLocalMemeTool() *FindAndPostLocalMemeTool {
	return &FindAndPostLocalMemeTool{}
}

func (t *FindAndPostLocalMemeTool) SetEmbeddingProvider(provider store.EmbeddingProvider) {
	t.embProvider = provider
}

func (t *FindAndPostLocalMemeTool) Name() string { return localMemeToolName }

func (t *FindAndPostLocalMemeTool) Description() string {
	return "Pick an existing local meme GIF, video, or image from configured libraries using semantic search over local metadata and attach it to the current reply. Use this for reaction clips or meme punches from local files before falling back to web search."
}

func (t *FindAndPostLocalMemeTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Optional short reaction, joke beat, or vibe to match against configured local meme GIF, video, or image metadata. Keep it short, for example: 'caught in 4k', 'suspicious cat', 'victory lap'. Omit or use 'random' for a random local clip.",
			},
			"library": map[string]any{
				"type":        "string",
				"description": "Optional configured library name to restrict the search to one meme library.",
			},
		},
	}
}

func (t *FindAndPostLocalMemeTool) Execute(ctx context.Context, args map[string]any) *Result {
	settings, err := resolveLocalMemeSettings(ctx)
	if err != nil {
		return ErrorResult(err.Error())
	}

	query := strings.TrimSpace(GetParamString(args, "query", ""))
	library := strings.TrimSpace(GetParamString(args, "library", ""))

	candidates, err := collectLocalMemeCandidates(ctx, t.embProvider, settings, library, query, ToolChannelFromCtx(ctx))
	if err != nil {
		return ErrorResult(err.Error())
	}
	if len(candidates) == 0 {
		return ErrorResult(localMemeNoMatchMessage(query, library, settings))
	}

	chosen := chooseLocalMemeCandidate(candidates)
	mimeType := mimeFromPath(chosen.Path)

	queryLabel := query
	if strings.TrimSpace(queryLabel) == "" {
		queryLabel = "random"
	}

	var metaLines []string
	if chosen.Description != "" {
		metaLines = append(metaLines, "Description: "+chosen.Description)
	}
	if len(chosen.Scenarios) > 0 {
		scenarios := append([]string(nil), chosen.Scenarios...)
		if len(scenarios) > 3 {
			scenarios = scenarios[:3]
		}
		metaLines = append(metaLines, "Scenarios: "+strings.Join(scenarios, "; "))
	}

	forLLM := fmt.Sprintf(
		"Attached a local meme from library %q for query %q.\nFilename: %s\nPath: %s",
		chosen.Library,
		queryLabel,
		filepath.Base(chosen.Path),
		chosen.Path,
	)
	if len(metaLines) > 0 {
		forLLM += "\n" + strings.Join(metaLines, "\n")
	}
	forLLM += "\nWrite only the caption or short context you want the user to see."

	return &Result{
		ForLLM: forLLM,
		Media: []bus.MediaFile{{
			Path:     chosen.Path,
			MimeType: mimeType,
		}},
		Deliverable: fmt.Sprintf("[Attached local meme: %s]\nLibrary: %s", filepath.Base(chosen.Path), chosen.Library),
	}
}

func resolveLocalMemeSettings(ctx context.Context) (localMemeSettings, error) {
	settings := BuiltinToolSettingsFromCtx(ctx)
	if settings == nil {
		return localMemeSettings{}, fmt.Errorf("no local meme libraries are configured for %s", localMemeToolName)
	}

	raw, ok := settings[localMemeToolName]
	if !ok || len(raw) == 0 {
		return localMemeSettings{}, fmt.Errorf("no local meme libraries are configured for %s", localMemeToolName)
	}

	var cfg localMemeSettings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return localMemeSettings{}, fmt.Errorf("invalid %s settings: %w", localMemeToolName, err)
	}

	if cfg.Path != "" && len(cfg.Libraries) == 0 {
		cfg.Libraries = []localMemeLibrary{{
			Name:         cfg.Name,
			Path:         cfg.Path,
			Enabled:      boolPtr(true),
			Recursive:    cfg.Recursive,
			MetadataPath: cfg.MetadataPath,
			MetadataRoot: cfg.MetadataRoot,
		}}
	}

	if len(cfg.Libraries) == 0 {
		return localMemeSettings{}, fmt.Errorf("no local meme libraries are configured for %s", localMemeToolName)
	}

	return cfg, nil
}

func collectLocalMemeCandidates(
	ctx context.Context,
	embProvider store.EmbeddingProvider,
	cfg localMemeSettings,
	libraryFilter, query, channel string,
) ([]localMemeCandidate, error) {
	allowedExts := normalizeLocalMemeExts(cfg.AllowedExtensions)
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultLocalMemeMaxBytes(channel)
	}
	excludeTerms := normalizeLocalMemeTerms(cfg.ExcludeTerms)

	queryEmbedding, err := buildLocalMemeQueryEmbedding(ctx, embProvider, query)
	if err != nil {
		slog.Warn("find_and_post_local_meme: query embedding failed, falling back to metadata text search", "error", err)
	}

	metadataCache := make(map[string][]localMemeMetadataEntry)
	var candidates []localMemeCandidate
	var matchedLibrary bool

	for _, lib := range cfg.Libraries {
		if !localMemeLibraryEnabled(lib) {
			continue
		}
		if !matchesLocalMemeLibrary(libraryFilter, lib.Name) {
			continue
		}
		matchedLibrary = true

		libraryName := strings.TrimSpace(lib.Name)
		if libraryName == "" {
			libraryName = defaultLocalMemeLibraryKey
		}

		root := cleanLocalMemePath(lib.Path)
		if root == "" {
			continue
		}

		files, err := collectLocalMemeFiles(root, lib.Recursive, allowedExts, maxBytes, excludeTerms, libraryName)
		if err != nil || len(files) == 0 {
			continue
		}

		usedPaths := make(map[string]bool)
		metadataPath := resolveLocalMemeMetadataPath(cfg, lib)
		if metadataPath != "" {
			entries, err := loadLocalMemeMetadata(metadataPath, metadataCache)
			if err != nil {
				slog.Warn("find_and_post_local_meme: metadata load failed", "path", metadataPath, "error", err)
			} else {
				metadataRoot := resolveLocalMemeMetadataRoot(cfg, lib, metadataPath)
				for _, candidate := range collectLocalMemeMetadataCandidates(query, root, metadataRoot, files, entries, queryEmbedding, excludeTerms) {
					candidates = append(candidates, candidate)
					usedPaths[filepath.Clean(candidate.Path)] = true
				}
			}
		}

		for _, file := range files {
			if usedPaths[filepath.Clean(file.Path)] {
				continue
			}
			score := float64(scoreLocalMemeCandidate(query, file.Base))
			if score <= 0 {
				continue
			}
			candidates = append(candidates, localMemeCandidate{
				Library: libraryName,
				Path:    file.Path,
				Base:    file.Base,
				Size:    file.Size,
				Score:   score,
			})
		}
	}

	if strings.TrimSpace(libraryFilter) != "" && !matchedLibrary {
		return nil, fmt.Errorf("local meme library %q is not configured", libraryFilter)
	}
	return candidates, nil
}

func collectLocalMemeFiles(
	root string,
	recursive bool,
	allowedExts map[string]bool,
	maxBytes int64,
	excludeTerms []string,
	libraryName string,
) ([]localMemeFileRecord, error) {
	entries, err := listLocalMemeFiles(root, recursive)
	if err != nil {
		return nil, err
	}

	files := make([]localMemeFileRecord, 0, len(entries))
	for _, path := range entries {
		base := filepath.Base(path)
		if shouldExcludeLocalMeme(base, excludeTerms) {
			continue
		}
		if !allowedExts[strings.ToLower(filepath.Ext(base))] {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if maxBytes > 0 && info.Size() > maxBytes {
			continue
		}
		files = append(files, localMemeFileRecord{
			Library: libraryName,
			Path:    filepath.Clean(path),
			Base:    base,
			Size:    info.Size(),
		})
	}
	return files, nil
}

func collectLocalMemeMetadataCandidates(
	query, libraryRoot, metadataRoot string,
	files []localMemeFileRecord,
	entries []localMemeMetadataEntry,
	queryEmbedding []float32,
	excludeTerms []string,
) []localMemeCandidate {
	if len(files) == 0 || len(entries) == 0 {
		return nil
	}

	filesByBase := make(map[string][]localMemeFileRecord)
	filesByPath := make(map[string]localMemeFileRecord)
	for _, file := range files {
		filesByBase[strings.ToLower(file.Base)] = append(filesByBase[strings.ToLower(file.Base)], file)
		filesByPath[filepath.Clean(file.Path)] = file
	}

	bestByPath := make(map[string]localMemeCandidate)
	for _, entry := range entries {
		if shouldExcludeLocalMeme(strings.Join(localMemeMetadataTexts(entry), " "), excludeTerms) {
			continue
		}

		file, ok := matchLocalMemeMetadataEntry(entry, libraryRoot, metadataRoot, filesByBase, filesByPath)
		if !ok {
			continue
		}

		textScore := scoreLocalMemeText(query, localMemeMetadataTexts(entry)...)
		semanticScore := 0.0
		if len(queryEmbedding) > 0 && len(entry.Embedding) > 0 {
			semanticScore = memory.CosineSimilarity(queryEmbedding, entry.Embedding)
		}
		score := computeLocalMemeCandidateScore(query, semanticScore, textScore)
		if score <= 0 {
			continue
		}

		candidate := localMemeCandidate{
			Library:       file.Library,
			Path:          file.Path,
			Base:          file.Base,
			Size:          file.Size,
			Score:         score,
			SemanticScore: semanticScore,
			TextScore:     textScore,
			Description:   strings.TrimSpace(entry.Description),
			Scenarios:     append([]string(nil), entry.Scenarios...),
		}
		prev, exists := bestByPath[file.Path]
		if !exists || candidateRanksAbove(candidate, prev) {
			bestByPath[file.Path] = candidate
		}
	}

	candidates := make([]localMemeCandidate, 0, len(bestByPath))
	for _, candidate := range bestByPath {
		candidates = append(candidates, candidate)
	}
	return candidates
}

func candidateRanksAbove(a, b localMemeCandidate) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.SemanticScore != b.SemanticScore {
		return a.SemanticScore > b.SemanticScore
	}
	if a.TextScore != b.TextScore {
		return a.TextScore > b.TextScore
	}
	if a.Size != b.Size {
		return a.Size < b.Size
	}
	return a.Base < b.Base
}

func matchLocalMemeMetadataEntry(
	entry localMemeMetadataEntry,
	libraryRoot, metadataRoot string,
	filesByBase map[string][]localMemeFileRecord,
	filesByPath map[string]localMemeFileRecord,
) (localMemeFileRecord, bool) {
	for _, candidatePath := range localMemeMetadataCandidatePaths(entry, libraryRoot, metadataRoot) {
		if file, ok := filesByPath[filepath.Clean(candidatePath)]; ok {
			return file, true
		}
	}

	base := strings.TrimSpace(entry.Filename)
	if base == "" {
		base = filepath.Base(strings.TrimSpace(entry.Path))
	}
	if base == "" {
		return localMemeFileRecord{}, false
	}

	matches := filesByBase[strings.ToLower(base)]
	if len(matches) == 0 {
		return localMemeFileRecord{}, false
	}
	return matches[0], true
}

func localMemeMetadataCandidatePaths(entry localMemeMetadataEntry, libraryRoot, metadataRoot string) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(path string) {
		path = cleanLocalMemePath(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}

	rawPath := strings.TrimSpace(entry.Path)
	if rawPath != "" && filepath.IsAbs(rawPath) {
		add(rawPath)
	}
	if metadataRoot != "" && rawPath != "" {
		add(filepath.Join(metadataRoot, rawPath))
	}
	if libraryRoot != "" {
		if rawPath != "" {
			add(filepath.Join(libraryRoot, filepath.Base(rawPath)))
		}
		if entry.Filename != "" {
			add(filepath.Join(libraryRoot, entry.Filename))
		}
	}
	return out
}

func localMemeMetadataTexts(entry localMemeMetadataEntry) []string {
	texts := []string{
		entry.ID,
		entry.Filename,
		entry.Animal,
		entry.Path,
		entry.Description,
	}
	texts = append(texts, entry.Scenarios...)
	return texts
}

func computeLocalMemeCandidateScore(query string, semanticScore float64, textScore int) float64 {
	if isRandomLocalMemeQuery(query) {
		return 1
	}
	if semanticScore <= 0 && textScore <= 0 {
		return 0
	}

	score := float64(textScore * 4)
	if semanticScore > 0 {
		score += semanticScore * 1000
	}
	return score
}

func buildLocalMemeQueryEmbedding(ctx context.Context, provider store.EmbeddingProvider, query string) ([]float32, error) {
	if provider == nil || isRandomLocalMemeQuery(query) {
		return nil, nil
	}
	embeddings, err := provider.Embed(ctx, []string{strings.TrimSpace(query)})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 || len(embeddings[0]) == 0 {
		return nil, fmt.Errorf("embedding provider returned no query vector")
	}
	return embeddings[0], nil
}

func loadLocalMemeMetadata(path string, cache map[string][]localMemeMetadataEntry) ([]localMemeMetadataEntry, error) {
	path = cleanLocalMemePath(path)
	if path == "" {
		return nil, nil
	}
	if cached, ok := cache[path]; ok {
		return cached, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var entries []localMemeMetadataEntry
	trimmed := strings.TrimSpace(string(data))
	switch {
	case strings.HasPrefix(trimmed, "["):
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, err
		}
	default:
		var envelope localMemeMetadataEnvelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			return nil, err
		}
		entries = envelope.Memes
	}

	cache[path] = entries
	return entries, nil
}

func resolveLocalMemeMetadataPath(cfg localMemeSettings, lib localMemeLibrary) string {
	if path := cleanLocalMemePath(lib.MetadataPath); path != "" {
		return path
	}
	return cleanLocalMemePath(cfg.MetadataPath)
}

func resolveLocalMemeMetadataRoot(cfg localMemeSettings, lib localMemeLibrary, metadataPath string) string {
	if root := cleanLocalMemePath(lib.MetadataRoot); root != "" {
		return root
	}
	if root := cleanLocalMemePath(cfg.MetadataRoot); root != "" {
		return root
	}
	return guessLocalMemeMetadataRoot(metadataPath)
}

func guessLocalMemeMetadataRoot(metadataPath string) string {
	dir := filepath.ToSlash(filepath.Dir(cleanLocalMemePath(metadataPath)))
	const marker = "/storage/greenscreen_memes"
	if idx := strings.Index(dir, marker); idx > 0 {
		return filepath.Clean(filepath.FromSlash(dir[:idx]))
	}
	return filepath.Clean(filepath.FromSlash(dir))
}

func cleanLocalMemePath(path string) string {
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

func listLocalMemeFiles(root string, recursive bool) ([]string, error) {
	if recursive {
		var files []string
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != root && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			files = append(files, path)
			return nil
		})
		return files, err
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		files = append(files, filepath.Join(root, entry.Name()))
	}
	return files, nil
}

func normalizeLocalMemeExts(exts []string) map[string]bool {
	if len(exts) == 0 {
		return defaultLocalMemeExts
	}
	out := make(map[string]bool, len(exts))
	for _, ext := range exts {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		out[ext] = true
	}
	if len(out) == 0 {
		return defaultLocalMemeExts
	}
	return out
}

func normalizeLocalMemeTerms(items []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, item := range items {
		term := normalizeLocalMemeText(item)
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		out = append(out, term)
	}
	return out
}

func shouldExcludeLocalMeme(name string, excludeTerms []string) bool {
	lower := normalizeLocalMemeText(name)
	for _, term := range excludeTerms {
		if term != "" && strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func scoreLocalMemeCandidate(query, base string) int {
	return scoreLocalMemeText(query, strings.TrimSuffix(base, filepath.Ext(base)))
}

func scoreLocalMemeText(query string, texts ...string) int {
	if isRandomLocalMemeQuery(query) {
		return 1
	}

	queryNorm := normalizeLocalMemeText(query)
	if queryNorm == "" {
		return 0
	}

	var corpusParts []string
	for _, text := range texts {
		if norm := normalizeLocalMemeText(text); norm != "" {
			corpusParts = append(corpusParts, norm)
		}
	}
	corpus := strings.Join(corpusParts, " ")
	if corpus == "" {
		return 0
	}

	score := 0
	if corpus == queryNorm {
		score += 100
	}
	if strings.Contains(corpus, queryNorm) {
		score += 35
	}

	tokens := localMemeQueryTokens(query)
	matched := 0
	for _, token := range tokens {
		switch {
		case containsWholeWord(corpus, token):
			score += 18
			matched++
		case strings.Contains(corpus, token):
			score += 10
			matched++
		}
	}

	if len(tokens) > 0 && matched == len(tokens) {
		score += 20
	}
	if score == 0 {
		return 0
	}
	return score
}

func chooseLocalMemeCandidate(candidates []localMemeCandidate) localMemeCandidate {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		if candidates[i].SemanticScore != candidates[j].SemanticScore {
			return candidates[i].SemanticScore > candidates[j].SemanticScore
		}
		if candidates[i].TextScore != candidates[j].TextScore {
			return candidates[i].TextScore > candidates[j].TextScore
		}
		if candidates[i].Size != candidates[j].Size {
			return candidates[i].Size < candidates[j].Size
		}
		return candidates[i].Base < candidates[j].Base
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

	n, err := crand.Int(crand.Reader, big.NewInt(int64(topCount)))
	if err != nil {
		return candidates[0]
	}
	return candidates[n.Int64()]
}

func localMemeNoMatchMessage(query, library string, cfg localMemeSettings) string {
	var hints []string
	for _, lib := range cfg.Libraries {
		if !localMemeLibraryEnabled(lib) {
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
		return fmt.Sprintf("no local meme matched %q in library %q", queryLabel, library)
	}
	if len(hints) == 0 {
		return fmt.Sprintf("no local meme matched %q", queryLabel)
	}
	return fmt.Sprintf("no local meme matched %q. Configured libraries: %s", queryLabel, strings.Join(hints, ", "))
}

func normalizeLocalMemeText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	return strings.Join(fields, " ")
}

func localMemeQueryTokens(query string) []string {
	fields := strings.Fields(normalizeLocalMemeText(query))
	var tokens []string
	seen := make(map[string]bool)
	for _, field := range fields {
		if field == "" || localMemeStopwords[field] || seen[field] {
			continue
		}
		seen[field] = true
		tokens = append(tokens, field)
	}
	return tokens
}

func containsWholeWord(s, word string) bool {
	if s == word {
		return true
	}
	for _, field := range strings.Fields(s) {
		if field == word {
			return true
		}
	}
	return false
}

func matchesLocalMemeLibrary(filter, name string) bool {
	filter = normalizeLocalMemeText(filter)
	if filter == "" {
		return true
	}
	name = normalizeLocalMemeText(name)
	return name == filter || strings.Contains(name, filter)
}

func localMemeLibraryEnabled(lib localMemeLibrary) bool {
	return lib.Enabled == nil || *lib.Enabled
}

func isRandomLocalMemeQuery(query string) bool {
	query = normalizeLocalMemeText(query)
	return query == "" || query == "random" || query == "anything" || len(localMemeQueryTokens(query)) == 0
}

func defaultLocalMemeMaxBytes(channel string) int64 {
	if strings.EqualFold(channel, "telegram") {
		return defaultTelegramMediaMax
	}
	return defaultGenericMediaMax
}

func boolPtr(v bool) *bool { return &v }
