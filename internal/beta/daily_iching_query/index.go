package dailyichingquery

import (
	"fmt"
	"sort"
	"strings"
	"time"

	dailyiching "github.com/nextlevelbuilder/goclaw/internal/beta/daily_iching"
)

type compiledIndex struct {
	SourceRoot      string
	SourceSignature string
	CachePath       string
	Extractor       string
	Sections        map[int]compiledSection
	FlatChunks      []indexedChunk
}

type compiledSection struct {
	Number        int
	Name          string
	Title         string
	DisplaySource string
	Heading       string
	SectionTokens []string
	Chunks        []indexedChunk
}

type indexedChunk struct {
	Key            string
	SectionNumber  int
	SectionName    string
	SectionTitle   string
	SectionHeading string
	SectionTokens  []string
	DisplaySource  string
	Order          int
	Text           string
	Normalized     string
	Tokens         []string
	HasHa          bool
	HasThuong      bool
}

func (f *DailyIChingQueryFeature) ensureIndex(rebuild bool) (*compiledIndex, error) {
	f.indexMu.RLock()
	current := f.index
	lastLoaded := f.lastIndexLoaded
	f.indexMu.RUnlock()

	if current != nil && !rebuild && time.Since(lastLoaded) < indexRefreshInterval {
		return current, nil
	}

	snapshot, err := dailyiching.LoadQueryIndexSnapshot(f.workspace, f.dataDir, rebuild)
	if err != nil {
		return nil, err
	}
	compiled, err := buildCompiledIndex(snapshot)
	if err != nil {
		return nil, err
	}

	f.indexMu.Lock()
	f.index = compiled
	f.lastIndexLoaded = time.Now().UTC()
	f.indexMu.Unlock()
	return compiled, nil
}

func buildCompiledIndex(snapshot *dailyiching.QueryIndexSnapshot) (*compiledIndex, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("daily iching query snapshot is empty")
	}

	index := &compiledIndex{
		SourceRoot:      strings.TrimSpace(snapshot.SourceRoot),
		SourceSignature: strings.TrimSpace(snapshot.SourceSignature),
		CachePath:       strings.TrimSpace(snapshot.CachePath),
		Extractor:       strings.TrimSpace(snapshot.Extractor),
		Sections:        make(map[int]compiledSection, len(snapshot.Sections)),
		FlatChunks:      make([]indexedChunk, 0, len(snapshot.Sections)*4),
	}

	for _, section := range snapshot.Sections {
		sectionTokens := dailyiching.TokenizeComparableText(section.Name + " " + section.Title + " " + section.Heading)
		compiled := compiledSection{
			Number:        section.Number,
			Name:          section.Name,
			Title:         section.Title,
			DisplaySource: section.DisplaySource,
			Heading:       section.Heading,
			SectionTokens: append([]string(nil), sectionTokens...),
			Chunks:        make([]indexedChunk, 0, len(section.Chunks)),
		}
		for _, chunk := range section.Chunks {
			indexed := indexedChunk{
				Key:            fmt.Sprintf("%d:%d", section.Number, chunk.Order),
				SectionNumber:  section.Number,
				SectionName:    section.Name,
				SectionTitle:   section.Title,
				SectionHeading: section.Heading,
				SectionTokens:  append([]string(nil), sectionTokens...),
				DisplaySource:  section.DisplaySource,
				Order:          chunk.Order,
				Text:           chunk.Text,
				Normalized:     chunk.Normalized,
				Tokens:         append([]string(nil), chunk.Tokens...),
				HasHa:          chunk.HasHa,
				HasThuong:      chunk.HasThuong,
			}
			compiled.Chunks = append(compiled.Chunks, indexed)
			index.FlatChunks = append(index.FlatChunks, indexed)
		}
		index.Sections[section.Number] = compiled
	}

	sort.Slice(index.FlatChunks, func(i, j int) bool {
		if index.FlatChunks[i].SectionNumber != index.FlatChunks[j].SectionNumber {
			return index.FlatChunks[i].SectionNumber < index.FlatChunks[j].SectionNumber
		}
		return index.FlatChunks[i].Order < index.FlatChunks[j].Order
	})

	return index, nil
}
