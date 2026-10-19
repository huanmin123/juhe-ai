package accounts

// B1 断点批：maintenance 真 fresh SQLite DDL（bootstrap.EnsureSQLiteSchema 的
// business 形态）上的连接写入原语测试。范围刻意收窄为 B1 连接面本身
// （account_connections / account_endpoint_modes / health-check 三字段事务
// 闭环）：创建链的供应商解析、分组解析与模型目录断言仍读 legacy 面
//（providers / provider_protocol_profiles / groups.provider_code / 旧目录表），
// 这些表在 fresh DDL 已删除/改形，端到端创建归供应商解析切片（B1 之后的
// 断点），此处以直插账户/模型行 + 写路径原语驱动真 DDL 校验。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
)

// TestB1FreshMaintenanceDDLConnectionWrite 在 maintenance fresh business DDL
// 上驱动 insertAccountConnections + finalizeAccountHealthCheckTrio：连接行
// （UNIQUE (account_id, id) 表）、endpoint mode 行与 health 三字段事务闭环。
func TestB1FreshMaintenanceDDLConnectionWrite(t *testing.T) {
	db, err := sql.Open("sqlite", "file:b1fresh-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("fresh business DDL ensure: %v", err)
	}
	store, err := NewStore(db, false, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := "2026-01-01T00:00:00.000Z"

	// 直插账户行（fresh accounts 列集：vendor_code / connection_driver_code /
	// protocol_code / protocol_version，无 provider 列；vendor_code 外键引用
	// vendors.code）+ 目标模型行。
	if _, err := db.Exec(`INSERT INTO vendors (id, code, name, created_at, updated_at)
		VALUES ('ven-b1-fresh', 'gpt', 'GPT', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, vendor_code, connection_driver_code,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, capability_domain,
		created_at, updated_at)
		VALUES ('acc-b1-fresh', 'owner-b1-fresh', 'gpt', 'gpt', 'gemini', 'v1beta', 'b1-fresh', 'api_key',
		'active', 'sealed', 'chat', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status,
		supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mod-b1-fresh', 'gpt', 'gpt-4o-mini', 'gpt-4o-mini', 'builtin', 'active',
		'["chat_json","chat_sse"]', ?, ?)`, now, now); err != nil {
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
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_endpoint_modes WHERE connection_id = ?`, connectionID).Scan(&modeCount); err != nil {
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
		enabled, created_at, updated_at) VALUES (?, 'acc-b1-fresh', 'xai_openai_v1', 'https://x.example', 1, ?, ?)`,
		connectionID, now, now); err == nil {
		t.Fatal("fresh UNIQUE (account_id, id) 未生效")
	}
}
