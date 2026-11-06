package oauthrefresh

// B3 闸门版本栅栏：闸门同步事务提交后按 ChangedIDs 递增共享序号；未接线的
// 端口保持既有 60s 窗口（bump 函数 nil 安全）。

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeStateVersionBumper struct {
	mu       sync.Mutex
	accounts []string
	apiKeys  []string
	err      error
}

func (f *fakeStateVersionBumper) BumpAccountState(_ context.Context, accountID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts = append(f.accounts, accountID)
	return f.err
}

func (f *fakeStateVersionBumper) BumpAPIKeyState(_ context.Context, apiKeyID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiKeys = append(f.apiKeys, apiKeyID)
	return f.err
}

func TestScheduleSyncBumpsStateVersions(t *testing.T) {
	store, db, _ := newTestStore(t)
	bumper := &fakeStateVersionBumper{}
	store.SetStateVersionBumpers(bumper, bumper)

	// 账户：窗口开始边界 0 → 1。
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedAccountWithSchedule(t, db, "acc-version", availabilityScheduleJSON, "", "active", false)
	setScheduleGate(t, db, "accounts", "acc-version", 0)
	result, err := store.SyncAccountScheduleStatuses(context.Background(), atStart, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 1 {
		t.Fatalf("account result=%+v, want 1 activated", result)
	}

	// API Key：窗口外 NULL → 0。
	seedApiKeyWithSchedule(t, db, "key-version", w12fScheduleJSON(t), "", "active")
	now := w12fBaseNow().Add(10*time.Hour + 30*time.Minute)
	if _, err := store.SyncApiKeyScheduleStatuses(context.Background(), now, 10, nil); err != nil {
		t.Fatal(err)
	}

	bumper.mu.Lock()
	defer bumper.mu.Unlock()
	if len(bumper.accounts) != 1 || bumper.accounts[0] != "acc-version" {
		t.Fatalf("account bumps = %v, want [acc-version]", bumper.accounts)
	}
	if len(bumper.apiKeys) != 1 || bumper.apiKeys[0] != "key-version" {
		t.Fatalf("api key bumps = %v, want [key-version]", bumper.apiKeys)
	}
}

func TestScheduleSyncNoBumpWhenGateUnchanged(t *testing.T) {
	store, db, _ := newTestStore(t)
	bumper := &fakeStateVersionBumper{}
	store.SetStateVersionBumpers(bumper, bumper)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedAccountWithSchedule(t, db, "acc-same", availabilityScheduleJSON, "", "active", false)
	setScheduleGate(t, db, "accounts", "acc-same", 1)
	if _, err := store.SyncAccountScheduleStatuses(context.Background(), atStart, 0, nil); err != nil {
		t.Fatal(err)
	}
	bumper.mu.Lock()
	defer bumper.mu.Unlock()
	if len(bumper.accounts) != 0 {
		t.Fatalf("同值闸门不应 bump: %v", bumper.accounts)
	}
}

// B4：J4 刷新家族的账户状态迁移（error/恢复 active）同样必须 bump 账户状态
// 序号；否则 gateway 在 60s 内仍可能派发已判定 error 的账户，或继续跳过已
// 恢复的账户。
func TestAccountFailureStateChangesBumpStateVersion(t *testing.T) {
	store, db, _ := newTestStore(t)
	bumper := &fakeStateVersionBumper{}
	store.SetStateVersionBumpers(bumper, bumper)
	seedAccountRow(t, db, accountRowSeed{ID: "acc-failure-bump", ProviderCode: "gpt", Type: "oauth",
		Status: "active", Credentials: openAICredentials(expiresInMillis(0)), Now: defaultNow()})

	updated, err := store.MarkAccountFailureState(context.Background(), "acc-failure-bump",
		OpenAIOAuthTokenRefreshFailedErrorCode, "refresh failed", 1, "active")
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("MarkAccountFailureState must update active account")
	}
	changed, status, err := store.ClearAccountFailureState(context.Background(), "acc-failure-bump", ManagedRefreshErrorCodes)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || status != "active" {
		t.Fatalf("ClearAccountFailureState = (changed=%v status=%q), want active", changed, status)
	}

	bumper.mu.Lock()
	defer bumper.mu.Unlock()
	if len(bumper.accounts) != 2 || bumper.accounts[0] != "acc-failure-bump" || bumper.accounts[1] != "acc-failure-bump" {
		t.Fatalf("account bumps = %v, want 2 次 acc-failure-bump", bumper.accounts)
	}
}
