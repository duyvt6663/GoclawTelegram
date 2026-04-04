package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/sodaubai"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestSoDauBaiManageToolRequiresLopTruong(t *testing.T) {
	svc := sodaubai.NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	tool := NewSoDauBaiManageTool(svc)

	ctx := store.WithSenderID(context.Background(), "999|someone_else")
	result := tool.Execute(ctx, map[string]any{
		"action": "add",
		"target": "@kryptonite2304",
	})
	if !result.IsError || !strings.Contains(result.ForLLM, "@duyvt6663") {
		t.Fatalf("Execute() = %+v, want permission error mentioning @duyvt6663", result)
	}
}

func TestSoDauBaiManageAndListTools(t *testing.T) {
	svc := sodaubai.NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	manage := NewSoDauBaiManageTool(svc)
	list := NewSoDauBaiTodayTool(svc)

	ctx := store.WithSenderID(context.Background(), "123|duyvt6663")
	addResult := manage.Execute(ctx, map[string]any{
		"action": "add",
		"target": "@kryptonite2304",
		"note":   "too loud",
	})
	if addResult.IsError {
		t.Fatalf("add Execute() error = %s", addResult.ForLLM)
	}
	if !strings.Contains(addResult.ForLLM, "Added @kryptonite2304") {
		t.Fatalf("add Execute() = %q", addResult.ForLLM)
	}

	listResult := list.Execute(context.Background(), nil)
	if listResult.IsError {
		t.Fatalf("list Execute() error = %s", listResult.ForLLM)
	}
	if !strings.Contains(listResult.ForLLM, "@kryptonite2304") || !strings.Contains(listResult.ForLLM, "too loud") {
		t.Fatalf("list Execute() = %q, want target and note", listResult.ForLLM)
	}

	removeResult := manage.Execute(ctx, map[string]any{
		"action": "remove",
		"target": "kryptonite2304",
	})
	if removeResult.IsError {
		t.Fatalf("remove Execute() error = %s", removeResult.ForLLM)
	}
	if !strings.Contains(removeResult.ForLLM, "Removed @kryptonite2304") {
		t.Fatalf("remove Execute() = %q", removeResult.ForLLM)
	}
}

func TestSoDauBaiManageCannotRemoveAlwaysDeniedUser(t *testing.T) {
	svc := sodaubai.NewService(filepath.Join(t.TempDir(), "so-dau-bai.json"))
	manage := NewSoDauBaiManageTool(svc)
	list := NewSoDauBaiTodayTool(svc)

	scope := sodaubai.ScopeKey("telegram-main", "-100123", "-100123")
	svc.SetAlways(scope, []string{"@kryptonite2304"})

	ctx := store.WithSenderID(context.Background(), "123|duyvt6663")
	ctx = WithToolChannel(ctx, "telegram-main")
	ctx = WithToolChatID(ctx, "-100123")
	ctx = WithToolLocalKey(ctx, "-100123")

	listResult := list.Execute(ctx, nil)
	if listResult.IsError {
		t.Fatalf("list Execute() error = %s", listResult.ForLLM)
	}
	if !strings.Contains(listResult.ForLLM, "@kryptonite2304") || !strings.Contains(listResult.ForLLM, "deny_from") {
		t.Fatalf("list Execute() = %q, want deny_from entry", listResult.ForLLM)
	}

	removeResult := manage.Execute(ctx, map[string]any{
		"action": "remove",
		"target": "@kryptonite2304",
	})
	if !removeResult.IsError || !strings.Contains(removeResult.ForLLM, "deny_from") {
		t.Fatalf("remove Execute() = %+v, want deny_from refusal", removeResult)
	}
}
