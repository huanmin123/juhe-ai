package main

// w0219 真实 dev PG 门禁化回归（BUG-0219）：newChainListAvailabilityDirtyMarker
// 的家族展开 SELECT 曾用 SQLite 风格 ? 占位符，而 gateway pgpool 无驱动层
// ? -> $n 改写（jobs 侧 rewriteDriver 不在本进程），pgx 词法把 ? 当操作符
// token 直接报 42601 语法错误，脏标记恒失败并向上传播导致 Redis 避让不写、
// 响应检查策略账号避让恒失效。这是 BUG-0177（manualtestrepo 全部 SQL 用 ?）
// 的同类残留。SQLite fixture（modernc 同时接受 ? 与 $n 两种占位符）永远
// 暴露不了此类缺陷，按 0177 先例以真实 PG 门禁化测试兜底：复用
// w1g2CoverPostgres 的临时子库 juhe_ai_sub2api_dev_w1cover（隔离铁律：
// env 缺失或 PG 不可达时 Skip），种子行全部 w0219 前缀并在事后清理，
// 不触碰既有数据。
//
// 断言采用相对基线（marker 前后 generation +1）而非绝对值：权威 schema 的
// accounts 表挂有 AFTER INSERT 语句级触发器
// account_list_availability_accounts_insert，本测试 seed 账号行时触发器会
// 预写 dirty 行（gen=1），marker 随后走 ON CONFLICT 自增分支；无触发器
// 预写时 marker 走首次 INSERT 分支（COALESCE(NULL,0)+1）。两条路径对
// “相对前值 +1”等价，断言因此对触发器在场与否稳定。projections 无
// w0219 行，max(source_generation)+1 分支已由 SQLite 侧
// TestW1XListAvailabilityDirtyMarkerArms 覆盖。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestW0219ListAvailabilityDirtyMarkerPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	appURL := w1g2CoverPostgres(t)
	db, err := sql.Open("pgx", appURL)
	if err != nil {
		t.Fatalf("打开临时子库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(2)
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("临时子库 ping 失败: %v", err)
	}

	// provider / protocol profile / 协议列动态取库里已有行：accounts 的
	// provider_code 与 provider_protocol_profile_id 均有 FK，硬编码会耦合
	// 种子内容与权威 DDL 演化。
	var providerCode, profileID, protocolCode, protocolVersion string
	if err := db.QueryRowContext(ctx, `
		SELECT pp.provider_code, pp.id, pp.protocol_code, pp.protocol_version
		FROM juhe_business.provider_protocol_profiles pp
		JOIN juhe_business.providers p ON p.code = pp.provider_code
		WHERE pp.enabled = 1
		LIMIT 1`).Scan(&providerCode, &profileID, &protocolCode, &protocolVersion); err != nil {
		t.Fatalf("读取既有 provider/protocol profile 失败: %v", err)
	}

	// system_accounts / accounts 的 NOT NULL 无默认列（username、
	// display_name、password_hash、created_at、updated_at 等）必须显式提供。
	for _, statement := range []string{
		`INSERT INTO juhe_business.system_accounts (id, username, display_name, password_hash, created_at, updated_at)
			VALUES ('w0219-sys', 'w0219-sys', 'w0219', 'w0219-hash', '2026-09-28T00:00:00.000Z', '2026-09-28T00:00:00.000Z')
			ON CONFLICT (id) DO NOTHING`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed system account: %v", err)
		}
	}
	seedAccount := func(id, sourceAccountID string) {
		t.Helper()
		var source any
		if sourceAccountID != "" {
			source = sourceAccountID
		}
		if _, err := db.ExecContext(ctx, `
	INSERT INTO juhe_business.accounts (
	  id, system_account_id, provider_code, provider_protocol_profile_id,
	  protocol_code, protocol_version, name, type, credentials_encrypted,
	  health_check_model, health_check_endpoint_mode, authorization_instance_source_account_id,
	  created_at, updated_at
	) VALUES ($1, 'w0219-sys', $2, $3, $4, $5, $1, 'api_key', 'w0219-creds', 'w0219-model', 'chat_json', $6,
	  '2026-09-28T00:00:00.000Z', '2026-09-28T00:00:00.000Z')
	ON CONFLICT (id) DO NOTHING`,
			id, providerCode, profileID, protocolCode, protocolVersion, source); err != nil {
			t.Fatalf("seed account %s: %v", id, err)
		}
	}
	// 先父后子：authorization_instance_source_account_id 有 FK 引用 accounts(id)。
	seedAccount("w0219-src", "")
	seedAccount("w0219-child", "w0219-src")

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, statement := range []string{
			`DELETE FROM juhe_business.account_list_availability_dirty WHERE account_id LIKE 'w0219-%'`,
			`DELETE FROM juhe_business.accounts WHERE id = 'w0219-child'`,
			`DELETE FROM juhe_business.accounts WHERE id LIKE 'w0219-%'`,
			`DELETE FROM juhe_business.system_accounts WHERE id = 'w0219-sys'`,
		} {
			if _, err := db.ExecContext(cleanupCtx, statement); err != nil {
				t.Logf("w0219 清理语句失败（残留行由子库重灌兜底）: %v", err)
			}
		}
	})

	// dirtyGenerationOf 读账号当前脏行 generation；无行（触发器未预写或
	// 首次运行）视为 0。
	dirtyGenerationOf := func(accountID string) int64 {
		t.Helper()
		var generation int64
		if err := db.QueryRowContext(ctx,
			`SELECT generation FROM juhe_business.account_list_availability_dirty WHERE account_id = $1`, accountID).
			Scan(&generation); err == sql.ErrNoRows {
			return 0
		} else if err != nil {
			t.Fatalf("读 %s 脏行基线: %v", accountID, err)
		}
		return generation
	}

	marker := newChainListAvailabilityDirtyMarker(&composition{db: db, pgDialect: true})
	if marker == nil {
		t.Fatal("postgres 方言必须装配脏标记")
	}

	// 记录 seed 触发器可能预写的基线（见文件头注释）。
	srcBase, childBase := dirtyGenerationOf("w0219-src"), dirtyGenerationOf("w0219-child")

	// 修复前：家族展开 SELECT 的裸 ? 在 pgx 下报 42601，此处直接失败。
	if err := marker(ctx, "w0219-src", "w0219 避让", 1234); err != nil {
		t.Fatalf("PG 方言脏标记写入失败（BUG-0219 占位符方言回归）: %v", err)
	}

	var generation int64
	var reason string
	if err := db.QueryRowContext(ctx,
		`SELECT generation, reason FROM juhe_business.account_list_availability_dirty WHERE account_id = 'w0219-src'`).
		Scan(&generation, &reason); err != nil {
		t.Fatalf("读父账号脏行: %v", err)
	}
	if generation != srcBase+1 || reason != "w0219 避让" {
		t.Fatalf("父账号脏行 = (gen %d, reason %q), want (基线 %d + 1, w0219 避让)", generation, reason, srcBase)
	}
	if got := dirtyGenerationOf("w0219-child"); got != childBase+1 {
		t.Fatalf("家族展开必须覆盖授权实例子账号: gen %d, want 基线 %d + 1", got, childBase)
	}

	// 重复写入：upsert 冲突分支 generation 再自增。
	if err := marker(ctx, "w0219-src", "w0219 避让-2", 2345); err != nil {
		t.Fatalf("PG 方言重复写入失败: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT generation FROM juhe_business.account_list_availability_dirty WHERE account_id = 'w0219-src'`).
		Scan(&generation); err != nil {
		t.Fatalf("复读父账号脏行: %v", err)
	}
	if generation != srcBase+2 {
		t.Fatalf("重复写入必须自增 generation: %d, want 基线 %d + 2", generation, srcBase)
	}
}
