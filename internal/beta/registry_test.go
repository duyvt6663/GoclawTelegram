package beta

import (
	"context"
	"fmt"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestActivatePendingHotActivatesEnabledFeature(t *testing.T) {
	t.Cleanup(func() { ShutdownAll(context.Background()) })

	feature := &testRegistryFeature{name: "test_hot_activate_pending"}
	Register(feature)

	sysConfigs := newTestSystemConfigStore()
	flags := NewFlagSource(sysConfigs)
	ctx := store.WithTenantID(context.Background(), store.MasterTenantID)

	if got := ActivatePending(ctx, flags, Deps{}); len(got) != 0 {
		t.Fatalf("ActivatePending() before enable = %v, want empty", got)
	}
	if IsActive(feature.name) {
		t.Fatalf("feature %q should not be active before enable", feature.name)
	}

	if err := sysConfigs.Set(ctx, "beta."+feature.name, "true"); err != nil {
		t.Fatalf("set system config: %v", err)
	}

	got := ActivatePending(ctx, flags, Deps{})
	if len(got) != 1 || got[0] != feature.name {
		t.Fatalf("ActivatePending() = %v, want [%q]", got, feature.name)
	}
	if !IsActive(feature.name) {
		t.Fatalf("feature %q should be active after enable", feature.name)
	}
	if feature.initCount != 1 {
		t.Fatalf("initCount = %d, want 1", feature.initCount)
	}

	if got := ActivatePending(ctx, flags, Deps{}); len(got) != 0 {
		t.Fatalf("ActivatePending() second call = %v, want empty", got)
	}
	if feature.initCount != 1 {
		t.Fatalf("initCount after second call = %d, want 1", feature.initCount)
	}
}

func TestShutdownAllStopsActiveFeatures(t *testing.T) {
	feature := &testRegistryFeature{name: "test_shutdown_all"}
	Register(feature)

	sysConfigs := newTestSystemConfigStore()
	flags := NewFlagSource(sysConfigs)
	ctx := store.WithTenantID(context.Background(), store.MasterTenantID)
	if err := sysConfigs.Set(ctx, "beta."+feature.name, "true"); err != nil {
		t.Fatalf("set system config: %v", err)
	}

	activated, err := EnsureActive(ctx, flags, Deps{}, feature.name)
	if err != nil {
		t.Fatalf("EnsureActive() error = %v", err)
	}
	if !activated {
		t.Fatalf("EnsureActive() = false, want true")
	}

	ShutdownAll(context.Background())

	if feature.shutdownCount != 1 {
		t.Fatalf("shutdownCount = %d, want 1", feature.shutdownCount)
	}
	if IsActive(feature.name) {
		t.Fatalf("feature %q should not remain active after ShutdownAll", feature.name)
	}
}

type testRegistryFeature struct {
	name          string
	initCount     int
	shutdownCount int
}

func (f *testRegistryFeature) Name() string { return f.name }

func (f *testRegistryFeature) Init(Deps) error {
	f.initCount++
	return nil
}

func (f *testRegistryFeature) Shutdown(context.Context) error {
	f.shutdownCount++
	return nil
}

type testSystemConfigStore struct {
	values map[string]string
}

func newTestSystemConfigStore() *testSystemConfigStore {
	return &testSystemConfigStore{values: make(map[string]string)}
}

func (s *testSystemConfigStore) Get(ctx context.Context, key string) (string, error) {
	value, ok := s.values[s.scopedKey(ctx, key)]
	if !ok {
		return "", fmt.Errorf("not found")
	}
	return value, nil
}

func (s *testSystemConfigStore) Set(ctx context.Context, key, value string) error {
	s.values[s.scopedKey(ctx, key)] = value
	return nil
}

func (s *testSystemConfigStore) Delete(ctx context.Context, key string) error {
	delete(s.values, s.scopedKey(ctx, key))
	return nil
}

func (s *testSystemConfigStore) List(ctx context.Context) (map[string]string, error) {
	result := make(map[string]string)
	prefix := s.scopedKey(ctx, "")
	for scopedKey, value := range s.values {
		if len(scopedKey) >= len(prefix) && scopedKey[:len(prefix)] == prefix {
			result[scopedKey[len(prefix):]] = value
		}
	}
	return result, nil
}

func (s *testSystemConfigStore) scopedKey(ctx context.Context, key string) string {
	return store.TenantIDFromContext(ctx).String() + ":" + key
}
