package dailyichingquery

import (
	"context"
	"sync"
	"testing"
	"time"
)

type testQueryStore struct {
	mu          sync.Mutex
	records     []chunkEmbeddingRecord
	upserted    []chunkEmbeddingRecord
	listCalls   int
	insertCalls int
}

func (s *testQueryStore) migrate() error { return nil }

func (s *testQueryStore) listChunkEmbeddings(tenantID, sourceSignature, providerName, providerModel string) ([]chunkEmbeddingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	out := make([]chunkEmbeddingRecord, len(s.records))
	copy(out, s.records)
	return out, nil
}

func (s *testQueryStore) upsertChunkEmbeddings(records []chunkEmbeddingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserted = append(s.upserted, records...)
	return nil
}

func (s *testQueryStore) insertRun(record *queryRunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.insertCalls++
	return nil
}

type testEmbeddingProvider struct {
	name      string
	model     string
	calledCh  chan struct{}
	releaseCh chan struct{}
}

func (p *testEmbeddingProvider) Name() string  { return p.name }
func (p *testEmbeddingProvider) Model() string { return p.model }

func (p *testEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if p.calledCh != nil {
		select {
		case p.calledCh <- struct{}{}:
		default:
		}
	}
	if p.releaseCh != nil {
		select {
		case <-p.releaseCh:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(i + 1), 0.5}
	}
	return out, nil
}

func TestPrepareChunkEmbeddingsStartsBackgroundWarmWithoutBlocking(t *testing.T) {
	store := &testQueryStore{}
	provider := &testEmbeddingProvider{
		name:      "test-embed",
		model:     "stub",
		calledCh:  make(chan struct{}, 1),
		releaseCh: make(chan struct{}),
	}
	feature := &DailyIChingQueryFeature{
		store:             store,
		chunkEmbeddings:   make(map[string][]float32),
		loadedNamespaces:  make(map[string]bool),
		warmingNamespaces: make(map[string]bool),
		queryEmbeddings:   make(map[string][]float32),
	}
	index := testCompiledIndex()

	start := time.Now()
	state, err := feature.prepareChunkEmbeddings(index, &resolvedEmbeddingProvider{
		provider: provider,
		name:     provider.Name(),
		model:    provider.Model(),
	}, "tenant-a")
	if err != nil {
		t.Fatalf("prepareChunkEmbeddings() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("prepareChunkEmbeddings() took %v, want non-blocking return", elapsed)
	}
	if state.LoadedCount != 0 || state.TotalCount != len(index.FlatChunks) || !state.Warming {
		t.Fatalf("unexpected warm state: %+v", state)
	}

	select {
	case <-provider.calledCh:
	case <-time.After(1 * time.Second):
		t.Fatal("background embedding warm did not start")
	}

	close(provider.releaseCh)
	waitForQueryCondition(t, time.Second, func() bool {
		return feature.loadedChunkEmbeddingCount(state.Namespace, index) == len(index.FlatChunks)
	})
}

func TestPrepareChunkEmbeddingsReadyWhenAllEmbeddingsCached(t *testing.T) {
	index := testCompiledIndex()
	store := &testQueryStore{
		records: []chunkEmbeddingRecord{
			{TenantID: "tenant-a", SourceSignature: index.SourceSignature, ProviderName: "test-embed", ProviderModel: "stub", SectionNumber: 1, ChunkOrder: 0, Embedding: []float32{1, 0.5}},
			{TenantID: "tenant-a", SourceSignature: index.SourceSignature, ProviderName: "test-embed", ProviderModel: "stub", SectionNumber: 1, ChunkOrder: 1, Embedding: []float32{2, 0.5}},
		},
	}
	provider := &testEmbeddingProvider{
		name:     "test-embed",
		model:    "stub",
		calledCh: make(chan struct{}, 1),
	}
	feature := &DailyIChingQueryFeature{
		store:             store,
		chunkEmbeddings:   make(map[string][]float32),
		loadedNamespaces:  make(map[string]bool),
		warmingNamespaces: make(map[string]bool),
		queryEmbeddings:   make(map[string][]float32),
	}

	state, err := feature.prepareChunkEmbeddings(index, &resolvedEmbeddingProvider{
		provider: provider,
		name:     provider.Name(),
		model:    provider.Model(),
	}, "tenant-a")
	if err != nil {
		t.Fatalf("prepareChunkEmbeddings() error = %v", err)
	}
	if !state.Ready() || state.Warming {
		t.Fatalf("unexpected warm state: %+v", state)
	}

	select {
	case <-provider.calledCh:
		t.Fatal("embedding provider should not be called when cache is already complete")
	case <-time.After(150 * time.Millisecond):
	}
}

func testCompiledIndex() *compiledIndex {
	return &compiledIndex{
		SourceSignature: "sig-1",
		Sections: map[int]compiledSection{
			1: {
				Number: 1,
				Name:   "Can",
				Title:  "Thuan Can",
				Chunks: []indexedChunk{
					{Key: "1:0", SectionNumber: 1, Order: 0, Text: "chunk one", Normalized: "chunk one"},
					{Key: "1:1", SectionNumber: 1, Order: 1, Text: "chunk two", Normalized: "chunk two"},
				},
			},
		},
		FlatChunks: []indexedChunk{
			{Key: "1:0", SectionNumber: 1, Order: 0, Text: "chunk one", Normalized: "chunk one"},
			{Key: "1:1", SectionNumber: 1, Order: 1, Text: "chunk two", Normalized: "chunk two"},
		},
	}
}

func waitForQueryCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
