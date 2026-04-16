package researchreviewercodex

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/agent"
	"github.com/nextlevelbuilder/goclaw/internal/beta"
	"github.com/nextlevelbuilder/goclaw/internal/bootstrap"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/sessions"
	storepkg "github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	featureName              = "research_reviewer_codex"
	reviewerAgentKey         = "research-reviewer-codex"
	reviewerAgentDisplayName = "AI Research Reviewer Codex"
	reviewerAgentFrontmatter = "Research-paper reviewer focused on novelty, methodology, baselines, ablations, writing quality, and citation hygiene."
	reviewerModel            = "gpt-5.4"
	reviewerReasoningEffort  = "xhigh"
	reviewerReasoningAuto    = "auto"
	reviewStatusRunning      = "running"
	reviewStatusCompleted    = "completed"
	reviewStatusFailed       = "failed"
	defaultTopKRelated       = 5
	maxTopKRelated           = 10
)

var (
	activeFeatureMu sync.RWMutex
	activeFeature   *ResearchReviewerCodexFeature
)

// ResearchReviewerCodexFeature provisions a dedicated reviewer agent and reuses
// the existing memory store as a local paper index for related-work retrieval.
//
// Plan:
// 1. Provision a predefined gpt-5.4/xhigh reviewer agent with a stable workspace and review-specific context files.
// 2. Parse paper inputs into structured sections, store them in beta-local tables, and mirror them into the memory index for local retrieval.
// 3. Expose prepare/search/review flows through tools, RPC methods, and HTTP routes so callers can use the agent directly or via APIs.
type ResearchReviewerCodexFeature struct {
	cfg           *config.Config
	store         *featureStore
	agentRouter   *agent.Router
	agentStore    storepkg.AgentStore
	providerStore storepkg.ProviderStore
	memory        storepkg.MemoryStore
	workspace     string
}

type ensuredAgent struct {
	ID                uuid.UUID `json:"id"`
	AgentKey          string    `json:"agent_key"`
	DisplayName       string    `json:"display_name"`
	Provider          string    `json:"provider"`
	Model             string    `json:"model"`
	ReasoningEffort   string    `json:"reasoning_effort"`
	Workspace         string    `json:"workspace"`
	MaxToolIterations int       `json:"max_tool_iterations"`
}

type PreparedReviewBundle struct {
	Agent          ensuredAgent   `json:"agent"`
	Mode           string         `json:"mode"`
	Focus          string         `json:"focus,omitempty"`
	RetrievalQuery string         `json:"retrieval_query,omitempty"`
	Paper          PreparedPaper  `json:"paper"`
	Related        []RelatedPaper `json:"related_work,omitempty"`
	PreparedAt     time.Time      `json:"prepared_at"`
}

type PreparedPaper struct {
	PaperID          string                `json:"paper_id"`
	Title            string                `json:"title"`
	SourceKind       string                `json:"source_kind"`
	SourceURL        string                `json:"source_url,omitempty"`
	MemoryPath       string                `json:"memory_path,omitempty"`
	ContentHash      string                `json:"content_hash"`
	WordCount        int                   `json:"word_count"`
	Keywords         []string              `json:"keywords,omitempty"`
	Figures          []string              `json:"figures,omitempty"`
	Tables           []string              `json:"tables,omitempty"`
	ReferenceEntries []string              `json:"reference_entries,omitempty"`
	Sections         []PaperSectionExcerpt `json:"sections,omitempty"`
}

type PaperSectionExcerpt struct {
	Name    string `json:"name"`
	Heading string `json:"heading"`
	Excerpt string `json:"excerpt"`
}

type RelatedPaper struct {
	PaperID    string  `json:"paper_id"`
	Title      string  `json:"title"`
	SourceKind string  `json:"source_kind"`
	SourceURL  string  `json:"source_url,omitempty"`
	Abstract   string  `json:"abstract,omitempty"`
	Snippet    string  `json:"snippet,omitempty"`
	Score      float64 `json:"score,omitempty"`
}

type FeatureStatus struct {
	Feature       string       `json:"feature"`
	Agent         ensuredAgent `json:"agent"`
	IndexedPapers int          `json:"indexed_papers"`
	StoredReviews int          `json:"stored_reviews"`
	LastReviewID  string       `json:"last_review_id,omitempty"`
	LastReviewAt  *time.Time   `json:"last_review_at,omitempty"`
}

type ReviewResultPayload struct {
	ReviewID   string         `json:"review_id"`
	Status     string         `json:"status"`
	Mode       string         `json:"mode"`
	Agent      ensuredAgent   `json:"agent"`
	Paper      PreparedPaper  `json:"paper"`
	Related    []RelatedPaper `json:"related_work,omitempty"`
	Report     string         `json:"report,omitempty"`
	Error      string         `json:"error,omitempty"`
	Usage      *ReviewUsage   `json:"usage,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
}

type ReviewUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (f *ResearchReviewerCodexFeature) Name() string { return featureName }

func ActiveFeature() *ResearchReviewerCodexFeature {
	activeFeatureMu.RLock()
	defer activeFeatureMu.RUnlock()
	return activeFeature
}

func setActiveFeature(feature *ResearchReviewerCodexFeature) {
	activeFeatureMu.Lock()
	defer activeFeatureMu.Unlock()
	activeFeature = feature
}

func (f *ResearchReviewerCodexFeature) Init(deps beta.Deps) error {
	if deps.Stores == nil || deps.Stores.DB == nil {
		return fmt.Errorf("%s requires a SQL store", featureName)
	}
	if deps.Stores.Agents == nil {
		return fmt.Errorf("%s requires an agent store", featureName)
	}

	f.cfg = deps.Config
	f.store = &featureStore{db: deps.Stores.DB}
	f.agentRouter = deps.AgentRouter
	f.agentStore = deps.Stores.Agents
	f.providerStore = deps.Stores.Providers
	f.memory = deps.Stores.Memory
	f.workspace = deps.Workspace

	if err := f.store.migrate(); err != nil {
		return fmt.Errorf("%s migration: %w", featureName, err)
	}

	if deps.ToolRegistry != nil {
		deps.ToolRegistry.Register(&prepareReviewTool{feature: f})
		deps.ToolRegistry.Register(&searchRelatedPapersTool{feature: f})
		deps.ToolRegistry.Register(&getIndexedPaperTool{feature: f})
	}
	if deps.MethodRouter != nil {
		registerMethods(f, deps.MethodRouter)
	}
	if deps.Server != nil {
		deps.Server.AddRouteRegistrar(&handler{feature: f})
	}

	setActiveFeature(f)
	slog.Info("beta research reviewer codex initialized")
	return nil
}

func (f *ResearchReviewerCodexFeature) PrepareReviewBundle(ctx context.Context, request ReviewRequest) (*PreparedReviewBundle, error) {
	return f.prepareReviewBundle(ctx, request)
}

func (f *ResearchReviewerCodexFeature) Review(ctx context.Context, userID string, request ReviewRequest) (*ReviewResultPayload, error) {
	return f.review(ctx, userID, request)
}

func (f *ResearchReviewerCodexFeature) GetStoredReview(ctx context.Context, reviewID string) (*ReviewResultPayload, error) {
	return f.getStoredReview(ctx, reviewID)
}

func (f *ResearchReviewerCodexFeature) statusSnapshot(ctx context.Context) (*FeatureStatus, error) {
	agentInfo, err := f.ensureReviewerAgent(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := f.store.statusStats(tenantKeyFromCtx(ctx))
	if err != nil {
		return nil, err
	}

	payload := &FeatureStatus{
		Feature:       featureName,
		Agent:         agentInfo,
		IndexedPapers: stats.IndexedPapers,
		StoredReviews: stats.StoredReviews,
		LastReviewID:  stats.LastReviewID,
	}
	if !stats.LastReviewAt.IsZero() {
		ts := stats.LastReviewAt
		payload.LastReviewAt = &ts
	}
	return payload, nil
}

func (f *ResearchReviewerCodexFeature) review(ctx context.Context, userID string, request ReviewRequest) (*ReviewResultPayload, error) {
	if f.agentRouter == nil {
		return nil, fmt.Errorf("%s requires an agent router", featureName)
	}

	bundle, err := f.prepareReviewBundle(ctx, request)
	if err != nil {
		return nil, err
	}

	reviewID := uuid.NewString()
	now := time.Now().UTC()
	record := &reviewRecord{
		ID:          reviewID,
		TenantID:    tenantKeyFromCtx(ctx),
		AgentID:     bundle.Agent.ID.String(),
		PaperID:     bundle.Paper.PaperID,
		Mode:        bundle.Mode,
		Focus:       strings.TrimSpace(bundle.Focus),
		Status:      reviewStatusRunning,
		RelatedJSON: mustJSON(bundle.Related),
		PromptText:  buildReviewPrompt(bundle),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := f.store.insertReview(record); err != nil {
		return nil, err
	}

	runUserID := strings.TrimSpace(userID)
	if runUserID == "" {
		runUserID = "research-reviewer"
	}

	loop, err := f.agentRouter.Get(ctx, bundle.Agent.AgentKey)
	if err != nil {
		record.Status = reviewStatusFailed
		record.ErrorMessage = err.Error()
		record.UpdatedAt = time.Now().UTC()
		_ = f.store.updateReview(record)
		return nil, err
	}

	sessionKey := sessions.BuildAgentMainSessionKey(bundle.Agent.ID.String(), "beta-review-"+reviewID[:8])
	result, err := loop.Run(ctx, agent.RunRequest{
		SessionKey:  sessionKey,
		Message:     record.PromptText,
		Channel:     "beta." + featureName,
		ChannelType: "http",
		ChatID:      "review:" + bundle.Paper.PaperID,
		PeerKind:    "direct",
		RunID:       reviewID,
		UserID:      runUserID,
		SenderID:    runUserID,
		Stream:      false,
	})
	if err != nil {
		record.Status = reviewStatusFailed
		record.ErrorMessage = err.Error()
		record.UpdatedAt = time.Now().UTC()
		_ = f.store.updateReview(record)
		return &ReviewResultPayload{
			ReviewID:   reviewID,
			Status:     reviewStatusFailed,
			Mode:       bundle.Mode,
			Agent:      bundle.Agent,
			Paper:      bundle.Paper,
			Related:    bundle.Related,
			Error:      err.Error(),
			CreatedAt:  record.CreatedAt,
			FinishedAt: &record.UpdatedAt,
		}, nil
	}

	record.Status = reviewStatusCompleted
	record.ReportText = strings.TrimSpace(result.Content)
	record.UpdatedAt = time.Now().UTC()
	if err := f.store.updateReview(record); err != nil {
		return nil, err
	}

	payload := &ReviewResultPayload{
		ReviewID:   reviewID,
		Status:     reviewStatusCompleted,
		Mode:       bundle.Mode,
		Agent:      bundle.Agent,
		Paper:      bundle.Paper,
		Related:    bundle.Related,
		Report:     record.ReportText,
		CreatedAt:  record.CreatedAt,
		FinishedAt: &record.UpdatedAt,
	}
	if result.Usage != nil {
		payload.Usage = &ReviewUsage{
			PromptTokens:     result.Usage.PromptTokens,
			CompletionTokens: result.Usage.CompletionTokens,
			TotalTokens:      result.Usage.TotalTokens,
		}
	}
	return payload, nil
}

func (f *ResearchReviewerCodexFeature) getStoredReview(ctx context.Context, reviewID string) (*ReviewResultPayload, error) {
	record, err := f.store.getReview(tenantKeyFromCtx(ctx), strings.TrimSpace(reviewID))
	if err != nil {
		return nil, err
	}

	agentInfo, err := f.ensureReviewerAgent(ctx)
	if err != nil {
		return nil, err
	}

	paper, err := f.store.getPaperByID(tenantKeyFromCtx(ctx), record.PaperID)
	if err != nil {
		return nil, err
	}
	preparedPaper, err := preparedPaperFromRecord(paper)
	if err != nil {
		return nil, err
	}

	related, err := record.relatedPapers()
	if err != nil {
		return nil, err
	}

	payload := &ReviewResultPayload{
		ReviewID:  record.ID,
		Status:    record.Status,
		Mode:      record.Mode,
		Agent:     agentInfo,
		Paper:     preparedPaper,
		Related:   related,
		Report:    record.ReportText,
		Error:     record.ErrorMessage,
		CreatedAt: record.CreatedAt,
	}
	if !record.UpdatedAt.IsZero() {
		ts := record.UpdatedAt
		payload.FinishedAt = &ts
	}
	return payload, nil
}

func (f *ResearchReviewerCodexFeature) prepareReviewBundle(ctx context.Context, request ReviewRequest) (*PreparedReviewBundle, error) {
	resolved, err := normalizeReviewRequest(request)
	if err != nil {
		return nil, err
	}

	agentInfo, err := f.ensureReviewerAgent(ctx)
	if err != nil {
		return nil, err
	}

	var paper *paperRecord
	if resolved.PaperID != "" {
		paper, err = f.store.getPaperByID(tenantKeyFromCtx(ctx), resolved.PaperID)
		if err != nil {
			return nil, err
		}
	} else {
		paper, err = f.loadOrIngestPaper(ctx, agentInfo, resolved)
		if err != nil {
			return nil, err
		}
	}

	related, retrievalQuery, err := f.findRelatedPapers(ctx, agentInfo, paper, resolved.TopKRelated)
	if err != nil {
		return nil, err
	}

	preparedPaper, err := preparedPaperFromRecord(paper)
	if err != nil {
		return nil, err
	}

	return &PreparedReviewBundle{
		Agent:          agentInfo,
		Mode:           resolved.Mode,
		Focus:          strings.TrimSpace(resolved.Focus),
		RetrievalQuery: retrievalQuery,
		Paper:          preparedPaper,
		Related:        related,
		PreparedAt:     time.Now().UTC(),
	}, nil
}

func (f *ResearchReviewerCodexFeature) loadOrIngestPaper(ctx context.Context, agentInfo ensuredAgent, request normalizedReviewRequest) (*paperRecord, error) {
	source := paperSource{
		Title:     strings.TrimSpace(request.Title),
		SourceURL: strings.TrimSpace(request.SourceURL),
		PDFPath:   strings.TrimSpace(request.PDFPath),
		RawText:   strings.TrimSpace(request.PaperText),
	}

	if source.SourceURL != "" {
		source.SourceURL = canonicalizeSourceURL(source.SourceURL)
	}
	if source.PDFPath != "" {
		resolvedPath, err := resolveLocalPaperPath(ctx, f.workspace, source.PDFPath)
		if err != nil {
			return nil, err
		}
		source.PDFPath = resolvedPath
	}

	sourceKey := buildPaperSourceKey(source)
	tenantID := tenantKeyFromCtx(ctx)
	if !request.ForceRefresh {
		existing, err := f.store.getPaperBySourceKey(tenantID, sourceKey)
		if err == nil && existing != nil {
			if err := f.ensurePaperIndexedInMemory(ctx, agentInfo, existing); err != nil {
				slog.Warn("beta research reviewer memory ensure failed", "paper_id", existing.ID, "error", err)
			}
			return existing, nil
		}
	}

	rawText, sourceMeta, err := loadPaperSource(ctx, source)
	if err != nil {
		return nil, err
	}

	title := strings.TrimSpace(request.Title)
	if title == "" {
		title = strings.TrimSpace(sourceMeta.Title)
	}
	if title == "" {
		title = inferPaperTitle(rawText, sourceMeta.CanonicalURL)
	}
	if title == "" {
		title = "Untitled Paper"
	}

	structured := extractStructuredPaper(title, sourceMeta.SourceKind, sourceMeta.CanonicalURL, rawText)
	contentHash := hashText(rawText)

	record, err := f.store.getPaperBySourceKey(tenantID, sourceKey)
	if err != nil || record == nil {
		record = &paperRecord{
			ID:         uuid.NewString(),
			TenantID:   tenantID,
			AgentID:    agentInfo.ID.String(),
			SourceKey:  sourceKey,
			MemoryPath: filepath.ToSlash(filepath.Join("papers", uuid.NewString()+".md")),
			CreatedAt:  time.Now().UTC(),
		}
	}

	record.AgentID = agentInfo.ID.String()
	record.Title = title
	record.SourceKind = structured.SourceKind
	record.SourceURL = structured.SourceURL
	record.ContentHash = contentHash
	record.StructuredJSON = mustJSON(structured)
	record.RawText = rawText
	record.UpdatedAt = time.Now().UTC()

	if record.MemoryPath == "" {
		record.MemoryPath = filepath.ToSlash(filepath.Join("papers", record.ID+".md"))
	}

	if err := f.store.upsertPaper(record); err != nil {
		return nil, err
	}
	if err := f.ensurePaperIndexedInMemory(ctx, agentInfo, record); err != nil {
		slog.Warn("beta research reviewer paper index failed", "paper_id", record.ID, "error", err)
	}
	return record, nil
}

func (f *ResearchReviewerCodexFeature) ensurePaperIndexedInMemory(ctx context.Context, agentInfo ensuredAgent, record *paperRecord) error {
	if f.memory == nil || record == nil {
		return nil
	}
	structured, err := record.structured()
	if err != nil {
		return err
	}
	content := buildMemoryDocumentContent(structured, record.RawText)
	if err := f.memory.PutDocument(ctx, agentInfo.ID.String(), "", record.MemoryPath, content); err != nil {
		return err
	}
	return f.memory.IndexDocument(ctx, agentInfo.ID.String(), "", record.MemoryPath)
}

func (f *ResearchReviewerCodexFeature) findRelatedPapers(ctx context.Context, agentInfo ensuredAgent, current *paperRecord, topK int) ([]RelatedPaper, string, error) {
	if current == nil {
		return nil, "", nil
	}
	structured, err := current.structured()
	if err != nil {
		return nil, "", err
	}

	query := buildRetrievalQuery(structured)
	if query == "" {
		return nil, "", nil
	}

	related, err := f.searchIndexedRelatedByQuery(ctx, agentInfo, current.TenantID, query, current.ID, topK)
	if err != nil {
		return nil, "", err
	}
	return related, query, nil
}

func (f *ResearchReviewerCodexFeature) searchIndexedRelatedByQuery(ctx context.Context, agentInfo ensuredAgent, tenantID, query, excludePaperID string, topK int) ([]RelatedPaper, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if topK <= 0 {
		topK = defaultTopKRelated
	}

	var related []RelatedPaper
	seen := map[string]struct{}{}
	if strings.TrimSpace(excludePaperID) != "" {
		seen[strings.TrimSpace(excludePaperID)] = struct{}{}
	}

	if f.memory != nil {
		searchResults, err := f.memory.Search(ctx, query, agentInfo.ID.String(), "", storepkg.MemorySearchOptions{
			MaxResults: topK * 4,
			PathPrefix: "papers/",
		})
		if err == nil {
			for _, result := range searchResults {
				matched, err := f.store.getPaperByMemoryPath(tenantID, result.Path)
				if err != nil || matched == nil {
					continue
				}
				if _, ok := seen[matched.ID]; ok {
					continue
				}
				item, itemErr := relatedPaperFromRecord(matched, result.Snippet, result.Score)
				if itemErr != nil {
					continue
				}
				seen[matched.ID] = struct{}{}
				related = append(related, item)
				if len(related) >= topK {
					return related, nil
				}
			}
		}
	}

	if len(related) < topK {
		fallback, err := f.store.searchPapersByText(tenantID, fallbackSearchTerm(query), strings.TrimSpace(excludePaperID), topK*2)
		if err == nil {
			for _, candidate := range fallback {
				if _, ok := seen[candidate.ID]; ok {
					continue
				}
				item, itemErr := relatedPaperFromRecord(candidate, "", 0)
				if itemErr != nil {
					continue
				}
				seen[candidate.ID] = struct{}{}
				related = append(related, item)
				if len(related) >= topK {
					break
				}
			}
		}
	}

	return related, nil
}

func (f *ResearchReviewerCodexFeature) ensureReviewerAgent(ctx context.Context) (ensuredAgent, error) {
	workspace := reviewerWorkspace(f.workspace)
	if err := os.MkdirAll(filepath.Join(workspace, "papers"), 0o755); err != nil {
		return ensuredAgent{}, err
	}

	tenantID := storepkg.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		tenantID = storepkg.MasterTenantID
	}

	provider := f.pickPreferredProvider(ctx)
	reasoningEffort := reviewerReasoningForProviderType(provider.ProviderType)
	otherConfig := reviewerOtherConfigJSON(provider.ProviderType)

	existing, err := f.agentStore.GetByKey(ctx, reviewerAgentKey)
	if err != nil && !isNotFoundError(err) {
		return ensuredAgent{}, err
	}

	var agentData *storepkg.AgentData
	if existing == nil {
		agentData = &storepkg.AgentData{
			TenantID:            tenantID,
			AgentKey:            reviewerAgentKey,
			DisplayName:         reviewerAgentDisplayName,
			Frontmatter:         reviewerAgentFrontmatter,
			OwnerID:             "system",
			Provider:            provider.Name,
			Model:               reviewerModel,
			ContextWindow:       0,
			MaxToolIterations:   18,
			Workspace:           workspace,
			RestrictToWorkspace: true,
			AgentType:           storepkg.AgentTypePredefined,
			Status:              storepkg.AgentStatusActive,
			OtherConfig:         otherConfig,
		}
		if err := f.agentStore.Create(ctx, agentData); err != nil {
			return ensuredAgent{}, err
		}
	} else {
		agentData = existing
		updates := map[string]any{}
		if strings.TrimSpace(existing.DisplayName) != reviewerAgentDisplayName {
			updates["display_name"] = reviewerAgentDisplayName
		}
		if strings.TrimSpace(existing.Frontmatter) != reviewerAgentFrontmatter {
			updates["frontmatter"] = reviewerAgentFrontmatter
		}
		if strings.TrimSpace(existing.Provider) != provider.Name {
			updates["provider"] = provider.Name
		}
		if strings.TrimSpace(existing.Model) != reviewerModel {
			updates["model"] = reviewerModel
		}
		if existing.MaxToolIterations != 18 {
			updates["max_tool_iterations"] = 18
		}
		if strings.TrimSpace(existing.Workspace) != workspace {
			updates["workspace"] = workspace
		}
		if !existing.RestrictToWorkspace {
			updates["restrict_to_workspace"] = true
		}
		if strings.TrimSpace(existing.AgentType) != storepkg.AgentTypePredefined {
			updates["agent_type"] = storepkg.AgentTypePredefined
		}
		if strings.TrimSpace(existing.Status) != storepkg.AgentStatusActive {
			updates["status"] = storepkg.AgentStatusActive
		}
		if !jsonBytesEqual(existing.OtherConfig, otherConfig) {
			updates["other_config"] = otherConfig
		}
		if len(updates) > 0 {
			if err := f.agentStore.Update(ctx, existing.ID, updates); err != nil {
				return ensuredAgent{}, err
			}
			reloaded, reloadErr := f.agentStore.GetByKey(ctx, reviewerAgentKey)
			if reloadErr == nil && reloaded != nil {
				agentData = reloaded
			}
		}
	}

	if _, err := bootstrap.SeedToStore(ctx, f.agentStore, agentData.ID, storepkg.AgentTypePredefined); err != nil {
		return ensuredAgent{}, err
	}
	for name, content := range reviewerContextFiles() {
		if err := f.agentStore.SetAgentContextFile(ctx, agentData.ID, name, content); err != nil {
			return ensuredAgent{}, err
		}
	}

	return ensuredAgent{
		ID:                agentData.ID,
		AgentKey:          agentData.AgentKey,
		DisplayName:       reviewerAgentDisplayName,
		Provider:          provider.Name,
		Model:             reviewerModel,
		ReasoningEffort:   reasoningEffort,
		Workspace:         workspace,
		MaxToolIterations: 18,
	}, nil
}

type reviewerProviderChoice struct {
	Name         string
	ProviderType string
}

func (f *ResearchReviewerCodexFeature) pickPreferredProvider(ctx context.Context) reviewerProviderChoice {
	if f.providerStore != nil {
		if providers, err := f.providerStore.ListProviders(ctx); err == nil {
			if choice := preferredProviderChoice(providers); choice.Name != "" {
				return choice
			}
		}
	}
	if f.cfg != nil && strings.TrimSpace(f.cfg.Agents.Defaults.Provider) != "" {
		return reviewerProviderChoice{
			Name: strings.TrimSpace(f.cfg.Agents.Defaults.Provider),
		}
	}
	return reviewerProviderChoice{
		Name:         "openai",
		ProviderType: storepkg.ProviderOpenAICompat,
	}
}
