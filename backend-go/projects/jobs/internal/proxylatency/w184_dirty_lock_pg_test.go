package proxylatency

// w184_dirty_lock_pg_test.go 在真实 PostgreSQL（w1cover 共享覆盖库）上复现并
// 锁 BUG-0237 的 2026-09-30 生产三方死锁环锁序契约（约定出处问题-0184/0192）：
//
//   - 修复前 J3a 形态（违规方）：事务先 SELECT ... FOR UPDATE 拿 proxy_profiles
//     行锁，再 UPDATE proxy_profiles —— 行级触发器
//     account_list_availability_proxies 经 mark_dirty_accounts 在触发器内取
//     pg_advisory_xact_lock(7001001)（advisorylock.AccountListDirty）。
//   - 合规方形态（模拟 circuitstore applyOneClaim）：事务首语句取 7001001，
//     再对同一 proxy 行做交叉写。
//
// 两方以屏障交错后构成环（违规方持行锁等 advisory、合规方持 advisory 等
// 行锁），PG 死锁检测器必然以 SQLSTATE 40P01 打破其中一方——这是"修复前
// 可复现"的判据；修复后两方都先取 7001001，事务在 advisory 上全序串行化，
// 两轮并发全部提交成功且无 40P01。
//
// 连接与跳过条件完全复用 w15/w10c 门禁模式：JUHE_AI_W184_PG_URL 优先，否则
// 读 shared.env 的 JUHE_AI_POSTGRES_URL 并改指 w1cover 覆盖库；PG 不可达时
// t.Skip。数据全部 w184- 前缀，测试结束清理；连接串不进日志与断言。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	w184ProxyID      = "w184-proxy"
	w184AccountID    = "w184-acct"
	w184RoundTimeout = 20 * time.Second
)

func w184W1CoverDSN(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("JUHE_AI_W184_PG_URL"); url != "" {
		return url
	}
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skip("w184 PG gated: shared.env 不可读")
	}
	base := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip("w184 PG gated: 缺少 JUHE_AI_POSTGRES_URL")
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

func w184OpenPG(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", w184W1CoverDSN(t))
	if err != nil {
		t.Skip("w184 PG gated: pgx 打开失败")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Skip("w184 PG gated: PG 不可达")
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// w184EnsureDirtyFunctions 幂等补齐 account_list_availability_* 函数族并挂上
// proxy_profiles 触发器（权威 DDL 来自 maintenance
// internal/schema/pg_schema_business_tables.go；共享覆盖库可能被外部流程重置
// 或停留在旧函数体，本 seed 保证触发器汇聚点内含 advisory 7001001，否则死锁
// 无法复现）。仅加法幂等 DDL（CREATE OR REPLACE / DROP+CREATE TRIGGER）。
func w184EnsureDirtyFunctions(t *testing.T, db *sql.DB) {
	t.Helper()
	const source = "../../../maintenance/internal/schema/pg_schema_business_tables.go"
	data, err := os.ReadFile(source)
	if err != nil {
		t.Skipf("w184: 无法读取权威 schema 源（%s）: %v", source, err)
	}
	text := strings.ReplaceAll(string(data), "`+\"`\"+`", "")
	re := regexp.MustCompile(`(?s)CREATE OR REPLACE FUNCTION account_list_availability_\w+\(.*?\$function\$;`)
	matches := re.FindAllString(text, -1)
	if len(matches) == 0 {
		t.Skip("w184: 权威 schema 源中未找到 account_list_availability 函数族")
	}
	applied := 0
	for _, match := range matches {
		statement := strings.TrimSpace(strings.ReplaceAll(match, "\r\n", "\n"))
		if _, err := db.Exec(`SET search_path = juhe_business; ` + statement); err != nil {
			t.Logf("w184 函数族 seed 跳过一条: %v", err)
			continue
		}
		applied++
	}
	if applied == 0 {
		t.Fatalf("w184: 函数族 seed 一条都未应用（共 %d 条）", len(matches))
	}
	var definition string
	if err := db.QueryRow(`SELECT pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'juhe_business' AND p.proname = 'account_list_availability_mark_dirty_accounts'`).Scan(&definition); err != nil {
		t.Fatalf("w184: mark_dirty_accounts 函数不存在，死锁契约无法验证: %v", err)
	}
	if !strings.Contains(definition, "7001001") {
		t.Fatalf("w184: mark_dirty_accounts 函数体缺少 pg_advisory_xact_lock(7001001)，触发器路径不会等待 dirty 序列化锁")
	}
	for _, statement := range []string{
		`DROP TRIGGER IF EXISTS account_list_availability_proxies ON juhe_business.proxy_profiles`,
		`CREATE TRIGGER account_list_availability_proxies AFTER UPDATE OR DELETE ON juhe_business.proxy_profiles FOR EACH ROW EXECUTE FUNCTION juhe_business.account_list_availability_proxy_dirty_trigger()`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("w184: proxy 触发器 seed 失败: %v", err)
		}
	}
}

// w184SeedFixture 写入一对 proxy/account 夹具（accounts.proxy_profile_id 指向
// 夹具 proxy，保证 mark_dirty_proxy 解析到非空账户集合——空集合会先于
// advisory 提前 RETURN，环不会形成）。
func w184SeedFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	var viewer string
	if err := db.QueryRowContext(ctx, `SELECT id FROM juhe_business.system_accounts ORDER BY id LIMIT 1`).Scan(&viewer); err != nil {
		viewer = "w184-sys"
		if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.system_accounts (id, username, display_name, password_hash) VALUES ('w184-sys', 'w184-sys', 'w184', 'x') ON CONFLICT (id) DO NOTHING`); err != nil {
			t.Skipf("w184 PG gated: system_accounts 种子不可用: %v", err)
		}
	}
	var provider, profile, protocolCode, protocolVersion string
	if err := db.QueryRowContext(ctx, `SELECT provider_code, id, protocol_code, protocol_version FROM juhe_business.provider_protocol_profiles ORDER BY id LIMIT 1`).Scan(&provider, &profile, &protocolCode, &protocolVersion); err != nil {
		t.Skipf("w184 PG gated: 无可用 provider_protocol_profiles 种子: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.proxy_profiles
		(id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES ($1, $2, 'w184', 'http', '127.0.0.1', 9, TRUE, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT (id) DO UPDATE SET test_status = 'unknown', last_test_message = NULL, updated_at = CURRENT_TIMESTAMP`, w184ProxyID, viewer); err != nil {
		t.Skipf("w184 PG gated: proxy_profiles 种子不可用: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.accounts
		(id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, credentials_encrypted, health_check_model, health_check_endpoint_mode, proxy_profile_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'w184', 'openai', '', 'w184-model', 'chat_json', $7, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
		ON CONFLICT (id) DO NOTHING`, w184AccountID, viewer, provider, profile, protocolCode, protocolVersion, w184ProxyID); err != nil {
		t.Skipf("w184 PG gated: accounts 种子不可用: %v", err)
	}
	t.Cleanup(func() {
		for _, statement := range []string{
			`DELETE FROM juhe_business.account_list_availability_dirty WHERE account_id LIKE 'w184-%'`,
			`DELETE FROM juhe_business.accounts WHERE id LIKE 'w184-%'`,
			`DELETE FROM juhe_business.proxy_profiles WHERE id LIKE 'w184-%'`,
			`DELETE FROM juhe_business.system_accounts WHERE id LIKE 'w184-%'`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Logf("w184 cleanup: %v", err)
			}
		}
	})
}

// w184ResetRound 每轮之间清理夹具状态（dirty 行与 proxy 测试态）。
func w184ResetRound(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM juhe_business.account_list_availability_dirty WHERE account_id LIKE 'w184-%'`); err != nil {
		t.Fatalf("w184 重置 dirty 失败: %v", err)
	}
	if _, err := db.Exec(`UPDATE juhe_business.proxy_profiles SET test_status = 'unknown', last_test_message = NULL WHERE id = $1`, w184ProxyID); err != nil {
		t.Fatalf("w184 重置 proxy 失败: %v", err)
	}
}

// w184IsDeadlock 判定 SQLSTATE 40P01（deadlock_detected）。
func w184IsDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

type w184Outcome struct {
	err        error
	deadlocked bool
}

// w184RunViolatorVsCompliant 执行一轮两方并发。violatorFirstLock 为 true 时
// 违规方先拿 proxy 行锁（修复前形态）；为 false 时违规方也先取 7001001
// （修复后形态）。返回两方结论。
func w184RunViolatorVsCompliant(ctx context.Context, db *sql.DB, violatorFirstLock bool) (w184Outcome, w184Outcome) {
	violatorLocked := make(chan struct{})
	compliantLocked := make(chan struct{})
	violatorDone := make(chan w184Outcome, 1)
	compliantDone := make(chan w184Outcome, 1)

	go func() {
		outcome := w184Outcome{}
		defer func() { violatorDone <- outcome }()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			outcome.err = err
			return
		}
		defer func() { _ = tx.Rollback() }()
		if violatorFirstLock {
			if _, err := tx.ExecContext(ctx, `SELECT id FROM juhe_business.proxy_profiles WHERE id = $1 FOR UPDATE`, w184ProxyID); err != nil {
				outcome.err = err
				return
			}
			close(violatorLocked)
			select {
			case <-compliantLocked:
			case <-ctx.Done():
				outcome.err = ctx.Err()
				return
			}
		} else {
			// 修复后：首语句先取 dirty 序列化锁，再拿行锁（lockAccountListDirtyInTx 契约）。
			if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(7001001)); err != nil {
				outcome.err = err
				return
			}
			if _, err := tx.ExecContext(ctx, `SELECT id FROM juhe_business.proxy_profiles WHERE id = $1 FOR UPDATE`, w184ProxyID); err != nil {
				outcome.err = err
				return
			}
			close(violatorLocked)
		}
		// UPDATE 触发 account_list_availability_proxies → mark_dirty_accounts
		// 在触发器内取 advisory 7001001。
		if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.proxy_profiles SET test_status = 'warning', last_test_message = 'w184-violator' WHERE id = $1`, w184ProxyID); err != nil {
			outcome.err = err
			outcome.deadlocked = w184IsDeadlock(err)
			return
		}
		if err := tx.Commit(); err != nil {
			outcome.err = err
			outcome.deadlocked = w184IsDeadlock(err)
			return
		}
	}()

	go func() {
		outcome := w184Outcome{}
		defer func() { compliantDone <- outcome }()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			outcome.err = err
			return
		}
		defer func() { _ = tx.Rollback() }()
		// 合规方：首语句取 7001001（applyOneClaim 契约）。
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(7001001)); err != nil {
			outcome.err = err
			return
		}
		close(compliantLocked)
		if violatorFirstLock {
			select {
			case <-violatorLocked:
			case <-ctx.Done():
				outcome.err = ctx.Err()
				return
			}
		}
		// 对同一 proxy 行的交叉写：等违规方的行锁（触发器内的 advisory 对本
		// 事务可重入，不构成自阻塞）。
		if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.proxy_profiles SET latency_ms = 1 WHERE id = $1`, w184ProxyID); err != nil {
			outcome.err = err
			outcome.deadlocked = w184IsDeadlock(err)
			return
		}
		if err := tx.Commit(); err != nil {
			outcome.err = err
			outcome.deadlocked = w184IsDeadlock(err)
			return
		}
	}()

	var violator, compliant w184Outcome
	for collected := 0; collected < 2; {
		select {
		case violator = <-violatorDone:
			collected++
		case compliant = <-compliantDone:
			collected++
		case <-ctx.Done():
			return w184Outcome{err: ctx.Err()}, w184Outcome{err: ctx.Err()}
		}
	}
	return violator, compliant
}

// TestW184ProxyDirtyAdvisoryLockOrderOnW1Cover 是 BUG-0237（文档 docs/bug/问题-0237）死锁锁序的真实 PG
// 复现门禁：修复前形态必须复现 SQLSTATE 40P01；修复后形态两轮并发全部成功。
func TestW184ProxyDirtyAdvisoryLockOrderOnW1Cover(t *testing.T) {
	db := w184OpenPG(t)
	w184EnsureDirtyFunctions(t, db)
	w184SeedFixture(t, db)

	// 阶段一：修复前形态（违规方先拿行锁）——屏障交错强制成环，重试至多
	// 5 轮，任一轮出现 40P01 即判"可复现"，并打印原始错误。
	reproduced := false
	for round := 1; round <= 5 && !reproduced; round++ {
		w184ResetRound(t, db)
		ctx, cancel := context.WithTimeout(context.Background(), w184RoundTimeout)
		violator, compliant := w184RunViolatorVsCompliant(ctx, db, true)
		cancel()
		deadlocks := 0
		for _, outcome := range []w184Outcome{violator, compliant} {
			if outcome.deadlocked {
				deadlocks++
				t.Logf("w184 修复前形态第 %d 轮捕获 40P01 原文: %v", round, outcome.err)
			}
		}
		switch {
		case deadlocks >= 1:
			reproduced = true
			// 环的另一端（幸存方）应在受害者回滚后正常提交。
			if violator.err != nil && !violator.deadlocked {
				t.Fatalf("w184 修复前形态违规方非 40P01 失败: %v", violator.err)
			}
			if compliant.err != nil && !compliant.deadlocked {
				t.Fatalf("w184 修复前形态合规方非 40P01 失败: %v", compliant.err)
			}
		case violator.err == nil && compliant.err == nil:
			t.Logf("w184 修复前形态第 %d 轮未成环（时序未交错），重试", round)
		default:
			t.Fatalf("w184 修复前形态第 %d 轮出现非预期错误: violator=%v compliant=%v", round, violator.err, compliant.err)
		}
	}
	if !reproduced {
		t.Fatalf("w184: 修复前形态在 5 轮内未复现 SQLSTATE 40P01，死锁复现门禁失效")
	}

	// 阶段二：修复后形态（双方首语句都取 7001001）——advisory 全序串行化，
	// 两轮并发全部提交成功且无 40P01。
	for round := 1; round <= 2; round++ {
		w184ResetRound(t, db)
		ctx, cancel := context.WithTimeout(context.Background(), w184RoundTimeout)
		violator, compliant := w184RunViolatorVsCompliant(ctx, db, false)
		cancel()
		for _, outcome := range []w184Outcome{violator, compliant} {
			if outcome.deadlocked {
				t.Fatalf("w184 修复后形态第 %d 轮仍出现 40P01: %v", round, outcome.err)
			}
			if outcome.err != nil {
				t.Fatalf("w184 修复后形态第 %d 轮提交失败: %v", round, outcome.err)
			}
		}
	}
	// 触发器路径仍正常写 dirty（串行化没有吞掉隐式脏标记）。
	var dirtyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_list_availability_dirty WHERE account_id = $1`, w184AccountID).Scan(&dirtyCount); err != nil {
		t.Fatalf("w184 验证 dirty 行失败: %v", err)
	}
	if dirtyCount < 1 {
		t.Fatalf("w184 修复后形态应留下账户 %s 的 dirty 行", w184AccountID)
	}
}

// w184StatementIndex 返回录制流中第一条包含 match 的语句下标（-1 未找到）。
func w184StatementIndex(statements []wfStatement, match string) int {
	for index, statement := range statements {
		if strings.Contains(statement.query, match) {
			return index
		}
	}
	return -1
}

// TestW184ResultProjectorTxsTakeDirtyLockFirst 用录制驱动锁定生产代码契约：
// PG 方言下凡是将要 UPDATE proxy_profiles 的投影事务（projectStored /
// ProjectManualNoTargets / ProjectManualOutbound），advisory 7001001 必须先于
// receipt/FOR UPDATE 行语句与 CAS UPDATE 出现；SQLite 方言不产生 advisory。
func TestW184ResultProjectorTxsTakeDirtyLockFirst(t *testing.T) {
	const advisoryQuery = "SELECT pg_advisory_xact_lock($1)"
	const fenceQuery = "FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE"
	ctx := context.Background()

	// projectStored（Drain/ProjectOutcome 共用的投影事务形状）。
	func(t *testing.T) {
		rec := newWFRecorder()
		db := wfOpenRecorderDB(t, rec)
		projector := wfNewPGProjector(db)
		observedAt := wfProjBase.Add(2 * time.Second)
		outcome := Outcome{
			OutcomeID: "outcome-w184-1", RequestID: "request-w184-1", ProxyID: "p-w184",
			ObservedAt: observedAt, InputVersion: 1, ConfigRevision: wfProjRevision, Trigger: TriggerPeriodic,
			OwnerFenceToken: 1, ProxyFenceToken: 1, OverallStatus: OverallPassed,
			Items: []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 12, Outcome: OutcomeSuccess}},
		}
		rec.script("FROM juhe_business.proxy_latency_projection_receipts WHERE outcome_id=$1 FOR UPDATE", nil, nil)
		rec.script(fenceQuery, []string{"id", "updated_at", "last_tested_at"}, [][]driver.Value{{outcome.ProxyID, wfProjRevision, nil}})
		rec.scriptExec("UPDATE juhe_business.proxy_profiles SET test_status=$1", 1)
		rec.scriptExec("INSERT INTO juhe_business.proxy_latency_projection_receipts", 1)
		result, err := projector.projectStored(ctx, StoredOutcome{StoredAt: wfProjBase, Outcome: outcome}, false)
		if err != nil || result.Disposition != ProjectionApplied {
			t.Fatalf("录制投影结果=%+v err=%v", result, err)
		}
		statements := rec.all()
		advisory := w184StatementIndex(statements, advisoryQuery)
		if advisory != 0 {
			t.Fatalf("projectStored 事务首语句必须是 advisory 7001001，实际下标=%d 语句=%q", advisory, statements[0].query)
		}
		if len(statements[0].args) != 1 || statements[0].args[0] != int64(7001001) {
			t.Fatalf("advisory 参数必须是 7001001，实际: %v", statements[0].args)
		}
		for _, required := range []string{fenceQuery, "UPDATE juhe_business.proxy_profiles SET test_status=$1"} {
			if index := w184StatementIndex(statements, required); index < 0 || index < advisory {
				t.Fatalf("projectStored 中 %q 必须在 advisory 之后，下标=%d advisory=%d", required, index, advisory)
			}
		}
	}(t)

	// ProjectManualNoTargets。
	func(t *testing.T) {
		rec := newWFRecorder()
		db := wfOpenRecorderDB(t, rec)
		projector := wfNewPGProjector(db)
		rec.script(fenceQuery, []string{"id", "updated_at", "last_tested_at"}, [][]driver.Value{{"p-w184", wfProjRevision, nil}})
		rec.scriptExec("UPDATE juhe_business.proxy_profiles SET test_status=$1", 1)
		request := ManualRequest{SchemaVersion: 1, ProxyID: "p-w184", ProxyName: "w184", ConfigRevision: wfProjRevision, ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080}
		result, err := projector.ProjectManualNoTargets(ctx, request, wfProjBase)
		if err != nil || result.Disposition != ProjectionApplied {
			t.Fatalf("录制 no-targets 结果=%+v err=%v", result, err)
		}
		statements := rec.all()
		if advisory := w184StatementIndex(statements, advisoryQuery); advisory != 0 {
			t.Fatalf("ProjectManualNoTargets 事务首语句必须是 advisory 7001001，实际下标=%d", advisory)
		}
		if fence := w184StatementIndex(statements, fenceQuery); fence != 1 {
			t.Fatalf("ProjectManualNoTargets 的 FOR UPDATE 必须紧跟 advisory 之后，下标=%d", fence)
		}
	}(t)

	// ProjectManualOutbound（CAS 快路径也必须持锁）。
	func(t *testing.T) {
		rec := newWFRecorder()
		db := wfOpenRecorderDB(t, rec)
		projector := wfNewPGProjector(db)
		rec.scriptExec("SET outbound_ip=$1,outbound_region=$2", 1)
		outcome := Outcome{ProxyID: "p-w184", ConfigRevision: wfProjRevision, ObservedAt: wfProjBase}
		if err := projector.ProjectManualOutbound(ctx, outcome, "1.2.3.4", "JP"); err != nil {
			t.Fatalf("录制 outbound 失败: %v", err)
		}
		statements := rec.all()
		if advisory := w184StatementIndex(statements, advisoryQuery); advisory != 0 {
			t.Fatalf("ProjectManualOutbound 事务首语句必须是 advisory 7001001，实际下标=%d", advisory)
		}
		if cas := w184StatementIndex(statements, "SET outbound_ip=$1,outbound_region=$2"); cas != 1 {
			t.Fatalf("outbound CAS 必须紧跟 advisory 之后，下标=%d", cas)
		}
	}(t)

	// SQLite 方言 no-op：不产生任何 pg_advisory 语句。
	func(t *testing.T) {
		rec := newWFRecorder()
		db := wfOpenRecorderDB(t, rec)
		projector := &ResultProjector{business: db, mode: StoreSQLite, cfg: ResultProjectorConfig{Now: func() time.Time { return wfProjBase }}}
		rec.scriptExec("SET outbound_ip=?,outbound_region=?", 1)
		outcome := Outcome{ProxyID: "p-w184", ConfigRevision: wfProjRevision, ObservedAt: wfProjBase}
		if err := projector.ProjectManualOutbound(ctx, outcome, "1.2.3.4", "JP"); err != nil {
			t.Fatalf("SQLite outbound 失败: %v", err)
		}
		for _, statement := range rec.all() {
			if strings.Contains(statement.query, "pg_advisory") {
				t.Fatalf("SQLite 方言不应执行 advisory 语句: %q", statement.query)
			}
		}
	}(t)
}
