package main

import (
	"os"
	"strings"
	"testing"
)

func TestComposeSystemAPIWiresSQLiteAccountDeleteAuthorizationRevoker(t *testing.T) {
	source, err := os.ReadFile("compose.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	needle := "accountStore.SetDeletedResourceGrantRevoker(authzStore)"
	if !strings.Contains(text, needle) {
		t.Fatalf("compose root must wire the SQLite account-delete authorization revoker: %s", needle)
	}
	accountStorePos := strings.Index(text, "accountStore, err := accounts.NewStore")
	wirePos := strings.Index(text, needle)
	if accountStorePos < 0 || wirePos < accountStorePos {
		t.Fatalf("account-delete authorization revoker must be wired after account store construction")
	}
}

// TestComposeSystemAPIWiresBalanceSnapshotCleaner（缺口 5）：组合根必须为
// 账户 store 装配 store 默认余额快照清理器（归档
// accounts.routes.ts:355-364 + account-balance-snapshot-cleanup.service.ts
// :220-224），且装配位于 store 构造之后。
func TestComposeSystemAPIWiresBalanceSnapshotCleaner(t *testing.T) {
	source, err := os.ReadFile("compose.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	needle := "accountStore.SetBalanceSnapshotCleaner(accounts.NewStoreBalanceSnapshotCleaner(accountStore))"
	if !strings.Contains(text, needle) {
		t.Fatalf("compose root must wire the store-backed balance snapshot cleaner: %s", needle)
	}
	accountStorePos := strings.Index(text, "accountStore, err := accounts.NewStore")
	wirePos := strings.Index(text, needle)
	if accountStorePos < 0 || wirePos < accountStorePos {
		t.Fatalf("balance snapshot cleaner must be wired after account store construction")
	}
}

// TestComposeSystemAPIWiresAPIKeyFailureObservation（缺口 B）：组合根必须把
// 进程内 API-Key 失败 guard 接入失败派发器（归档 failure-dispatch.ts:421-434
// captureGatewayAccountApiKeyFailureObservation）。
func TestComposeSystemAPIWiresAPIKeyFailureObservation(t *testing.T) {
	source, err := os.ReadFile("compose.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	needle := "AccountAPIKeyObservation: chainServices.AccountAPIKeyGuard"
	if !strings.Contains(text, needle) {
		t.Fatalf("compose root must wire the api-key failure observation capture: %s", needle)
	}
}
