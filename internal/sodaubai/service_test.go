package sodaubai

import (
	"path/filepath"
	"testing"
	"time"
)

func TestServiceResetsWhenDayChanges(t *testing.T) {
	svc := NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	loc := time.FixedZone("UTC+7", 7*60*60)
	svc.loc = loc
	svc.now = func() time.Time {
		return time.Date(2026, time.April, 4, 10, 0, 0, 0, loc)
	}

	if _, added, err := svc.AddToday("@kryptonite2304", "@duyvt6663", "too much chaos"); err != nil {
		t.Fatalf("AddToday() error = %v", err)
	} else if !added {
		t.Fatal("AddToday() did not add entry")
	}

	state, err := svc.Today()
	if err != nil {
		t.Fatalf("Today() error = %v", err)
	}
	if state.Date != "2026-04-04" || len(state.Entries) != 1 {
		t.Fatalf("Today() = %+v, want date 2026-04-04 with 1 entry", state)
	}

	svc.now = func() time.Time {
		return time.Date(2026, time.April, 5, 8, 0, 0, 0, loc)
	}

	state, err = svc.Today()
	if err != nil {
		t.Fatalf("Today() after rollover error = %v", err)
	}
	if state.Date != "2026-04-05" {
		t.Fatalf("Today().Date = %q, want 2026-04-05", state.Date)
	}
	if len(state.Entries) != 0 {
		t.Fatalf("Today().Entries = %v, want reset to empty", state.Entries)
	}

	match, err := svc.MatchToday("123|kryptonite2304", "123")
	if err != nil {
		t.Fatalf("MatchToday() error = %v", err)
	}
	if match != nil {
		t.Fatalf("MatchToday() = %+v, want nil after daily reset", match)
	}
}

func TestServiceMatchAndRemoveToday(t *testing.T) {
	svc := NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	loc := time.FixedZone("UTC+7", 7*60*60)
	svc.loc = loc
	svc.now = func() time.Time {
		return time.Date(2026, time.April, 4, 12, 0, 0, 0, loc)
	}

	if _, _, err := svc.AddToday("@kryptonite2304", "@duyvt6663", ""); err != nil {
		t.Fatalf("AddToday() error = %v", err)
	}

	match, err := svc.MatchToday("999|kryptonite2304", "999")
	if err != nil {
		t.Fatalf("MatchToday() error = %v", err)
	}
	if match == nil || match.Target != "@kryptonite2304" {
		t.Fatalf("MatchToday() = %+v, want target @kryptonite2304", match)
	}

	removed, ok, err := svc.RemoveToday("kryptonite2304")
	if err != nil {
		t.Fatalf("RemoveToday() error = %v", err)
	}
	if !ok || removed == nil || removed.Target != "@kryptonite2304" {
		t.Fatalf("RemoveToday() = %+v, %v; want removed @kryptonite2304", removed, ok)
	}

	match, err = svc.MatchToday("999|kryptonite2304", "999")
	if err != nil {
		t.Fatalf("MatchToday() after remove error = %v", err)
	}
	if match != nil {
		t.Fatalf("MatchToday() after remove = %+v, want nil", match)
	}
}

func TestServiceScopeAlwaysRulesAreIncludedAndPersistAcrossDayRollover(t *testing.T) {
	svc := NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	loc := time.FixedZone("UTC+7", 7*60*60)
	svc.loc = loc
	svc.now = func() time.Time {
		return time.Date(2026, time.April, 4, 12, 0, 0, 0, loc)
	}

	scope := ScopeKey("telegram-main", "-100123:topic:9", "-100123")
	svc.SetAlways(scope, []string{"@kryptonite2304", "@kryptonite2304", "123456"})

	state, err := svc.TodayForScope(scope)
	if err != nil {
		t.Fatalf("TodayForScope() error = %v", err)
	}
	if len(state.Entries) != 2 {
		t.Fatalf("TodayForScope().Entries = %v, want 2 unique always entries", state.Entries)
	}

	match, err := svc.MatchTodayForScope(scope, "999|kryptonite2304", "999")
	if err != nil {
		t.Fatalf("MatchTodayForScope() error = %v", err)
	}
	if match == nil || match.Target != "@kryptonite2304" {
		t.Fatalf("MatchTodayForScope() = %+v, want @kryptonite2304", match)
	}

	svc.now = func() time.Time {
		return time.Date(2026, time.April, 5, 8, 0, 0, 0, loc)
	}

	state, err = svc.TodayForScope(scope)
	if err != nil {
		t.Fatalf("TodayForScope() after rollover error = %v", err)
	}
	if state.Date != "2026-04-05" {
		t.Fatalf("TodayForScope().Date = %q, want 2026-04-05", state.Date)
	}
	if len(state.Entries) != 2 {
		t.Fatalf("TodayForScope().Entries after rollover = %v, want always entries preserved", state.Entries)
	}
}
