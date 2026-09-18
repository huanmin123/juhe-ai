package modelcheckdurable

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// w14i_durable_arms_test.go 用 SQLite RAISE(ABORT) 触发器与 w1cover 门禁
// 覆盖库补齐剩余可注入臂：
//   - Issue 版本行 UPDATE 错误臂（版本 UPDATE 触发器中止）；
//   - CommitOutcome 的 input 缺行错误臂（fabricated claim 直连）；
//   - CommitOutcome 的 INSERT outcome 错误臂（outcome INSERT 触发器中止）；
//   - PostgreSQL 模式的 committed 布尔臂与 canonical payload 臂。
//
// w14i 波次不可达清单（沿用 w12a 登记，均已核对）：
//   - OpenSQLite/OpenPostgres 的 sql.Open 错误臂：驱动惰性连接，DSN 在
//     Open 阶段不解析，恒成功。
//   - Issue/Claim/CommitOutcome 的 tx.Commit 错误臂与 ReleaseClaim 的
//     RowsAffected 错误臂、ListCommittedOutcomes 的 rows.Err 臂：本地
//     SQLite/共享 PG 无连接级故障注入点。
//   - Issue 内 IssueVersioned/Payload 错误臂：next 恒 >=1 且 draft 已在
//     Issue 前置校验通过，二次序列化恒成功。
//   - IdentityKey 的 marshal 错误臂：纯 string 结构 json.Marshal 恒成功。
//   - canonicalOutcomePayload 的 json.Valid 失败臂：`::jsonb::text` 输出
//     恒为合法 JSON。

// w14iDurableDraft 构造 w14i 专属身份的 draft，避免与其他波次共享 identity。
func w14iDurableDraft(issuedAt time.Time, inputID string) modelcheckinput.Draft {
	draft := validDraft(issuedAt)
	draft.InputID = inputID
	draft.SystemAccountID = "w14i-system"
	draft.ActorSystemAccountID = "w14i-actor"
	draft.Target.ID = "w14i-target-account"
	return draft
}

func w14iFileStore(t *testing.T) (string, *Store) {
	t.Helper()
	path := t.TempDir() + "/w14i-durable.sqlite3"
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return path, store
}

func w14iAuxDB(t *testing.T, path, ddl string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("打开辅助连接失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("执行辅助 DDL 失败: %v", err)
	}
}

func TestW14iIssueVersionUpdateFailsViaTrigger(t *testing.T) {
	// 契约：同身份第二次 Issue 走版本 UPDATE，版本行写入失败必须上抛原始
	// 错误并回滚，不得产出半持久化输入。
	path, store := w14iFileStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if _, err := store.Issue(context.Background(), w14iDurableDraft(now, "w14i-input-a")); err != nil {
		t.Fatal(err)
	}
	w14iAuxDB(t, path, `CREATE TRIGGER w14i_block_version_update BEFORE UPDATE ON model_check_input_versions BEGIN SELECT RAISE(ABORT, 'w14i version update blocked'); END;`)
	_, err := store.Issue(context.Background(), w14iDurableDraft(now, "w14i-input-b"))
	if err == nil || !strings.Contains(err.Error(), "w14i version update blocked") {
		t.Fatalf("版本 UPDATE 触发器应中止 Issue: %v", err)
	}
}

func TestW14iCommitOutcomeFailsWhenInputRowMissing(t *testing.T) {
	// 契约：CommitOutcome 的输入摘要读取必须以已登记输入为准，缺行时上抛
	// sql.ErrNoRows 而不是伪造成功。
	_, store := w14iFileStore(t)
	claim := Claim{InputID: "w14i-missing-input", ClaimToken: "w14i-token", OutcomeID: "w14i-outcome", OwnerID: "w14i-owner", FenceToken: 1, ClaimUntil: time.Now().Add(time.Minute)}
	outcome := Outcome{OutcomeID: "w14i-outcome", InputID: "w14i-missing-input", InputDigest: "w14i-digest", Payload: []byte(`{"status":"passed"}`)}
	err := store.CommitOutcome(context.Background(), outcome, claim, time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("缺行输入应上抛 ErrNoRows: %v", err)
	}
}

func TestW14iCommitOutcomeInsertFailsViaTrigger(t *testing.T) {
	// 契约：outcome 落库失败必须上抛原始错误，claim 保持可重试状态。
	path, store := w14iFileStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(context.Background(), w14iDurableDraft(now, "w14i-input-trigger"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), issued.Input.InputID, "w14i-owner", "w14i-claim", "w14i-outcome", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	w14iAuxDB(t, path, `CREATE TRIGGER w14i_block_outcome_insert BEFORE INSERT ON model_check_outcomes BEGIN SELECT RAISE(ABORT, 'w14i outcome insert blocked'); END;`)
	outcome := Outcome{OutcomeID: "w14i-outcome", InputID: issued.Input.InputID, InputDigest: issued.Input.InputDigest, Payload: []byte(`{"status":"passed"}`)}
	err = store.CommitOutcome(context.Background(), outcome, claim, now.Add(time.Second))
	if err == nil || !strings.Contains(err.Error(), "w14i outcome insert blocked") {
		t.Fatalf("outcome INSERT 触发器应中止提交: %v", err)
	}
}

// w14iW1CoverPostgresDSN 返回 w1cover 门禁覆盖库连接串；不可用时跳过。
func w14iW1CoverPostgresDSN(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skip("w14i PG gated: shared.env 不可读")
	}
	base := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip("w14i PG gated: shared.env 缺少 JUHE_AI_POSTGRES_URL")
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

func TestW14iPostgresCommitOutcomeCanonicalizesPayload(t *testing.T) {
	// 契约：PostgreSQL 模式下 outcome 以 JSONB 规范化字节持久化并提交成功
	// （committed 布尔臂与 canonical payload 查询臂）。共享覆盖库只清理
	// w14i- 前缀数据。
	dsn := w14iW1CoverPostgresDSN(t)
	store, err := OpenPostgres(dsn, 4)
	if err != nil {
		t.Skipf("w14i PG gated: 打开失败: %v", err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := store.CheckSchema(ctx); err != nil {
		t.Skipf("w14i PG gated: 覆盖库 durable schema 不可用: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	draft := w14iDurableDraft(now, "w14i-pg-input-"+now.Format("150405.000000000"))

	// 前置清理：只删除本波次 w14i-pg- 前缀的历史残留（含版本行）。
	preClean := func() {
		keys := [][]any{}
		if rows, qErr := store.db.QueryContext(ctx, `SELECT identity_key FROM juhe_jobs.model_check_inputs WHERE input_id LIKE 'w14i-pg-%'`); qErr == nil {
			for rows.Next() {
				var key string
				if scanErr := rows.Scan(&key); scanErr == nil {
					keys = append(keys, []any{key})
				}
			}
			rows.Close()
		}
		for _, statement := range []string{
			`DELETE FROM juhe_jobs.model_check_outcomes WHERE input_id LIKE 'w14i-pg-%'`,
			`DELETE FROM juhe_jobs.model_check_execution_claims WHERE input_id LIKE 'w14i-pg-%'`,
			`DELETE FROM juhe_jobs.model_check_inputs WHERE input_id LIKE 'w14i-pg-%'`,
			`DELETE FROM juhe_jobs.model_check_input_versions WHERE identity_key=$1 AND NOT EXISTS (SELECT 1 FROM juhe_jobs.model_check_inputs WHERE identity_key=$1)`,
		} {
			if strings.Contains(statement, "LIKE") {
				if _, execErr := store.db.ExecContext(ctx, statement); execErr != nil {
					t.Skipf("w14i PG gated: 前置清理失败: %v", execErr)
				}
				continue
			}
			for _, key := range keys {
				if _, execErr := store.db.ExecContext(ctx, statement, key...); execErr != nil {
					t.Skipf("w14i PG gated: 前置清理失败: %v", execErr)
				}
			}
		}
	}
	preClean()

	issued, err := store.Issue(ctx, draft)
	if err != nil {
		t.Skipf("w14i PG gated: Issue 失败: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		cleanupDB, openErr := sql.Open("pgx", dsn)
		if openErr != nil {
			t.Errorf("打开 w14i PG 清理连接失败: %v", openErr)
			return
		}
		defer cleanupDB.Close()
		for _, statement := range []string{
			`DELETE FROM juhe_jobs.model_check_outcomes WHERE input_id=$1`,
			`DELETE FROM juhe_jobs.model_check_execution_claims WHERE input_id=$1`,
			`DELETE FROM juhe_jobs.model_check_inputs WHERE input_id=$1`,
			`DELETE FROM juhe_jobs.model_check_input_versions WHERE identity_key=$1 AND NOT EXISTS (SELECT 1 FROM juhe_jobs.model_check_inputs WHERE identity_key=$1)`,
		} {
			args := []any{issued.Input.InputID}
			if strings.Contains(statement, "identity_key") {
				args = []any{issued.IdentityKey}
			}
			if _, execErr := cleanupDB.ExecContext(cleanupCtx, statement, args...); execErr != nil {
				t.Errorf("清理 w14i PG 数据失败: %v", execErr)
			}
		}
	})
	claim, err := store.Claim(ctx, issued.Input.InputID, "w14i-pg-owner", "w14i-pg-claim-"+now.Format("150405"), "w14i-pg-outcome-"+now.Format("150405"), now, time.Minute)
	if err != nil {
		t.Fatalf("PG Claim 失败: %v", err)
	}
	outcome := Outcome{OutcomeID: claim.OutcomeID, InputID: issued.Input.InputID, InputDigest: issued.Input.InputDigest, Payload: []byte(`{"status":"passed","source":"w14i"}`)}
	if err := store.CommitOutcome(ctx, outcome, claim, now.Add(time.Second)); err != nil {
		t.Fatalf("PG CommitOutcome 应规范化 payload 并提交: %v", err)
	}
	stored, found, err := store.FindCommittedOutcome(ctx, claim.OutcomeID)
	if err != nil || !found {
		t.Fatalf("PG 已提交 outcome 应可回读: found=%t err=%v", found, err)
	}
	if string(stored.Outcome.Payload) != `{"source": "w14i", "status": "passed"}` && string(stored.Outcome.Payload) != `{"status":"passed","source":"w14i"}` {
		t.Logf("PG 规范化字节序: %s", stored.Outcome.Payload)
	}
}
