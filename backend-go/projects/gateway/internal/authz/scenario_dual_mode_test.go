// scenario_dual_mode_test.go —— 授权域双模式（SQLite / 真实 PostgreSQL）全场景
// 回归套件（写入执行代理交付物 1）。设计要点：
//
//   - SQLite 臂无条件运行（复用既有 newFixture 夹具 + usageFixtureDDL stats
//     表）；PG 臂由环境变量 JUHE_AI_DUALMODE_PG_DSN 门控：未设置 t.Skip，
//     设置了但连不上或缺表 t.Fatal 大声失败（共享 dev 库前置检查）。
//   - 共享 dev 库隔离：本套件所有造数 ID 一律带 "dsc"+6hex+"-" 前缀（e.p）；
//     store 随机生成的 grant/runtime/source/实例 ID 无法前缀匹配，通过关系
//     谓词（owner/grantee/resource/team 前缀、实例来源账户外键）在清理时回收；
//     清理后用同一组谓词 COUNT 自审计，残留 > 0 记 t.Errorf 并打印表名与计数
//     （绝不打印 DSN）。
//   - PG 真实 schema（maintenance/internal/schema/pg_schema_*.go）与 SQLite
//     夹具 DDL 的差异（外键、额外 NOT NULL 列、groups 无 status 列、
//     system_team_members 多 created_by、groups.created_at、真实库的
//     system_teams.name 唯一索引与 account-list-availability 触发器）全部在
//     seed/清理层吸收，断言层不分臂。
package authz

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

type dualEnv struct {
	t     *testing.T
	db    *sql.DB
	store *Store
	fx    *fixture // SQLite 臂的底层夹具（推进时钟要用它的闭包）
	pg    bool
	p     string // 全部造数前缀："dsc"+6hex+"-"
	now   time.Time
}

func dualHexChars(n int) string {
	buf := make([]byte, (n+1)/2)
	if _, err := crand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)[:n]
}

// dualPGDSN 只从环境变量读取；DSN 永不进入日志、断言或错误信息。
func dualPGDSN() string { return os.Getenv("JUHE_AI_DUALMODE_PG_DSN") }

// dualProbeTables 是 PG 臂的前置检查清单：本套件会触达的全部 business/stats
// 表（含 providers/protocols/provider_protocol_profiles 造数依赖链与
// account-list-availability 触发器维护表）。缺任何一张都视为共享 dev 库
// schema 不完整，t.Fatal 大声失败。
var dualProbeTables = []string{
	"juhe_business.system_accounts",
	"juhe_business.system_teams",
	"juhe_business.system_team_members",
	"juhe_business.groups",
	"juhe_business.accounts",
	"juhe_business.group_accounts",
	"juhe_business.account_list_availability_dirty",
	"juhe_business.account_list_availability_projection_viewer_health",
	"juhe_business.account_name_search_terms",
	"juhe_business.account_name_search_documents",
	"juhe_business.account_circuit_outbox",
	"juhe_business.account_health_jobs_input_versions",
	"juhe_business.account_health_jobs_input_outbox",
	"juhe_business.request_quota_hourly_window_scope_bindings",
	"juhe_business.resource_authorizations",
	"juhe_business.resource_authorization_sources",
	"juhe_business.resource_authorization_grants",
	"juhe_business.providers",
	"juhe_business.protocols",
	"juhe_business.provider_protocol_profiles",
	"juhe_stats.authorization_team_usage_summary_daily",
	"juhe_stats.authorization_user_usage_summary_daily",
	"juhe_stats.authorization_team_usage_range_windows",
	"juhe_stats.usage_scope_range_windows",
	"juhe_stats.usage_quota_hourly_window_dirty_scopes",
}

// newDualEnv 构造指定臂的测试环境。pg=true 时要求 DSN 已设置（未设置 t.Skip，
// 与门控约定一致）；pg=false 时永远走 SQLite 内存夹具。
func newDualEnv(t *testing.T, pg bool) *dualEnv {
	t.Helper()
	e := &dualEnv{
		t:   t,
		pg:  pg,
		p:   "dsc" + dualHexChars(6) + "-",
		now: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
	}
	if pg {
		dsn := dualPGDSN()
		if dsn == "" {
			e.t.Skip("JUHE_AI_DUALMODE_PG_DSN 未设置，跳过 PG 臂")
		}
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			e.t.Fatalf("PG 臂 sql.Open 失败（DSN 不打印）: %v", err)
		}
		// 生产 PG 模式为单连接池；测试同构以排除连接池并发下的会话状态干扰。
		db.SetMaxOpenConns(1)
		pingCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := db.PingContext(pingCtx); err != nil {
			db.Close()
			e.t.Fatalf("PG 臂连接失败（共享 dev 库不可达，DSN 不打印）: %v", err)
		}
		for _, table := range dualProbeTables {
			var qualified sql.NullString
			if err := db.QueryRowContext(pingCtx, `SELECT to_regclass($1)`, table).Scan(&qualified); err != nil {
				db.Close()
				e.t.Fatalf("PG 臂前置检查失败：to_regclass(%s) 查询错误: %v", table, err)
			}
			if !qualified.Valid || qualified.String == "" {
				db.Close()
				e.t.Fatalf("PG 臂前置检查失败：共享 dev 库缺少表 %s", table)
			}
		}
		e.db = db
		store, err := NewStore(db, true, func() time.Time { return e.now })
		if err != nil {
			db.Close()
			e.t.Fatal(err)
		}
		e.store = store
		t.Cleanup(func() { _ = db.Close() })
	} else {
		f := newFixture(t)
		if _, err := f.db.Exec(usageFixtureDDL); err != nil {
			e.t.Fatal(err)
		}
		// 清理自审计会触达 usage_quota_hourly_window_dirty_scopes（PG 写路径的
		// 脏标记表；SQLite 夹具 DDL 未含），按真实列形状补建空表保证双臂清理
		// 代码同构。
		if _, err := f.db.Exec(`CREATE TABLE IF NOT EXISTS usage_quota_hourly_window_dirty_scopes (
			system_account_id TEXT NOT NULL,
			scope_type TEXT NOT NULL,
			scope_id TEXT NOT NULL DEFAULT '',
			generation INTEGER NOT NULL DEFAULT 1,
			first_dirty_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id))`); err != nil {
			e.t.Fatal(err)
		}
		f.store.AttachStatsDatabase(f.db)
		f.now = e.now // 夹具时钟闭包读 f.now，双臂必须同一时钟基准。
		e.db = f.db
		e.fx = f
		e.store = f.store
	}
	e.store.AttachTimezoneSource(func(ctx context.Context) (string, error) {
		return "UTC", nil
	})
	t.Cleanup(e.cleanupAndAudit)
	return e
}

// runDual 在两个臂上各跑一次用例体；断言全部使用中文失败信息。
func runDual(t *testing.T, fn func(e *dualEnv)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(newDualEnv(t, false)) })
	t.Run("pg", func(t *testing.T) { fn(newDualEnv(t, true)) })
}

// advance 推进双臂共享的测试时钟（SQLite 臂需同步写回 fixture 闭包）。
func (e *dualEnv) advance(d time.Duration) {
	e.now = e.now.Add(d)
	if e.fx != nil {
		e.fx.now = e.now
	}
}

func (e *dualEnv) q(query string) string { return e.store.bind(query) }

func (e *dualEnv) tbl(name string) string {
	if e.pg {
		return "juhe_business." + name
	}
	return name
}

func (e *dualEnv) stbl(name string) string {
	if e.pg {
		return "juhe_stats." + name
	}
	return name
}

func (e *dualEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Exec(e.q(query), args...); err != nil {
		e.t.Fatalf("双模式造数/清理 SQL 失败: %v（query=%s）", err, query)
	}
}

func (e *dualEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(e.q(query), args...).Scan(&n); err != nil {
		e.t.Fatalf("双模式计数查询失败: %v（query=%s）", err, query)
	}
	return n
}

func (e *dualEnv) queryString(query string, args ...any) string {
	e.t.Helper()
	var value string
	if err := e.db.QueryRow(e.q(query), args...).Scan(&value); err != nil {
		e.t.Fatalf("双模式标量查询失败: %v（query=%s）", err, query)
	}
	return value
}

// ---------------------------------------------------------------------------
// 清理与自审计（依赖逆序 DELETE + 同谓词 COUNT）
// ---------------------------------------------------------------------------

// dualGrantPred / dualRuntimePred / dualInstPred 是关系谓词片段：store 随机
// 生成的行 ID 全部经由前缀列（owner/grantee/resource/team/实例来源外键）覆盖。
func (e *dualEnv) dualGrantPred() string {
	return `resource_owner_system_account_id LIKE ? OR grantee_system_account_id LIKE ?
		OR grantee_team_id LIKE ? OR resource_id LIKE ?`
}

func (e *dualEnv) dualRuntimePred() string {
	return `resource_owner_system_account_id LIKE ? OR grantee_system_account_id LIKE ? OR resource_id LIKE ?`
}

func (e *dualEnv) dualInstPred() string {
	return `(authorization_instance_source_account_id LIKE ? OR authorization_instance_owner_system_account_id LIKE ? OR id LIKE ?)`
}

// dualCleanupSteps 返回（清理目标表, 谓词, 谓词参数）序列，依赖逆序：
// stats → bindings → 账户伴生表 → 触发器维护表 → 实例账户 → 源账户 →
// sources → grants → runtime → groups → teams/members → accounts →
// PG 专属供应商链。
func (e *dualEnv) dualCleanupSteps() []struct {
	table string
	where string
	args  []any
} {
	p := e.p + "%" // LIKE 前缀谓词必须带尾随通配符，否则删除与审计永不命中。
	likeP := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = p
		}
		return out
	}
	grantArgs := likeP(4)
	runtimeArgs := likeP(3)
	instArgs := likeP(3)
	steps := []struct {
		table string
		where string
		args  []any
	}{
		{e.stbl("authorization_team_usage_summary_daily"), `system_account_id LIKE ?`, likeP(1)},
		{e.stbl("authorization_user_usage_summary_daily"), `system_account_id LIKE ? OR grantee_filter_system_account_id LIKE ?`, likeP(2)},
		{e.stbl("authorization_team_usage_range_windows"), `system_account_id LIKE ?`, likeP(1)},
		{e.stbl("usage_scope_range_windows"), `system_account_id LIKE ?`, likeP(1)},
		{e.stbl("usage_quota_hourly_window_dirty_scopes"), `system_account_id LIKE ?`, likeP(1)},
		// 绑定行：本 env 的 system_account/scope 前缀，加 source_id 关系子查询
		// 兜底（source_id 是随机 grant ID）。
		{e.tbl("request_quota_hourly_window_scope_bindings"),
			`system_account_id LIKE ? OR scope_id LIKE ? OR source_id IN (SELECT id FROM ` + e.tbl("resource_authorization_grants") + ` WHERE ` + e.dualGrantPred() + `)`,
			append(append([]any{}, likeP(2)...), grantArgs...)},
		// 健康任务输入 fanout（account_id 是随机实例 ID，走实例关系子查询）。
		{e.tbl("account_health_jobs_input_outbox"),
			`account_id IN (SELECT id FROM ` + e.tbl("accounts") + ` WHERE ` + e.dualInstPred() + `) OR account_id LIKE ?`,
			append(append([]any{}, instArgs...), p)},
		{e.tbl("account_health_jobs_input_versions"),
			`account_id IN (SELECT id FROM ` + e.tbl("accounts") + ` WHERE ` + e.dualInstPred() + `) OR account_id LIKE ?`,
			append(append([]any{}, instArgs...), p)},
		{e.tbl("account_name_search_terms"),
			`account_id IN (SELECT id FROM ` + e.tbl("accounts") + ` WHERE ` + e.dualInstPred() + ` OR id LIKE ?) OR system_account_id LIKE ?`,
			append(append([]any{}, instArgs...), p, p)},
		{e.tbl("account_name_search_documents"),
			`account_id IN (SELECT id FROM ` + e.tbl("accounts") + ` WHERE ` + e.dualInstPred() + ` OR id LIKE ?) OR system_account_id LIKE ?`,
			append(append([]any{}, instArgs...), p, p)},
		{e.tbl("account_circuit_outbox"),
			`account_id IN (SELECT id FROM ` + e.tbl("accounts") + ` WHERE ` + e.dualInstPred() + `) OR account_id LIKE ?`,
			append(append([]any{}, instArgs...), p)},
		{e.tbl("group_accounts"),
			`system_account_id LIKE ? OR group_id LIKE ? OR account_id IN (SELECT id FROM ` + e.tbl("accounts") + ` WHERE ` + e.dualInstPred() + ` OR id LIKE ?)`,
			append(append(append([]any{}, p, p), instArgs...), p)},
		// 先删实例（FK 指向源账户与 runtime），再删带前缀的源账户。
		{e.tbl("accounts"), e.dualInstPred(), instArgs},
		{e.tbl("accounts"), `id LIKE ?`, likeP(1)},
		{e.tbl("resource_authorization_sources"),
			`authorization_id IN (SELECT id FROM ` + e.tbl("resource_authorizations") + ` WHERE ` + e.dualRuntimePred() + `) OR source_team_id LIKE ?`,
			append(append([]any{}, runtimeArgs...), p)},
		{e.tbl("resource_authorization_grants"), e.dualGrantPred(), grantArgs},
		{e.tbl("resource_authorizations"), e.dualRuntimePred(), runtimeArgs},
		{e.tbl("groups"), `id LIKE ? OR system_account_id LIKE ?`, likeP(2)},
		{e.tbl("system_team_members"), `team_id LIKE ? OR system_account_id LIKE ?`, likeP(2)},
		{e.tbl("system_teams"), `id LIKE ? OR created_by LIKE ?`, likeP(2)},
		{e.tbl("system_accounts"), `id LIKE ?`, likeP(1)},
	}
	if e.pg {
		// 触发器维护表（account-list-availability dirty / viewer health）：真实 PG
		// 的 accounts/runtime/grants/group_accounts 写入会经触发器隐式种 dirty 行，
		// 全部以本 env 的账户 / viewer 为键（dirty 行对 accounts/system_accounts 均
		// ON DELETE CASCADE，此处显式删除让审计谓词可复核）。插在账户删除之前。
		triggerSteps := []struct {
			table string
			where string
			args  []any
		}{
			{e.tbl("account_list_availability_dirty"),
				`viewer_system_account_id LIKE ? OR account_id IN (SELECT id FROM ` + e.tbl("accounts") + ` WHERE ` + e.dualInstPred() + ` OR id LIKE ?)`,
				append(append(append([]any{}, p), instArgs...), p)},
			{e.tbl("account_list_availability_projection_viewer_health"), `viewer_system_account_id LIKE ?`, likeP(1)},
		}
		insertAt := len(steps)
		for i, step := range steps {
			if step.table == e.tbl("accounts") {
				insertAt = i
				break
			}
		}
		steps = append(steps[:insertAt], append(triggerSteps, steps[insertAt:]...)...)
		steps = append(steps,
			struct{ table, where string; args []any }{e.tbl("provider_protocol_profiles"), `id LIKE ?`, likeP(1)},
			struct{ table, where string; args []any }{e.tbl("protocols"), `code LIKE ? OR id LIKE ?`, likeP(2)},
			struct{ table, where string; args []any }{e.tbl("providers"), `code LIKE ? OR id LIKE ?`, likeP(2)},
		)
	}
	return steps
}

func (e *dualEnv) cleanupAndAudit() {
	e.t.Helper()
	steps := e.dualCleanupSteps()
	for _, step := range steps {
		e.exec(`DELETE FROM `+step.table+` WHERE `+step.where, step.args...)
	}
	for _, step := range steps {
		if n := e.count(`SELECT COUNT(*) FROM `+step.table+` WHERE `+step.where, step.args...); n > 0 {
			e.t.Errorf("清理自审计发现残留：表=%s 残留行=%d（谓词基于前缀 %s）", step.table, n, e.p)
		}
	}
	e.t.Logf("清理自审计完成：%d 张表按前缀 %s 全部零残留", len(steps), e.p)
}

// ---------------------------------------------------------------------------
// 造数 helpers（方言感知）
// ---------------------------------------------------------------------------

// dualProviderChain 承载 accounts/groups 供应商外键依赖链；PG 臂真实 schema 有
// providers/protocols/provider_protocol_profiles 表与 FK，需要先种链；SQLite
// 夹具没有这三张表（相关列是自由文本），链号直接复用。
type dualProviderChain struct {
	ProviderCode    string
	ProtocolCode    string
	ProtocolVersion string
	ProfileID       string
}

func (e *dualEnv) seedProviderChain() dualProviderChain {
	e.t.Helper()
	chain := dualProviderChain{
		ProviderCode:    e.p + "prov",
		ProtocolCode:    e.p + "proto",
		ProtocolVersion: "v1",
		ProfileID:       e.p + "profile",
	}
	if !e.pg {
		// SQLite 夹具无 FK，链号直接可用。
		return chain
	}
	stamp := "2026-09-01T00:00:00.000Z"
	e.exec(`INSERT INTO `+e.tbl("providers")+` (id, code, name, enabled, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		e.p+"prov-id", chain.ProviderCode, e.p+"provider", stamp, stamp)
	e.exec(`INSERT INTO `+e.tbl("protocols")+` (id, code, version, name, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?)`,
		e.p+"proto-id", chain.ProtocolCode, chain.ProtocolVersion, e.p+"protocol", stamp, stamp)
	e.exec(`INSERT INTO `+e.tbl("provider_protocol_profiles")+` (id, provider_code, name, enabled, protocol_code, protocol_version,
		base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, 'https://example.invalid', 'gpt-4o-mini', '["oauth"]', '[]', ?, ?)`,
		chain.ProfileID, chain.ProviderCode, e.p+"profile", chain.ProtocolCode, chain.ProtocolVersion, stamp, stamp)
	return chain
}

func (e *dualEnv) seedSystemAccount(id, status string) {
	e.t.Helper()
	e.exec(`INSERT INTO `+e.tbl("system_accounts")+` (id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, 'user', ?, 'pbkdf2$sha512$120000$abc$def', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, id, id, status)
}

func (e *dualEnv) seedTeam(teamID, name string) {
	e.t.Helper()
	e.exec(`INSERT INTO `+e.tbl("system_teams")+` (id, name, status, created_by, created_at, updated_at)
		VALUES (?, ?, 'active', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, teamID, name, e.p+"team-creator")
}

// seedTeamMember：PG 的 system_team_members 多一个 NOT NULL created_by（SQLite
// 夹具没有该列），方言分支只在 seed 层。
func (e *dualEnv) seedTeamMember(teamID, memberID string) {
	e.t.Helper()
	if e.pg {
		e.exec(`INSERT INTO `+e.tbl("system_team_members")+` (id, team_id, system_account_id, status, joined_at, created_by, created_at, updated_at)
			VALUES (?, ?, ?, 'active', '2026-01-01T00:00:00Z', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			"tm_"+memberID, teamID, memberID, e.p+"team-creator")
		return
	}
	e.exec(`INSERT INTO `+e.tbl("system_team_members")+` (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
		VALUES (?, ?, ?, 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		"tm_"+memberID, teamID, memberID)
}

// seedTeamWithMembers = seedTeam + 逐个 seedTeamMember。
func (e *dualEnv) seedTeamWithMembers(teamID, teamName string, memberIDs ...string) {
	e.t.Helper()
	e.seedTeam(teamID, teamName)
	for _, member := range memberIDs {
		e.seedTeamMember(teamID, member)
	}
}

// seedGroup：PG groups 无 status 列且 created_at NOT NULL；SQLite 夹具相反。
func (e *dualEnv) seedGroup(groupID, ownerID, name string, chain dualProviderChain, isDefault bool) {
	e.t.Helper()
	defaultFlag := 0
	if isDefault {
		defaultFlag = 1
	}
	if e.pg {
		e.exec(`INSERT INTO `+e.tbl("groups")+` (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			groupID, ownerID, name, chain.ProviderCode, defaultFlag)
		return
	}
	e.exec(`INSERT INTO `+e.tbl("groups")+` (id, name, system_account_id, status, provider_code, enabled, is_default, updated_at)
		VALUES (?, ?, ?, 'active', ?, 1, ?, '2026-01-01T00:00:00Z')`,
		groupID, name, ownerID, chain.ProviderCode, defaultFlag)
}

// seedSourceAccount：两个臂的同名列超集（fixture accounts 列覆盖 PG 必填列）。
func (e *dualEnv) seedSourceAccount(accountID, ownerID, name string, chain dualProviderChain) {
	e.t.Helper()
	e.exec(`INSERT INTO `+e.tbl("accounts")+` (id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code,
		protocol_version, name, type, status, credentials_encrypted, credential_mask, health_check_model,
		health_check_endpoint_mode, concurrency_limit, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'oauth', 'active', '{}', '', 'gpt-4o-mini', 'chat_json', 100,
		'2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		accountID, ownerID, chain.ProviderCode, chain.ProfileID, chain.ProtocolCode, chain.ProtocolVersion, name)
}

// ---------------------------------------------------------------------------
// 断言 helpers
// ---------------------------------------------------------------------------

type dualRuntime struct {
	id        string
	status    string
	effective string
}

func (e *dualEnv) runtimeRow(resourceID, granteeID string) dualRuntime {
	e.t.Helper()
	var row dualRuntime
	err := e.db.QueryRow(e.q(`SELECT id, status, COALESCE(effective_source_type,'') FROM `+e.tbl("resource_authorizations")+`
		WHERE resource_id = ? AND grantee_system_account_id = ?`), resourceID, granteeID).
		Scan(&row.id, &row.status, &row.effective)
	if err != nil {
		e.t.Fatalf("读取 runtime 行失败（resource=%s grantee=%s）: %v", resourceID, granteeID, err)
	}
	return row
}

func (e *dualEnv) runtimeStatus(resourceID, granteeID string) string {
	return e.runtimeRow(resourceID, granteeID).status
}

// instanceIDForRuntime 断言 runtime 恰好有一个存活实例并返回其 ID。
func (e *dualEnv) instanceIDForRuntime(runtimeID string) string {
	e.t.Helper()
	rows, err := e.db.Query(e.q(`SELECT id FROM `+e.tbl("accounts")+`
		WHERE authorization_instance_authorization_id = ? AND deleted_at IS NULL ORDER BY id`), runtimeID)
	if err != nil {
		e.t.Fatalf("读取授权实例失败: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	if len(ids) != 1 {
		e.t.Fatalf("授权 %s 的存活实例数 = %d，want 1", runtimeID, len(ids))
	}
	return ids[0]
}

type dualBinding struct {
	scopeID     string
	sourceID    string
	windowHours int
}

func (e *dualEnv) bindingsOf(systemAccountID, scopeType string) []dualBinding {
	e.t.Helper()
	rows, err := e.db.Query(e.q(`SELECT scope_id, source_id, window_hours FROM `+e.tbl("request_quota_hourly_window_scope_bindings")+`
		WHERE system_account_id = ? AND scope_type = ? ORDER BY scope_id`), systemAccountID, scopeType)
	if err != nil {
		e.t.Fatalf("读取配额窗口绑定失败: %v", err)
	}
	defer rows.Close()
	var out []dualBinding
	for rows.Next() {
		var binding dualBinding
		if err := rows.Scan(&binding.scopeID, &binding.sourceID, &binding.windowHours); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, binding)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *dualEnv) grantVersion(grantID string) string {
	return e.queryString(`SELECT updated_at FROM `+e.tbl("resource_authorization_grants")+` WHERE id = ?`, grantID)
}

func (e *dualEnv) grantStatus(grantID string) string {
	return e.queryString(`SELECT status FROM `+e.tbl("resource_authorization_grants")+` WHERE id = ?`, grantID)
}

func (e *dualEnv) inTx(fn func(tx *sql.Tx) error) {
	e.t.Helper()
	tx, err := e.db.BeginTx(context.Background(), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		e.t.Fatalf("事务内级联调用失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		e.t.Fatal(err)
	}
}

func dualNearlyEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func dualAssertLimitsJSON(e *dualEnv, limits any, want string) {
	e.t.Helper()
	if limits == nil {
		if want != "null" {
			e.t.Fatalf("limits 回显 = nil，want %s", want)
		}
		return
	}
	encoded, err := json.Marshal(limits)
	if err != nil {
		e.t.Fatalf("limits 序列化失败: %v", err)
	}
	var got, expected any
	if err := json.Unmarshal(encoded, &got); err != nil {
		e.t.Fatalf("limits 回显解码失败: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		e.t.Fatalf("期望 limits 解码失败: %v", err)
	}
	if !reflect.DeepEqual(got, expected) {
		e.t.Fatalf("limits 回显 = %s，want %s", encoded, want)
	}
}

// ---------------------------------------------------------------------------
// 场景矩阵 A：写路径生命周期
// ---------------------------------------------------------------------------

// A1 个人账户直授权全生命周期。
func TestDualPersonalAccountGrantLifecycle(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		ctx := context.Background()
		owner := e.p + "owner"
		grantee := e.p + "grantee"
		e.seedSystemAccount(owner, "active")
		e.seedSystemAccount(grantee, "active")
		chain := e.seedProviderChain()
		sourceAcc := e.p + "acc-src"
		e.seedSourceAccount(sourceAcc, owner, "源账户A", chain)
		defaultGroup := e.p + "grp-default"
		e.seedGroup(defaultGroup, grantee, "被授权人默认分组", chain, true)

		hourlyLimits := `{"hourly":{"enabled":true,"hours":24,"limit":100}}`
		created, err := e.store.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: sourceAcc,
			GranteeType: "system_account", GranteeID: grantee,
			LimitsJSON: &hourlyLimits,
		}, owner)
		if err != nil {
			e.t.Fatalf("创建账户授权失败: %v", err)
		}
		if !created.Created || created.Item.Status != StatusActive {
			e.t.Fatalf("创建结果异常: created=%v status=%s", created.Created, created.Item.Status)
		}
		grantID := created.Item.ID
		rt := e.runtimeRow(sourceAcc, grantee)
		if rt.status != StatusActive || rt.effective != "manual" {
			e.t.Fatalf("创建后 runtime = %+v，want active/manual", rt)
		}
		// 授权实例存在并绑定到被授权人默认分组。
		instanceID := e.instanceIDForRuntime(rt.id)
		if got := e.queryString(`SELECT group_id FROM `+e.tbl("group_accounts")+` WHERE account_id = ? AND account_authorization_id = ?`, instanceID, rt.id); got != defaultGroup {
			e.t.Fatalf("实例绑定分组 = %s，want %s", got, defaultGroup)
		}
		// hourly 作用域绑定行出现（account_authorization 作用域）。
		bindings := e.bindingsOf(grantee, "account_authorization")
		if len(bindings) != 1 || bindings[0].scopeID != rt.id || bindings[0].sourceID != grantID || bindings[0].windowHours != 24 {
			e.t.Fatalf("account_authorization 绑定 = %+v，want scope=%s source=%s hours=24", bindings, rt.id, grantID)
		}

		// 重复 Create 幂等。
		retry, err := e.store.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: sourceAcc,
			GranteeType: "system_account", GranteeID: grantee,
			LimitsJSON: &hourlyLimits,
		}, owner)
		if err != nil || retry.Created || retry.Item.ID != grantID {
			e.t.Fatalf("重复创建应幂等: result=%+v err=%v", retry, err)
		}

		// PATCH limits（hourly+total）→ Find 回显等值 limits，窗口绑定同步为 12 小时。
		totalLimits := `{"hourly":{"enabled":true,"hours":12,"limit":80},"total":{"enabled":true,"limit":50}}`
		patch, err := e.store.Patch(ctx, grantID, PatchInput{LimitsJSON: &totalLimits, LimitsSet: true}, created.Item.UpdatedAt, owner)
		if err != nil || patch.Status != "updated" {
			e.t.Fatalf("PATCH limits 失败: %+v err=%v", patch, err)
		}
		detail, err := e.store.Find(ctx, grantID)
		if err != nil || detail == nil {
			e.t.Fatalf("Find 失败: %v", err)
		}
		dualAssertLimitsJSON(e, detail.Limits, totalLimits)
		if bindings := e.bindingsOf(grantee, "account_authorization"); len(bindings) != 1 || bindings[0].windowHours != 12 {
			e.t.Fatalf("PATCH limits 后窗口绑定应同步为 12 小时: %+v", bindings)
		}

		// PATCH expiresAt 过去时间 → expired（grant 与 runtime 同步）。
		past := e.now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		expirePatch, err := e.store.Patch(ctx, grantID, PatchInput{ExpiresAt: &past, ExpiresAtSet: true}, patch.Result.UpdatedAt, owner)
		if err != nil || expirePatch.Result.Status != StatusExpired {
			e.t.Fatalf("过去时间 PATCH 应置 expired: status=%s err=%v", expirePatch.Result.Status, err)
		}
		if status := e.runtimeStatus(sourceAcc, grantee); status != StatusExpired {
			e.t.Fatalf("过期后 runtime status = %s，want expired", status)
		}
		if bindings := e.bindingsOf(grantee, "account_authorization"); len(bindings) != 0 {
			e.t.Fatalf("过期后窗口绑定应清空: %+v", bindings)
		}

		// expired → active 恢复必须携带新过期时间。
		if _, err := e.store.Patch(ctx, grantID, PatchInput{Status: ptrString(StatusActive)}, expirePatch.Result.UpdatedAt, owner); err == nil ||
			!strings.Contains(err.Error(), "到期授权恢复时请同时调整过期时间") {
			e.t.Fatalf("无新过期时间的恢复应被拒绝: %v", err)
		}
		future := e.now.Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)
		restore, err := e.store.Patch(ctx, grantID, PatchInput{Status: ptrString(StatusActive), ExpiresAt: &future, ExpiresAtSet: true},
			expirePatch.Result.UpdatedAt, owner)
		if err != nil || restore.Result.Status != StatusActive {
			e.t.Fatalf("携带新过期时间的恢复失败: %+v err=%v", restore, err)
		}
		if status := e.runtimeStatus(sourceAcc, grantee); status != StatusActive {
			e.t.Fatalf("恢复后 runtime status = %s，want active", status)
		}

		// 暂停 → 恢复。
		paused, err := e.store.Patch(ctx, grantID, PatchInput{Status: ptrString(StatusPaused)}, restore.Result.UpdatedAt, owner)
		if err != nil || paused.Result.Status != StatusPaused {
			e.t.Fatalf("暂停失败: %+v err=%v", paused, err)
		}
		if status := e.runtimeStatus(sourceAcc, grantee); status != StatusPaused {
			e.t.Fatalf("暂停后 runtime status = %s，want paused", status)
		}
		resumed, err := e.store.Patch(ctx, grantID, PatchInput{Status: ptrString(StatusActive)}, paused.Result.UpdatedAt, owner)
		if err != nil || resumed.Result.Status != StatusActive {
			e.t.Fatalf("恢复失败: %+v err=%v", resumed, err)
		}

		// grantee 归还 → returned，runtime 投影同步（实例隐藏语义经 runtime 状态表达）。
		returned, err := e.store.Return(ctx, grantID, e.grantVersion(grantID), grantee)
		if err != nil || returned.Status != "updated" || returned.Result.Status != StatusReturned {
			e.t.Fatalf("归还失败: %+v err=%v", returned, err)
		}
		if status := e.runtimeStatus(sourceAcc, grantee); status != StatusReturned {
			e.t.Fatalf("归还后 runtime status = %s，want returned", status)
		}
		if bindings := e.bindingsOf(grantee, "account_authorization"); len(bindings) != 0 {
			e.t.Fatalf("归还后窗口绑定应清空: %+v", bindings)
		}

		// 再次 Create → 复活且实例 ID 不变（幂等复用）。
		revived, err := e.store.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: sourceAcc,
			GranteeType: "system_account", GranteeID: grantee,
			LimitsJSON: &totalLimits,
		}, owner)
		if err != nil || revived.Created || revived.Item.ID != grantID {
			e.t.Fatalf("终态复活应复用行: result=%+v err=%v", revived, err)
		}
		if revived.PreviousStatus == nil || *revived.PreviousStatus != StatusReturned {
			e.t.Fatalf("复活应带回 previousStatus=returned: %+v", revived.PreviousStatus)
		}
		if status := e.runtimeStatus(sourceAcc, grantee); status != StatusActive {
			e.t.Fatalf("复活后 runtime status = %s，want active", status)
		}
		if after := e.instanceIDForRuntime(rt.id); after != instanceID {
			e.t.Fatalf("复活后实例应复用原 ID: before=%s after=%s", instanceID, after)
		}
		if bindings := e.bindingsOf(grantee, "account_authorization"); len(bindings) != 1 || bindings[0].windowHours != 12 {
			e.t.Fatalf("复活后窗口绑定应恢复: %+v", bindings)
		}

		// 最后 owner 回收 DELETE → revoked。
		revoked, err := e.store.Revoke(ctx, grantID, e.grantVersion(grantID), owner)
		if err != nil || revoked.Status != "updated" || revoked.Result.Status != StatusRevoked {
			e.t.Fatalf("回收失败: %+v err=%v", revoked, err)
		}
		if e.grantStatus(grantID) != StatusRevoked {
			e.t.Fatalf("回收后 grant status = %s，want revoked", e.grantStatus(grantID))
		}
		if status := e.runtimeStatus(sourceAcc, grantee); status != StatusRevoked {
			e.t.Fatalf("回收后 runtime status = %s，want revoked", status)
		}
	})
}

// A2 团队账户授权：展开、加成员/移除成员/停用/重启级联（缺默认分组整体失败
// 在 TestDualTeamGrantMissingMemberDefaultGroupFails 单独覆盖）。
func TestDualTeamAccountGrantFanout(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		ctx := context.Background()
		owner := e.p + "owner"
		m1 := e.p + "m1"
		m2 := e.p + "m2"
		m3 := e.p + "m3"
		teamA := e.p + "teamA"
		e.seedSystemAccount(owner, "active")
		e.seedSystemAccount(m1, "active")
		e.seedSystemAccount(m2, "active")
		e.seedSystemAccount(m3, "active")
		e.seedTeamWithMembers(teamA, "团队A", m1, m2, owner) // 归属人同时在团队内
		chain := e.seedProviderChain()
		sourceAcc := e.p + "acc-team"
		e.seedSourceAccount(sourceAcc, owner, "团队源账户", chain)
		grpM1 := e.p + "grp-m1"
		grpM2 := e.p + "grp-m2"
		e.seedGroup(grpM1, m1, "m1默认分组", chain, true)
		e.seedGroup(grpM2, m2, "m2默认分组", chain, true)

		hourlyLimits := `{"hourly":{"enabled":true,"hours":24,"limit":100}}`
		created, err := e.store.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: sourceAcc,
			GranteeType: "team", GranteeID: teamA,
			LimitsJSON: &hourlyLimits,
		}, owner)
		if err != nil {
			e.t.Fatalf("团队授权创建失败: %v", err)
		}
		grantID := created.Item.ID

		// 每个活跃成员：runtime + 实例 + 各自默认分组绑定 + 两类作用域绑定行。
		instanceByMember := map[string]string{}
		for _, member := range []string{m1, m2} {
			rt := e.runtimeRow(sourceAcc, member)
			if rt.status != StatusActive || rt.effective != "team" {
				e.t.Fatalf("成员 %s runtime = %+v，want active/team", member, rt)
			}
			instanceID := e.instanceIDForRuntime(rt.id)
			instanceByMember[member] = instanceID
			wantGroup := grpM1
			if member == m2 {
				wantGroup = grpM2
			}
			if got := e.queryString(`SELECT group_id FROM `+e.tbl("group_accounts")+` WHERE account_id = ? AND account_authorization_id = ?`, instanceID, rt.id); got != wantGroup {
				e.t.Fatalf("成员 %s 实例绑定分组 = %s，want 自己的默认分组 %s", member, got, wantGroup)
			}
			direct := e.bindingsOf(member, "account_authorization")
			if len(direct) != 1 || direct[0].scopeID != rt.id {
				e.t.Fatalf("成员 %s account_authorization 绑定 = %+v，want scope=%s", member, direct, rt.id)
			}
			team := e.bindingsOf(member, "account_authorization_team")
			if len(team) != 1 || team[0].scopeID != instanceID+":"+teamA {
				e.t.Fatalf("成员 %s account_authorization_team 绑定 = %+v，want scope=%s:%s", member, team, instanceID, teamA)
			}
		}
		// 归属人在团队内但不获实例/runtime。
		if n := e.count(`SELECT COUNT(*) FROM `+e.tbl("resource_authorizations")+` WHERE resource_id = ? AND grantee_system_account_id = ?`, sourceAcc, owner); n != 0 {
			e.t.Fatalf("归属人不应获得团队 runtime 行，实际 %d 行", n)
		}

		// 后加成员：真实调用方（addSystemTeamMembersAsync）先写成员行，再在事务内
		// 级联补 runtime + 实例 + 配额绑定。
		grpM3 := e.p + "grp-m3"
		e.seedGroup(grpM3, m3, "m3默认分组", chain, true)
		e.seedTeamMember(teamA, m3)
		now := e.now.UTC().Format(time.RFC3339Nano)
		e.inTx(func(tx *sql.Tx) error {
			return e.store.ApplyActiveTeamGrantsToMembersTx(ctx, tx, teamA, []string{m3}, owner, now)
		})
		rtM3 := e.runtimeRow(sourceAcc, m3)
		if rtM3.status != StatusActive {
			e.t.Fatalf("新成员 runtime status = %s，want active", rtM3.status)
		}
		instanceM3 := e.instanceIDForRuntime(rtM3.id)
		if len(e.bindingsOf(m3, "account_authorization")) != 1 || len(e.bindingsOf(m3, "account_authorization_team")) != 1 {
			e.t.Fatalf("新成员应有两条作用域绑定行")
		}

		// 移除成员：runtime 失效、实例行保留。真实调用方（system-team 仓库）先把
		// 成员行置 removed，这里同步模拟。
		e.exec(`UPDATE `+e.tbl("system_team_members")+` SET status = 'removed' WHERE team_id = ? AND system_account_id = ?`, teamA, m2)
		e.inTx(func(tx *sql.Tx) error {
			return e.store.RevokeTeamSourcesForMemberTx(ctx, tx, teamA, m2, owner, now)
		})
		if status := e.runtimeStatus(sourceAcc, m2); status == StatusActive {
			e.t.Fatalf("被移除成员 runtime 应失效，实际 %s", status)
		}
		if got := e.queryString(`SELECT COALESCE(deleted_at,'') FROM `+e.tbl("accounts")+` WHERE id = ?`, instanceByMember[m2]); got != "" {
			e.t.Fatalf("被移除成员实例行应保留（deleted_at=%q）", got)
		}

		// 团队停用：全部活跃成员 runtime 失效。
		e.inTx(func(tx *sql.Tx) error {
			return e.store.RevokeAllTeamSourcesTx(ctx, tx, teamA, owner, now, "team_disabled")
		})
		for _, member := range []string{m1, m3} {
			if status := e.runtimeStatus(sourceAcc, member); status == StatusActive {
				e.t.Fatalf("团队停用后成员 %s runtime 应失效，实际 %s", member, status)
			}
		}

		// 重新启用：恢复且实例幂等不重复。
		e.inTx(func(tx *sql.Tx) error {
			return e.store.ReactivateTeamGrantsTx(ctx, tx, teamA, owner, now)
		})
		for _, member := range []string{m1, m3} {
			rt := e.runtimeRow(sourceAcc, member)
			if rt.status != StatusActive {
				e.t.Fatalf("重启后成员 %s runtime status = %s，want active", member, rt.status)
			}
			wantInstance := instanceByMember[m1]
			if member == m3 {
				wantInstance = instanceM3
			}
			if again := e.instanceIDForRuntime(rt.id); again != wantInstance {
				e.t.Fatalf("重启后成员 %s 实例应复用: before=%s after=%s", member, wantInstance, again)
			}
		}
		if n := e.count(`SELECT COUNT(*) FROM `+e.tbl("accounts")+` WHERE authorization_instance_authorization_id = ? AND deleted_at IS NULL`, rtM3.id); n != 1 {
			e.t.Fatalf("重启后 m3 实例数 = %d，want 1（幂等不重复）", n)
		}
		_ = grantID
	})
}

// A2b 成员缺默认分组 → 整体失败，且 grants/runtime/instances/bindings 四表零残留。
func TestDualTeamGrantMissingMemberDefaultGroupFails(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		owner := e.p + "owner"
		m1 := e.p + "m1"
		m2 := e.p + "m2"
		teamA := e.p + "teamA"
		e.seedSystemAccount(owner, "active")
		e.seedSystemAccount(m1, "active")
		e.seedSystemAccount(m2, "active")
		e.seedTeamWithMembers(teamA, "缺分组团队", m1, m2, owner)
		chain := e.seedProviderChain()
		sourceAcc := e.p + "acc-missing"
		e.seedSourceAccount(sourceAcc, owner, "缺分组源账户", chain)
		e.seedGroup(e.p+"grp-has", m1, "m1默认分组", chain, true)
		// m2 故意没有默认分组。

		_, err := e.store.Create(context.Background(), CreateInput{
			ResourceType: "account", ResourceID: sourceAcc,
			GranteeType: "team", GranteeID: teamA,
		}, owner)
		if err == nil || !strings.Contains(err.Error(), "缺少该供应商的默认分组") {
			e.t.Fatalf("缺默认分组成员应使整体失败: err=%v", err)
		}
		// 四表零残留（整体事务回滚）。
		if n := e.count(`SELECT COUNT(*) FROM `+e.tbl("resource_authorization_grants")+` WHERE grantee_team_id = ?`, teamA); n != 0 {
			e.t.Fatalf("失败创建后 grants 残留 %d 行", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM `+e.tbl("resource_authorizations")+` WHERE resource_id = ?`, sourceAcc); n != 0 {
			e.t.Fatalf("失败创建后 runtime 残留 %d 行", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM `+e.tbl("accounts")+` WHERE authorization_instance_source_account_id = ?`, sourceAcc); n != 0 {
			e.t.Fatalf("失败创建后实例残留 %d 行", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM `+e.tbl("request_quota_hourly_window_scope_bindings")+` WHERE source_id IN (SELECT id FROM `+e.tbl("resource_authorization_grants")+` WHERE grantee_team_id = ?)`, teamA); n != 0 {
			e.t.Fatalf("失败创建后 bindings 残留 %d 行", n)
		}
	})
}

// A3 PATCH 复活补实例：过期期间加入的成员被级联门禁跳过，恢复后补齐。
func TestDualPatchReviveProvisionsLateMembers(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		ctx := context.Background()
		owner := e.p + "owner"
		m1 := e.p + "m1"
		m3 := e.p + "m3"
		teamA := e.p + "teamA"
		e.seedSystemAccount(owner, "active")
		e.seedSystemAccount(m1, "active")
		e.seedSystemAccount(m3, "active")
		e.seedTeamWithMembers(teamA, "复活团队", m1, owner)
		chain := e.seedProviderChain()
		sourceAcc := e.p + "acc-revive"
		e.seedSourceAccount(sourceAcc, owner, "复活源账户", chain)
		e.seedGroup(e.p+"grp-m1", m1, "m1默认分组", chain, true)

		expires := e.now.Add(time.Hour).UTC().Format(time.RFC3339Nano)
		hourlyLimits := `{"hourly":{"enabled":true,"hours":24,"limit":100}}`
		created, err := e.store.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: sourceAcc,
			GranteeType: "team", GranteeID: teamA, ExpiresAt: &expires,
			LimitsJSON: &hourlyLimits,
		}, owner)
		if err != nil {
			e.t.Fatalf("团队授权创建失败: %v", err)
		}
		grantID := created.Item.ID
		rtM1 := e.runtimeRow(sourceAcc, m1)
		instanceM1 := e.instanceIDForRuntime(rtM1.id)

		// 推进时钟使授权过期，并用 jobs sweep 的直接翻转模拟（只动 grants 行）。
		e.advance(2 * time.Hour)
		nowText := e.now.UTC().Format("2006-01-02T15:04:05.000Z")
		e.exec(`UPDATE `+e.tbl("resource_authorization_grants")+` SET status = 'expired', revoked_at = ?, updated_at = ? WHERE id = ?`,
			nowText, nowText, grantID)

		// 过期期间加成员：级联被过期门禁跳过（active 团队授权查询不含 expired grant）。
		e.seedGroup(e.p+"grp-m3", m3, "m3默认分组", chain, true)
		e.seedTeamMember(teamA, m3)
		e.inTx(func(tx *sql.Tx) error {
			return e.store.ApplyActiveTeamGrantsToMembersTx(ctx, tx, teamA, []string{m1, m3}, owner, nowText)
		})
		if n := e.count(`SELECT COUNT(*) FROM `+e.tbl("resource_authorizations")+` WHERE resource_id = ? AND grantee_system_account_id = ?`, sourceAcc, m3); n != 0 {
			e.t.Fatalf("过期期间加成员不应产生 runtime 行，实际 %d", n)
		}

		// PATCH 恢复 active + 新 expiresAt → 新成员获得 runtime/实例/绑定，老成员实例 ID 不变。
		newExpiry := e.now.Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)
		restore, err := e.store.Patch(ctx, grantID, PatchInput{
			Status: ptrString(StatusActive), ExpiresAt: &newExpiry, ExpiresAtSet: true,
		}, e.grantVersion(grantID), owner)
		if err != nil || restore.Result.Status != StatusActive {
			e.t.Fatalf("PATCH 复活失败: %+v err=%v", restore, err)
		}
		rtM3 := e.runtimeRow(sourceAcc, m3)
		if rtM3.status != StatusActive {
			e.t.Fatalf("恢复后新成员 runtime = %s，want active", rtM3.status)
		}
		e.instanceIDForRuntime(rtM3.id) // 新成员有实例
		if len(e.bindingsOf(m3, "account_authorization")) != 1 {
			e.t.Fatalf("恢复后新成员应有 account_authorization 绑定")
		}
		if after := e.instanceIDForRuntime(rtM1.id); after != instanceM1 {
			e.t.Fatalf("老成员实例 ID 应不变: before=%s after=%s", instanceM1, after)
		}
	})
}

// A4 来源优先级合并：先个人后团队 → 同一条 runtime、effective=team、manual 来源被覆盖；
// 回收团队授权 → runtime revoked 且个人来源不恢复。
func TestDualSourcePriorityMergeAndTeamRevoke(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		ctx := context.Background()
		owner := e.p + "owner"
		grantee := e.p + "grantee"
		teamA := e.p + "teamA"
		e.seedSystemAccount(owner, "active")
		e.seedSystemAccount(grantee, "active")
		e.seedTeamWithMembers(teamA, "优先级团队", grantee, owner)
		chain := e.seedProviderChain()
		grp := e.p + "grp-prio"
		e.seedGroup(grp, owner, "优先级分组", chain, false)

		if _, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grp,
			GranteeType: "system_account", GranteeID: grantee,
		}, owner); err != nil {
			e.t.Fatalf("个人授权创建失败: %v", err)
		}
		teamGrant, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grp,
			GranteeType: "team", GranteeID: teamA,
		}, owner)
		if err != nil {
			e.t.Fatalf("团队授权创建失败: %v", err)
		}
		// 同一条 runtime 行、effective=team、manual 来源 superseded。
		if n := e.count(`SELECT COUNT(*) FROM `+e.tbl("resource_authorizations")+` WHERE resource_id = ? AND grantee_system_account_id = ?`, grp, grantee); n != 1 {
			e.t.Fatalf("runtime 行数 = %d，want 1（来源合并）", n)
		}
		rt := e.runtimeRow(grp, grantee)
		if rt.effective != "team" {
			e.t.Fatalf("合并后 effective_source_type = %s，want team", rt.effective)
		}
		manualStatus := e.queryString(`SELECT status FROM `+e.tbl("resource_authorization_sources")+`
			WHERE authorization_id = ? AND source_type = 'manual'`, rt.id)
		if manualStatus != "superseded" {
			e.t.Fatalf("manual 来源状态 = %s，want superseded", manualStatus)
		}

		// 回收团队授权 → runtime revoked，个人来源不恢复。
		mutation, err := e.store.Revoke(ctx, teamGrant.Item.ID, teamGrant.Item.UpdatedAt, owner)
		if err != nil || mutation.Status != "updated" {
			e.t.Fatalf("团队授权回收失败: %+v err=%v", mutation, err)
		}
		if status := e.runtimeStatus(grp, grantee); status != StatusRevoked {
			e.t.Fatalf("团队回收后 runtime status = %s，want revoked", status)
		}
		manualStatus = e.queryString(`SELECT status FROM `+e.tbl("resource_authorization_sources")+`
			WHERE authorization_id = ? AND source_type = 'manual'`, rt.id)
		if manualStatus != "superseded" {
			e.t.Fatalf("团队回收后 manual 来源应保持 superseded，实际 %s", manualStatus)
		}
	})
}

// A5 分组授权：个人直授权归还 → returned；团队展开零实例并拉回 runtime。
func TestDualGroupGrantNoInstanceAndReturn(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		ctx := context.Background()
		owner := e.p + "owner"
		grantee := e.p + "grantee"
		teamA := e.p + "teamA"
		e.seedSystemAccount(owner, "active")
		e.seedSystemAccount(grantee, "active")
		e.seedTeamWithMembers(teamA, "分组团队", grantee, owner)
		grp := e.p + "grp-noinst"
		e.seedGroup(grp, owner, "零实例分组", e.seedProviderChain(), false)

		if _, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grp,
			GranteeType: "system_account", GranteeID: grantee,
		}, owner); err != nil {
			e.t.Fatalf("分组个人授权创建失败: %v", err)
		}
		rt := e.runtimeRow(grp, grantee)
		// 分组授权零实例。
		if n := e.count(`SELECT COUNT(*) FROM `+e.tbl("accounts")+` WHERE authorization_instance_authorization_id = ?`, rt.id); n != 0 {
			e.t.Fatalf("分组授权不应产生授权实例，实际 %d", n)
		}
		// 无 hourly limits → 无窗口绑定行。
		if n := e.count(`SELECT COUNT(*) FROM `+e.tbl("request_quota_hourly_window_scope_bindings")+` WHERE source_id IN (SELECT id FROM `+e.tbl("resource_authorization_grants")+` WHERE resource_id = ?)`, grp); n != 0 {
			e.t.Fatalf("无 limits 分组授权不应产生绑定行，实际 %d", n)
		}
		// 个人直授权分组归还（ReturnGroupForGrantee）→ returned。归还必须在团队
		// 授权覆盖 manual 来源之前（superseded 的 manual 来源不可归还）。
		receipt, err := e.store.ReturnGroupForGrantee(ctx, grp, grantee, grantee)
		if err != nil || receipt == nil {
			e.t.Fatalf("分组归还失败: receipt=%+v err=%v", receipt, err)
		}
		if receipt.ID != rt.id || receipt.ResourceName == "" {
			e.t.Fatalf("归还回执异常: %+v", receipt)
		}
		if status := e.runtimeStatus(grp, grantee); status != StatusReturned {
			e.t.Fatalf("分组归还后 runtime status = %s，want returned", status)
		}
		// 团队展开同样零实例；团队来源把归还后的 runtime 重新拉回 active。
		if _, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grp,
			GranteeType: "team", GranteeID: teamA,
		}, owner); err != nil {
			e.t.Fatalf("分组团队授权创建失败: %v", err)
		}
		rtAfter := e.runtimeRow(grp, grantee)
		if rtAfter.id != rt.id || rtAfter.effective != "team" {
			e.t.Fatalf("团队展开后 runtime = %+v，want 同行 + effective=team", rtAfter)
		}
		if n := e.count(`SELECT COUNT(*) FROM `+e.tbl("accounts")+` WHERE authorization_instance_authorization_id = ?`, rtAfter.id); n != 0 {
			e.t.Fatalf("分组团队授权不应产生授权实例，实际 %d", n)
		}
	})
}

// A6 资源删除回收：RevokeGrantsForResourceDeleted → 相关 grant/runtime 转 revoked。
func TestDualResourceDeletedRevokesGrants(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		owner := e.p + "owner"
		grantee := e.p + "grantee"
		teamA := e.p + "teamA"
		e.seedSystemAccount(owner, "active")
		e.seedSystemAccount(grantee, "active")
		e.seedTeamWithMembers(teamA, "删除团队", grantee, owner)
		grp := e.p + "grp-deleted"
		e.seedGroup(grp, owner, "待删除分组", e.seedProviderChain(), false)
		ctx := context.Background()

		direct, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grp,
			GranteeType: "system_account", GranteeID: grantee,
		}, owner)
		if err != nil {
			e.t.Fatalf("个人授权创建失败: %v", err)
		}
		teamGrant, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grp,
			GranteeType: "team", GranteeID: teamA,
		}, owner)
		if err != nil {
			e.t.Fatalf("团队授权创建失败: %v", err)
		}
		now := e.now.UTC().Format(time.RFC3339Nano)
		e.inTx(func(tx *sql.Tx) error {
			return e.store.RevokeGrantsForResourceDeleted(ctx, tx, "group", grp, owner, now)
		})
		if status := e.grantStatus(direct.Item.ID); status != StatusRevoked {
			e.t.Fatalf("资源删除后个人 grant status = %s，want revoked", status)
		}
		if status := e.grantStatus(teamGrant.Item.ID); status != StatusRevoked {
			e.t.Fatalf("资源删除后团队 grant status = %s，want revoked", status)
		}
		if status := e.runtimeStatus(grp, grantee); status != StatusRevoked {
			e.t.Fatalf("资源删除后 runtime status = %s，want revoked", status)
		}
	})
}
