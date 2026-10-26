package oauthrefresh

import (
	"context"
	"testing"
)

// W25 回归（真实 PG）：ClearAccountFailureState 生产 PG 臂的占位符/参数计数。
// 修复前 args 尾随无占位符的 'error' 字面量（占位符 2+n，参数 3+n），pgx 严格
// 校验报 mismatched param and argument count，清除 OAuth 失败状态全路径失效
// （refresh.go restoreIfRecovered 恢复链吞错降级，error 账户永不自动恢复）。

func TestW25ClearAccountFailureStateRestoresManagedErrorAccount(t *testing.T) {
	store, db, _ := newTestStore(t)
	seedAccountRow(t, db, accountRowSeed{ID: "w25-restore", ProviderCode: "gpt", Type: "oauth",
		Status: "error", LastErrorCode: "oauth_token_refresh_failed",
		Credentials: openAICredentials(expiresInMillis(0)), Now: defaultNow()})

	changed, status, err := store.ClearAccountFailureState(context.Background(), "w25-restore", ManagedRefreshErrorCodes)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("managed error account must clear (changed=true)")
	}
	if status != "active" {
		t.Fatalf("returned status=%q, want active", status)
	}
	rowStatus, rowCode, revision, _, _ := readAccountRow(t, db, "w25-restore")
	if rowStatus != "active" || rowCode != "" {
		t.Fatalf("row status=%q code=%q, want active/NULL", rowStatus, rowCode)
	}
	if revision != 2 {
		t.Fatalf("config_revision=%d, want 2", revision)
	}
}

func TestW25ClearAccountFailureStateKeepsUnmanagedErrorCode(t *testing.T) {
	store, db, _ := newTestStore(t)
	seedAccountRow(t, db, accountRowSeed{ID: "w25-keep", ProviderCode: "gpt", Type: "oauth",
		Status: "error", LastErrorCode: "upstream_quota_exhausted",
		Credentials: openAICredentials(expiresInMillis(0)), Now: defaultNow()})

	changed, _, err := store.ClearAccountFailureState(context.Background(), "w25-keep", ManagedRefreshErrorCodes)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("unmanaged error code must not clear (changed=false)")
	}
	rowStatus, rowCode, revision, _, _ := readAccountRow(t, db, "w25-keep")
	if rowStatus != "error" || rowCode != "upstream_quota_exhausted" || revision != 1 {
		t.Fatalf("row status=%q code=%q revision=%d, want unchanged error state", rowStatus, rowCode, revision)
	}
}

// 空码集：IN 守卫恒不可满足（PG 不支持空 IN 列表），必须按 CAS 未命中返回且
// 不产生任何状态变更——SQLite 旧基线对 `IN ()` 的宽松行为不再是依据。
func TestW25ClearAccountFailureStateEmptyGuardIsNoOp(t *testing.T) {
	store, db, _ := newTestStore(t)
	seedAccountRow(t, db, accountRowSeed{ID: "w25-empty", ProviderCode: "gpt", Type: "oauth",
		Status: "error", LastErrorCode: "oauth_token_refresh_failed",
		Credentials: openAICredentials(expiresInMillis(0)), Now: defaultNow()})

	changed, _, err := store.ClearAccountFailureState(context.Background(), "w25-empty", nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("empty guard must never clear (changed=false)")
	}
	rowStatus, rowCode, revision, _, _ := readAccountRow(t, db, "w25-empty")
	if rowStatus != "error" || rowCode != "oauth_token_refresh_failed" || revision != 1 {
		t.Fatalf("row status=%q code=%q revision=%d, want unchanged error state", rowStatus, rowCode, revision)
	}
}
