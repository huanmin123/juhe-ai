package auditlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteHotSearchAppendSearchAndTrim(t *testing.T) {
	root := t.TempDir()
	cfg := sqliteConfig(t, root)
	store := openSQLiteStore(t, cfg)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	input := fixture("hot-search-one", LifecycleFinalized)
	input.CreatedAt = time.Date(2026, 8, 9, 10, 15, 0, 0, time.UTC).Format(time.RFC3339Nano)
	input.ErrorMessage = "needle error"
	if n, err := store.AppendHotSearch(ctx, lease, []AuditLogInput{input}); err != nil || n == 0 {
		t.Fatalf("append n=%d err=%v", n, err)
	}
	result, err := store.SearchHotSearch(ctx, HotSearchOptions{Keywords: []string{"needle"}, StartAt: time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC), EndAt: time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AuditLogIDs) != 1 || result.AuditLogIDs[0] != input.ID {
		t.Fatalf("search result=%+v", result)
	}
	retention, err := store.CleanupRetention(ctx, lease, RetentionConfig{
		SuccessHotCutoff: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), SuccessCutoff: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), FailureCutoff: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), ErrorGroupCutoff: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), BatchSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retention.DeletedHotSearchFiles != 1 {
		t.Fatalf("trim result=%+v", retention)
	}
	if _, err := os.Stat(filepath.Join(root, "search-hot")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestSQLiteHotSearchIndexesHotRetainedSuccessfulPayload(t *testing.T) {
	root := t.TempDir()
	store := openSQLiteStore(t, sqliteConfig(t, root))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	now := time.Now().UTC().Truncate(time.Second)
	input := fixture("hot-search-success-body", LifecycleFinalized)
	input.AuditOutcome = AuditOutcomeSuccess
	input.SampleReason = "success_hot_full_retention"
	input.CreatedAt = now.Format(time.RFC3339Nano)
	input.Payloads = []AuditLogPayloadInput{{
		PartType:      PayloadPartClientRequest,
		ContentType:   "application/json",
		CaptureStatus: PayloadCaptureComplete,
		Body:          PayloadBody{Bytes: []byte(`{"prompt":"hot-success-body-needle"}`), Present: true},
	}}
	if n, err := store.AppendHotSearch(ctx, lease, []AuditLogInput{input}); err != nil || n == 0 {
		t.Fatalf("append n=%d err=%v", n, err)
	}
	result, err := store.SearchHotSearch(ctx, HotSearchOptions{
		Keywords: []string{"hot-success-body-needle"},
		StartAt:  now.Add(-time.Minute),
		EndAt:    now.Add(time.Minute),
		Limit:    10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AuditLogIDs) != 1 || result.AuditLogIDs[0] != input.ID {
		t.Fatalf("search result=%+v", result)
	}
}

func TestSQLiteHotSearchRejectsLostLeaseBeforeFileWrite(t *testing.T) {
	root := t.TempDir()
	store := openSQLiteStore(t, sqliteConfig(t, root))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	input := fixture("hot-search-lost", LifecycleFinalized)
	_, err := store.AppendHotSearch(ctx, lease, []AuditLogInput{input})
	if !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("lost lease error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "search-hot")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost lease created hot directory: %v", err)
	}
}
