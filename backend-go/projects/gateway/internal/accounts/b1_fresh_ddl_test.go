package accounts

// B1 断点批：maintenance 真 fresh DDL（bootstrap.EnsurePostgres 的生产形态，
// 原 SQLite 形态 bootstrap.EnsureSQLiteSchema 的 PG 对应物）上的连接写入原语
// 与草稿/探针快照链测试。创建链 legacy 读取面已随 `7d86c1cbf` 清零（profile
// 解析切 registry/vendor 推导、分组解析去 provider 维度、目录断言切 models
// 身份）；本批（profile 解析第三处残留拆除）之后，草稿/探针快照链同样只读
// groups + vendors（§8 组无 provider 维度、§2.1 vendor 归属），协议上下文与
// oauth 缺省 base_url 由 registry 语义推导，fresh DDL（无 providers /
// provider_protocol_profiles）上端到端可用。生产 DDL 外键真实生效：种子先落
// system_accounts 属主行（accounts.system_account_id 生产外键），使断言命中的
// 约束与被测语义一致。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// b1FreshSeedOwner 落 fresh DDL 的属主行（accounts.system_account_id 生产
// 外键引用 system_accounts(id)，种子必须满足，避免与被测约束混淆）。
func b1FreshSeedOwner(t *testing.T, db *sql.DB, owner string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, password_hash, created_at, updated_at)
		VALUES ($1, $2, $3, 'hash', $4, $5)`, owner, owner, owner, "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z"); err != nil {
		t.Fatalf("seed owner %s: %v", owner, err)
	}
}

// TestB1FreshMaintenanceDDLConnectionWrite 在 maintenance fresh business DDL
// 上驱动 insertAccountConnections + finalizeAccountHealthCheckTrio：连接行
// （UNIQUE (account_id, id) 表）、endpoint mode 行与 health 三字段事务闭环。
func TestB1FreshMaintenanceDDLConnectionWrite(t *testing.T) {
	db := newFreshPGDatabase(t)
	b1FreshSeedOwner(t, db, "owner-b1-fresh")
	store, err := NewStore(db, true, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := "2026-01-01T00:00:00.000Z"

	// 直插账户行（fresh accounts 列集：vendor_code / connection_driver_code /
	// protocol_code / protocol_version，无 provider 列；vendor_code 外键引用
	// vendors.code）+ 目标模型行。
	if _, err := db.Exec(`INSERT INTO vendors (id, code, name, created_at, updated_at)
		VALUES ('ven-b1-fresh', 'gpt', 'GPT', $1, $2)`, now, now); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, vendor_code, connection_driver_code,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, capability_domain,
		created_at, updated_at)
		VALUES ('acc-b1-fresh', 'owner-b1-fresh', 'gpt', 'gpt', 'gemini', 'v1beta', 'b1-fresh', 'api_key',
		'active', 'sealed', 'chat', $1, $2)`, now, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status,
		supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mod-b1-fresh', 'gpt', 'gpt-4o-mini', 'gpt-4o-mini', 'builtin', 'active',
		'["chat_json","chat_sse"]', $1, $2)`, now, now); err != nil {
		t.Fatalf("seed model: %v", err)
	}

	// 连接行 + mode 行写入（B1 写入序第二、三步原语）。
	resolved, err := resolveConnectionSet("chat", "gpt", "api_key", "", []ConnectionInput{{
		ConnectionPresetCode: "gpt_openai_v1",
		BaseURL:              "https://api.openai.com/v1",
		EndpointModes:        []string{"chat_json", "chat_sse"},
		Enabled:              true,
	}})
	if err != nil {
		t.Fatalf("resolveConnectionSet: %v", err)
	}
	if err := insertAccountConnections(ctx, db, store, "acc-b1-fresh", resolved, now); err != nil {
		t.Fatalf("insertAccountConnections on fresh DDL: %v", err)
	}

	// health-check 三字段事务闭环（fresh accounts 含 health_check_connection_id
	// 复合外键列集）。
	if err := store.finalizeAccountHealthCheckTrio(ctx, db, "acc-b1-fresh", "gpt", "owner-b1-fresh",
		"gpt_openai_v1", "chat_json", "gpt-4o-mini", []string{"gpt-4o-mini"}); err != nil {
		t.Fatalf("finalizeAccountHealthCheckTrio on fresh DDL: %v", err)
	}

	var connectionID, surface, baseURL string
	if err := db.QueryRow(`SELECT id, protocol_surface_code, base_url FROM account_connections
		WHERE account_id = 'acc-b1-fresh'`).Scan(&connectionID, &surface, &baseURL); err != nil {
		t.Fatalf("fresh account_connections row: %v", err)
	}
	if !strings.HasPrefix(connectionID, "conn-") || surface != "openai_v1" || baseURL != "https://api.openai.com/v1" {
		t.Fatalf("fresh 连接行事实不符：%q %q %q", connectionID, surface, baseURL)
	}
	var modeCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_endpoint_modes WHERE connection_id = $1`, connectionID).Scan(&modeCount); err != nil {
		t.Fatal(err)
	}
	if modeCount != 2 {
		t.Fatalf("fresh endpoint mode 行数：%d", modeCount)
	}
	var healthConnection, healthMode, healthModel string
	if err := db.QueryRow(`SELECT health_check_connection_id, health_check_endpoint_mode, health_check_model
		FROM accounts WHERE id = 'acc-b1-fresh'`).Scan(&healthConnection, &healthMode, &healthModel); err != nil {
		t.Fatal(err)
	}
	if healthConnection != connectionID || healthMode != "chat_json" || healthModel != "gpt-4o-mini" {
		t.Fatalf("fresh health 三字段不完整：%q %q %q", healthConnection, healthMode, healthModel)
	}
	// (account_id, id) 唯一键：同账户重复 id 必须被 fresh 约束拒绝。
	if _, err := db.Exec(`INSERT INTO account_connections (id, account_id, connection_preset_code, base_url,
		enabled, created_at, updated_at) VALUES ($1, 'acc-b1-fresh', 'xai_openai_v1', 'https://x.example', 1, $2, $3)`,
		connectionID, now, now); err == nil {
		t.Fatal("fresh UNIQUE (account_id, id) 未生效")
	}
}

// TestB1FreshMaintenanceDDLCompositeForeignKeyRejectsOrphanConnection 是复合外
// 键的显式行为验证（#B1 剩余④；此前仅有列集验证）：生产 DDL 下
// accounts.health_check_connection_id 的复合外键
// (id, health_check_connection_id) → account_connections(account_id, id)
//   - 插入指向不存在连接的账户行 → 外键拒绝（SQLite 臂经
//     `PRAGMA foreign_keys=ON` 启用，PG 外键恒生效）；
//   - 连接行 + 账户行的正常写入序（先 accounts 三字段 NULL——复合外键任一列
//     NULL 跳过 MATCH，再连接行，再回填三字段）通过。
func TestB1FreshMaintenanceDDLCompositeForeignKeyRejectsOrphanConnection(t *testing.T) {
	db := newFreshPGDatabase(t)
	b1FreshSeedOwner(t, db, "owner-b1-fk")
	ctx := context.Background()
	now := "2026-01-01T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO vendors (id, code, name, created_at, updated_at)
		VALUES ('ven-b1-fk', 'gpt', 'GPT', $1, $2)`, now, now); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}

	// 孤儿臂：health_check_connection_id 指向不存在的连接行 → 复合外键拒绝。
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, vendor_code, connection_driver_code,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, capability_domain,
		health_check_connection_id, created_at, updated_at)
		VALUES ('acc-b1-fk-orphan', 'owner-b1-fk', 'gpt', 'gpt', 'openai', 'v1', 'b1-fk-orphan', 'api_key',
		'active', 'sealed', 'chat', 'conn-not-exists', $1, $2)`, now, now); err == nil {
		t.Fatal("fresh 复合外键未拒绝孤儿 health_check_connection_id")
	}

	// 正常序：先插账户（三字段暂 NULL，复合外键 NULL 列跳过 MATCH）→ 连接行 →
	// 回填三字段。
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, vendor_code, connection_driver_code,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, capability_domain,
		created_at, updated_at)
		VALUES ('acc-b1-fk-ok', 'owner-b1-fk', 'gpt', 'gpt', 'openai', 'v1', 'b1-fk-ok', 'api_key',
		'active', 'sealed', 'chat', $1, $2)`, now, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO account_connections (id, account_id, connection_preset_code,
		protocol_surface_code, base_url, enabled, created_at, updated_at)
		VALUES ('conn-b1-fk-ok', 'acc-b1-fk-ok', 'gpt_openai_v1', 'openai_v1', 'https://api.openai.com/v1', 1, $1, $2)`, now, now); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	for _, mode := range []string{"chat_json", "chat_sse"} {
		if _, err := db.Exec(`INSERT INTO account_endpoint_modes (connection_id, endpoint_mode)
			VALUES ('conn-b1-fk-ok', $1)`, mode); err != nil {
			t.Fatalf("seed endpoint mode: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status,
		supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mod-b1-fk', 'gpt', 'gpt-4o-mini', 'gpt-4o-mini', 'builtin', 'active',
		'["chat_json","chat_sse"]', $1, $2)`, now, now); err != nil {
		t.Fatalf("seed model: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models (account_id, model_id, created_at)
		VALUES ('acc-b1-fk-ok', 'mod-b1-fk', $1)`, now); err != nil {
		t.Fatalf("seed supported model: %v", err)
	}
	store, err := NewStore(db, true, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.finalizeAccountHealthCheckTrio(ctx, db, "acc-b1-fk-ok", "gpt", "owner-b1-fk",
		"gpt_openai_v1", "chat_json", "gpt-4o-mini", []string{"gpt-4o-mini"}); err != nil {
		t.Fatalf("正常序三字段回填应通过复合外键：%v", err)
	}
	// 回填后外键生效：连接行被 RESTRICT 删除必须拒绝（health-check 引用在）。
	if _, err := db.Exec(`DELETE FROM account_connections WHERE id = 'conn-b1-fk-ok'`); err == nil {
		t.Fatal("fresh 复合外键未 RESTRICT 被健康检查引用的连接行删除")
	}
}

// TestB1FreshMaintenanceDDLDraftTestSnapshot 在 fresh business DDL 上驱动草稿/
// 探针快照链（prepareAccountDraftTestSnapshot，草稿测试与余额探针家族的草稿
// 准备）。拆前该链读 groups.provider_code 与 providers ⋈
// provider_protocol_profiles，在 fresh DDL 上分别断在
// 「no such column: provider_code」与「no such table: providers」；profile 解析
// 拆除后只读 groups + vendors，协议上下文由 vendorDefaultProtocolContext 推导、
// oauth 缺省 base_url 按 §1.1 default_base_url 表落 vendor 根。
func TestB1FreshMaintenanceDDLDraftTestSnapshot(t *testing.T) {
	db := newFreshPGDatabase(t)
	store, err := NewStore(db, true, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := "2026-01-01T00:00:00.000Z"
	for _, vendor := range []struct{ id, code, name string }{
		{"ven-b1-draft-gpt", "gpt", "GPT"},
		{"ven-b1-draft-anthropic", "anthropic", "Anthropic"},
	} {
		if _, err := db.Exec(`INSERT INTO vendors (id, code, name, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5)`, vendor.id, vendor.code, vendor.name, now, now); err != nil {
			t.Fatalf("seed vendor %s: %v", vendor.code, err)
		}
	}
	// fresh groups 无 provider 维度列（§8）：直插行即目标形态。
	if _, err := db.Exec(`INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-b1-draft', 'owner-b1-draft', '默认分组', 1, 1, 'personal', $1, $2)`, now, now); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	access := AccessScope{ViewerID: "owner-b1-draft", IsAdmin: true}

	// api_key 草稿：协议上下文由 vendor 默认推导（gpt → openai/v1 +
	// profile_gpt_openai_v1 pin）；入参不再携带 providerProtocolProfileId。
	draft, err := store.prepareAccountDraftTestSnapshot(ctx, &TestDraftAccountInput{
		ProviderCode: "gpt", Name: "d1", Type: "api_key",
		HealthCheckModel: "gpt-4o-mini", HealthCheckEndpointMode: "chat_json", GroupID: "grp-b1-draft",
		Credentials:     Credentials{"api_key": "sk-b1-draft", "base_url": "https://api.openai.com/v1"},
		SupportedModels: []string{"gpt-4o-mini"},
	}, access, "")
	if err != nil {
		t.Fatalf("fresh DDL 草稿快照链: %v", err)
	}
	if draft.ProviderProtocolProfileID == nil || *draft.ProviderProtocolProfileID != accountscore.GptOpenAIV1ProfileID ||
		draft.ProtocolCode == nil || *draft.ProtocolCode != accountscore.OpenAIProtocolCode ||
		draft.ProtocolVersion == nil || *draft.ProtocolVersion != accountscore.OpenAIProtocolVersion {
		t.Fatalf("fresh 推导协议上下文不符：%v %v %v",
			draft.ProviderProtocolProfileID, draft.ProtocolCode, draft.ProtocolVersion)
	}

	// oauth 草稿：缺省 base_url 按 §1.1 default_base_url 表落 anthropic 上游根
	//（anthropic 协议 oauth 凭据要求 access_token）。
	oauthDraft, err := store.prepareAccountDraftTestSnapshot(ctx, &TestDraftAccountInput{
		ProviderCode: "anthropic", Name: "d2", Type: "oauth",
		HealthCheckModel: "claude-sonnet", HealthCheckEndpointMode: "messages_json", GroupID: "grp-b1-draft",
		Credentials:     Credentials{"access_token": "t", "refresh_token": "r"},
		SupportedModels: []string{"claude-sonnet"},
	}, access, "")
	if err != nil {
		t.Fatalf("fresh DDL oauth 草稿快照链: %v", err)
	}
	if oauthDraft.Credentials["base_url"] != "https://api.anthropic.com/v1" {
		t.Fatalf("fresh oauth 缺省 base_url：%v", oauthDraft.Credentials["base_url"])
	}
	if oauthDraft.ProtocolCode == nil || *oauthDraft.ProtocolCode != accountscore.AnthropicProtocolCode {
		t.Fatalf("fresh anthropic 推导协议：%v", oauthDraft.ProtocolCode)
	}
}
