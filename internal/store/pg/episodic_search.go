package pg

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type episodicScored struct {
	id         string
	sessionKey string
	l0         string
	score      float64
	createdAt  time.Time
}

// Search performs hybrid FTS + vector search over episodic summaries.
func (s *PGEpisodicStore) Search(ctx context.Context, query, agentID, userID string, opts store.EpisodicSearchOptions) ([]store.EpisodicSearchResult, error) {
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}

	textWeight := opts.TextWeight
	vectorWeight := opts.VectorWeight
	if textWeight == 0 && vectorWeight == 0 {
		textWeight = 0.7
		vectorWeight = 0.3
	}

	aid := mustParseUUID(agentID)

	ftsResults, err := s.ftsSearch(ctx, query, aid, userID, maxResults*2)
	if err != nil {
		return nil, err
	}

	var vecResults []episodicScored
	if s.embProvider != nil {
		if vecs, embErr := s.embProvider.Embed(ctx, []string{query}); embErr == nil && len(vecs) > 0 {
			vecResults, err = s.vectorSearch(ctx, vecs[0], aid, userID, maxResults*2)
			if err != nil {
				vecResults = nil
			}
		}
	}

	if len(ftsResults) == 0 && len(vecResults) > 0 {
		textWeight, vectorWeight = 0, 1
	} else if len(vecResults) == 0 && len(ftsResults) > 0 {
		textWeight, vectorWeight = 1, 0
	}

	merged := mergeEpisodicScores(ftsResults, vecResults, textWeight, vectorWeight)
	sort.Slice(merged, func(i, j int) bool { return merged[i].score > merged[j].score })

	var results []store.EpisodicSearchResult
	for _, hit := range merged {
		if opts.MinScore > 0 && hit.score < opts.MinScore {
			continue
		}
		results = append(results, store.EpisodicSearchResult{
			EpisodicID: hit.id,
			L0Abstract: hit.l0,
			Score:      hit.score,
			CreatedAt:  hit.createdAt,
			SessionKey: hit.sessionKey,
		})
		if len(results) >= maxResults {
			break
		}
	}
	return results, nil
}

func (s *PGEpisodicStore) ftsSearch(ctx context.Context, query string, agentID any, userID string, limit int) ([]episodicScored, error) {
	tid := store.TenantIDFromContext(ctx)

	var (
		rows *sql.Rows
		err  error
	)
	if userID != "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, session_key, l0_abstract,
			       ts_rank(search_vector, plainto_tsquery('simple', $1)) AS score,
			       created_at
			FROM episodic_summaries
			WHERE agent_id = $2
			  AND user_id = $3
			  AND tenant_id = $4
			  AND (expires_at IS NULL OR expires_at > NOW())
			  AND search_vector @@ plainto_tsquery('simple', $1)
			ORDER BY score DESC
			LIMIT $5`,
			query, agentID, userID, tid, limit,
		)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, session_key, l0_abstract,
			       ts_rank(search_vector, plainto_tsquery('simple', $1)) AS score,
			       created_at
			FROM episodic_summaries
			WHERE agent_id = $2
			  AND user_id = ''
			  AND tenant_id = $3
			  AND (expires_at IS NULL OR expires_at > NOW())
			  AND search_vector @@ plainto_tsquery('simple', $1)
			ORDER BY score DESC
			LIMIT $4`,
			query, agentID, tid, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []episodicScored
	for rows.Next() {
		var hit episodicScored
		if scanErr := rows.Scan(&hit.id, &hit.sessionKey, &hit.l0, &hit.score, &hit.createdAt); scanErr != nil {
			return nil, scanErr
		}
		results = append(results, hit)
	}
	return results, rows.Err()
}

func (s *PGEpisodicStore) vectorSearch(ctx context.Context, embedding []float32, agentID any, userID string, limit int) ([]episodicScored, error) {
	vec := vectorToString(embedding)
	tid := store.TenantIDFromContext(ctx)

	var (
		rows *sql.Rows
		err  error
	)
	if userID != "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, session_key, l0_abstract,
			       1 - (embedding <=> $1::vector) AS score,
			       created_at
			FROM episodic_summaries
			WHERE agent_id = $2
			  AND user_id = $3
			  AND tenant_id = $4
			  AND (expires_at IS NULL OR expires_at > NOW())
			  AND embedding IS NOT NULL
			ORDER BY embedding <=> $1::vector
			LIMIT $5`,
			vec, agentID, userID, tid, limit,
		)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, session_key, l0_abstract,
			       1 - (embedding <=> $1::vector) AS score,
			       created_at
			FROM episodic_summaries
			WHERE agent_id = $2
			  AND user_id = ''
			  AND tenant_id = $3
			  AND (expires_at IS NULL OR expires_at > NOW())
			  AND embedding IS NOT NULL
			ORDER BY embedding <=> $1::vector
			LIMIT $4`,
			vec, agentID, tid, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []episodicScored
	for rows.Next() {
		var hit episodicScored
		if scanErr := rows.Scan(&hit.id, &hit.sessionKey, &hit.l0, &hit.score, &hit.createdAt); scanErr != nil {
			return nil, scanErr
		}
		results = append(results, hit)
	}
	return results, rows.Err()
}

func mergeEpisodicScores(fts, vec []episodicScored, textWeight, vectorWeight float64) []episodicScored {
	byID := make(map[string]*episodicScored)
	for _, hit := range fts {
		byID[hit.id] = &episodicScored{
			id:         hit.id,
			sessionKey: hit.sessionKey,
			l0:         hit.l0,
			score:      hit.score * textWeight,
			createdAt:  hit.createdAt,
		}
	}
	for _, hit := range vec {
		if existing, ok := byID[hit.id]; ok {
			existing.score += hit.score * vectorWeight
			continue
		}
		byID[hit.id] = &episodicScored{
			id:         hit.id,
			sessionKey: hit.sessionKey,
			l0:         hit.l0,
			score:      hit.score * vectorWeight,
			createdAt:  hit.createdAt,
		}
	}

	merged := make([]episodicScored, 0, len(byID))
	for _, hit := range byID {
		merged = append(merged, *hit)
	}
	return merged
}
