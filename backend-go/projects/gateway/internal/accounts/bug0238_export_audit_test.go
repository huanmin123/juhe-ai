package accounts

import (
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// BUG-0238 决策 a：导出凭据保留明文（迁移语义），但导出必须留操作日志审计。
// admin 面与 my-accounts 面共用 exportHandler，两个面都断言 accounts.export
// 条目：Summary 标明凭据导出规模，credentials 变更条目标记 Sensitive 且不携带
// 任何凭据材料（响应链对敏感条目灰显，语义与 create 的凭据条目一致）。

// exportAuditEntries copies the recorded accounts.export entries.
func exportAuditEntries(t *testing.T, sink *recordingSink) []authsys.OperationLogEntry {
	t.Helper()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	out := []authsys.OperationLogEntry{}
	for _, entry := range sink.entries {
		if entry.Module == "accounts" && entry.Action == "export" {
			out = append(out, entry)
		}
	}
	return out
}

func findCredentialChange(entry authsys.OperationLogEntry) *authsys.OperationLogChange {
	for i := range entry.Changes {
		if entry.Changes[i].Field == "credentials" {
			return &entry.Changes[i]
		}
	}
	return nil
}

func TestBug0238ExportOperationLogAudit(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	ids := []string{}
	for _, name := range []string{"alpha", "bravo"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, payload)
		}
		ids = append(ids, dataMap(t, payload)["id"].(string))
	}

	// Admin surface: POST /accounts/export by ids exports both accounts.
	code, exported := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export",
		`{"accountIds":["`+strings.Join(ids, `","`)+`"]}`)
	if code != http.StatusOK {
		t.Fatalf("admin export: %d %v", code, exported)
	}
	if dataMap(t, exported)["summary"].(map[string]any)["accounts"] != float64(2) {
		t.Fatalf("admin export summary: %v", exported)
	}

	entries := exportAuditEntries(t, env.sink)
	if len(entries) != 1 {
		t.Fatalf("admin export must record one accounts.export entry: %d", len(entries))
	}
	adminEntry := entries[0]
	if adminEntry.OperationKey != "accounts.export" || adminEntry.ResourceType != "account" ||
		adminEntry.Mode != "admin" || adminEntry.ActorSystemAccountID != adminID {
		t.Fatalf("admin export entry contract: %+v", adminEntry)
	}
	if adminEntry.Summary != "导出账户凭据：2 个账户" {
		t.Fatalf("admin export summary: %q", adminEntry.Summary)
	}
	credential := findCredentialChange(adminEntry)
	if credential == nil {
		t.Fatalf("admin export entry lacks credentials change: %+v", adminEntry.Changes)
	}
	if !credential.Sensitive || credential.After != "已导出 2 个账户的明文凭据" {
		t.Fatalf("credentials change must be sensitive with the export scale: %+v", credential)
	}

	// Self surface: a regular user exports their own single account.
	userID := env.login(t, "eve", "eve-pass", "user")
	env.seedAccount(t, "acc-eve", userID, "eve-账户", "active")
	code, selfExported := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/export",
		`{"accountIds":["acc-eve"]}`)
	if code != http.StatusOK {
		t.Fatalf("self export: %d %v", code, selfExported)
	}
	if dataMap(t, selfExported)["summary"].(map[string]any)["accounts"] != float64(1) {
		t.Fatalf("self export summary: %v", selfExported)
	}

	entries = exportAuditEntries(t, env.sink)
	if len(entries) != 2 {
		t.Fatalf("both surfaces must record accounts.export entries: %d", len(entries))
	}
	selfEntry := entries[1]
	if selfEntry.Mode != "self" || selfEntry.OperationScopeSystemAccountID != userID {
		t.Fatalf("self export entry must be self-scoped to the caller: %+v", selfEntry)
	}
	if selfEntry.Summary != "导出账户凭据：1 个账户" {
		t.Fatalf("self export summary: %q", selfEntry.Summary)
	}
	credential = findCredentialChange(selfEntry)
	if credential == nil || !credential.Sensitive || credential.After != "已导出 1 个账户的明文凭据" {
		t.Fatalf("self export credentials change: %+v", credential)
	}
	viewerSeen := false
	for _, viewer := range selfEntry.Viewers {
		if viewer.SystemAccountID == userID && viewer.Reason == "resource_owner" {
			viewerSeen = true
		}
	}
	if !viewerSeen {
		t.Fatalf("self export entry must carry the owner viewer: %+v", selfEntry.Viewers)
	}

	// No credential material ever rides on the audit entries.
	if !env.sink.sensitive("credentials", "sk-live-secret-1234567890") ||
		!env.sink.sensitive("credentials", "sk-seeded-acc-eve") {
		t.Fatal("credentials changes must stay sensitive without material")
	}
}
