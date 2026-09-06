package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
)

// account-balance-auto-detect-recovery 组合根适配器，逐语义对齐 Node
// modules/background/account-balance-auto-detect.service.ts 与
// storage/account-balance.repository.ts：
//   - 候选来自持久化探测意图（balance_query_next_refresh_at 到期 +
//     balanceDetectionCandidateWhere 资格谓词），分页扫描保持 Node 的
//     模块级游标语义（进程内持续、重启重置）；
//   - 候选凭据在列表阶段解密并按「至少一把 Key」过滤（hasAtLeastOneApiKey）；
//   - 提交/开启/快照写入全部以 config_revision + due 时间戳围栏；
//   - 配置 JSON 与快照 JSON 均为 Node camelCase 键（balance_query_config_json
//     由 J2 直读 reader 与 Node 网关消费，键名不得改变）；
//   - 每候选互斥租约复用 background_job_leases（Node
//     runWithAccountBalanceLease：leaseKey=account-balance:{id}、30s）；
//   - builtin 余额查询委托同模块 J2 迁移实现 accountbalance.ExecuteBalanceQuery
//     （registry 已登记 J2 为 account-balance-refresh 的 Go 等价接管）。
//
// 中文文案逐字节对齐 Node 日志事件。

const (
	// balanceRefreshLeaseMS 对齐 Node balanceRefreshLeaseMs = 30_000。
	balanceRefreshLeaseMS = 30_000
	// balanceDetectInputTTL 是 J2 输入信封的有效期（≤15min 上限内的保守值）。
	balanceDetectInputTTL = 5 * time.Minute
)

// balanceConfigJSON 是 balance_query_config_json 的 Node camelCase 序列化
// 形状（normalizeAccountBalanceConfig 输出）。
type balanceConfigJSON struct {
	Adapter                 string          `json:"adapter"`
	IntervalMinutes         int             `json:"intervalMinutes,omitempty"`
	PreferredBuiltinAdapter string          `json:"preferredBuiltinAdapter,omitempty"`
	Custom                  *map[string]any `json:"custom,omitempty"`
}

// normalizeBalanceConfigJSON 等价 Node normalizeAccountBalanceConfig 的
// 序列化输出：intervalMinutes 缺省 5，preferred/custom 空值省略。
func normalizeBalanceConfigJSON(adapter string, intervalMinutes int, preferred string) (string, error) {
	if intervalMinutes == 0 {
		intervalMinutes = 5
	}
	config := balanceConfigJSON{Adapter: adapter, IntervalMinutes: intervalMinutes}
	if strings.TrimSpace(preferred) != "" {
		config.PreferredBuiltinAdapter = preferred
	}
	serialized, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	return string(serialized), nil
}

func hasAtLeastOneAPIKey(credentials map[string]any) bool {
	if keys, ok := credentials["api_keys"].([]any); ok {
		for _, key := range keys {
			if text, ok := key.(string); ok && strings.TrimSpace(text) != "" {
				return true
			}
		}
		return false
	}
	if key, ok := credentials["api_key"].(string); ok {
		return strings.TrimSpace(key) != ""
	}
	return false
}

func textOrEmpty(v any) string {
	if text, ok := v.(string); ok {
		return text
	}
	return ""
}

func intOrZero(v any) int {
	switch typed := v.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	}
	return 0
}

func balanceConfigJSONEqual(stored, expected string) bool {
	var decoded any
	if err := json.Unmarshal([]byte(stored), &decoded); err != nil {
		return false
	}
	config, ok := decoded.(map[string]any)
	if !ok {
		return false
	}
	normalized, err := normalizeBalanceConfigJSON(
		textOrEmpty(config["adapter"]),
		intOrZero(config["intervalMinutes"]),
		textOrEmpty(config["preferredBuiltinAdapter"]))
	if err != nil {
		return false
	}
	return normalized == expected
}

func parseBalanceInstant(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("余额探测时间戳必须是带 Z 或数值 offset 的 RFC3339 时间：%s", value)
	}
	return parsed, nil
}

// balanceDetectRuntime 承载探测意图仓储、互斥租约与 builtin 探测器共享的
// 句柄与状态（对齐 Node 仓储的模块级游标/凭据解密语境）。
type balanceDetectRuntime struct {
	business   *businessDB
	statsDB    *sql.DB
	statsPG    bool
	secret     string
	client     *http.Client
	nowFunc    func() time.Time
	leasestore *taskruns.Store

	mu sync.Mutex
	// cursor 对齐 Node postgresBalanceDetectionDueCursor 模块级游标
	// （进程内持续、扫描耗尽或重启后重置）。
	cursor *balanceDueCursor
	// detected 缓存列表阶段解密的凭据与 detector 返回的完整 J2 快照
	// （remainingUsd 等丰富字段），快照写入优先使用它持久化 Node 等价
	// JSON。键为 "credentials:{id}" / "snapshot:{id}"。
	detected sync.Map
}

type balanceDueCursor struct {
	nextRefreshAt time.Time
	id            string
}

// balanceDetectionCandidateWhere 对齐 Node balanceDetectionCandidateWhere：
// 首次探测读写必须保留的资格谓词。
func balanceDetectionCandidateWhere(postgres bool) string {
	return `
    status = 'active'
    AND schedulable = ` + boolLit(postgres, true) + `
    AND type = 'api_key'
    AND balance_query_enabled = ` + boolLit(postgres, false) + `
    AND balance_query_config_json = '{}'
    AND deleted_at IS NULL
    AND authorization_instance_authorization_id IS NULL
  `
}

type balanceDetectionRow struct {
	id              string
	systemAccountID string
	dispatchVersion *int64
	configRevision  int64
	credentials     string
	nextRefreshAt   *time.Time
	proxyProfileID  sql.NullString
}

func (r *balanceDetectRuntime) decryptCredentials(envelope string) (map[string]any, bool) {
	var credentials map[string]any
	if err := json.Unmarshal([]byte(envelope), &credentials); err == nil {
		// 明文 JSON（测试/未启用凭据封套的本地库）按 Node 解密后形状接受。
		if hasAtLeastOneAPIKey(credentials) {
			return credentials, true
		}
		return nil, false
	}
	plain, err := accountbalance.DecryptV1Envelope(r.secret, envelope)
	if err != nil {
		return nil, false
	}
	if err := json.Unmarshal(plain, &credentials); err != nil {
		return nil, false
	}
	if !hasAtLeastOneAPIKey(credentials) {
		return nil, false
	}
	return credentials, true
}

// ListDueCandidates 对齐 listAccountsDueForBalanceAutoDetectionAsync：
// 最多 4 页扫描（pageSize = max(40, limit*4)），游标推进，允许回绕一次。
func (r *balanceDetectRuntime) ListDueCandidates(ctx context.Context, limit int) ([]opsjobs.BalanceDetectionCandidate, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	now := r.nowFunc()
	scanPageSize := 40
	if page := limit * 4; page > scanPageSize {
		scanPageSize = page
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	postgres := r.business.postgres
	selected := make([]opsjobs.BalanceDetectionCandidate, 0, limit)
	selectedIDs := map[string]struct{}{}
	cursor := r.cursor
	wrapped := false
	for page := 0; page < 4 && len(selected) < limit; page++ {
		query := fmt.Sprintf(`
      SELECT id, system_account_id, dispatch_revision, config_revision, credentials_encrypted, balance_query_next_refresh_at, proxy_profile_id
      FROM %s
      WHERE balance_query_next_refresh_at IS NOT NULL
        AND balance_query_next_refresh_at <= ?
        AND (? = '' OR balance_query_next_refresh_at > ? OR (balance_query_next_refresh_at = ? AND id > ?))
        AND %s
      ORDER BY balance_query_next_refresh_at ASC, id ASC
      LIMIT ?
    `, r.business.table("accounts"), balanceDetectionCandidateWhere(postgres))
		cursorText := ""
		var cursorTime any = timeParam(postgres, time.Time{})
		cursorID := ""
		if cursor != nil {
			cursorText = cursor.nextRefreshAt.UTC().Format(time.RFC3339Nano)
			cursorTime = timeParam(postgres, cursor.nextRefreshAt)
			cursorID = cursor.id
		}
		rows, err := r.business.db.QueryContext(ctx, query,
			timeParam(postgres, now), textParam(cursorText), cursorTime, cursorTime, textParam(cursorID), scanPageSize)
		if err != nil {
			return nil, fmt.Errorf("读取余额自动探测候选失败: %w", err)
		}
		var pageRows []balanceDetectionRow
		for rows.Next() {
			var (
				row                balanceDetectionRow
				id, systemID       sql.NullString
				dispatch, revision sql.NullInt64
				credentials        sql.NullString
				dueRaw             any
			)
			if err := rows.Scan(&id, &systemID, &dispatch, &revision, &credentials, &dueRaw, &row.proxyProfileID); err != nil {
				rows.Close()
				return nil, err
			}
			row.id = id.String
			row.systemAccountID = systemID.String
			if dispatch.Valid {
				value := dispatch.Int64
				row.dispatchVersion = &value
			}
			row.configRevision = revision.Int64
			row.credentials = credentials.String
			due, err := scanNullTime(postgres, dueRaw)
			if err != nil {
				rows.Close()
				return nil, err
			}
			row.nextRefreshAt = due
			pageRows = append(pageRows, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		if len(pageRows) == 0 {
			r.cursor = nil
			if wrapped {
				break
			}
			wrapped = true
			page--
			continue
		}
		var lastExamined *balanceDetectionRow
		consumedAll := true
		for index := range pageRows {
			row := pageRows[index]
			lastExamined = &pageRows[index]
			parsedCredentials, ok := r.decryptCredentials(row.credentials)
			if !ok {
				continue
			}
			if _, seen := selectedIDs[row.id]; seen {
				continue
			}
			selectedIDs[row.id] = struct{}{}
			next := ""
			if row.nextRefreshAt != nil {
				next = row.nextRefreshAt.UTC().Format(time.RFC3339Nano)
			}
			selected = append(selected, opsjobs.BalanceDetectionCandidate{
				ID:              row.id,
				SystemAccountID: row.systemAccountID,
				InputVersion:    row.dispatchVersion,
				ConfigRevision:  row.configRevision,
				NextRefreshAt:   ptrString(next),
				ProxyProfileID:  row.proxyProfileID.String,
			})
			r.detected.Store("credentials:"+row.id, parsedCredentials)
			r.detected.Store("envelope:"+row.id, row.credentials)
			if len(selected) >= limit {
				consumedAll = index == len(pageRows)-1
				break
			}
		}
		if lastExamined != nil && lastExamined.nextRefreshAt != nil {
			r.cursor = &balanceDueCursor{nextRefreshAt: *lastExamined.nextRefreshAt, id: lastExamined.id}
		} else {
			r.cursor = nil
		}
		if consumedAll && len(pageRows) < scanPageSize {
			r.cursor = nil
			if wrapped {
				break
			}
			wrapped = true
		}
	}
	return selected, nil
}

func ptrString(v string) *string { return &v }

// CommitDetectionDue 对齐 commitAccountBalanceDetectionDueAsync：移动或清空
// 持久化首次探测意图，不触碰用户余额配置。
func (r *balanceDetectRuntime) CommitDetectionDue(ctx context.Context, input opsjobs.BalanceCommitDueInput) (bool, error) {
	postgres := r.business.postgres
	if input.ExpectedNextRefreshAt == nil || *input.ExpectedNextRefreshAt == "" {
		return false, errors.New("余额探测意图提交缺少 due 围栏")
	}
	expected, err := parseBalanceInstant(*input.ExpectedNextRefreshAt)
	if err != nil {
		return false, err
	}
	var next any
	if input.NextRefreshAt != nil {
		parsed, parseErr := parseBalanceInstant(*input.NextRefreshAt)
		if parseErr != nil {
			return false, parseErr
		}
		next = timeParam(postgres, parsed)
	}
	query := fmt.Sprintf(`
    UPDATE %s
    SET balance_query_next_refresh_at = ?,
        updated_at = ?
    WHERE id = ?
      AND config_revision = ?
      AND balance_query_next_refresh_at = ?
      AND %s
  `, r.business.table("accounts"), balanceDetectionCandidateWhere(postgres))
	result, err := r.business.db.ExecContext(ctx, query,
		next, timeParam(postgres, r.nowFunc()), textParam(input.AccountID),
		input.ExpectedConfigRevision, timeParam(postgres, expected))
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed > 0, nil
}

// EnableDetectedQuery 对齐 enableDetectedAccountBalanceQueryAsync：
// expectedNextRefreshAt 为 nil 时与 Node `?? undefined` 一致——不加围栏子句。
func (r *balanceDetectRuntime) EnableDetectedQuery(ctx context.Context, input opsjobs.BalanceEnableInput) (bool, error) {
	postgres := r.business.postgres
	configText, err := normalizeBalanceConfigJSON(input.Config.Adapter, input.Config.IntervalMinutes, input.Config.PreferredBuiltinAdapter)
	if err != nil {
		return false, err
	}
	next, err := parseBalanceInstant(input.NextRefreshAt)
	if err != nil {
		return false, err
	}
	fence := ""
	args := []any{
		textParam(configText), timeParam(postgres, next), timeParam(postgres, r.nowFunc()),
		textParam(input.AccountID), input.ExpectedConfigRevision,
	}
	if input.ExpectedNextRefreshAt != nil && *input.ExpectedNextRefreshAt != "" {
		parsed, parseErr := parseBalanceInstant(*input.ExpectedNextRefreshAt)
		if parseErr != nil {
			return false, parseErr
		}
		fence = " AND balance_query_next_refresh_at = ?"
		args = append(args, timeParam(postgres, parsed))
	}
	query := fmt.Sprintf(`
    UPDATE %s
    SET balance_query_enabled = %s,
        balance_query_config_json = ?,
        balance_query_next_refresh_at = ?,
        updated_at = ?
    WHERE id = ?
      AND config_revision = ?
      AND %s%s
  `, r.business.table("accounts"), boolLit(postgres, true), balanceDetectionCandidateWhere(postgres), fence)
	result, err := r.business.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed > 0, nil
}

// balanceSnapshotPersist 是 account_usage_snapshots.snapshot_json 的持久化
// 形状（Node AccountBalanceSnapshot 的被写字段，camelCase 键）。
type balanceSnapshotPersist struct {
	Status         string  `json:"status"`
	ConfigRevision int64   `json:"configRevision"`
	RemainingUSD   *string `json:"remainingUsd,omitempty"`
	RawRemaining   *string `json:"rawRemaining,omitempty"`
	RawUnit        *string `json:"rawUnit,omitempty"`
	Basis          *string `json:"basis,omitempty"`
	ErrorMessage   *string `json:"errorMessage,omitempty"`
	LastAttemptAt  string  `json:"lastAttemptAt,omitempty"`
	LastSuccessAt  string  `json:"lastSuccessAt,omitempty"`
}

// ReplaceSnapshotIfCurrent 对齐 replaceAccountBalanceSnapshotIfCurrentAsync：
// 配置一致（config_revision + 归一化配置 JSON 相等）才替换
// account_usage_snapshots 的 relay_balance 行。
func (r *balanceDetectRuntime) ReplaceSnapshotIfCurrent(ctx context.Context, input opsjobs.BalanceSnapshotInput) (bool, error) {
	postgres := r.business.postgres
	expectedConfigText, err := normalizeBalanceConfigJSON(input.ExpectedConfig.Adapter, input.ExpectedConfig.IntervalMinutes, input.ExpectedConfig.PreferredBuiltinAdapter)
	if err != nil {
		return false, err
	}
	var storedConfig string
	configQuery := fmt.Sprintf(`
    SELECT balance_query_config_json
    FROM %s
    WHERE id = ?
      AND config_revision = ?
      AND balance_query_enabled = %s
      AND deleted_at IS NULL
    LIMIT 1
  `, r.business.table("accounts"), boolLit(postgres, true))
	if err := r.business.db.QueryRowContext(ctx, configQuery, textParam(input.AccountID), input.ExpectedConfigRevision).Scan(&storedConfig); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if !balanceConfigJSONEqual(storedConfig, expectedConfigText) {
		return false, nil
	}
	snapshot, err := r.buildSnapshotJSON(input)
	if err != nil {
		return false, err
	}
	now := r.nowFunc().UTC()
	lastAttempt := now.Format(time.RFC3339Nano)
	if snapshot.LastAttemptAt != "" {
		lastAttempt = snapshot.LastAttemptAt
	}
	var lastSuccess any
	if snapshot.LastSuccessAt != "" {
		lastSuccess = snapshot.LastSuccessAt
	}
	var nextRefreshAfter any
	if input.NextRefreshAfter != "" {
		parsed, parseErr := parseBalanceInstant(input.NextRefreshAfter)
		if parseErr != nil {
			return false, parseErr
		}
		nextRefreshAfter = timeParam(postgres, parsed)
	}
	upsert := fmt.Sprintf(`
    INSERT INTO %s (
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
      updated_at = excluded.updated_at
  `, statsTable(postgres, "account_usage_snapshots"))
	serialized, err := json.Marshal(snapshot)
	if err != nil {
		return false, err
	}
	if _, err := r.statsDB.ExecContext(ctx, upsert,
		textParam(input.SystemAccountID), textParam(input.AccountID), textParam(string(serialized)), textParam(snapshot.Status),
		textParam(lastAttempt), lastSuccess, nextRefreshAfter, nullableTextPtr(snapshot.ErrorMessage),
		timeParam(postgres, now), timeParam(postgres, now)); err != nil {
		return false, fmt.Errorf("写入余额探测快照失败: %w", err)
	}
	return true, nil
}

func nullableTextPtr(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// buildSnapshotJSON 组装 Node 等价快照 JSON：优先使用 detector 缓存的完整
// J2 快照（remainingUsd/rawUnit/basis 等），并按 Node 规则覆盖
// configRevision/lastAttemptAt/lastSuccessAt。
func (r *balanceDetectRuntime) buildSnapshotJSON(input opsjobs.BalanceSnapshotInput) (*balanceSnapshotPersist, error) {
	// 注意：opsjobs.BalanceSnapshotWrite 窄投影不含 errorMessage；error 信息
	// 仅在 detector 缓存的完整 J2 快照可用时持久化（Node 快照形状）。
	view := &balanceSnapshotPersist{
		Status:         string(input.Snapshot.Status),
		ConfigRevision: input.Snapshot.ConfigRevision,
		LastAttemptAt:  input.Snapshot.LastAttemptAt,
		LastSuccessAt:  input.Snapshot.LastSuccessAt,
	}
	if cachedRaw, ok := r.detected.LoadAndDelete("snapshot:" + input.AccountID); ok {
		if full, ok := cachedRaw.(*accountbalance.Snapshot); ok && full != nil {
			view.RemainingUSD = optionalString(full.RemainingUSD)
			view.RawRemaining = optionalString(full.RawRemaining)
			if full.RawUnit != "" {
				unit := string(full.RawUnit)
				view.RawUnit = &unit
			}
			if full.Basis != "" {
				basis := string(full.Basis)
				view.Basis = &basis
			}
			if full.ErrorMessage != "" {
				message := full.ErrorMessage
				view.ErrorMessage = &message
			}
			return view, nil
		}
	}
	if input.Snapshot.DisplayBalance != nil {
		text := fmt.Sprintf("%g", *input.Snapshot.DisplayBalance)
		view.RemainingUSD = &text
	}
	if input.Snapshot.RawStatus != "" {
		view.RawRemaining = &input.Snapshot.RawStatus
	}
	return view, nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// RunWithLease 以 background_job_leases 实现候选级互斥租约
// （对齐 Node runWithAccountBalanceLease：acquire → run → release）。
func (r *balanceDetectRuntime) RunWithLease(ctx context.Context, candidate opsjobs.BalanceDetectionCandidate, run func(ctx context.Context) error) (bool, error) {
	if r.leasestore == nil {
		return false, errors.New("余额探测租约存储未初始化")
	}
	leaseKey := "account-balance:" + candidate.ID
	ownerID := "account-balance-" + newRandomToken()
	now := r.nowFunc().UTC()
	acquired, err := r.leasestore.AcquireLease(ctx, taskruns.LeaseAcquireInput{
		LeaseKey:   leaseKey,
		JobName:    "account-balance-refresh",
		ShardKey:   candidate.SystemAccountID,
		OwnerID:    ownerID,
		LeaseUntil: now.Add(time.Duration(balanceRefreshLeaseMS) * time.Millisecond),
		Now:        &now,
	})
	if err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}
	defer func() {
		_ = r.leasestore.ReleaseLease(ctx, leaseKey, ownerID)
	}()
	return true, run(ctx)
}

// QueryBuiltin 对齐 Node queryBuiltinAccountBalance 的 detection 调用面：
// 候选 → J2 Input → ExecuteBalanceQuery（adapter=builtin 路径）。
func (r *balanceDetectRuntime) QueryBuiltin(ctx context.Context, candidate opsjobs.BalanceDetectionCandidate, config opsjobs.BalanceQueryConfig) (opsjobs.BalanceBuiltinQueryResult, error) {
	input, err := r.buildQueryInput(ctx, candidate, config)
	if err != nil {
		return opsjobs.BalanceBuiltinQueryResult{}, err
	}
	result, execErr := func() (accountbalance.QueryResult, error) {
		// 仅在注入了客户端时传 Client；nil 接口值会被 J2 视为“测试自有传输”
		// 而绕过共享客户端构造。
		if r.client != nil {
			return accountbalance.ExecuteBalanceQuery(ctx, input, accountbalance.QueryOptions{
				Secret:  r.secret,
				Client:  r.client,
				Timeout: 15 * time.Second,
				Now:     r.nowFunc,
			})
		}
		return accountbalance.ExecuteBalanceQuery(ctx, input, accountbalance.QueryOptions{
			Secret:  r.secret,
			Timeout: 15 * time.Second,
			Now:     r.nowFunc,
		})
	}()
	if execErr != nil {
		return opsjobs.BalanceBuiltinQueryResult{}, execErr
	}
	snapshotCopy := result.Snapshot
	r.detected.Store("snapshot:"+candidate.ID, &snapshotCopy)
	var display *float64
	if result.Snapshot.RemainingUSD != "" {
		var value float64
		if _, err := fmt.Sscanf(result.Snapshot.RemainingUSD, "%g", &value); err == nil {
			display = &value
		}
	}
	return opsjobs.BalanceBuiltinQueryResult{
		Adapter: string(result.Adapter),
		Snapshot: opsjobs.BalanceSnapshot{
			Status:         opsjobs.BalanceSnapshotStatus(result.Snapshot.Status),
			DisplayBalance: display,
			RawStatus:      result.Snapshot.RawRemaining,
			ErrorMessage:   result.Snapshot.ErrorMessage,
		},
	}, nil
}

func (r *balanceDetectRuntime) buildQueryInput(ctx context.Context, candidate opsjobs.BalanceDetectionCandidate, config opsjobs.BalanceQueryConfig) (accountbalance.Input, error) {
	var (
		providerCode, credentialsText string
	)
	query := fmt.Sprintf(`
      SELECT a.provider_code, a.credentials_encrypted, p.id, p.type, p.host, p.port, p.username, p.password_encrypted
      FROM %s a
      LEFT JOIN %s p ON p.id = a.proxy_profile_id
      WHERE a.id = ? AND a.deleted_at IS NULL
    `, r.business.table("accounts"), r.business.table("proxy_profiles"))
	var (
		proxyID, proxyKind, proxyHost, proxyUser, proxyPassword sql.NullString
		proxyPort                                               sql.NullInt64
	)
	if err := r.business.db.QueryRowContext(ctx, query, textParam(candidate.ID)).
		Scan(&providerCode, &credentialsText, &proxyID, &proxyKind, &proxyHost, &proxyPort, &proxyUser, &proxyPassword); err != nil {
		return accountbalance.Input{}, fmt.Errorf("读取余额探测账户失败: %w", err)
	}
	credentials := r.cachedCredentials(candidate.ID, credentialsText)
	if credentials == nil {
		return accountbalance.Input{}, fmt.Errorf("余额自动探测候选 %s 的凭据不可用", candidate.ID)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(textOrEmpty(credentials["base_url"])), "/")
	if baseURL == "" {
		return accountbalance.Input{}, errors.New("余额自动探测缺少 base_url")
	}
	inputVersion := int64(1)
	if candidate.InputVersion != nil && *candidate.InputVersion >= 1 {
		inputVersion = *candidate.InputVersion
	}
	now := r.nowFunc().UTC()
	input := accountbalance.Input{
		AccountID:       candidate.ID,
		SystemAccountID: candidate.SystemAccountID,
		InputVersion:    inputVersion,
		ConfigRevision:  candidate.ConfigRevision,
		Provider:        providerCode,
		Type:            "api_key",
		Status:          "active",
		Schedulable:     true,
		BaseURL:         baseURL,
		Config: accountbalance.QueryConfig{
			Adapter:                 accountbalance.Adapter(config.Adapter),
			IntervalMinutes:         config.IntervalMinutes,
			PreferredBuiltinAdapter: accountbalance.Adapter(config.PreferredBuiltinAdapter),
		},
		APIKey:    accountbalance.CredentialEnvelope{Kind: "api_key", Ciphertext: credentialsText},
		Trigger:   accountbalance.TriggerFirstProbe,
		IssuedAt:  now,
		ExpiresAt: now.Add(balanceDetectInputTTL),
	}
	if candidate.NextRefreshAt != nil {
		parsed, err := parseBalanceInstant(*candidate.NextRefreshAt)
		if err != nil {
			return accountbalance.Input{}, err
		}
		input.NextRefreshAt = &parsed
	}
	if strings.TrimSpace(candidate.ProxyProfileID) != "" && proxyHost.Valid {
		proxy, err := r.proxyEnvelope(proxyID.String, proxyKind.String, proxyHost.String, proxyPort.Int64, proxyUser.String, proxyPassword.String)
		if err != nil {
			return accountbalance.Input{}, err
		}
		input.Proxy = proxy
	}
	return input, nil
}

func (r *balanceDetectRuntime) cachedCredentials(accountID, envelopeText string) map[string]any {
	if cached, ok := r.detected.Load("credentials:" + accountID); ok {
		if credentials, ok := cached.(map[string]any); ok {
			return credentials
		}
	}
	credentials, ok := r.decryptCredentials(envelopeText)
	if !ok {
		return nil
	}
	return credentials
}

// proxyEnvelope 对齐 J2 reader 的代理解析（socks5 → socks5h 远程 DNS）。
func (r *balanceDetectRuntime) proxyEnvelope(proxyID, kind, host string, port int64, username, encryptedPassword string) (*accountbalance.CredentialEnvelope, error) {
	if kind == "socks5" {
		kind = "socks5h"
	}
	if kind != "http" && kind != "https" && kind != "socks5" && kind != "socks5h" {
		return nil, fmt.Errorf("不支持的 proxy 类型=%s", kind)
	}
	if strings.TrimSpace(host) == "" || port < 1 || port > 65535 {
		return nil, errors.New("代理地址无效")
	}
	password := ""
	if encryptedPassword != "" {
		plain, err := accountbalance.DecryptV1Envelope(r.secret, encryptedPassword)
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
	target := &url.URL{Scheme: kind, Host: fmt.Sprintf("%s:%d", host, port)}
	if username != "" {
		target.User = url.UserPassword(username, password)
	}
	envelope, err := accountbalance.NewCredentialEnvelope(r.secret, "proxy_url", map[string]string{"url": target.String()})
	if err != nil {
		return nil, err
	}
	return &envelope, nil
}

// wireBalanceDetectFamily 装配 account-balance-auto-detect-recovery。
// 任一依赖缺失→登记 disabled 并说明（不阻塞其他 job）。
func (a *workerAssembly) wireBalanceDetectFamily(ctx context.Context) error {
	name := "account-balance-auto-detect-recovery"
	if !a.config.BalanceDetectEnabled {
		a.registerDisabledJob(name, "JUHE_AI_JOBS_BALANCE_DETECT_ENABLED=false")
		return nil
	}
	if a.config.Driver != "postgres" && a.config.StatsSQLitePath == "" {
		a.registerDisabledJob(name, "缺 JUHE_AI_STATS_DATABASE_PATH（relay_balance 快照库）")
		return nil
	}
	if a.config.Secret == "" {
		a.registerDisabledJob(name, "缺 JUHE_AI_SECRET（凭据封套解密不可用）")
		return nil
	}
	if a.taskRunsStore == nil {
		a.registerDisabledJob(name, "缺 background_job_leases 租约存储（JUHE_AI_JOBS_TASK_RUNS_ENABLED=false）")
		return nil
	}
	business, err := openBusinessDB(a, "balance-detect-business")
	if err != nil {
		return err
	}
	statsPG := a.config.Driver == "postgres"
	statsDB := business.db
	if !statsPG {
		if statsDB, err = a.openSQLite(a.config.StatsSQLitePath, "balance-detect-stats"); err != nil {
			_ = business.close()
			return err
		}
	}
	if err := ensureAccountsBalanceColumns(ctx, business); err != nil {
		a.registerDisabledJob(name, "业务库契约校验失败："+err.Error())
		_ = business.close()
		return nil
	}
	if err := ensureAccountUsageSnapshotsTable(ctx, statsDB, statsPG); err != nil {
		a.registerDisabledJob(name, "统计库快照表校验失败："+err.Error())
		_ = business.close()
		return nil
	}
	runtimeState := &balanceDetectRuntime{
		business:   business,
		statsDB:    statsDB,
		statsPG:    statsPG,
		secret:     a.config.Secret,
		nowFunc:    func() time.Time { return time.Now().UTC() },
		leasestore: a.taskRunsStore,
	}
	deps := opsjobs.BalanceAutoDetectDependencies{
		Repo:     runtimeState,
		Lease:    runtimeState,
		Detector: runtimeState,
		NowMS:    func() int64 { return runtimeState.nowFunc().UnixMilli() },
		// Node passiveScheduleDelayMs 使用 Math.random 对称抖动。
		Random: opsjobs.RandomUnit(func() float64 { return rand.Float64() }),
	}
	a.addCloser(business.close)
	a.scheduleWiredJob(name, func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		summary, runErr := opsjobs.RunBalanceAutoDetectionRecovery(taskCtx, deps)
		if runErr != nil {
			return jobsched.TaskResult{}, runErr
		}
		a.logger.Info("AI 账户余额自动探测补偿完成",
			"event", "account_balance_auto_detect_recovery_completed",
			"outcome", summary.Outcome,
			"selectedCount", summary.SelectedCount,
			"enabledCount", summary.EnabledCount,
			"unsupportedCount", summary.UnsupportedCount,
			"retryCount", summary.RetryCount,
			"staleCount", summary.StaleCount,
			"deferredCount", summary.DeferredCount)
		return jobsched.TaskResult{}, nil
	})
	return nil
}

// ensureAccountUsageSnapshotsTable 校验 relay_balance 快照表存在（冻结
// schema；缺失时 fail closed 登记 disabled，不静默建表——生产 DDL 归
// maintenance 项目）。
func ensureAccountUsageSnapshotsTable(ctx context.Context, db *sql.DB, postgres bool) error {
	var count int
	if postgres {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'juhe_stats' AND table_name = 'account_usage_snapshots'`).Scan(&count); err != nil {
			return err
		}
	} else {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'account_usage_snapshots'`).Scan(&count); err != nil {
			return err
		}
	}
	if count < 1 {
		return errors.New("缺少 account_usage_snapshots 表")
	}
	return nil
}
