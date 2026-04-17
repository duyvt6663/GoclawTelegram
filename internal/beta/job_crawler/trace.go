package jobcrawler

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

type JobDecisionStage struct {
	Stage   string   `json:"stage"`
	Outcome string   `json:"outcome"`
	Reasons []string `json:"reasons,omitempty"`
}

type JobDecisionTrace struct {
	TraceID         string             `json:"trace_id"`
	JobHash         string             `json:"job_hash,omitempty"`
	Source          string             `json:"source"`
	Title           string             `json:"title"`
	Company         string             `json:"company"`
	URL             string             `json:"url"`
	RoleType        string             `json:"role_type,omitempty"`
	FinalOutcome    string             `json:"final_outcome"`
	Reasons         []string           `json:"reasons,omitempty"`
	Rank            int                `json:"rank,omitempty"`
	PostedRank      int                `json:"posted_rank,omitempty"`
	Score           float64            `json:"score,omitempty"`
	SemanticScore   float64            `json:"semantic_score,omitempty"`
	KeywordScore    float64            `json:"keyword_score,omitempty"`
	SourceBoost     float64            `json:"source_boost,omitempty"`
	LocationWeight  float64            `json:"location_weight,omitempty"`
	RoleMatch       float64            `json:"role_match,omitempty"`
	RecencyWeight   float64            `json:"recency_weight,omitempty"`
	DynamicBoost    float64            `json:"dynamic_boost,omitempty"`
	PenaltyScore    float64            `json:"penalty_score,omitempty"`
	MatchedKeywords []string           `json:"matched_keywords,omitempty"`
	Stages          []JobDecisionStage `json:"stages,omitempty"`
}

type decisionTraceSet struct {
	order []string
	items map[string]*JobDecisionTrace
}

func newDecisionTraceSet() *decisionTraceSet {
	return &decisionTraceSet{
		order: make([]string, 0, 32),
		items: make(map[string]*JobDecisionTrace),
	}
}

func makeTraceID(source, title, company, rawURL string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(source)) + "|" +
		strings.ToLower(strings.TrimSpace(title)) + "|" +
		strings.ToLower(strings.TrimSpace(company)) + "|" +
		canonicalizeURL(rawURL)))
	return hex.EncodeToString(sum[:])
}

func buildTraceIdentifiers(source, title, company, rawURL string) (string, string) {
	return makeTraceID(source, title, company, rawURL), makeJobHash(title, company, rawURL)
}

func (s *decisionTraceSet) ensure(traceID, jobHash, source, title, company, rawURL string) *JobDecisionTrace {
	if s == nil {
		return nil
	}
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		traceID = makeTraceID(source, title, company, rawURL)
	}

	if existing, ok := s.items[traceID]; ok {
		if existing.JobHash == "" && jobHash != "" {
			existing.JobHash = jobHash
		}
		if existing.Source == "" && source != "" {
			existing.Source = strings.TrimSpace(source)
		}
		if existing.Title == "" && title != "" {
			existing.Title = cleanText(title)
		}
		if existing.Company == "" && company != "" {
			existing.Company = cleanText(company)
		}
		if existing.URL == "" && rawURL != "" {
			existing.URL = canonicalizeURL(rawURL)
		}
		return existing
	}

	trace := &JobDecisionTrace{
		TraceID: traceID,
		JobHash: jobHash,
		Source:  strings.TrimSpace(source),
		Title:   cleanText(title),
		Company: cleanText(company),
		URL:     canonicalizeURL(rawURL),
	}
	s.items[traceID] = trace
	s.order = append(s.order, traceID)
	return trace
}

func (s *decisionTraceSet) ensureCandidate(job RankedJob) *JobDecisionTrace {
	if s == nil {
		return nil
	}
	traceID := strings.TrimSpace(job.TraceID)
	if traceID == "" {
		traceID = makeTraceID(job.Source, job.Title, job.Company, job.URL)
	}
	return s.ensure(traceID, job.JobHash, job.Source, job.Title, job.Company, job.URL)
}

func (s *decisionTraceSet) noteRaw(source, title, company, rawURL, stage, outcome string, reasons ...string) *JobDecisionTrace {
	if s == nil {
		return nil
	}
	traceID, jobHash := buildTraceIdentifiers(source, title, company, rawURL)
	trace := s.ensure(traceID, jobHash, source, title, company, rawURL)
	s.noteStage(trace, stage, outcome, reasons...)
	return trace
}

func (s *decisionTraceSet) noteCandidate(job RankedJob, stage, outcome string, reasons ...string) *JobDecisionTrace {
	trace := s.ensureCandidate(job)
	s.noteStage(trace, stage, outcome, reasons...)
	return trace
}

func (s *decisionTraceSet) noteStage(trace *JobDecisionTrace, stage, outcome string, reasons ...string) {
	if s == nil || trace == nil {
		return
	}
	stage = strings.TrimSpace(stage)
	outcome = strings.TrimSpace(outcome)
	cleanReasons := compactStrings(reasons)
	trace.Stages = append(trace.Stages, JobDecisionStage{
		Stage:   stage,
		Outcome: outcome,
		Reasons: append([]string(nil), cleanReasons...),
	})
	trace.Reasons = appendUniqueStrings(trace.Reasons, cleanReasons...)
	if outcome == "dropped" {
		trace.FinalOutcome = "dropped"
	}
}

func (s *decisionTraceSet) updateScores(job RankedJob) {
	trace := s.ensureCandidate(job)
	if trace == nil {
		return
	}
	trace.JobHash = job.JobHash
	trace.RoleType = job.RoleType
	trace.Score = job.Score
	trace.SemanticScore = job.SemanticScore
	trace.KeywordScore = job.KeywordScore
	trace.SourceBoost = job.SourceBoost
	trace.LocationWeight = job.LocationWeight
	trace.RoleMatch = job.RoleMatch
	trace.RecencyWeight = job.RecencyWeight
	trace.DynamicBoost = job.DynamicBoost
	trace.PenaltyScore = job.PenaltyScore
	trace.MatchedKeywords = append([]string(nil), job.MatchedKeywords...)
}

func (s *decisionTraceSet) markRanked(jobs []RankedJob) {
	if s == nil {
		return
	}
	for idx := range jobs {
		trace := s.ensureCandidate(jobs[idx])
		if trace == nil {
			continue
		}
		trace.Rank = idx + 1
		if trace.FinalOutcome == "" || trace.FinalOutcome == "ranked" {
			trace.FinalOutcome = "ranked"
		}
		s.noteStage(trace, "ranked", "kept")
	}
}

func (s *decisionTraceSet) markReranked(jobs []RankedJob, limit int, usedFallback bool) {
	if s == nil || limit <= 0 {
		return
	}
	if limit > len(jobs) {
		limit = len(jobs)
	}
	reason := "llm_order_applied"
	if usedFallback {
		reason = "fallback_order_applied"
	}
	for idx := 0; idx < limit; idx++ {
		trace := s.ensureCandidate(jobs[idx])
		if trace == nil {
			continue
		}
		if trace.FinalOutcome == "" || trace.FinalOutcome == "ranked" {
			trace.FinalOutcome = "ranked"
		}
		s.noteStage(trace, "rerank", "kept", reason)
	}
}

func (s *decisionTraceSet) markPostSelection(selected, remainder []RankedJob) {
	if s == nil {
		return
	}
	for idx := range selected {
		trace := s.ensureCandidate(selected[idx])
		if trace == nil {
			continue
		}
		trace.PostedRank = idx + 1
		trace.FinalOutcome = "posted"
		s.noteStage(trace, "post", "kept", "selected_for_post")
	}
	for _, job := range remainder {
		trace := s.ensureCandidate(job)
		if trace == nil || trace.FinalOutcome == "posted" {
			continue
		}
		trace.FinalOutcome = "dropped"
		s.noteStage(trace, "post", "dropped", "rank_below_limit")
	}
}

func (s *decisionTraceSet) list() []JobDecisionTrace {
	if s == nil || len(s.items) == 0 {
		return nil
	}
	out := make([]JobDecisionTrace, 0, len(s.items))
	for _, traceID := range s.order {
		trace, ok := s.items[traceID]
		if !ok || trace == nil {
			continue
		}
		copied := *trace
		copied.Reasons = append([]string(nil), trace.Reasons...)
		copied.MatchedKeywords = append([]string(nil), trace.MatchedKeywords...)
		copied.Stages = append([]JobDecisionStage(nil), trace.Stages...)
		out = append(out, copied)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if traceOutcomeOrder(out[i].FinalOutcome) != traceOutcomeOrder(out[j].FinalOutcome) {
			return traceOutcomeOrder(out[i].FinalOutcome) < traceOutcomeOrder(out[j].FinalOutcome)
		}
		if out[i].PostedRank > 0 || out[j].PostedRank > 0 {
			switch {
			case out[i].PostedRank == 0:
				return false
			case out[j].PostedRank == 0:
				return true
			default:
				return out[i].PostedRank < out[j].PostedRank
			}
		}
		if out[i].Rank > 0 || out[j].Rank > 0 {
			switch {
			case out[i].Rank == 0:
				return false
			case out[j].Rank == 0:
				return true
			default:
				return out[i].Rank < out[j].Rank
			}
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].TraceID < out[j].TraceID
	})
	return out
}

func appendUniqueStrings(base []string, values ...string) []string {
	if len(values) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base))
	for _, value := range base {
		if value == "" {
			continue
		}
		seen[value] = struct{}{}
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		base = append(base, value)
	}
	return base
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func traceOutcomeOrder(outcome string) int {
	switch strings.TrimSpace(strings.ToLower(outcome)) {
	case "posted":
		return 0
	case "ranked":
		return 1
	case "dropped":
		return 2
	default:
		return 3
	}
}
