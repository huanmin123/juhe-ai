package accounts

// w13g DB 流程补测：FindAdvancedDetail、FindCloneContext、Delete、
// api-key-runtime/revalidate 路由错误臂。
//
// 不可达登记（w13g）：
// - m11_reads.go:330-334.83 / 334.83-337 mappingRows.Scan err 与 343-345
//   mappingRows.Err 臂：单连接 SQLite 无法在 Next/Scan/Err 之间注入故障，
//   列类型均为亲和文本/整数，Scan 恒成功。
// - m11_reads.go:431-433 findAccountLockState err：lock 查询同连接内、
//   accounts 主行已成功读取，无法在两查询间 DROP（单连接互等）。
// - clone.go:102 / 303-305 重试与冲突臂：两次读取之间无注入点（纯查询、
//   无 effects 钩子），单连接内无法制造 revision 漂移。
// - clone.go:253-256 relationRows.Scan err、273-275 relationRows.Err、
//   294-299 二次读取 ErrNoRows/err：同上，读取间无故障注入口。
// - delete.go:71-73 / 77-80 / 84-86 / 97-99 / 100-102 / 106-108 / 110-112 /
//   115-117 事务内查询/更新/提交错误臂：事务持有唯一连接（SetMaxOpenConns(1)），
//   事务存活期间外部 db.Exec DDL 会互等挂死，SQLite 内存库无其他故障注入口。
// - api_key_runtime_revalidate.go:104-107 auth==nil 臂：RequireAdmin /
//   RequireSession(true) 中间件在最外层拦截匿名请求，处理器内的
//   AuthContextFrom 恒非 nil。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

func w13gAdvancedAuthorizedEnv(t *testing.T) (*testEnv, *authz.Store, *Store, string, string) {
	t.Helper()
	base, authzStore, store := w13gTrafficAuthorizedEnv(t)
	ownerID := base.login(t, "adv-owner-w13g", "owner-pass", "user")
	memberID := base.login(t, "adv-member-w13g", "member-pass", "user")
	base.seedProviderAndDefaultGroup(t, ownerID)
	base.seedProviderAndDefaultGroup(t, memberID)
	base.seedTeamMember(t, "team-w13g-adv", ownerID, memberID)
	return base, authzStore, store, ownerID, memberID
}

func TestW13GFindAdvancedDetailArms(t *testing.T) {
	env, _, store, ownerID, _ := w13gAdvancedAuthorizedEnv(t)
	env.seedAccount(t, "acc-w13g-adv1", ownerID, "w13g-adv1", "active")

	// 空 ID → nil。
	if out, err := store.FindAdvancedDetail(context.Background(), "  ", AccessScope{ViewerID: ownerID}); err != nil || out != nil {
		t.Fatalf("空 ID：%v %v", out, err)
	}
	// 不存在 → nil。
	if out, err := store.FindAdvancedDetail(context.Background(), "acc-w13g-none", AccessScope{ViewerID: ownerID}); err != nil || out != nil {
		t.Fatalf("不存在：%v %v", out, err)
	}
	// 空 scope（非管理员、无 viewer）→ 行读出后作用域检查拒绝。
	if out, err := store.FindAdvancedDetail(context.Background(), "acc-w13g-adv1", AccessScope{}); err != nil || out != nil {
		t.Fatalf("空 scope：%v %v", out, err)
	}
	// accounts 表缺失 → 查询错误。
	env.exec(t, `DROP TABLE accounts`)
	if _, err := store.FindAdvancedDetail(context.Background(), "acc-w13g-adv1", AccessScope{ViewerID: ownerID}); err == nil {
		t.Fatal("accounts 缺失应报错")
	}
}

func TestW13GFindAdvancedDetailOwnerArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-adv2", adminID, "w13g-adv2", "active")
	access := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 损坏的凭据信封 → 解密错误。
	env.exec(t, `UPDATE accounts SET credentials_encrypted = 'not-sealed' WHERE id = 'acc-w13g-adv2'`)
	if _, err := env.store.FindAdvancedDetail(context.Background(), "acc-w13g-adv2", access); err == nil {
		t.Fatal("坏凭据应报错")
	}
	// 非法额度恢复策略。
	sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk", "quota_recovery_policy": "x"})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = 'acc-w13g-adv2'`, sealed)
	if _, err := env.store.FindAdvancedDetail(context.Background(), "acc-w13g-adv2", access); err == nil ||
		!strings.Contains(err.Error(), "必须是对象") {
		t.Fatalf("坏策略：%v", err)
	}
	// 非法错误处理规则。
	sealed, err = EncryptJSON(testSecret, Credentials{"api_key": "sk", "error_handling_rules": "x"})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = 'acc-w13g-adv2'`, sealed)
	if _, err := env.store.FindAdvancedDetail(context.Background(), "acc-w13g-adv2", access); err == nil ||
		!strings.Contains(err.Error(), "格式无效") {
		t.Fatalf("坏错误规则：%v", err)
	}
	// account_model_mappings 表缺失 → 映射查询错误。
	if _, err := env.store.FindAdvancedDetail(context.Background(), "acc-w13g-adv2", access); err == nil {
		t.Fatal("映射查询应先成功")
	}
	env.exec(t, `DROP TABLE account_model_mappings`)
	if _, err := env.store.FindAdvancedDetail(context.Background(), "acc-w13g-adv2", access); err == nil {
		t.Fatal("account_model_mappings 缺失应报错")
	}
}

func TestW13GFindAdvancedDetailAuthorizedHappyPath(t *testing.T) {
	env, authzStore, store, ownerID, memberID := w13gAdvancedAuthorizedEnv(t)
	env.seedAccount(t, "acc-w13g-adv-src", ownerID, "w13g-adv-src", "active")
	if _, err := authzStore.Create(context.Background(), authz.CreateInput{
		ResourceType: "account", ResourceID: "acc-w13g-adv-src",
		GranteeType: "team", GranteeID: "team-w13g-adv",
	}, ownerID); err != nil {
		t.Fatal(err)
	}
	runtimeID := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = 'acc-w13g-adv-src'`, memberID)
	env.seedAuthorizationInstance(t, "acc-w13g-adv-inst", memberID, runtimeID, "acc-w13g-adv-src")
	// 摘要 join 需要 owner stamp（resource_owner_system_account_id）。
	env.exec(t, `UPDATE accounts SET authorization_instance_owner_system_account_id = ?
		WHERE id = 'acc-w13g-adv-inst'`, ownerID)
	groupID := env.queryCell(t, `SELECT id FROM groups WHERE system_account_id = ? AND is_default = 1`, memberID)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at, account_authorization_id)
		VALUES (?, ?, 'acc-w13g-adv-inst', 1, ?, ?, ?)`, memberID, groupID,
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", runtimeID)
	// 源账户上的映射与支持模型 → authorized 详情的映射投影。
	now := "2026-01-01T00:00:00Z"
	env.exec(t, `INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family,
		upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
		VALUES ('acc-w13g-adv-src', 'gpt', 'gpt-4o', 'chat_completions', 'up-4o', 'chat_completions', 1, ?, ?)`, now, now)
	// 锁状态行 → 详情锁字段。
	env.exec(t, `INSERT INTO account_lock_states (account_id, enabled, lock_state, lock_death_timeout_seconds,
		lock_retry_interval_seconds, updated_at) VALUES ('acc-w13g-adv-inst', 1, 'ENGAGED', 600, 10, ?)`, now)

	// 授权实例缺 active 授权（状态 revoked）→ (nil,nil)。
	env.exec(t, `UPDATE resource_authorizations SET status = 'revoked' WHERE id = ?`, runtimeID)
	if out, err := store.FindAdvancedDetail(context.Background(), "acc-w13g-adv-inst", AccessScope{ViewerID: memberID}); err != nil || out != nil {
		t.Fatalf("revoked 授权：%v %v", out, err)
	}
	env.exec(t, `UPDATE resource_authorizations SET status = 'active' WHERE id = ?`, runtimeID)

	detail, err := store.FindAdvancedDetail(context.Background(), "acc-w13g-adv-inst", AccessScope{ViewerID: memberID})
	if err != nil || detail == nil {
		t.Fatalf("authorized 详情：%v %v", detail, err)
	}
	if detail.AccessType != "authorized" || detail.ID != "acc-w13g-adv-inst" {
		t.Fatalf("authorized 详情头：%+v", detail)
	}
	if len(detail.ModelMappings) != 1 || detail.ModelMappings[0].SourceModel != "gpt-4o" {
		t.Fatalf("authorized 映射：%+v", detail.ModelMappings)
	}
	if !detail.LockEnabled || detail.LockState != "ENGAGED" || detail.LockDeathTimeoutSeconds != 600 {
		t.Fatalf("锁字段：%+v", detail)
	}
	if detail.BalanceQueryEnabled {
		t.Fatal("authorized 详情余额查询应恒 false")
	}
}

func TestW13GFindCloneContextArms(t *testing.T) {
	// 同一测试函数内的多个 newTestEnv 共享 cache=shared 内存库 DSN，用
	// 子测试名隔离库。
	t.Run("arms", func(t *testing.T) {
		env := newTestEnv(t)
		adminID := env.login(t, "root", "root-pass", "super_admin")
		env.seedProviderAndDefaultGroup(t, adminID)
		env.seedAccount(t, "acc-w13g-clone", adminID, "w13g-clone", "active")
		now := "2026-01-01T00:00:00Z"
		env.exec(t, `INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family,
			upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
			VALUES ('acc-w13g-clone', 'gpt', 'gpt-4o', 'chat_completions', 'up-4o', 'chat_completions', 1, ?, ?)`, now, now)
		env.exec(t, `INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
			VALUES ('acc-w13g-clone', 'gpt', 'gpt-4o', ?)`, now)
		env.exec(t, `INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at)
			VALUES ('tag-w13g-clone', ?, '克隆标签', ?, ?)`, adminID, now, now)
		env.exec(t, `INSERT INTO account_tag_bindings (system_account_id, account_id, tag_id, created_at)
			VALUES (?, 'acc-w13g-clone', 'tag-w13g-clone', ?)`, adminID, now)
		adminAccess := AccessScope{ViewerID: adminID, IsAdmin: true}

		// 空 ID → nil。
		if out, err := env.store.FindCloneContext(context.Background(), " ", adminAccess); err != nil || out != nil {
			t.Fatalf("空 ID：%v %v", out, err)
		}
		// 空 scope（非管理员无 viewer）→ 行读出后归属检查拒绝。
		if out, err := env.store.FindCloneContext(context.Background(), "acc-w13g-clone", AccessScope{}); err != nil || out != nil {
			t.Fatalf("空 scope：%v %v", out, err)
		}
		// 授权实例 → 403。
		env.seedAccount(t, "acc-w13g-clone-inst", adminID, "w13g-clone-inst", "active")
		env.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = 'ra-x',
			authorization_instance_source_account_id = 'src' WHERE id = 'acc-w13g-clone-inst'`)
		if _, err := env.store.FindCloneContext(context.Background(), "acc-w13g-clone-inst", adminAccess); err == nil ||
			!strings.Contains(err.Error(), "授权实例不能克隆") {
			t.Fatalf("授权实例克隆：%v", err)
		}
		// 坏凭据信封。
		env.exec(t, `UPDATE accounts SET credentials_encrypted = 'not-sealed' WHERE id = 'acc-w13g-clone'`)
		if _, err := env.store.FindCloneContext(context.Background(), "acc-w13g-clone", adminAccess); err == nil {
			t.Fatal("坏凭据应报错")
		}
		// relations 表缺失。
		env.exec(t, `UPDATE accounts SET credentials_encrypted = '' WHERE id = 'acc-w13g-clone'`)
		env.exec(t, `DROP TABLE account_model_mappings`)
		if _, err := env.store.FindCloneContext(context.Background(), "acc-w13g-clone", adminAccess); err == nil {
			t.Fatal("account_model_mappings 缺失应报错")
		}
		// accounts 表缺失。
		env.exec(t, `DROP TABLE accounts`)
		if _, err := env.store.FindCloneContext(context.Background(), "acc-w13g-clone", adminAccess); err == nil {
			t.Fatal("accounts 缺失应报错")
		}
	})

	// 独立环境：非管理员自身读取（scope 子句生效）+ 关系投影。
	t.Run("self-scope", func(t *testing.T) {
		env2 := newTestEnv(t)
		userID := env2.login(t, "alice", "alice-pass", "user")
		env2.seedProviderAndDefaultGroup(t, userID)
		env2.seedAccount(t, "acc-w13g-clone2", userID, "w13g-clone2", "active")
		env2.exec(t, `INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
			VALUES ('acc-w13g-clone2', 'gpt', 'gpt-4o', '2026-01-01T00:00:00Z')`)
		context, err := env2.store.FindCloneContext(context.Background(), "acc-w13g-clone2", AccessScope{ViewerID: userID})
		if err != nil || context == nil {
			t.Fatalf("非管理员克隆上下文：%v %v", context, err)
		}
		if context.ID != "acc-w13g-clone2" || len(context.SupportedModels) != 1 {
			t.Fatalf("克隆上下文：%+v", context)
		}
	})
}

func TestW13GDeleteArms(t *testing.T) {
	// 同一测试函数内的多个 newTestEnv 共享 cache=shared 内存库 DSN，用
	// 子测试名隔离库。
	t.Run("gates", func(t *testing.T) {
		env := newTestEnv(t)
		adminID := env.login(t, "root", "root-pass", "super_admin")
		env.seedProviderAndDefaultGroup(t, adminID)
		env.seedAccount(t, "acc-w13g-del1", adminID, "w13g-del1", "active")
		// 授权实例行 → 拒绝删除。
		env.seedAccount(t, "acc-w13g-del-inst", adminID, "w13g-del-inst", "active")
		env.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = 'ra-x' WHERE id = 'acc-w13g-del-inst'`)
		access := AccessScope{ViewerID: adminID, IsAdmin: true}

		// 空 ID → (false,nil)。
		if deleted, err := env.store.Delete(context.Background(), "  ", access); deleted || err != nil {
			t.Fatalf("空 ID：%v %v", deleted, err)
		}
		// 授权实例 → ValidationError。
		if _, err := env.store.Delete(context.Background(), "acc-w13g-del-inst", access); err == nil ||
			!strings.Contains(err.Error(), "归还操作") {
			t.Fatalf("授权实例删除：%v", err)
		}
		// accounts 表缺失 → 查询错误。
		env.exec(t, `DROP TABLE accounts`)
		if _, err := env.store.Delete(context.Background(), "acc-w13g-del1", access); err == nil {
			t.Fatal("accounts 缺失应报错")
		}
	})

	t.Run("closed-db", func(t *testing.T) {
		env2 := newTestEnv(t)
		admin2 := env2.login(t, "root", "root-pass", "super_admin")
		env2.seedProviderAndDefaultGroup(t, admin2)
		env2.seedAccount(t, "acc-w13g-del3", admin2, "w13g-del3", "active")
		env2.db.Close()
		if _, err := env2.store.Delete(context.Background(), "acc-w13g-del3",
			AccessScope{ViewerID: admin2, IsAdmin: true}); err == nil {
			t.Fatal("db 关闭应报错")
		}
	})

	// 资源授权表缺失 → revoke 阶段错误。
	t.Run("authz-drop", func(t *testing.T) {
		env3 := newTestEnv(t)
		admin3 := env3.login(t, "root", "root-pass", "super_admin")
		env3.seedProviderAndDefaultGroup(t, admin3)
		env3.seedAccount(t, "acc-w13g-del4", admin3, "w13g-del4", "active")
		env3.exec(t, `DROP TABLE resource_authorizations`)
		if _, err := env3.store.Delete(context.Background(), "acc-w13g-del4",
			AccessScope{ViewerID: admin3, IsAdmin: true}); err == nil {
			t.Fatal("resource_authorizations 缺失应报错")
		}
	})

	// 完整删除：实例级联 + 标签清理 + tombstone。
	t.Run("cascade", func(t *testing.T) {
		env4 := newTestEnv(t)
		admin4 := env4.login(t, "root", "root-pass", "super_admin")
		env4.seedProviderAndDefaultGroup(t, admin4)
		env4.seedAccount(t, "acc-w13g-del5", admin4, "w13g-del5", "active")
		env4.seedAccount(t, "acc-w13g-del5-inst", admin4, "w13g-del5-inst", "active")
		env4.exec(t, `UPDATE accounts SET authorization_instance_source_account_id = 'acc-w13g-del5'
			WHERE id = 'acc-w13g-del5-inst'`)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		env4.exec(t, `INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at)
			VALUES ('tag-w13g-del', ?, '删除标签', ?, ?)`, admin4, now, now)
		env4.exec(t, `INSERT INTO account_tag_bindings (system_account_id, account_id, tag_id, created_at)
			VALUES (?, 'acc-w13g-del5', 'tag-w13g-del', ?)`, admin4, now)
		deleted, err := env4.store.Delete(context.Background(), "acc-w13g-del5",
			AccessScope{ViewerID: admin4, IsAdmin: true})
		if err != nil || !deleted {
			t.Fatalf("删除失败：%v %v", deleted, err)
		}
		var count int
		if err := env4.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE deleted_at IS NULL AND id IN
			('acc-w13g-del5', 'acc-w13g-del5-inst')`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("源与实例都应软删除：%d", count)
		}
		if env4.count(t, `SELECT COUNT(*) FROM account_tag_bindings WHERE account_id = 'acc-w13g-del5'`) != 0 {
			t.Fatal("标签绑定应清理")
		}
		if env4.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox WHERE account_id = 'acc-w13g-del5'
			AND event_kind = 'tombstone'`) != 1 {
			t.Fatal("应写入删除 tombstone")
		}
	})
}

func TestW13GAPIKeyRuntimeRevalidateRouteArms(t *testing.T) {
	// 变更守卫按 {accountId, expectedConfigRevision} 指纹去重，敏感场景各自
	// 使用独立账户，避免重放缓存污染断言。
	scenarios := []struct {
		name    string
		account string
		run     func(t *testing.T, env *testEnv, base string)
	}{
		{"bad-json", "acc-w13g-rev-a", func(t *testing.T, env *testEnv, base string) {
			if code, _ := env.doRaw(t, http.MethodPost, base, "{not-json"); code != http.StatusBadRequest {
				t.Fatalf("坏 JSON 应 400：%d", code)
			}
		}},
		{"blank-scope", "acc-w13g-rev-b", func(t *testing.T, env *testEnv, base string) {
			if code, _ := env.do(t, http.MethodPost, base+"?systemAccountId=%20",
				`{"expectedConfigRevision":1}`); code != http.StatusBadRequest {
				t.Fatal("空白作用域应 400")
			}
		}},
		{"effects-nil", "acc-w13g-rev-c", func(t *testing.T, env *testEnv, base string) {
			if code, payload := env.do(t, http.MethodPost, base, `{"expectedConfigRevision":1}`); code != http.StatusInternalServerError {
				t.Fatalf("未接线 effects 应 500：%d %v", code, payload)
			}
		}},
		{"effects-error", "acc-w13g-rev-d", func(t *testing.T, env *testEnv, base string) {
			failing := &fakeRevalidateEffects{err: context.DeadlineExceeded}
			env.store.SetRuntimeResetEffects(failing)
			if code, payload := env.do(t, http.MethodPost, base, `{"expectedConfigRevision":1}`); code != http.StatusInternalServerError {
				t.Fatalf("effects 错误应 500：%d %v", code, payload)
			}
		}},
		{"ineligible-empty-reason", "acc-w13g-rev-e", func(t *testing.T, env *testEnv, base string) {
			failing := &fakeRevalidateEffects{result: AccountAPIKeyRuntimeRevalidation{Eligible: false}}
			env.store.SetRuntimeResetEffects(failing)
			code, payload := env.do(t, http.MethodPost, base, `{"expectedConfigRevision":1}`)
			if code != http.StatusConflict {
				t.Fatalf("ineligible 应 409：%d %v", code, payload)
			}
			if payload["reason"] != "not_supported" {
				t.Fatalf("缺省 reason：%v", payload)
			}
		}},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			env := newTestEnv(t)
			adminID := env.login(t, "root", "root-pass", "super_admin")
			env.seedProviderAndDefaultGroup(t, adminID)
			env.seedM11Account(t, scenario.account, adminID, "w13g-rev", "api_key", "active", Credentials{
				"api_keys": []any{"sk-a", "sk-b"}, "base_url": "https://api.example.com/v1",
			})
			scenario.run(t, env, "/__aisys__/api/accounts/"+scenario.account+"/api-key-runtime/revalidate")
		})
	}
	// accounts 表缺失 → 读错误 500。
	t.Run("accounts-drop", func(t *testing.T) {
		env := newTestEnv(t)
		adminID := env.login(t, "root", "root-pass", "super_admin")
		env.seedProviderAndDefaultGroup(t, adminID)
		env.seedM11Account(t, "acc-w13g-rev-z", adminID, "w13g-rev-z", "api_key", "active", Credentials{
			"api_keys": []any{"sk-a", "sk-b"}, "base_url": "https://api.example.com/v1",
		})
		env.store.SetRuntimeResetEffects(&fakeRevalidateEffects{})
		env.exec(t, `DROP TABLE accounts`)
		base := "/__aisys__/api/accounts/acc-w13g-rev-z/api-key-runtime/revalidate"
		if code, _ := env.do(t, http.MethodPost, base, `{"expectedConfigRevision":1}`); code != http.StatusInternalServerError {
			t.Fatal("accounts 缺失应 500")
		}
	})
}

func TestW13GIneligibleRevalidateMessages(t *testing.T) {
	// revalidateIneligibleMessage 全枚举。
	cases := map[string]string{
		"account_not_active":       "账户当前未启用",
		"account_unschedulable":    "账户当前不可调度",
		"config_revision_conflict": "请刷新后重试",
		"account_not_found":        "账户不存在或已删除",
		"no_revalidatable_key":     "没有可重新验证",
		"whatever":                 "不是启用中的多 Key API Key 池",
	}
	for reason, want := range cases {
		if got := revalidateIneligibleMessage(reason); !strings.Contains(got, want) {
			t.Fatalf("%s → %s 不含 %s", reason, got, want)
		}
	}
	// 序列化烟测：payload 键序无关。
	raw, err := json.Marshal(map[string]any{"reason": "not_supported"})
	if err != nil || !strings.Contains(string(raw), "not_supported") {
		t.Fatalf("json：%v %v", string(raw), err)
	}
}
