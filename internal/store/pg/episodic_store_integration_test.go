package pg

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestPGEpisodicStore_CreateGetSearch(t *testing.T) {
	dsn := os.Getenv("GOCLAW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOCLAW_TEST_POSTGRES_DSN not set")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}

	ctx := store.WithTenantID(context.Background(), store.MasterTenantID)
	agentID := uuid.New()
	agentKey := "episodic-test-" + agentID.String()
	userID := "episodic-user"

	_, err = db.ExecContext(ctx, `
		INSERT INTO agents (id, agent_key, owner_id, model, tenant_id)
		VALUES ($1, $2, $3, $4, $5)`,
		agentID, agentKey, "owner:test", "gpt-4o-mini", store.MasterTenantID,
	)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM episodic_summaries WHERE agent_id = $1`, agentID)
		_, _ = db.ExecContext(ctx, `DELETE FROM agents WHERE id = $1`, agentID)
	})

	episodic := NewPGEpisodicStore(db)
	sourceID := "session:test:1"
	summary := &store.EpisodicSummary{
		TenantID:   store.MasterTenantID,
		AgentID:    agentID,
		UserID:     userID,
		SessionKey: "agent:test:ws:direct:user1",
		Summary:    "We discussed release rollback blockers, migration sequencing, and the final memory rollout plan.",
		L0Abstract: "Discussed release rollback blockers and the memory rollout plan.",
		KeyTopics:  []string{"release", "rollback", "migration"},
		SourceType: "session",
		SourceID:   sourceID,
		TurnCount:  8,
		TokenCount: 512,
		ExpiresAt:  ptrTime(time.Now().UTC().Add(24 * time.Hour)),
	}

	if err := episodic.Create(ctx, summary); err != nil {
		t.Fatalf("create episodic summary: %v", err)
	}
	if summary.ID == uuid.Nil {
		t.Fatal("expected create to populate summary.ID")
	}

	got, err := episodic.Get(ctx, summary.ID.String())
	if err != nil {
		t.Fatalf("get episodic summary: %v", err)
	}
	if got == nil || got.SourceID != sourceID {
		t.Fatalf("unexpected get result: %+v", got)
	}

	exists, err := episodic.ExistsBySourceID(ctx, agentID.String(), userID, sourceID)
	if err != nil {
		t.Fatalf("exists by source id: %v", err)
	}
	if !exists {
		t.Fatal("expected ExistsBySourceID to return true")
	}

	results, err := episodic.Search(ctx, "rollback blockers", agentID.String(), userID, store.EpisodicSearchOptions{
		MaxResults: 5,
		TextWeight: 1,
	})
	if err != nil {
		t.Fatalf("search episodic summaries: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results")
	}
	if results[0].EpisodicID != summary.ID.String() {
		t.Fatalf("unexpected top result id: got %s want %s", results[0].EpisodicID, summary.ID.String())
	}
	if results[0].L0Abstract == "" {
		t.Fatal("expected l0 abstract in search result")
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
