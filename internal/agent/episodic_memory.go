package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const (
	defaultAutoInjectThreshold = 0.3
	defaultAutoInjectMaxTokens = 200
	defaultEpisodicTTLDays     = 90
	autoInjectMaxEntries       = 5
)

var episodicTrivialStopwords = map[string]bool{
	"hi": true, "hello": true, "hey": true, "ok": true, "okay": true,
	"yes": true, "no": true, "thanks": true, "thank": true, "you": true,
	"sure": true, "right": true, "got": true, "it": true, "the": true,
	"a": true, "an": true, "is": true, "are": true, "was": true, "i": true,
	"me": true, "my": true, "we": true, "do": true, "did": true, "please": true,
	"good": true, "great": true, "nice": true, "hmm": true, "ah": true,
	"oh": true, "um": true, "well": true, "so": true, "and": true,
	"but": true, "or": true, "that": true, "this": true,
}

func (l *Loop) buildAutoMemorySection(ctx context.Context, userMessage string) string {
	if !l.hasMemory || l.episodicStore == nil || l.agentUUID == uuid.Nil {
		return ""
	}
	if !autoInjectEnabled(l.memoryCfg) || isTrivialMemoryMessage(userMessage) {
		return ""
	}

	userID := store.MemoryUserID(ctx)
	opts := store.EpisodicSearchOptions{
		MaxResults:   autoInjectMaxEntries * 2,
		MinScore:     autoInjectThreshold(l.memoryCfg),
		VectorWeight: 0.3,
		TextWeight:   0.7,
	}

	results := l.searchEpisodicResults(ctx, userMessage, l.agentUUID.String(), userID, opts)
	if leaderID := tools.LeaderAgentIDFromCtx(ctx); leaderID != "" && leaderID != l.agentUUID.String() {
		results = append(results, l.searchEpisodicResults(ctx, userMessage, leaderID, userID, opts)...)
	}
	results = dedupeAndSortEpisodicResults(results)
	if len(results) == 0 {
		return ""
	}

	maxChars := max(autoInjectMaxTokens(l.memoryCfg)*4, 256)
	var sb strings.Builder
	sb.WriteString("## Memory Context\n\nRelevant memories from past sessions (use memory_search for details):\n")

	injected := 0
	for _, r := range results {
		if injected >= autoInjectMaxEntries {
			break
		}
		text := strings.TrimSpace(r.L0Abstract)
		if text == "" {
			continue
		}

		line := "- " + text + "\n"
		if sb.Len()+len(line) > maxChars {
			if injected > 0 {
				break
			}
			remaining := maxChars - sb.Len() - len("- \n")
			if remaining <= 8 {
				break
			}
			line = "- " + truncateRunes(text, remaining) + "\n"
		}
		sb.WriteString(line)
		injected++
	}

	if injected == 0 {
		return ""
	}
	return sb.String()
}

func (l *Loop) maybeCreateEpisodicSummary(ctx context.Context, sessionKey string, tokenCount int) {
	if !l.hasMemory || l.episodicStore == nil || l.agentUUID == uuid.Nil {
		return
	}

	userID := store.MemoryUserID(ctx)
	sourceID := fmt.Sprintf("%s:%d", sessionKey, l.sessions.GetCompactionCount(ctx, sessionKey))

	muI, _ := l.episodicMu.LoadOrStore(sourceID, &sync.Mutex{})
	mu := muI.(*sync.Mutex)
	if !mu.TryLock() {
		return
	}
	defer func() {
		mu.Unlock()
		l.episodicMu.Delete(sourceID)
	}()

	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()

	exists, err := l.episodicStore.ExistsBySourceID(bgCtx, l.agentUUID.String(), userID, sourceID)
	if err != nil {
		slog.Warn("episodic summary dedup failed", "agent", l.id, "session", sessionKey, "error", err)
		return
	}
	if exists {
		return
	}

	history := l.sessions.GetHistory(bgCtx, sessionKey)
	summary := strings.TrimSpace(l.sessions.GetSummary(bgCtx, sessionKey))
	if summary == "" {
		summary = strings.TrimSpace(l.summarizeEpisodicHistory(bgCtx, history))
	}
	if summary == "" {
		return
	}

	if tokenCount <= 0 {
		tokenCount, _ = l.sessions.GetLastPromptTokens(bgCtx, sessionKey)
	}

	expiresAt := time.Now().UTC().Add(time.Duration(episodicTTLDays(l.memoryCfg)) * 24 * time.Hour)
	ep := &store.EpisodicSummary{
		TenantID:   store.TenantIDFromContext(bgCtx),
		AgentID:    l.agentUUID,
		UserID:     userID,
		SessionKey: sessionKey,
		Summary:    summary,
		L0Abstract: generateL0Abstract(summary),
		KeyTopics:  extractEntityNames(summary),
		SourceType: "session",
		SourceID:   sourceID,
		TurnCount:  len(history),
		TokenCount: tokenCount,
		ExpiresAt:  &expiresAt,
	}

	if err := l.episodicStore.Create(bgCtx, ep); err != nil {
		slog.Warn("episodic summary create failed", "agent", l.id, "session", sessionKey, "error", err)
		return
	}

	slog.Debug("episodic summary created", "agent", l.id, "session", sessionKey, "source_id", sourceID)
}

func (l *Loop) summarizeEpisodicHistory(ctx context.Context, history []providers.Message) string {
	if l.provider == nil || len(history) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, m := range history {
		if m.Role == "system" {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		content = truncateRunes(content, 500)
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(content)
		sb.WriteByte('\n')
		if sb.Len() > 8000 {
			sb.WriteString("...(truncated)\n")
			break
		}
	}
	if sb.Len() == 0 {
		return ""
	}

	resp, err := l.provider.Chat(ctx, providers.ChatRequest{
		Messages: []providers.Message{{
			Role:    "user",
			Content: compactionSummaryPrompt + sb.String(),
		}},
		Model:   l.model,
		Options: map[string]any{"max_tokens": 1024, "temperature": 0.3},
	})
	if err != nil {
		slog.Warn("episodic summary generation failed", "agent", l.id, "error", err)
		return ""
	}
	return SanitizeAssistantContent(resp.Content)
}

func (l *Loop) searchEpisodicResults(ctx context.Context, query, agentID, userID string, opts store.EpisodicSearchOptions) []store.EpisodicSearchResult {
	results, err := l.episodicStore.Search(ctx, query, agentID, userID, opts)
	if err == nil && (len(results) > 0 || userID == "") {
		return results
	}
	if userID == "" {
		return nil
	}
	fallback, ferr := l.episodicStore.Search(ctx, query, agentID, "", opts)
	if ferr != nil {
		return nil
	}
	return fallback
}

func dedupeAndSortEpisodicResults(results []store.EpisodicSearchResult) []store.EpisodicSearchResult {
	if len(results) <= 1 {
		return results
	}

	seen := make(map[string]int, len(results))
	deduped := make([]store.EpisodicSearchResult, 0, len(results))
	for _, r := range results {
		if idx, ok := seen[r.EpisodicID]; ok {
			if r.Score > deduped[idx].Score {
				deduped[idx] = r
			}
			continue
		}
		seen[r.EpisodicID] = len(deduped)
		deduped = append(deduped, r)
	}

	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].Score == deduped[j].Score {
			return deduped[i].CreatedAt.After(deduped[j].CreatedAt)
		}
		return deduped[i].Score > deduped[j].Score
	})
	return deduped
}

func autoInjectEnabled(cfg *config.MemoryConfig) bool {
	if cfg == nil || cfg.AutoInjectEnabled == nil {
		return true
	}
	return *cfg.AutoInjectEnabled
}

func autoInjectThreshold(cfg *config.MemoryConfig) float64 {
	if cfg != nil && cfg.AutoInjectThreshold > 0 {
		return cfg.AutoInjectThreshold
	}
	return defaultAutoInjectThreshold
}

func autoInjectMaxTokens(cfg *config.MemoryConfig) int {
	if cfg != nil && cfg.AutoInjectMaxTokens > 0 {
		return cfg.AutoInjectMaxTokens
	}
	return defaultAutoInjectMaxTokens
}

func episodicTTLDays(cfg *config.MemoryConfig) int {
	if cfg != nil && cfg.EpisodicTTLDays > 0 {
		return cfg.EpisodicTTLDays
	}
	return defaultEpisodicTTLDays
}

func isTrivialMemoryMessage(msg string) bool {
	words := strings.Fields(strings.ToLower(msg))
	meaningful := 0
	for _, w := range words {
		w = strings.Trim(w, ".,!?;:'\"()-")
		if len(w) > 0 && !episodicTrivialStopwords[w] {
			meaningful++
			if meaningful >= 3 {
				return false
			}
		}
	}
	return true
}

func generateL0Abstract(summary string) string {
	sentences := splitSentences(summary)
	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)
		runes := []rune(sentence)
		if len(runes) < 20 {
			continue
		}
		if len(runes) > 200 {
			return string(runes[:200]) + "..."
		}
		return sentence
	}
	return truncateRunes(summary, 200)
}

func splitSentences(text string) []string {
	var sentences []string
	var current strings.Builder

	runes := []rune(text)
	for i, r := range runes {
		current.WriteRune(r)
		if (r == '.' || r == '!' || r == '?') && i+1 < len(runes) &&
			(unicode.IsSpace(runes[i+1]) || runes[i+1] == '\n') {
			sentences = append(sentences, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		sentences = append(sentences, current.String())
	}
	return sentences
}

func extractEntityNames(text string) []string {
	words := strings.Fields(text)
	seen := make(map[string]bool)
	var entities []string

	for i := 0; i < len(words); i++ {
		word := words[i]
		runes := []rune(word)
		if len(runes) < 2 || !unicode.IsUpper(runes[0]) {
			continue
		}

		phrase := cleanEntityWord(word)
		for j := i + 1; j < len(words); j++ {
			next := words[j]
			nextRunes := []rune(next)
			if len(nextRunes) < 2 || !unicode.IsUpper(nextRunes[0]) {
				break
			}
			phrase += " " + cleanEntityWord(next)
			i = j
		}
		if len(phrase) >= 3 && !seen[phrase] {
			seen[phrase] = true
			entities = append(entities, phrase)
			if len(entities) >= 20 {
				break
			}
		}
	}
	return entities
}

func cleanEntityWord(word string) string {
	return strings.TrimRightFunc(word, func(r rune) bool {
		return unicode.IsPunct(r) && r != '-' && r != '\''
	})
}

func truncateRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}
