package settings

// w15b PostgreSQL 门禁回归(BUG-0308):upsertSystemSettings 的 account-health
// 分区在 reserveAccountHealthInputVersions 里必须先把外层 accounts 流式
// SELECT 读完并关闭 rows,再在同事务上做版本预留 QueryRow/Exec。修复前的
// 写法在"存在至少一行 openai 账户"时 100% 得到 driver: bad connection
// (与 modelcheckowner claim 同根因);SQLite 全量缓冲无此约束,故本契约只
// 能由真实 pgx + PostgreSQL 钉死。
//
// 隔离铁律（与 auditlog w10b 先例一致）：
//   - 只触碰一次性子库 juhe_ai_sub2api_dev_setver（本测试 DROP 后重建，
//     WITH (FORCE)），主库一个字节不动；绝不 DROP 共享对象。
//   - 连接配置只从 .local/project-resources/dev/env/shared.env 读取，走
//     DEV_POSTGRES_DIRECT_PORT 直连；任何输出不得携带连接串或密码。
//   - env 缺失或 PG 不可达一律 t.Skip。

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	w15bSettingsCoverDB  = "juhe_ai_sub2api_dev_setver"
	w15bSettingsEnvPath  = "../../../../../.local/project-resources/dev/env/shared.env"
	w15bSettingsPgBudget = 30 * time.Second
)

func w15bSettingsSharedEnv(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(w15bSettingsEnvPath))
	if err != nil {
		t.Skipf("dev env 不可达（跳过 PG 门禁测试）: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

func w15bSettingsCoverPostgresDB(t *testing.T) *sql.DB {
	t.Helper()
	env := w15bSettingsSharedEnv(t)
	host := env["DEV_POSTGRES_HOST"]
	directPort := env["DEV_POSTGRES_DIRECT_PORT"]
	adminUser := env["DEV_POSTGRES_ADMIN_USERNAME"]
	adminPass := env["DEV_POSTGRES_ADMIN_PASSWORD"]
	appURL := env["JUHE_AI_POSTGRES_URL"]
	if host == "" || directPort == "" || adminUser == "" || adminPass == "" || appURL == "" {
		t.Skipf("dev env 缺少 PG 键（跳过）")
	}
	admin, err := sql.Open("pgx", fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", adminUser, adminPass, host, directPort))
	if err != nil {
		t.Fatalf("打开 dev PG 管理连接失败: %v", err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), w15bSettingsPgBudget)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+w15bSettingsCoverDB+` WITH (FORCE)`); err != nil {
		t.Fatalf("清理一次性子库失败: %v", err)
	}
	owner := env["DEV_POSTGRES_APP_USERNAME"]
	if owner == "" {
		owner = adminUser
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+w15bSettingsCoverDB+` OWNER `+owner); err != nil {
		t.Fatalf("创建一次性子库失败: %v", err)
	}
	parsed, err := url.Parse(appURL)
	if err != nil {
		t.Skipf("dev env JUHE_AI_POSTGRES_URL 形态异常（跳过）: %v", err)
	}
	parsed.Host = host + ":" + directPort
	parsed.Path = "/" + w15bSettingsCoverDB
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatalf("打开一次性子库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE SCHEMA juhe_business`,
		`CREATE TABLE juhe_business.system_settings (
			system_account_id text,
			key text,
			value_json text,
			updated_at text,
			PRIMARY KEY (system_account_id, key))`,
		`CREATE TABLE juhe_business.accounts (
			id text PRIMARY KEY,
			deleted_at text,
			provider_code text,
			type text,
			config_revision int,
			dispatch_revision int)`,
		`CREATE TABLE juhe_business.account_health_jobs_input_versions (
			account_id text PRIMARY KEY,
			current_version int,
			reserved_at text)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("落最小业务 schema 失败: %v", err)
		}
	}
	return db
}

func TestW15BUpsertAccountHealthSectionUnderPgx(t *testing.T) {
	db := w15bSettingsCoverPostgresDB(t)
	ctx := context.Background()
	now := time.Now()
	for _, account := range []struct{ id, provider, typ string }{
		{"acc-w15b-oauth", "openai", "oauth"},
		{"acc-w15b-key", "openai", "api_key"},
		{"acc-w15b-other", "anthropic", "api_key"},
	} {
		if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, deleted_at, provider_code, type, config_revision, dispatch_revision)
			VALUES ($1, NULL, $2, $3, 1, 1)`, account.id, account.provider, account.typ); err != nil {
			t.Fatalf("种子账户失败: %v", err)
		}
	}
	store, err := NewStore(db, true, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatalf("构建 settings store 失败: %v", err)
	}
	if err := store.upsertSystemSettings(ctx, "account-health", map[string]any{"accountHealthCheckIntervalHours": 6}); err != nil {
		t.Fatalf("account-health 分区写入失败(修复前此处为 driver: bad connection,即 BUG-0308): %v", err)
	}
	var valueJSON string
	if err := db.QueryRow(`SELECT value_json FROM juhe_business.system_settings WHERE system_account_id=$1 AND key=$2`, SystemSettingsAccountID, "accountHealthCheckIntervalHours").Scan(&valueJSON); err != nil {
		t.Fatalf("设置行未落库: %v", err)
	}
	for _, account := range []struct {
		id      string
		version int
	}{
		{"acc-w15b-oauth", 1},
		{"acc-w15b-key", 1},
	} {
		var currentVersion int
		if err := db.QueryRow(`SELECT current_version FROM juhe_business.account_health_jobs_input_versions WHERE account_id=$1`, account.id).Scan(&currentVersion); err != nil {
			t.Fatalf("版本预留行缺失: %v", err)
		}
		if currentVersion != account.version {
			t.Fatalf("账户 %s 版本应为 %d,实际 %d", account.id, account.version, currentVersion)
		}
	}
	var others int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_business.account_health_jobs_input_versions`).Scan(&others); err != nil {
		t.Fatalf("统计版本行失败: %v", err)
	}
	if others != 2 {
		t.Fatalf("只应预留 2 个 openai 账户,实际 %d 行", others)
	}
	// 第二次写入走 UPDATE 臂:版本自增,且仍不得触发流式 busy。
	if err := store.upsertSystemSettings(ctx, "account-health", map[string]any{"accountHealthCheckIntervalHours": 12}); err != nil {
		t.Fatalf("第二次 account-health 写入失败: %v", err)
	}
	var second int
	if err := db.QueryRow(`SELECT current_version FROM juhe_business.account_health_jobs_input_versions WHERE account_id=$1`, "acc-w15b-oauth").Scan(&second); err != nil {
		t.Fatalf("读取自增版本失败: %v", err)
	}
	if second != 2 {
		t.Fatalf("第二次写入后版本应为 2,实际 %d", second)
	}
}
