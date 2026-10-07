package main

// BUG-0162 第五刀（去跨进程战役）：两个执行端口的进程内化装配。
//
//   - ManualBalanceRefresher：手动余额刷新执行原为 jobs /account-balance/manual
//     HTTP 桥（Node account-balance-handover.ts runAccountBalanceManualViaGo），
//     第四刀删除桥后本端口无生产实现（路由恒 500 / draft 降级快照）。本文件在
//     gateway 进程内装配共享 backend-go-platform/accountbalance 执行核心
//     （owner lease + account lease + snapshot CAS 与 jobs 周期任务共用同一
//     juhe_jobs.account_balance_* 四表和 lease_key，互斥语义零改动）。
//   - ModelCatalogRefresher：模型目录刷新在全仓 Go 侧原本无上游 /models 拉取
//     实现（恒 400「获取上游模型目录失败」）。本文件经共享
//     backend-go-platform/upstreamcatalog 直连上游（Node
//     discoverAccountUpstreamModels 的进程内移植，多 Key 池取交集），与本地
//     provider_model_catalog 投影比对后返回
//     {addedModels, recommendedHealthCheckModel}（归档
//     account-model-catalog-refresh.service.ts:44-57 的结果形状）。
//
// 手动余额刷新在双方言装配（2026-10-04 起 SQLite 分支不再保持 nil 端口）：
// PG 模式经共享池使用 juhe_jobs.account_balance_* 租约四表；SQLite 模式经
// StoreSQLite 在独立 account-balance.sqlite3 自建同构四表（打开即
// WAL+busy_timeout+单写者，EnsureSchema 以 BEGIN IMMEDIATE 幂等建表补列），
// gateway 独占该文件、无跨进程争用；与周期探测 auto-detect-recovery
// （task-runs 租约 + 直写 stats）之间无四表级互斥——并发仅为幂等读，无数据
// 风险。store 打开/契约校验失败时按 nil 端口降级（路由保持既有 500/降级
// 快照契约）并登记告警。目录刷新无 store 依赖，与余额刷新共用同一装配门
// （双模都可用，见 wireInProcessBalanceAndCatalogRefresh）。SQLite 模式下
// committed 快照由本进程直写 stats 库 account_usage_snapshots kind=
// 'relay_balance'（列表 overlay 的最终一致通道；PG 模式由 jobs 的
// account-balance-stats-projection 周期投影承担，gateway 不直写）。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamcatalog"
)

// manualBalanceInputTTL 对齐归档 Node 桥的 20s 输入窗口
// （account-balance-handover.ts:228 deadline 20s；runner 探测上限 15s + CAS
// 写入余量）。
const manualBalanceInputTTL = 20 * time.Second

// sharedDBPool 把组合根共享业务库句柄适配成 accountbalance.PoolHandle。
// Close 是 no-op：连接池生命周期仍由 gateway pgpool registry 持有
// （composeSystemAPI Acquire 的 handle），store.Close 只做适配器收尾。
type sharedDBPool struct{ db *sql.DB }

func (p sharedDBPool) DB() *sql.DB  { return p.db }
func (p sharedDBPool) Close() error { return nil }

// gatewayManualBalanceRefresher 实现 accounts.ManualBalanceRefresher。
type gatewayManualBalanceRefresher struct {
	db     *sql.DB
	pg     bool
	secret string
	store  *accountbalance.Store
	runner *accountbalance.Runner
	// statsDB 是 SQLite 模式 committed 快照直写 stats 库的句柄（裸表名 +
	// `?` 占位符）；PG 模式保持 nil——PG 由 jobs 的
	// account-balance-stats-projection 周期投影，gateway 不直写。
	statsDB *sql.DB
	now     func() time.Time
}

// gatewayModelCatalogRefresher 实现 accounts.ModelCatalogRefresher。
type gatewayModelCatalogRefresher struct {
	db      *sql.DB
	pg      bool
	secret  string
	catalog *providers.Store
	now     func() time.Time
}

// wireInProcessBalanceAndCatalogRefresh 装配两个进程内执行端口（双方言）。
// 手动余额刷新的租约四表在 PG 模式要求由受控数据库流程预置（只读契约校验），
// 在 SQLite 模式由 StoreSQLite 打开独立文件并幂等自建；任一模式契约校验
// （CheckSchema）失败时按 nil 端口降级（路由保持既有 500/降级快照契约）并
// 登记告警，不阻塞组合根（对齐 wireInProcessAccountTestDispatch 的降级先例）；
// store 打开或 runner 创建失败则原样上抛使组合根启动失败（双方言同分级）。
func wireInProcessBalanceAndCatalogRefresh(composed *composition, cfg runtimeConfig, accountStore *accounts.Store, providerStore *providers.Store) error {
	// 模型目录刷新：纯进程内 HTTP + 本地目录投影读，SQLite/PG 双模可用。
	accountStore.SetModelCatalogRefresher(&gatewayModelCatalogRefresher{
		db:      composed.db,
		pg:      composed.pgDialect,
		secret:  cfg.Secret,
		catalog: providerStore,
		now:     time.Now,
	})

	// 手动余额刷新：SQLite 模式打开独立租约库（gateway 独占，EnsureSchema
	// 幂等建表）；路径缺失按既有 nil 端口降级语义处理（生产 loadRuntimeConfig
	// 恒派生该路径，此分支只覆盖手构配置的组合根测试与显式清空 env 的部署）。
	if !composed.pgDialect {
		if strings.TrimSpace(cfg.AccountBalanceDatabasePath) == "" {
			slog.Warn("SQLite 模式缺少余额租约库路径，进程内余额手动刷新不装配（路由保持 500 契约）",
				"event", "account_balance_manual_refresher_skipped_sqlite")
			return nil
		}
		store, err := accountbalance.OpenStore(accountbalance.StoreConfig{
			Mode:         accountbalance.StoreSQLite,
			DatabasePath: cfg.AccountBalanceDatabasePath,
		})
		if err != nil {
			return fmt.Errorf("open account-balance gateway store: %w", err)
		}
		// SQLite 模式 CheckSchema 即 EnsureSchema（幂等建表补列）；I/O 类失败
		// 按 nil 端口降级，与 PG 分支契约校验失败同门。
		if err := store.CheckSchema(context.Background()); err != nil {
			_ = store.Close()
			slog.Warn("SQLite 余额租约库初始化失败，进程内余额手动刷新不装配（路由保持 500 契约）",
				"event", "account_balance_manual_refresher_sqlite_store_failed", "error", err.Error())
			return nil
		}
		runner, err := accountbalance.NewRunner(accountbalance.RunnerConfig{
			Store:            store,
			OwnerID:          newGatewayBalanceOwnerID(),
			OwnerLeaseTTL:    time.Minute,
			AccountLeaseTTL:  30 * time.Second,
			CredentialSecret: cfg.Secret,
			ProbeTimeout:     15 * time.Second,
			Now:              time.Now,
		})
		if err != nil {
			_ = store.Close()
			return fmt.Errorf("create account-balance gateway runner: %w", err)
		}
		composed.shutdowns = append(composed.shutdowns, func() { _ = store.Close() })
		accountStore.SetManualBalanceRefresher(&gatewayManualBalanceRefresher{
			db:      composed.db,
			pg:      composed.pgDialect,
			secret:  cfg.Secret,
			store:   store,
			runner:  runner,
			statsDB: composed.statsDB,
			now:     time.Now,
		})
		return nil
	}
	store, err := accountbalance.OpenStore(accountbalance.StoreConfig{
		Mode:         accountbalance.StorePostgres,
		PostgresPool: sharedDBPool{db: composed.db},
	})
	if err != nil {
		return fmt.Errorf("open account-balance gateway store: %w", err)
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		// juhe_jobs 契约表缺失/角色不符：nil 端口降级（路由保持 Node 500
		// 契约），保留原始错误便于运维定位。
		slog.Warn("juhe_jobs 余额租约契约校验失败，进程内余额手动刷新不装配（路由保持 500 契约）",
			"event", "account_balance_manual_refresher_tables_missing", "error", err.Error())
		return nil
	}
	runner, err := accountbalance.NewRunner(accountbalance.RunnerConfig{
		Store:            store,
		OwnerID:          newGatewayBalanceOwnerID(),
		OwnerLeaseTTL:    time.Minute,
		AccountLeaseTTL:  30 * time.Second,
		CredentialSecret: cfg.Secret,
		ProbeTimeout:     15 * time.Second,
		Now:              time.Now,
	})
	if err != nil {
		return fmt.Errorf("create account-balance gateway runner: %w", err)
	}
	composed.shutdowns = append(composed.shutdowns, func() { _ = store.Close() })
	accountStore.SetManualBalanceRefresher(&gatewayManualBalanceRefresher{
		db:     composed.db,
		pg:     composed.pgDialect,
		secret: cfg.Secret,
		store:  store,
		runner: runner,
		now:    time.Now,
	})
	return nil
}

// newGatewayBalanceOwnerID 生成进程内唯一的 owner 身份（owner lease 行全局
// 单行，互斥由 lease_until 保证；ID 仅用于持有者标识与诊断）。
func newGatewayBalanceOwnerID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return "gateway-manual-" + hex.EncodeToString(buf)
}

// resolveProxyURLEnvelope 复用 J2 代理解析语义（socks5 → socks5h 远程 DNS，
// 对齐 jobs worker_balance_detect.go proxyEnvelope）：按 profile ID 读取
// proxy_profiles 行并封成 proxy_url envelope。profile 不存在或 proxyID 为空
// 返回 (nil, nil)（直连）。
func resolveProxyURLEnvelope(ctx context.Context, db *sql.DB, pg bool, secret, proxyProfileID string) (*accountbalance.CredentialEnvelope, error) {
	profileID := strings.TrimSpace(proxyProfileID)
	if profileID == "" {
		return nil, nil
	}
	table := "proxy_profiles"
	if pg {
		table = "juhe_business.proxy_profiles"
	}
	var (
		id, kind, host, username, encryptedPassword sql.NullString
		port                                        sql.NullInt64
	)
	query := `SELECT id, type, host, port, username, password_encrypted
		FROM ` + table + ` WHERE id = ? LIMIT 1`
	if pg {
		query = strings.Replace(query, "?", "$1", 1)
	}
	err := db.QueryRowContext(ctx, query, profileID).
		Scan(&id, &kind, &host, &port, &username, &encryptedPassword)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	proxyKind := kind.String
	if proxyKind == "socks5" {
		proxyKind = "socks5h"
	}
	if proxyKind != "http" && proxyKind != "https" && proxyKind != "socks5" && proxyKind != "socks5h" {
		return nil, fmt.Errorf("不支持的 proxy 类型=%s", proxyKind)
	}
	if strings.TrimSpace(host.String) == "" || port.Int64 < 1 || port.Int64 > 65535 {
		return nil, errors.New("代理地址无效")
	}
	password := ""
	if encryptedPassword.String != "" {
		plain, err := accountbalance.DecryptV1Envelope(secret, encryptedPassword.String)
		if err != nil {
			return nil, err
		}
		var value map[string]any
		if err := json.Unmarshal(plain, &value); err != nil {
			return nil, err
		}
		if text, ok := value["password"].(string); ok {
			password = text
		}
	}
	target := &url.URL{Scheme: proxyKind, Host: fmt.Sprintf("%s:%d", host.String, port.Int64)}
	if username.String != "" {
		target.User = url.UserPassword(username.String, password)
	}
	envelope, err := accountbalance.NewCredentialEnvelope(secret, "proxy_url", map[string]string{"url": target.String()})
	if err != nil {
		return nil, err
	}
	return &envelope, nil
}

// buildManualInput 把候选行映射成共享执行核心的冻结输入（对齐归档 Node
// prepareAccountBalanceHandoverInput account-balance-handover.ts:220-269 +
// balanceCandidatesFromRows account-balance.repository.ts:938-971：
// InputVersion=dispatch_revision、Provider 缺省 openai、凭据信封直用
// credentials_encrypted 列密文（同 JUHE_AI_SECRET v1 信封）。单 Key 与多 Key
// 池（api_keys 数组）封套都原样直传：单 Key 输入由共享执行核心原样委托既有
// 单 Key 路径（行为零变化），多 Key 输入逐 Key 查询并合并快照；0 Key 仍拒绝。
func (r *gatewayManualBalanceRefresher) buildManualInput(ctx context.Context, candidate accounts.BalanceRefreshCandidate) (accountbalance.Input, error) {
	var credentials accounts.Credentials
	if err := accounts.DecryptJSON(r.secret, candidate.CredentialsEnvelope, &credentials); err != nil {
		return accountbalance.Input{}, fmt.Errorf("余额手动刷新候选凭据无法解封: %w", err)
	}
	// 多 Key 门禁解除：≥1 个有效 Key 的凭据封套原样放行（不拆包），多 Key
	// 池由共享执行核心按 EffectiveAPIKeys 分派逐 Key 查询；0 Key 仍拒绝
	// （保留既有错误文案）。
	if len(accounts.EffectiveAccountApiKeys(credentials)) == 0 {
		return accountbalance.Input{}, errors.New("余额手动刷新输入必须包含一个有效的 API Key")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(textFromCredentials(credentials["base_url"])), "/")
	if baseURL == "" {
		return accountbalance.Input{}, errors.New("余额手动刷新缺少 base_url")
	}
	var configMap map[string]any
	if err := json.Unmarshal([]byte(candidate.ConfigJSON), &configMap); err != nil {
		return accountbalance.Input{}, fmt.Errorf("余额手动刷新查询配置无效: %w", err)
	}
	config, err := accountbalance.NormalizeConfig(configMap)
	if err != nil {
		return accountbalance.Input{}, fmt.Errorf("余额手动刷新查询配置无效: %w", err)
	}
	if candidate.ConfigRevision < 1 {
		return accountbalance.Input{}, errors.New("余额手动刷新 configRevision 必须是正整数")
	}
	inputVersion := candidate.DispatchRevision
	if inputVersion < 1 {
		inputVersion = 1
	}
	provider := strings.TrimSpace(candidate.ProviderCode)
	if provider == "" {
		provider = "openai"
	}
	status := strings.TrimSpace(candidate.Status)
	if status == "" {
		status = "active"
	}
	now := r.now().UTC()
	input := accountbalance.Input{
		AccountID:       candidate.ID,
		SystemAccountID: candidate.SystemAccountID,
		InputVersion:    inputVersion,
		ConfigRevision:  candidate.ConfigRevision,
		Provider:        provider,
		Type:            "api_key",
		Status:          status,
		Schedulable:     candidate.Schedulable,
		BaseURL:         baseURL,
		Config:          config,
		// credentials_encrypted 列即 v1 信封密文，解封后含 api_key 字段
		// （同 jobs buildQueryInput 的直用语义，不重复加密）。
		// CredentialFingerprint / ConfigJSON 原文随候选行携带：执行核据此打
		// 快照 inputDigest（BUG-0286），与 J2 周期路径同源同列。
		CredentialFingerprint: candidate.CredentialFingerprint,
		ConfigJSON:            candidate.ConfigJSON,
		APIKey:    accountbalance.CredentialEnvelope{Kind: "api_key", Ciphertext: candidate.CredentialsEnvelope},
		Trigger:   accountbalance.TriggerManual,
		IssuedAt:  now,
		ExpiresAt: now.Add(manualBalanceInputTTL),
		// 手动刷新没有 due fence（Node business fence 只约束周期恢复）。
		NextRefreshAt: nil,
	}
	proxy, err := resolveProxyURLEnvelope(ctx, r.db, r.pg, r.secret, candidate.ProxyProfileID.String)
	if err != nil {
		return accountbalance.Input{}, err
	}
	input.Proxy = proxy
	return input, nil
}

// RefreshManual 执行一次冻结手动输入并回读该输入的不可变 outcome
// （Service.RunManual 的进程内等价物，共享核心 accountbalance.Service 的
// outcome 解析语义：Errors→原样错误、Stale→stale、Skipped→lease_busy）。
func (r *gatewayManualBalanceRefresher) RefreshManual(ctx context.Context, candidate accounts.BalanceRefreshCandidate) (accounts.BalanceManualRefreshOutcome, error) {
	input, err := r.buildManualInput(ctx, candidate)
	if err != nil {
		return accounts.BalanceManualRefreshOutcome{}, err
	}
	report, err := r.runner.RunManual(ctx, input)
	if err != nil {
		return accounts.BalanceManualRefreshOutcome{}, err
	}
	if itemErr, ok := report.Errors[input.AccountID]; ok {
		return accounts.BalanceManualRefreshOutcome{}, itemErr
	}
	if report.Stale > 0 {
		return accounts.BalanceManualRefreshOutcome{Persisted: false, Outcome: "stale"}, nil
	}
	if report.Skipped > 0 || report.Executed == 0 {
		return accounts.BalanceManualRefreshOutcome{Persisted: false, Outcome: "lease_busy"}, nil
	}
	outcome, found, err := r.store.LoadOutcome(ctx, accountbalance.OutcomeIDForInput(input))
	if err != nil {
		return accounts.BalanceManualRefreshOutcome{}, err
	}
	if !found {
		return accounts.BalanceManualRefreshOutcome{}, errors.New("余额手动刷新 outcome 未生成结果")
	}
	snapshot := snapshotToMap(outcome.Snapshot)
	// SQLite 分支直写 stats overlay（PG 分支由 jobs 投影 job 承担，行为零
	// 变化）；直写失败只登记告警，不影响本次已 committed 的返回（快照已
	// 持久化于租约四表）。
	if !r.pg {
		r.persistSQLiteStatsSnapshot(ctx, outcome)
	}
	return accounts.BalanceManualRefreshOutcome{Persisted: true, Outcome: "committed", Snapshot: snapshot}, nil
}

// persistSQLiteStatsSnapshot 把已 committed 的手动刷新 outcome 按 jobs
// ReplaceSnapshotIfCurrent（worker_balance_detect.go）与 J2 投影
// projectRow（worker_balance_projection.go）同款 UPSERT 形状直写 stats 库
// account_usage_snapshots kind='relay_balance'：列集合、冲突目标
// (system_account_id, account_id, kind)、next_refresh_after 语义完全一致；
// snapshot_json 为 shared Snapshot 的 camelCase 形状并注入 configRevision
// （gateway 读端 BalanceSnapshotMatchesConfiguration / list_snapshot 按
// configRevision 比对配置一致性，列契约必须成立）。SQLite stats 库无 schema
// 前缀，绑定用 `?` 占位符 + RFC3339Nano UTC 文本（jobs timeParam 的 SQLite
// 臂等价）。写入失败不静默（warn + event），由下一次手动刷新收敛。
func (r *gatewayManualBalanceRefresher) persistSQLiteStatsSnapshot(ctx context.Context, outcome accountbalance.Outcome) {
	if r.statsDB == nil {
		return
	}
	// 独立构造持久化 JSON：不污染 RefreshManual 返回给路由的 snapshot map。
	persist := snapshotToMap(outcome.Snapshot)
	persist["configRevision"] = outcome.ConfigRevision
	serialized, err := json.Marshal(persist)
	if err != nil {
		slog.Warn("余额手动刷新快照 stats 直写序列化失败", "event", "account_balance_manual_stats_write_failed",
			"accountId", outcome.AccountID, "error", err.Error())
		return
	}
	now := r.now().UTC()
	lastAttempt := now.Format(time.RFC3339Nano)
	if outcome.Snapshot.LastAttemptAt != "" {
		lastAttempt = outcome.Snapshot.LastAttemptAt
	}
	var lastSuccess any
	if outcome.Snapshot.LastSuccessAt != "" {
		lastSuccess = outcome.Snapshot.LastSuccessAt
	}
	var nextRefreshAfter any
	if outcome.NextRefreshAt != nil {
		nextRefreshAfter = outcome.NextRefreshAt.UTC().Format(time.RFC3339Nano)
	}
	var errorMessage any
	if outcome.Snapshot.ErrorMessage != "" {
		errorMessage = outcome.Snapshot.ErrorMessage
	}
	nowText := now.Format(time.RFC3339Nano)
	upsert := `INSERT INTO account_usage_snapshots (
      system_account_id, account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at, created_at
    ) VALUES (?, ?, 'relay_balance', 'upstream_api', ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
      source = excluded.source,
      snapshot_json = excluded.snapshot_json,
      refresh_status = excluded.refresh_status,
      last_attempt_at = excluded.last_attempt_at,
      last_success_at = excluded.last_success_at,
      next_refresh_after = excluded.next_refresh_after,
      last_error_message = excluded.last_error_message,
      updated_at = excluded.updated_at`
	if _, err := r.statsDB.ExecContext(ctx, upsert,
		outcome.SystemAccountID, outcome.AccountID, string(serialized), string(outcome.Snapshot.Status),
		lastAttempt, lastSuccess, nextRefreshAfter, errorMessage, nowText, nowText); err != nil {
		slog.Warn("余额手动刷新快照 stats 直写失败（列表 overlay 待下次刷新收敛）",
			"event", "account_balance_manual_stats_write_failed",
			"accountId", outcome.AccountID, "error", err.Error())
	}
}

// TestDraft 执行非持久化草稿探测（Node testAccountBalanceCandidate 契约：
// 失败也解析成 status=failed 的快照，错误只用于路由渲染失败快照文案）。
// 草稿没有持久账户身份，执行信封使用占位身份并保持 20s 窗口。多 Key 池封进
// api_keys 封套走共享多 Key 执行核心（逐 Key 查询 + 合并快照）；单 Key 封套
// 形状保持不变（共享入口内部原样委托既有单 Key 路径，行为零变化）；0 Key
// 仍拒绝。
func (r *gatewayManualBalanceRefresher) TestDraft(ctx context.Context, input accounts.BalanceDraftProbeInput) (map[string]any, error) {
	apiKeys := accounts.EffectiveAccountApiKeys(input.Credentials)
	if len(apiKeys) == 0 {
		return nil, errors.New("余额查询测试缺少 API Key")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(textFromCredentials(input.Credentials["base_url"])), "/")
	if baseURL == "" {
		return nil, errors.New("余额查询测试缺少 base_url")
	}
	config, err := accountbalance.NormalizeConfig(input.Config)
	if err != nil {
		return nil, fmt.Errorf("余额查询测试配置无效: %w", err)
	}
	accountID := strings.TrimSpace(input.ID)
	if accountID == "" {
		accountID = "draft"
	}
	now := r.now().UTC()
	// 单 Key 保持既有 {"api_key": ...} 封面字节不变；多 Key 改用
	// {"api_keys": [...]} 池封套（共享核心 EffectiveAPIKeys 语义）。
	var keyPayload any = map[string]any{"api_key": apiKeys[0]}
	if len(apiKeys) > 1 {
		keyPayload = map[string]any{"api_keys": apiKeys}
	}
	keyEnvelope, err := accountbalance.NewCredentialEnvelope(r.secret, "api_key", keyPayload)
	if err != nil {
		return nil, err
	}
	probeInput := accountbalance.Input{
		AccountID:       accountID,
		SystemAccountID: "draft",
		InputVersion:    1,
		ConfigRevision:  1,
		Provider:        "openai",
		Type:            "api_key",
		Status:          "active",
		Schedulable:     true,
		BaseURL:         baseURL,
		Config:          config,
		APIKey:          keyEnvelope,
		Trigger:         accountbalance.TriggerManual,
		IssuedAt:        now,
		ExpiresAt:       now.Add(manualBalanceInputTTL),
	}
	proxy, err := resolveProxyURLEnvelope(ctx, r.db, r.pg, r.secret, derefText(input.ProxyProfileID))
	if err != nil {
		return nil, err
	}
	probeInput.Proxy = proxy
	// 多 Key 感知执行入口：单 Key 输入内部原样委托 ExecuteBalanceQuery
	// （行为零变化），多 Key 输入逐 Key 查询并合并为一份快照。
	result, err := accountbalance.ExecuteAccountBalanceQuery(ctx, probeInput, accountbalance.QueryOptions{
		Secret: r.secret,
		Now:    r.now,
	})
	if err != nil {
		return nil, err
	}
	return snapshotToMap(accountbalance.ApplyManualQueryResult(result, r.now().UTC())), nil
}

// RefreshDraftModelCatalog 拉取上游模型目录并与本地 provider_model_catalog
// 投影比对（归档 refreshAccountDraftModelCatalogAsync
// account-model-catalog-refresh.service.ts:19-58 的进程内移植；多 Key 池取
// 交集 :60-94，addedModels :129-147，recommendedHealthCheckModel :114-127）。
func (r *gatewayModelCatalogRefresher) RefreshDraftModelCatalog(ctx context.Context, input accounts.ModelCatalogDiscoveryInput) (map[string]any, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(textFromCredentials(input.Credentials["base_url"])), "/")
	if baseURL == "" {
		return nil, errors.New("获取上游模型目录失败：账户缺少 base_url")
	}
	// BUG-0175 D-169: OAuth 账户没有 api_key 池，凭据解封后直接携带
	// access_token（accounts normalizeOAuthAccountCredentials 契约）。归档
	// discoverAccountUpstreamModels 对 OAuth candidate 用该 access token 直连
	// 上游 /models（Bearer 语义，upstreamcatalog requestHeadersForProtocol 的
	// openai 分支），api_key 池路径保持不变。
	apiKeys := accounts.EffectiveAccountApiKeys(input.Credentials)
	if len(apiKeys) == 0 {
		if accessToken := strings.TrimSpace(textFromCredentials(input.Credentials["access_token"])); accessToken != "" {
			apiKeys = []string{accessToken}
		} else {
			return nil, errors.New("获取上游模型目录失败：账户缺少 API Key")
		}
	}
	proxyURL, err := r.resolveProxyURL(ctx, input.ProxyProfileID)
	if err != nil {
		return nil, err
	}
	catalogs := make([][]string, 0, len(apiKeys))
	for _, key := range apiKeys {
		ids, err := upstreamcatalog.FetchUpstreamModelIDs(ctx, upstreamcatalog.FetchOptions{
			BaseURL:                   baseURL,
			ProtocolCode:              input.ProtocolCode,
			Credential:                key,
			ProviderCode:              input.ProviderCode,
			ProviderProtocolProfileID: input.ProviderProfileID,
			CredentialType:            input.AccountType,
			OAuthType:                 textFromCredentials(input.Credentials["oauth_type"]),
			OAuthQuotaProjectID:       textFromCredentials(input.Credentials["quota_project_id"]),
			ProxyURL:                  proxyURL,
		})
		if err != nil {
			return nil, err
		}
		catalogs = append(catalogs, ids)
	}
	upstreamIDs := map[string]bool{}
	for _, id := range upstreamcatalog.IntersectModelIDs(catalogs) {
		upstreamIDs[id] = true
	}
	// 本地目录投影（Node listProviderModelCatalogAsync 传 includeUnpriced:
	// true；测试目录投影近似为含未上架模型的全量目录，只取 model +
	// supportedApiProtocols 两列）。
	localModels, err := r.catalog.ListProviderModelsForRequest(ctx, input.ProviderCode, input.OwnerSystemAccountID, false, true)
	if err != nil {
		return nil, err
	}
	testModels, err := r.catalog.ListProviderModelsForRequest(ctx, input.ProviderCode, input.OwnerSystemAccountID, true, true)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"addedModels": accountModelCatalogAdditions(input.SupportedModels, upstreamIDs, localModels, input.ProviderCode, input.ProtocolCode),
	}
	if recommended := recommendedAccountHealthCheckModel(input.HealthCheckModel, upstreamIDs, testModels, input.ProviderCode, input.ProtocolCode); recommended != "" {
		result["recommendedHealthCheckModel"] = recommended
	}
	return result, nil
}

// resolveProxyURL 解出代理串（直连返回 ""）。失败按错误上抛（路由渲染
// 400「获取上游模型目录失败」契约）。
func (r *gatewayModelCatalogRefresher) resolveProxyURL(ctx context.Context, proxyProfileID *string) (string, error) {
	envelope, err := resolveProxyURLEnvelope(ctx, r.db, r.pg, r.secret, derefText(proxyProfileID))
	if err != nil || envelope == nil {
		return "", err
	}
	plain, err := accountbalance.DecryptV1Envelope(r.secret, envelope.Ciphertext)
	if err != nil {
		return "", err
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		return "", err
	}
	return payload.URL, nil
}

// providerModelSupportsProtocolProfile 移植归档
// account-model-normalization.ts:93-119（Go 协议档案没有 endpointFamilies
// 扩展，走默认 family 分支）。
func providerModelSupportsProtocolProfile(modelProtocols []string, providerCode, protocolCode string) bool {
	if len(modelProtocols) == 0 {
		return true
	}
	if strings.ToLower(strings.TrimSpace(providerCode)) == "gpt" {
		return true
	}
	profileProtocols := defaultProtocolFamilies(protocolCode)
	if len(profileProtocols) == 0 {
		return false
	}
	for _, protocol := range modelProtocols {
		if profileProtocols[protocol] {
			return true
		}
	}
	return false
}

// defaultProtocolFamilies 对齐归档 provider-protocol.ts:29-38 的 family 常量
// 与 account-model-normalization.ts:104-117 的默认集合。
func defaultProtocolFamilies(protocolCode string) map[string]bool {
	switch strings.ToLower(strings.TrimSpace(protocolCode)) {
	case "openai":
		return map[string]bool{"chat_completions": true, "responses": true}
	case "anthropic":
		return map[string]bool{"messages": true}
	case "gemini":
		return map[string]bool{
			"generate_content": true, "stream_generate_content": true,
			"count_tokens": true, "embed_content": true, "interactions": true,
		}
	default:
		return map[string]bool{}
	}
}

// accountModelCatalogAdditions 移植归档 accountModelCatalogAdditions
// （account-model-catalog-refresh.service.ts:129-147）。
func accountModelCatalogAdditions(supportedModels []string, upstreamIDs map[string]bool, localModels []providers.ModelCatalogItem, providerCode, protocolCode string) []string {
	selected := map[string]bool{}
	for _, model := range supportedModels {
		trimmed := strings.TrimSpace(model)
		if trimmed != "" {
			selected[trimmed] = true
		}
	}
	additions := []string{}
	seen := map[string]bool{}
	for _, item := range localModels {
		model := strings.TrimSpace(item.Model)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		if !providerModelSupportsProtocolProfile(item.SupportedAPIProtocols, providerCode, protocolCode) {
			continue
		}
		if !upstreamIDs[model] || selected[model] {
			continue
		}
		additions = append(additions, model)
	}
	return additions
}

// recommendedAccountHealthCheckModel 移植归档
// recommendedAccountHealthCheckModel（account-model-catalog-refresh.service.ts
// :114-127）：已配置模型在候选中则保持不变，否则取第一个候选，无候选返回空。
func recommendedAccountHealthCheckModel(configuredHealthCheckModel string, upstreamIDs map[string]bool, testModels []providers.ModelCatalogItem, providerCode, protocolCode string) string {
	candidates := []string{}
	for _, item := range testModels {
		model := strings.TrimSpace(item.Model)
		if model == "" || !upstreamIDs[model] {
			continue
		}
		if !providerModelSupportsProtocolProfile(item.SupportedAPIProtocols, providerCode, protocolCode) {
			continue
		}
		candidates = append(candidates, model)
	}
	configured := strings.TrimSpace(configuredHealthCheckModel)
	for _, candidate := range candidates {
		if candidate == configured {
			return configured
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

// snapshotToMap 把余额快照投成 JSON map。accountbalance.Snapshot 全字段为
// string 底层类型 / int（types.go:46-58），json.Marshal 及其产物 Unmarshal 进
// map[string]any 恒成功，无 error 路径。
func snapshotToMap(snapshot accountbalance.Snapshot) map[string]any {
	encoded, _ := json.Marshal(snapshot)
	var payload map[string]any
	_ = json.Unmarshal(encoded, &payload)
	return payload
}

func textFromCredentials(value any) string {
	text, _ := value.(string)
	return text
}

func derefText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
