package oauthrefresh

// w12f_oauthrefresh_arms_test.go 覆盖刷新族的 SQL fail-closed 错误臂与
// 组合分支：store/availability/keepalive/healthfanout 在句柄关闭或已回滚
// 事务上的原始错误传播、account 锁 busy、Gemini fallback 链、runner 的
// jitter 与失败计数臂。

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func w12fClosedStore(t *testing.T) *Store {
	t.Helper()
	store, db, _ := newTestStore(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return store
}

func w12fClosedTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestW12fOauthClosedStoreArms(t *testing.T) {
	store := w12fClosedStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

	// store.go 查询链第一错误。
	if _, err := store.openAIProfileIDs(ctx); err == nil {
		t.Fatal("句柄关闭后 openAIProfileIDs 必须报错")
	}
	if _, err := store.ListDueOpenAIRefreshAccounts(ctx, 60, 10, "", now); err == nil {
		t.Fatal("句柄关闭后 ListDueOpenAIRefreshAccounts 必须报错")
	}
	if _, err := store.FindRotationAccount(ctx, "w12f-acc"); err == nil {
		t.Fatal("句柄关闭后 FindRotationAccount 必须报错")
	}
	if _, err := store.RotateCredentials(ctx, RotateCredentialsInput{AccountID: "w12f-acc"}); err == nil {
		t.Fatal("句柄关闭后 RotateCredentials 必须报错")
	}
	if _, err := store.UpdateAccountCredentials(ctx, "w12f-acc", map[string]any{"access_token": "w12f"}, 1); err == nil {
		t.Fatal("句柄关闭后 UpdateAccountCredentials 必须报错")
	}
	if _, err := store.ListDueKeepaliveAccounts(ctx, "openai", "api_key", "", time.Hour, 10, now); err == nil {
		t.Fatal("句柄关闭后 ListDueKeepaliveAccounts 必须报错")
	}

	// availability.go 查询链第一错误。
	if _, err := store.SyncApiKeyScheduleStatuses(ctx, now, 10); err == nil {
		t.Fatal("句柄关闭后 SyncApiKeyScheduleStatuses 必须报错")
	}
	if _, err := store.SyncAccountScheduleStatuses(ctx, now, 10, nil); err == nil {
		t.Fatal("句柄关闭后 SyncAccountScheduleStatuses 必须报错")
	}
}

func TestW12fOauthScheduleUpdateBadTxArams(t *testing.T) {
	store, db, _ := newTestStore(t)
	closed := w12fClosedStore(t)
	ctx := context.Background()
	badTx := w12fClosedTx(t, db)
	updates := []scheduleSyncUpdate{{id: "w12f-id", nextCheckAt: "2026-09-14T10:00:00.000Z", status: "active"}}

	// 空更新 → 直接成功（无事务）。
	if err := store.applyScheduleUpdates(ctx, store.table("api_keys"), store.table("api_key_schedule_status_events"), "api_key_id", false, nil, isoMillis(time.Now()), nil, nil); err != nil {
		t.Fatalf("空更新必须成功: %v", err)
	}
	// 关闭句柄上的事务开启失败。
	if err := closed.applyScheduleUpdates(ctx, closed.table("api_keys"), closed.table("api_key_schedule_status_events"), "api_key_id", false, updates, isoMillis(time.Now()), &ScheduleStatusSyncResult{}, nil); err == nil {
		t.Fatal("句柄关闭后 applyScheduleUpdates 必须报错")
	}
	// 回滚事务上的 nextCheckAt 与事件写入。
	if err := store.updateScheduleNextCheckAt(ctx, badTx, store.table("api_keys"), "w12f-id", "2026-09-14T10:00:00.000Z"); err == nil {
		t.Fatal("回滚事务上 updateScheduleNextCheckAt 必须报错")
	}
	executed, err := store.insertScheduleStatusEvent(ctx, badTx, store.table("api_key_schedule_status_events"), "api_key_id", scheduleSyncUpdate{id: "w12f-id"}, isoMillis(time.Now()))
	if err == nil {
		t.Fatal("回滚事务上 insertScheduleStatusEvent 必须报错")
	}
	_ = executed
	flip, err := store.applyScheduleStatusFlip(ctx, badTx, store.table("api_keys"), false, scheduleSyncUpdate{id: "w12f-id", status: "active"}, isoMillis(time.Now()), &ScheduleStatusSyncResult{})
	if err == nil {
		t.Fatal("回滚事务上 applyScheduleStatusFlip 必须报错")
	}
	_ = flip
}

func TestW12fOauthKeepaliveAndHealthFanoutArms(t *testing.T) {
	store := w12fClosedStore(t)
	ctx := context.Background()
	// keepalive 列表查询错误。
	if _, err := store.ListDueKeepaliveAccounts(ctx, "openai", "api_key", "", time.Hour, 10, time.Now()); err == nil {
		t.Fatal("句柄关闭后 keepalive 列表必须报错")
	}
	// healthfanout：非 account 资源直接 0；pg 方言锁后缀分支 + 坏 tx。
	_, db, _ := newTestStore(t)
	badTx := w12fClosedTx(t, db)
	zero, err := store.EnqueueAccountHealthInputsForAuthorizationSourceTx(ctx, badTx, "provider", "w12f-src", AuthorizationGrantHealthFanoutReason)
	if err != nil || zero != 0 {
		t.Fatalf("非 account 资源必须零值返回: %d %v", zero, err)
	}
	pgStore := &Store{db: db, pg: true, secret: "w12f", now: store.now}
	if _, err := pgStore.EnqueueAccountHealthInputsForAuthorizationSourceTx(ctx, badTx, "account", "w12f-src", AuthorizationGrantHealthFanoutReason); err == nil {
		t.Fatal("回滚事务必须报错")
	}
	if _, err := store.EnqueueAccountHealthInputsForAuthorizationSourceTx(ctx, badTx, "account", "w12f-src", AuthorizationGrantHealthFanoutReason); err == nil {
		t.Fatal("回滚事务必须报错")
	}
}

func TestW12fOauthAccountLockBusy(t *testing.T) {
	job := NewRefreshJob(w12fClosedStore(t), nil, WithClock(ClockFunc(func() time.Time { return time.UnixMilli(1_700_000_000_000).UTC() })))
	err := job.locks.TryLock("w12f-acc", func() error {
		inner := job.locks.TryLock("w12f-acc", func() error { return nil })
		var busy *LockBusyError
		if !errors.As(inner, &busy) {
			t.Fatalf("嵌套锁定必须返回 LockBusyError: %v", inner)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("外层任务不得报错: %v", err)
	}
}

// --- providers Gemini fallback 链 ---

func TestW12fGeminiBuildCredentialsFallback(t *testing.T) {
	fallback := &GeminiCredentialFallback{
		RefreshToken: "w12f-rt", OAuthType: "google_one", ClientID: "w12f-cid",
		ClientSecret: "w12f-secret", ProjectID: "w12f-project", TierID: "ai_premium",
		QuotaProjectID: "w12f-quota", BaseURL: "https://w12f.example", Scope: "w12f-scope",
	}
	info := &GeminiTokenInfo{AccessToken: "w12f-at", ExpiresAt: "2026-09-14T10:00:00Z", TokenType: "Bearer"}
	credentials := BuildGeminiOAuthCredentials(info, fallback)
	for _, key := range []string{"access_token", "refresh_token", "oauth_type", "base_url", "scope", "project_id", "tier_id", "quota_project_id", "expires_at", "token_type"} {
		if credentials[key] == nil || credentials[key] == "" {
			t.Fatalf("fallback 必须补齐 %s", key)
		}
	}
	// info 全量提供时 fallback 不覆盖。
	full := &GeminiTokenInfo{AccessToken: "w12f-at", OAuthType: "ai_studio", TierID: "paid", ProjectID: "w12f-p", Scope: "w12f-s", BaseURL: "https://w12f2.example"}
	credentials = BuildGeminiOAuthCredentials(full, nil)
	if credentials["tier_id"] != "aistudio_paid" {
		t.Fatalf("canonical tier=%v", credentials["tier_id"])
	}
	if _, ok := credentials["supported_endpoint_modes"]; ok {
		t.Fatal("ai_studio 不得带 vertex endpoint modes")
	}
	if credentials["oauth_type"] != "ai_studio" {
		t.Fatalf("oauth_type=%v", credentials["oauth_type"])
	}
}

// --- runner jitter / 失败计数臂 ---

func TestW12fRunnerPassiveJitterArm(t *testing.T) {
	cfg := RunnerConfig{Interval: time.Millisecond, RunTimeout: 10 * time.Millisecond, PassiveJitter: true}
	var runs int
	runner := NewRunner("w12f-jitter", cfg, func(ctx context.Context) error {
		runs++
		return nil
	}, ClockFunc(func() time.Time { return time.UnixMilli(1_700_000_000_000).UTC() }), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后必须返回 ctx.Err: %v", err)
	}
	if runs == 0 {
		t.Fatal("至少执行一次任务")
	}
}
