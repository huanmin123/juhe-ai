package accounts

// W2 M11 授权实例流程测试：流量迁移的 authorized 绑定分支（含
// temporary_unavailable / disabled / unchanged 三种来源状态）与授权实例的
// runtime-reset。授权链构造沿用 m11_test.go 的契约夹具模式。

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// w2SeedAuthorizedPair 构造同组内的两个授权实例（源 + 目标），并返回授权 ID。
func w2SeedAuthorizedPair(t *testing.T, env *testEnv, granteeID, ownerID, groupID string) (sourceID, targetID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.seedM11Account(t, "acc-auth-src", ownerID, "授权源A", "api_key", "active", Credentials{"api_key": "sk-src-a"})
	env.seedM11Account(t, "acc-auth-dst", ownerID, "授权源B", "api_key", "active", Credentials{"api_key": "sk-src-b"})
	env.seedAuthorizationInstance(t, "acc-inst-src", granteeID, "ra-w2-src", "acc-auth-src")
	env.seedAuthorizationInstance(t, "acc-inst-dst", granteeID, "ra-w2-dst", "acc-auth-dst")
	for _, pair := range [][2]string{{"ra-w2-src", "acc-auth-src"}, {"ra-w2-dst", "acc-auth-dst"}} {
		env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
			grantee_system_account_id, status, effective_source_type, created_by, created_at, updated_at)
			VALUES (?, 'account', ?, ?, ?, 'active', 'manual', ?, ?, ?)`, pair[0], pair[1], ownerID, granteeID, ownerID, now, now)
	}
	// 两个实例绑定同一个分组（account_authorization_id 各自指向授权行）。
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id,
		local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at)
		VALUES (?, ?, 'acc-inst-src', 'ra-w2-src', 5, 0, 0, 1, ?, ?)`, granteeID, groupID, now, now)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id,
		local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at)
		VALUES (?, ?, 'acc-inst-dst', 'ra-w2-dst', 5, 0, 0, 1, ?, ?)`, granteeID, groupID, now, now)
	return "acc-inst-src", "acc-inst-dst"
}

func TestW2AuthorizedTrafficMigration(t *testing.T) {
	env, _ := newM11TestEnv(t)
	granteeID := env.login(t, "grantee-w2", "grantee-pass", "user")
	ownerID := env.login(t, "owner-w2", "owner-pass", "user")
	env.login(t, "grantee-w2", "grantee-pass", "user")
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
		VALUES ('grp-authz-w2', ?, '授权迁移分组', 'gpt', 1, 'personal', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, ownerID)
	source, target := w2SeedAuthorizedPair(t, env, granteeID, ownerID, "grp-authz-w2")

	t.Run("默认临时不可用迁移", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/"+source+"/traffic-migration",
			`{"targetAccountId":"`+target+`"}`)
		if code != http.StatusOK {
			t.Fatalf("迁移状态码：%d %v", code, payload)
		}
		data := dataMap(t, payload)
		if data["sourceStatus"] != "temporary_unavailable" {
			t.Fatalf("来源状态不一致：%v", data["sourceStatus"])
		}
		sourceRow := env.queryCell(t, `SELECT status || '|' || schedulable FROM accounts WHERE id = ?`, source)
		if sourceRow != "temporary_unavailable|1" {
			t.Fatalf("源账户未进入临时不可用：%s", sourceRow)
		}
		if cooldown := env.queryCell(t, `SELECT COALESCE(cooldown_until,'') FROM accounts WHERE id = ?`, source); cooldown == "" {
			t.Fatal("临时不可用应带初始冷却")
		}
		if reason := env.queryCell(t, `SELECT COALESCE(last_error_message,'') FROM accounts WHERE id = ?`, source); !strings.Contains(reason, "流量") && reason == "" {
			t.Fatalf("迁移原因缺失：%q", reason)
		}
	})
	t.Run("unchanged 状态只回显不写库", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/"+source+"/traffic-migration",
			`{"targetAccountId":"`+target+`","sourceStatus":"unchanged"}`)
		if code != http.StatusOK {
			t.Fatalf("unchanged 状态码：%d %v", code, payload)
		}
		if dataMap(t, payload)["sourceStatus"] != "unchanged" {
			t.Fatalf("来源状态不一致：%v", dataMap(t, payload)["sourceStatus"])
		}
	})
	t.Run("disabled 迁移停用源", func(t *testing.T) {
		env.exec(t, `UPDATE accounts SET status = 'active', cooldown_until = NULL WHERE id = ?`, source)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/"+source+"/traffic-migration",
			`{"targetAccountId":"`+target+`","sourceStatus":"disabled"}`)
		if code != http.StatusOK {
			t.Fatalf("disabled 迁移状态码：%d %v", code, payload)
		}
		if row := env.queryCell(t, `SELECT status || '|' || schedulable FROM accounts WHERE id = ?`, source); row != "disabled|0" {
			t.Fatalf("源账户未停用：%s", row)
		}
	})
	t.Run("同账户拒绝", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/"+source+"/traffic-migration",
			`{"targetAccountId":"`+source+`"}`)
		if code != http.StatusBadRequest {
			t.Fatalf("同账户应 400：%d %v", code, payload)
		}
	})
	t.Run("目标不在同组拒绝", func(t *testing.T) {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		env.seedM11Account(t, "acc-auth-out", ownerID, "授权源C", "api_key", "active", Credentials{"api_key": "sk-src-c"})
		env.seedAuthorizationInstance(t, "acc-inst-out", granteeID, "ra-w2-out", "acc-auth-out")
		env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
			grantee_system_account_id, status, effective_source_type, created_by, created_at, updated_at)
			VALUES ('ra-w2-out', 'account', 'acc-auth-out', ?, ?, 'active', 'manual', ?, ?, ?)`, ownerID, granteeID, ownerID, now, now)
		env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id,
			local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at)
			VALUES (?, 'grp-authz-other', 'acc-inst-out', 'ra-w2-out', 5, 0, 0, 1, ?, ?)`, granteeID, now, now)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/"+source+"/traffic-migration",
			`{"targetAccountId":"acc-inst-out"}`)
		if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "同一个分组") {
			t.Fatalf("跨组目标应拒绝：%d %v", code, payload)
		}
	})
	t.Run("账户不存在返回 404", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-missing-w2/traffic-migration",
			`{"targetAccountId":"`+target+`"}`)
		if code != http.StatusNotFound || payload["message"] != "账户不存在或无权迁移" {
			t.Fatalf("不存在账户应 404：%d %v", code, payload)
		}
	})
	t.Run("body 校验", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/"+source+"/traffic-migration", `{"bogus":1}`)
		if code != http.StatusBadRequest || payload["message"] != "迁移流量参数无效" {
			t.Fatalf("未知键：%d %v", code, payload)
		}
	})
}

func TestW2AuthorizedRuntimeReset(t *testing.T) {
	env, _ := newM11TestEnv(t)
	granteeID := env.login(t, "grantee-rr", "grantee-pass", "user")
	ownerID := env.login(t, "owner-rr", "owner-pass", "user")
	env.login(t, "grantee-rr", "grantee-pass", "user")
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
		VALUES ('grp-authz-rr', ?, '授权重置分组', 'gpt', 1, 'personal', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, ownerID)
	source, _ := w2SeedAuthorizedPair(t, env, granteeID, ownerID, "grp-authz-rr")

	// 预置失败痕迹后重置：授权实例走绑定调度恢复分支。
	env.exec(t, `UPDATE accounts SET status = 'temporary_unavailable', schedulable = 0,
		cooldown_until = '2030-01-01T00:00:00Z', last_error_code = 'rate_limited' WHERE id = ?`, source)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/"+source+"/runtime-reset",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("授权重置状态码：%d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["status"] != "active" || data["schedulable"] != true {
		t.Fatalf("重置后状态不一致：%v", data)
	}
	if row := env.queryCell(t, `SELECT COALESCE(last_error_code,'') || '|' || schedulable FROM accounts WHERE id = ?`, source); row != "|1" {
		t.Fatalf("失败痕迹未清理：%s", row)
	}

	// 未绑定分组的授权实例 → 404（重置面无法定位绑定）。
	env.seedAuthorizationInstance(t, "acc-inst-nobind", granteeID, "ra-w2-nobind", "acc-auth-src")
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-inst-nobind/runtime-reset",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusNotFound {
		t.Fatalf("未绑定实例应 404：%d %v", code, payload)
	}
}
