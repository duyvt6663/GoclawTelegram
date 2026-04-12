package dailyiching

type QueryIndexSnapshot struct {
	IndexVersion    int                    `json:"index_version"`
	SourceRoot      string                 `json:"source_root"`
	SourceSignature string                 `json:"source_signature,omitempty"`
	CachePath       string                 `json:"cache_path,omitempty"`
	Extractor       string                 `json:"extractor,omitempty"`
	SourceCount     int                    `json:"source_count"`
	Sections        []QueryHexagramSection `json:"sections"`
}

type QueryHexagramSection struct {
	Number        int          `json:"number"`
	Name          string       `json:"name"`
	Title         string       `json:"title"`
	DisplaySource string       `json:"display_source,omitempty"`
	Heading       string       `json:"heading,omitempty"`
	Chunks        []QueryChunk `json:"chunks"`
}

type QueryChunk struct {
	Order      int      `json:"order"`
	Text       string   `json:"text"`
	Normalized string   `json:"normalized"`
	Tokens     []string `json:"tokens,omitempty"`
	HasHa      bool     `json:"has_ha"`
	HasThuong  bool     `json:"has_thuong"`
}

// LoadQueryIndexSnapshot loads the current daily_iching index_v4 snapshot from the
// shared cache, rebuilding it when requested or when the cache is missing.
func LoadQueryIndexSnapshot(workspace, dataDir string, rebuild bool) (*QueryIndexSnapshot, error) {
	sourceRoot, err := resolveBookSourceRoot(workspace)
	if err != nil {
		return nil, err
	}
	cachePath, err := resolveBookCachePathForVersion(workspace, dataDir, bookIndexVersion)
	if err != nil {
		return nil, err
	}
	index, err := loadOrBuildBookIndexWithOptions(sourceRoot, cachePath, rebuild)
	if err != nil {
		return nil, err
	}

	snapshot := &QueryIndexSnapshot{
		IndexVersion:    index.effectiveVersion(),
		SourceRoot:      sourceRoot,
		SourceSignature: index.effectiveSourceSignature(),
		CachePath:       cachePath,
		Extractor:       index.Extractor,
		SourceCount:     len(index.Sources),
		Sections:        make([]QueryHexagramSection, 0, len(index.Sections)),
	}
	for _, section := range index.Sections {
		outSection := QueryHexagramSection{
			Number:        section.Number,
			Name:          section.Name,
			Title:         section.Title,
			DisplaySource: section.DisplaySource,
			Heading:       section.Heading,
			Chunks:        make([]QueryChunk, 0, len(section.Chunks)),
		}
		for _, chunk := range section.Chunks {
			outSection.Chunks = append(outSection.Chunks, QueryChunk{
				Order:      chunk.Order,
				Text:       chunk.Text,
				Normalized: chunk.Normalized,
				Tokens:     append([]string(nil), chunk.Tokens...),
				HasHa:      chunk.HasHa,
				HasThuong:  chunk.HasThuong,
			})
		}
		snapshot.Sections = append(snapshot.Sections, outSection)
	}
	return snapshot, nil
}

func NormalizeComparableText(value string) string {
	return normalizeComparableText(value)
}

func TokenizeComparableText(value string) []string {
	return tokenizeComparableText(value)
}

func OCRTextNoisePenalty(value string) int {
	return ocrTextNoisePenalty(value)
}

func IsLikelyNoisyOCRText(value string) bool {
	return isLikelyNoisyOCRText(value)
}
