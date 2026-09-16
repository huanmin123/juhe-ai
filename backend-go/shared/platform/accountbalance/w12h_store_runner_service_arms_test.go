package accountbalance

// w12h 补充 arms：Store 打开/收口/触发器矩阵、Runner 直调臂、Service 桥接臂。
// 本文件补充登记的不可达/无法本机确定性触发语句：
//   - SQLite tx.Commit 错误分支（store.go 483/631/673/690/199 等）：驱动对合法
//     事务不产生提交错误；
//   - store.go checkPostgresSchema / EnsureSchema PostgreSQL 路径的错误分支与
//     ensureBalancePGSchema 查询错误：共享 w1cover 库不允许破坏 juhe_jobs 契约，
//     information_schema/rows.Err/扫描错误无注入点；
//   - store.go existingOutcomeMatchesTx 非空扫描错误（715）：ErrNoRows 已由
//     同 outcome_id 不同 request_id 场景覆盖，其余错误无驱动注入点；
//   - store.go writeSnapshotTx snapshot json.Marshal 错误（779）：Snapshot 字段
//     均为可序列化类型；
//   - runner.go db worker 的 ctx.Done 入队分支（287-289）：需要 ctx 在
//     prepareInput 成功与入队 select 之间的窗口内取消；
//   - service.go runCycle 定时循环与 Load*/Run* 错误分支（170/188/195/201/213）：
//     依赖真实 PG 业务库契约（只读事务 + ::timestamptz），SQLite 池无法承载。

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- Store 打开/初始化臂 ----

func TestW12HOpenStoreArms(t *testing.T) {
	// SQLite 路径指向目录：PRAGMA 执行失败。
	if _, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir()}); err == nil {
		t.Fatal("目录路径必须失败")
	}
	// pgx 的 DSN 解析在 sql.Open 中惰性执行，openErr 分支本机无法触发
	// （登记不可达）。
	_ = StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://u:p@127.0.0.1:5432/db"}
	// nil store / nil db 防御。
	var nilStore *Store
	if err := nilStore.EnsureSchema(context.Background()); err == nil {
		t.Fatal("nil store 必须失败")
	}
	if err := nilStore.CheckSchema(context.Background()); err == nil {
		t.Fatal("nil store CheckSchema 必须失败")
	}
	if err := (&Store{}).CheckSchema(context.Background()); err == nil {
		t.Fatal("nil db CheckSchema 必须失败")
	}
}

// outcomes 换成视图：schema 内针对 outcomes 的索引语句失败
// （CREATE TABLE IF NOT EXISTS 对同名视图静默通过，但 CREATE INDEX 会失败）。
func TestW12HEnsureSchemaOutcomeViewBlocked(t *testing.T) {
	path := t.TempDir() + `\w12h-outcome-view.sqlite`
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE VIEW account_balance_outcomes AS SELECT 'x' AS outcome_id`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.EnsureSchema(context.Background()); err == nil {
		t.Fatal("outcomes 视图必须让初始化失败")
	}
}

// 外部连接持有写锁：EnsureSchema 的 BEGIN IMMEDIATE 超时失败。
func TestW12HEnsureSchemaLockBlocked(t *testing.T) {
	path := t.TempDir() + `\w12h-locked.sqlite`
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	rawLock, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawLock.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = rawLock.ExecContext(context.Background(), "ROLLBACK"); _ = rawLock.Close() }()
	if err := store.EnsureSchema(context.Background()); err == nil {
		t.Fatal("写锁必须让初始化失败")
	}
}

// 关闭句柄矩阵：每个入口都必须失败而非静默。
func TestW12HStoreClosedHandleMatrix(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	ctx := context.Background()
	owner := w7cOwner(t, store, "w12h-closed-owner")
	account := w7cAccount(t, store, owner, "w12h-closed-acct")
	input := w7cValidBalanceInput("w12h-closed-acct")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadSnapshot(ctx, "w12h-closed-acct"); err == nil {
		t.Fatal("关闭后 LoadSnapshot 必须失败")
	}
	if _, _, err := store.LoadOutcome(ctx, "w12h-outcome"); err == nil {
		t.Fatal("关闭后 LoadOutcome 必须失败")
	}
	if _, _, err := store.AcquireOwnerLease(ctx, "w12h-other", time.Minute); err == nil {
		t.Fatal("关闭后 AcquireOwnerLease 必须失败")
	}
	if _, _, err := store.AcquireAccountLease(ctx, owner, "w12h-closed-acct-2", time.Minute); err == nil {
		t.Fatal("关闭后 AcquireAccountLease 必须失败")
	}
	if _, err := store.WriteSnapshotCAS(ctx, owner, account, SnapshotMutation{Input: input, Snapshot: Snapshot{Status: StatusFresh}}); err == nil {
		t.Fatal("关闭后 WriteSnapshotCAS 必须失败")
	}
	if _, err := store.AppendOutcome(ctx, owner, account, Outcome{OutcomeID: "w12h-o", RequestID: "w12h-r", AccountID: account.AccountID, InputVersion: 1, ConfigRevision: 1, ObservedAt: time.Now(), Snapshot: Snapshot{Status: StatusFresh}}); err == nil {
		t.Fatal("关闭后 AppendOutcome 必须失败")
	}
	if _, _, err := store.AcquireAccountLease(ctx, owner, "w12h-closed-acct-3", time.Minute); err == nil {
		t.Fatal("关闭后 AcquireAccountLease 必须失败")
	}
	if err := store.ReleaseAccountLease(ctx, owner, account); err == nil {
		t.Fatal("关闭后 ReleaseAccountLease 必须失败")
	}
}

// 触发器与坏类型矩阵：租约验证、CAS、幂等回读、payload 类型。
func TestW12HStoreTriggerAndTypeMatrix(t *testing.T) {
	ctx := context.Background()

	t.Run("owner upsert 写中止", func(t *testing.T) {
		store := w7cNewSQLiteStore(t)
		if _, _, err := store.AcquireOwnerLease(ctx, "w12h-t1", time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`UPDATE account_balance_owner_leases SET lease_until='2020-01-01T00:00:00.000000000Z'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`CREATE TRIGGER w12h_no_owner_upd BEFORE UPDATE ON account_balance_owner_leases BEGIN SELECT RAISE(ABORT, 'w12h owner abort'); END`); err != nil {
			t.Fatal(err)
		}
		// 已有行 → upsert 走 DO UPDATE → 触发器中止。
		if _, _, err := store.AcquireOwnerLease(ctx, "w12h-t1", time.Minute); err == nil {
			t.Fatal("owner upsert 中止必须失败")
		}
	})

	t.Run("account claim 写中止与续约写中止", func(t *testing.T) {
		store := w7cNewSQLiteStore(t)
		owner := w7cOwner(t, store, "w12h-t2-owner")
		w7cAccount(t, store, owner, "w12h-t2")
		if _, err := store.db.Exec(`UPDATE account_balance_account_leases SET lease_until='2020-01-01T00:00:00.000000000Z' WHERE account_id='w12h-t2'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`CREATE TRIGGER w12h_no_acct_upd BEFORE UPDATE ON account_balance_account_leases BEGIN SELECT RAISE(ABORT, 'w12h acct abort'); END`); err != nil {
			t.Fatal(err)
		}
		// 过期行 → upsert 走 DO UPDATE → 触发器中止。
		if _, _, err := store.AcquireAccountLease(ctx, owner, "w12h-t2", time.Minute); err == nil {
			t.Fatal("claim 中止必须失败")
		}
		// 新账户走 INSERT 不触发；随后用存活租约触发续约 UPDATE 中止。
		live := w7cAccount(t, store, owner, "w12h-t3")
		if _, err := store.RenewAccountLease(ctx, owner, live, time.Minute); err == nil {
			t.Fatal("续约中止必须失败")
		}
	})

	t.Run("owner 租约缺失与坏 fence 类型", func(t *testing.T) {
		store := w7cNewSQLiteStore(t)
		owner := w7cOwner(t, store, "w12h-t4-owner")
		if _, err := store.db.Exec(`DELETE FROM account_balance_owner_leases`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.AcquireAccountLease(ctx, owner, "w12h-t4", time.Minute); !errors.Is(err, ErrOwnerLeaseLost) {
			t.Fatalf("owner 行缺失必须报租约丢失: %v", err)
		}
		owner2 := w7cOwner(t, store, "w12h-t4b-owner")
		if _, err := store.db.Exec(`UPDATE account_balance_owner_leases SET fence_token='garbage'`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.AcquireAccountLease(ctx, owner2, "w12h-t4b", time.Minute); err == nil {
			t.Fatal("坏 fence 类型必须失败")
		}
	})

	t.Run("snapshot 坏类型与 NULL payload", func(t *testing.T) {
		store := w7cNewSQLiteStore(t)
		owner := w7cOwner(t, store, "w12h-t5-owner")
		account := w7cAccount(t, store, owner, "w12h-t5")
		input := w7cValidBalanceInput("w12h-t5")
		now2 := time.Now()
		ok, err := store.WriteSnapshotCAS(ctx, owner, account, SnapshotMutation{Input: input, Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "1.00"}, NextRefreshAt: cloneTime(&now2)})
		if err != nil || !ok {
			t.Fatalf("写入快照失败: %v %t", err, ok)
		}
		if _, err := store.db.Exec(`UPDATE account_balance_snapshots SET input_version='garbage'`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.LoadSnapshot(ctx, "w12h-t5"); err == nil {
			t.Fatal("坏 input_version 必须失败")
		}
		if _, err := store.db.Exec(`UPDATE account_balance_snapshots SET input_version=1, snapshot_json=123`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.LoadSnapshot(ctx, "w12h-t5"); err == nil {
			t.Fatal("整数 snapshot 必须失败")
		}
	})

	t.Run("outcome 幂等回读与 stale", func(t *testing.T) {
		store := w7cNewSQLiteStore(t)
		owner := w7cOwner(t, store, "w12h-t6-owner")
		account := w7cAccount(t, store, owner, "w12h-t6")
		now := time.Now().UTC()
		base := Outcome{OutcomeID: "w12h-dup", RequestID: "w12h-dup-r1", AccountID: "w12h-t6", InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now, Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "2.00"}, NextRefreshAt: &now}
		inserted, err := store.AppendOutcome(ctx, owner, account, base)
		if err != nil || !inserted {
			t.Fatalf("首次 outcome 必须写入: %v %t", err, inserted)
		}
		// 同 outcome_id 不同 request_id：UNIQUE(outcome_id) 冲突原样传播。
		dup := base
		dup.RequestID = "w12h-dup-r2"
		if _, err = store.AppendOutcome(ctx, owner, account, dup); err == nil {
			t.Fatal("重复 outcome_id 必须失败")
		}
		// payload 存成整数：default 分支 json.Marshal 兜底。
		if _, err := store.db.Exec(`INSERT INTO account_balance_outcomes (outcome_id, request_id, account_id, input_version, config_revision, trigger, observed_at, payload, committed) VALUES ('w12h-int', 'w12h-int-r', 'w12h-t6', 1, 1, 'periodic', ?, 123, 0)`, now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		// 整数 payload 走 default 分支 json.Marshal 后仍是非法 Outcome 形状。
		if _, _, err := store.LoadOutcome(ctx, "w12h-int"); err == nil {
			t.Fatal("整数 payload 必须在解析阶段失败")
		}
	})

	t.Run("快照 INSERT/UPDATE 写中止", func(t *testing.T) {
		store := w7cNewSQLiteStore(t)
		owner := w7cOwner(t, store, "w12h-t7-owner")
		input := w7cValidBalanceInput("w12h-t7")
		// 新账户 → INSERT 路径。
		if _, err := store.db.Exec(`CREATE TRIGGER w12h_no_snap_ins BEFORE INSERT ON account_balance_snapshots BEGIN SELECT RAISE(ABORT, 'w12h snap ins abort'); END`); err != nil {
			t.Fatal(err)
		}
		account := w7cAccount(t, store, owner, "w12h-t7")
		if _, err := store.AppendOutcome(ctx, owner, account, Outcome{OutcomeID: "w12h-t7-o", RequestID: "w12h-t7-r", AccountID: "w12h-t7", InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: time.Now().UTC(), Snapshot: Snapshot{Status: StatusFresh}}); err == nil {
			t.Fatal("快照插入中止必须失败")
		}
		_ = input
	})

	t.Run("CAS 期望输入但快照缺失", func(t *testing.T) {
		store := w7cNewSQLiteStore(t)
		owner := w7cOwner(t, store, "w12h-t8-owner")
		account := w7cAccount(t, store, owner, "w12h-t8-missing")
		input := w7cValidBalanceInput("w12h-t8-missing")
		ok, err := store.WriteSnapshotCAS(ctx, owner, account, SnapshotMutation{Input: input, Snapshot: Snapshot{Status: StatusFresh}, ExpectedInput: 3})
		if err != nil || ok {
			t.Fatalf("缺失快照+期望版本必须拒绝: %v %t", err, ok)
		}
	})

	t.Run("markOutcomeCommitted 写中止", func(t *testing.T) {
		store := w7cNewSQLiteStore(t)
		owner := w7cOwner(t, store, "w12h-t9-owner")
		if _, err := store.db.Exec(`CREATE TRIGGER w12h_no_outcome_upd BEFORE UPDATE ON account_balance_outcomes BEGIN SELECT RAISE(ABORT, 'w12h outcome abort'); END`); err != nil {
			t.Fatal(err)
		}
		account := w7cAccount(t, store, owner, "w12h-t9")
		now2 := time.Now().Add(time.Minute)
		if _, err := store.AppendOutcome(ctx, owner, account, Outcome{OutcomeID: "w12h-t9-o", RequestID: "w12h-t9-r", AccountID: "w12h-t9", InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: time.Now().UTC(), Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "3.00"}, NextRefreshAt: cloneTime(&now2)}); err == nil {
			t.Fatal("committed 标记中止必须失败")
		}
	})
}

// ---- Runner 直调臂 ----

func w12hNewRunner(t *testing.T, store *Store, ownerID string, doer HTTPDoer) *Runner {
	t.Helper()
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: ownerID, CredentialSecret: "w12h-runner-secret", MaxConcurrent: 2, IOConcurrency: 1, DBConcurrency: 1, DBQueueSize: 2, HTTPClient: doer})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestW12HRunnerPrepareAndPersistArms(t *testing.T) {
	ctx := context.Background()
	store := w7cNewSQLiteStore(t)
	runner := w12hNewRunner(t, store, "w12h-runner", nil)
	owner := w7cOwner(t, store, "w12h-runner-owner")

	// ctx 已取消 → prepareInput 跳过。
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	input := w7cValidBalanceInput("w12h-rp-1")
	state, _, _, err := runner.prepareInput(canceled, owner, input)
	if err == nil || state != runStateSkipped {
		t.Fatalf("取消 ctx 必须跳过: %v %v", state, err)
	}

	// 账户租约被他主持有 → 静默跳过。
	_, _, err = store.AcquireAccountLease(ctx, owner, "w12h-rp-2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	held := w7cValidBalanceInput("w12h-rp-2")
	state, _, _, err = runner.prepareInput(ctx, owner, held)
	if err != nil || state != runStateSkipped {
		t.Fatalf("租约被持有必须跳过: %v %v", state, err)
	}

	// 快照表缺失 → persistInput 读取失败。
	broken := w7cNewSQLiteStore(t)
	brokenRunner := w12hNewRunner(t, broken, "w12h-runner-2", nil)
	brokenOwner := w7cOwner(t, broken, "w12h-runner-2-owner")
	brokenAccount := w7cAccount(t, broken, brokenOwner, "w12h-rp-3")
	if _, err := broken.db.Exec(`DROP TABLE account_balance_snapshots`); err != nil {
		t.Fatal(err)
	}
	persistInput := w7cValidBalanceInput("w12h-rp-3")
	persistInput.ExpiresAt = time.Now().Add(time.Hour)
	if _, err := brokenRunner.persistInput(ctx, brokenOwner, persistInput, brokenAccount, QueryResult{Snapshot: Snapshot{Status: StatusFresh}}); err == nil {
		t.Fatal("快照表缺失必须失败")
	}

	// interval 缺省 + 正常落库。
	okInput := w7cValidBalanceInput("w12h-rp-4")
	okInput.Config.IntervalMinutes = 0
	okInput.ExpiresAt = time.Now().Add(time.Hour)
	account := w7cAccount(t, store, owner, "w12h-rp-4")
	state, err = runner.persistInput(ctx, owner, okInput, account, QueryResult{Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "5.00"}, Adapter: AdapterOpenAIBilling})
	if err != nil || state != runStateExecuted {
		t.Fatalf("interval 缺省必须正常写入: %v %v", state, err)
	}

	// outcome 插入中止 → persistInput 传播。
	if _, err := store.db.Exec(`CREATE TRIGGER w12h_no_outcome_ins BEFORE INSERT ON account_balance_outcomes BEGIN SELECT RAISE(ABORT, 'w12h outcome ins abort'); END`); err != nil {
		t.Fatal(err)
	}
	expiredInput := w7cValidBalanceInput("w12h-rp-4")
	expiredInput.ExpiresAt = time.Now().Add(-time.Minute)
	if _, err := runner.persistInput(ctx, owner, expiredInput, account, QueryResult{Snapshot: Snapshot{Status: StatusFresh}}); err == nil {
		t.Fatal("过期输入必须拒绝")
	}
	freshInput := w7cValidBalanceInput("w12h-rp-5")
	freshInput.ExpiresAt = time.Now().Add(time.Hour)
	account5 := w7cAccount(t, store, owner, "w12h-rp-5")
	if _, err := runner.persistInput(ctx, owner, freshInput, account5, QueryResult{Snapshot: Snapshot{Status: StatusFresh}}); err == nil {
		t.Fatal("outcome 插入中止必须失败")
	}
}

// 候选资格检查的 keyCount 回退分支。
func TestW12HCandidateEligibleKeyCountFallback(t *testing.T) {
	candidate := Candidate{AccountID: "w12h-cand", Type: "api_key", Status: "active", Schedulable: true, BalanceEnabled: true, APIKeyCount: 1}
	if err := candidateEligible(candidate, TriggerPeriodic); err != nil {
		t.Fatalf("单 key 候选必须合格: %v", err)
	}
	withCipher := candidate
	withCipher.APIKeyCount = 0
	withCipher.APIKey = CredentialEnvelope{Kind: "api_key", Ciphertext: "cipher"}
	if err := candidateEligible(withCipher, TriggerPeriodic); err != nil {
		t.Fatalf("密文回退 keyCount 必须合格: %v", err)
	}
	if err := candidateEligible(withCipher, TriggerFirstProbe); err == nil {
		t.Fatal("BalanceEnabled 候选不得进入首探")
	}
}

// 周期路径把 runInputs 错误映射到候选报告。
func TestW12HRunCandidatesErrorPropagation(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	runner := w12hNewRunner(t, store, "w12h-prop-owner", nil)
	candidate := Candidate{AccountID: "w12h-prop", SystemAccountID: "sys", InputVersion: 1, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, BalanceEnabled: true,
		BaseURL: "https://w12h-unreachable.invalid", APIKeyCount: 1,
		APIKey: CredentialEnvelope{Kind: "api_key", Ciphertext: "cipher"}, IssuedAt: time.Now().UTC()}
	report, err := runner.Run(context.Background(), TriggerPeriodic, []Candidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) == 0 {
		t.Fatal("上游不可达必须记录候选错误")
	}
}

// w12hBlockingDoer 在请求后阻塞直到 ctx 结束，用于取消竞态检查点。
type w12hBlockingDoer struct {
	once    sync.Once
	started chan struct{}
}

func (d *w12hBlockingDoer) Do(request *http.Request) (*http.Response, error) {
	d.once.Do(func() { close(d.started) })
	<-request.Context().Done()
	return nil, request.Context().Err()
}

// 喂入循环在 ctx 取消后立即收口并返回部分结果（取消窗口分支）。
func TestW12HRunInputsCancelDuringFeed(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	doer := &w12hBlockingDoer{started: make(chan struct{})}
	runner := w12hNewRunner(t, store, "w12h-cancel-owner", doer)
	credential, credErr := NewCredentialEnvelope("w12h-runner-secret", "api_key", map[string]string{"api_key": "sk-w12h-cancel"})
	if credErr != nil {
		t.Fatal(credErr)
	}
	inputs := make([]Input, 0, 120)
	for index := 0; index < 120; index++ {
		input := w7cValidBalanceInput("w12h-cancel-" + strings.Repeat("a", index+1))
		input.APIKey = credential
		inputs = append(inputs, input)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-doer.started
		cancel()
	}()
	_, err := runner.runInputs(ctx, TriggerPeriodic, inputs)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("喂入阶段取消必须返回 ctx 错误: %v", err)
	}
}

// ---- Service 桥接臂 ----

type w12hFakePool struct{ db *sql.DB }

func (p w12hFakePool) DB() *sql.DB  { return p.db }
func (p w12hFakePool) Close() error { return p.db.Close() }

func w12hServiceConfig(t *testing.T, pool PoolHandle, secret string) RuntimeConfig {
	t.Helper()
	return RuntimeConfig{
		Enabled:             true,
		Store:               StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "\\w12h-service.sqlite"},
		InputPostgresPool:   pool,
		CredentialSecret:    secret,
		BusinessPostgresURL: "postgres://ignored",
		InputTTL:            time.Minute,
		OwnerID:             "w12h-service",
	}
}

func TestW12HServiceConstructionArms(t *testing.T) {
	// 空密钥 → reader 构造失败。
	raw, err := sql.Open("sqlite", "file:"+t.TempDir()+"\\w12h-svc-pool.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := NewService(w12hServiceConfig(t, w12hFakePool{db: raw}, "   "), nil); err == nil {
		t.Fatal("空密钥必须失败")
	}
	// 好密钥 + 空业务库 → CheckContract 失败。
	cfg := w12hServiceConfig(t, w12hFakePool{db: raw}, "w12h-service-secret")
	if _, err := NewService(cfg, nil); err == nil {
		t.Fatal("业务契约缺失必须失败")
	}
}

func w12hNewBridgeService(t *testing.T, ownerID string) *Service {
	t.Helper()
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "\\w12h-bridge.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: ownerID, CredentialSecret: "w12h-bridge-secret", MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	return &Service{store: store, runner: runner, logger: errLogger()}
}

func errLogger() *slog.Logger { return slog.Default() }

func TestW12HServiceRunManualArms(t *testing.T) {
	ctx := context.Background()

	// runner.RunManual 错误：ctx 已取消。
	service := w12hNewBridgeService(t, "w12h-manual-owner")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := service.RunManual(canceled, w7cValidBalanceInput("w12h-manual-a")); err == nil {
		t.Fatal("取消 ctx 必须失败")
	}

	// 输入校验错误进入 report.Errors。
	bad := w7cValidBalanceInput("   ")
	if _, _, err := service.RunManual(ctx, bad); err == nil {
		t.Fatal("非法输入必须报错")
	}

	// 快照 stale 映射 ErrOutcomeStale。
	staleStore, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "\\w12h-svc-stale.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = staleStore.Close() }()
	if err := staleStore.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	staleRunner, err := NewRunner(RunnerConfig{Store: staleStore, OwnerID: "w12h-stale-owner", CredentialSecret: "w12h-bridge-secret", MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	staleService := &Service{store: staleStore, runner: staleRunner, logger: errLogger()}
	now := time.Now().UTC()
	base := w7cValidBalanceInput("w12h-manual-stale")
	bridgeCredential, credErr := NewCredentialEnvelope("w12h-bridge-secret", "api_key", map[string]string{"api_key": "sk-w12h-bridge"})
	if credErr != nil {
		t.Fatal(credErr)
	}
	base.APIKey = bridgeCredential
	base.Trigger = TriggerManual
	base.IssuedAt = now
	base.ExpiresAt = now.Add(time.Minute)
	owner := w7cOwner(t, staleStore, "w12h-stale-seeder")
	account := w7cAccount(t, staleStore, owner, base.AccountID)
	if _, err := staleStore.AppendOutcome(ctx, owner, account, Outcome{OutcomeID: "w12h-seed-o", RequestID: "w12h-seed-r", AccountID: base.AccountID, InputVersion: 9, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now, Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "9.99"}, NextRefreshAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := staleStore.ReleaseAccountLease(ctx, owner, account); err != nil {
		t.Fatal(err)
	}
	if err := staleStore.ReleaseOwnerLease(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := staleService.RunManual(ctx, base); !errors.Is(err, ErrOutcomeStale) {
		t.Fatalf("stale 手动刷新必须报 stale: %v", err)
	}

	// 账户租约被持有 → ErrAccountLeaseHeld。
	heldStore := w7cNewSQLiteStore(t)
	heldRunner := w12hNewRunner(t, heldStore, "w12h-held-manual", nil)
	heldService := &Service{store: heldStore, runner: heldRunner, logger: errLogger()}
	other := w7cOwner(t, heldStore, "w12h-held-other")
	_, acquired, err := heldStore.AcquireAccountLease(ctx, other, "w12h-manual-held", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("预占租约失败: %v %t", err, acquired)
	}
	heldInput := w7cValidBalanceInput("w12h-manual-held")
	heldInput.ExpiresAt = now.Add(time.Minute)
	if _, _, err := heldService.RunManual(ctx, heldInput); !errors.Is(err, ErrAccountLeaseHeld) {
		t.Fatalf("租约被持有必须报 409 语义: %v", err)
	}
}

func TestW12HServiceCloseArms(t *testing.T) {
	raw, err := sql.Open("sqlite", "file:"+t.TempDir()+"\\w12h-svc-close.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "\\w12h-svc-close-store.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{store: store, inputDB: raw, logger: errLogger()}
	if err := service.Close(); err != nil {
		t.Fatalf("无 pool 句柄的 Close 必须走 inputDB: %v", err)
	}
}
