// Package connectivityhealth 是非对话账户（图像 / 音频 / 视频，以及只声明
// embeddings / rerank / count_tokens 等输入型能力的 chat 账户）的后台连通性
// 健康检查通道。
//
// 语义（《AI账户检查模型与人工测试设计》测试方式分层）：
//   - 只验证 API Key 与上游地址可达，不调用模型、不生成产物；
//   - 2xx 或注册表登记的预期拒绝码 = 成功；
//   - 401/403 = 凭证失效，账户落 error 且不进入冷却；
//   - 网络 / 5xx / 其他非认证失败 = 只记录健康事实，不改调度状态。
//
// 本包不复制请求构造：探测规格与执行链来自 contracts 连接器注册表与
// accountprobe 连接测试入口；账户视图与凭据解密来自 proberepo。
package connectivityhealth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
	capabilitydomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

const (
	// DefaultInterval 在系统设置缺失时使用；正常部署由 accountHealthCheckIntervalHours
	// 提供（与 J1 模型检查同源）。
	DefaultInterval = 24 * time.Hour
	// transientRetryDelay 是网络/5xx 等非认证失败的复测间隔上限：只记录事实、
	// 不改账户状态，但也不按 24h 周期把上游故障拖到一天后才重试。
	transientRetryDelay = 5 * time.Minute
	probeTimeout        = 2 * time.Minute
	defaultBatchSize    = 50
	defaultConcurrency  = 4
)

// AccountReader 是 proberepo.Store 的窄读取面。
type AccountReader interface {
	LoadAccountForTest(ctx context.Context, accountID string) (*proberepo.AccountForTestView, error)
	LoadAccountForGroup(ctx context.Context, groupID, accountID, systemAccountID string) (*proberepo.CandidateAccount, error)
}

// ProbeRunner 是 accountprobe.Service 的连接测试诊断面。
type ProbeRunner interface {
	ManualConnectivityDiagnostics(ctx context.Context, view *accountprobe.View, limited bool) (*accountquality.ProbeObservation, []accountprobe.PoolKeyAttempt, error)
}

// StateVersionBumper 在账户状态写入成功后通知 gateway 运行态缓存失效。
type StateVersionBumper interface {
	BumpAccountState(ctx context.Context, accountID string) error
}

// Config 是 Runner 的装配输入。
type Config struct {
	DB           *sql.DB
	Reader       AccountReader
	Prober       ProbeRunner
	StateVersion StateVersionBumper
	Logger       *slog.Logger
	Now          func() time.Time
	BatchSize    int
	Concurrency  int
}

// Runner 每轮扫描到期账户并执行一次连通性探测。
type Runner struct {
	db           *sql.DB
	reader       AccountReader
	prober       ProbeRunner
	stateVersion StateVersionBumper
	logger       *slog.Logger
	now          func() time.Time
	batchSize    int
	concurrency  int
}

// CycleResult 是一轮扫描的结构化结果（日志与测试消费）。
type CycleResult struct {
	Scanned       int
	Probed        int
	Succeeded     int
	AuthFailed    int
	Transient     int
	Restored      int
	Skipped       int
	Stale         int
	NextDueAt     time.Time
	IntervalHours int
}

// NewRunner 构造连通性健康检查 Runner。
func NewRunner(cfg Config) (*Runner, error) {
	if cfg.DB == nil {
		return nil, errors.New("connectivity health: business DB 不能为空")
	}
	if cfg.Reader == nil {
		return nil, errors.New("connectivity health: account reader 不能为空")
	}
	if cfg.Prober == nil {
		return nil, errors.New("connectivity health: probe runner 不能为空")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	return &Runner{
		db:           cfg.DB,
		reader:       cfg.Reader,
		prober:       cfg.Prober,
		stateVersion: cfg.StateVersion,
		logger:       logger,
		now:          now,
		batchSize:    batchSize,
		concurrency:  concurrency,
	}, nil
}

// Run 执行一轮健康检查。返回 error 仅代表扫描/加载级故障；单账户探测失败
// 已落库并计入结果，不会中断整轮。
func (r *Runner) Run(ctx context.Context) (CycleResult, error) {
	var result CycleResult
	now := r.now().UTC()
	schedule := r.loadSchedule(ctx)
	result.IntervalHours = int(schedule.Interval / time.Hour)
	if result.IntervalHours < 1 {
		result.IntervalHours = 1
	}
	candidates, err := r.loadCandidates(ctx, now)
	if err != nil {
		return result, err
	}
	result.Scanned = len(candidates)
	if len(candidates) == 0 {
		return result, nil
	}
	sem := make(chan struct{}, r.concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, candidate := range candidates {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			observation := r.processCandidate(ctx, candidate, schedule)
			mu.Lock()
			defer mu.Unlock()
			switch observation.outcome {
			case outcomeSuccess:
				result.Probed++
				result.Succeeded++
			case outcomeAuthFailed:
				result.Probed++
				result.AuthFailed++
			case outcomeTransient:
				result.Probed++
				result.Transient++
			case outcomeRestored:
				result.Probed++
				result.Succeeded++
				result.Restored++
			case outcomeStale:
				result.Probed++
				result.Stale++
			default:
				result.Skipped++
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	r.logger.Info("account connectivity health cycle",
		"scanned", result.Scanned,
		"probed", result.Probed,
		"succeeded", result.Succeeded,
		"auth_failed", result.AuthFailed,
		"transient", result.Transient,
		"restored", result.Restored,
		"skipped", result.Skipped,
		"stale", result.Stale,
		"interval_hours", result.IntervalHours,
	)
	return result, nil
}

type candidate struct {
	ID                string
	CapabilityDomain  string
	Status            string
	LastErrorCode     string
	ConfigRevision    int64
	DispatchRevision  int64
	FailureCount      int
	FailureStartedAt  sql.NullString
	NextHealthCheckAt sql.NullString
}

func (r *Runner) loadCandidates(ctx context.Context, now time.Time) ([]candidate, error) {
	nowText := now.Format(time.RFC3339Nano)
	rows, err := r.db.QueryContext(ctx, connectivityCandidateSQL, nowText, r.batchSize)
	if err != nil {
		return nil, fmt.Errorf("读取非对话账户连通性健康候选失败: %w", err)
	}
	defer rows.Close()
	output := make([]candidate, 0, r.batchSize)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(
			&item.ID, &item.CapabilityDomain, &item.Status, &item.LastErrorCode,
			&item.ConfigRevision, &item.DispatchRevision,
			&item.FailureCount, &item.FailureStartedAt, &item.NextHealthCheckAt,
		); err != nil {
			return nil, fmt.Errorf("解码非对话账户连通性健康候选失败: %w", err)
		}
		output = append(output, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return output, nil
}

// connectivityCandidateSQL 选出所有到期且属于连接测试口径的账户：媒体域
// 全部，或 chat 域只声明非对话形态的账户。对话生成形态账户留给 J1 模型
// 检查，两者不重叠。
const connectivityCandidateSQL = `
SELECT a.id, a.capability_domain, a.status, COALESCE(a.last_error_code, ''),
  a.config_revision, a.dispatch_revision,
  a.health_check_failure_count, a.health_check_failure_started_at,
  a.next_health_check_at
FROM juhe_business.accounts a
WHERE a.deleted_at IS NULL
  AND a.background_probe_enabled = 1
  AND a.status IN ('active', 'pending_test', 'error')
  AND (a.availability_schedule_gate IS NULL OR a.availability_schedule_gate = 1)
  AND (a.account_expires_at IS NULL OR a.account_expires_at > $1)
  AND (a.next_health_check_at IS NULL OR a.next_health_check_at <= $1
    OR (a.status = 'pending_test' AND a.last_health_check_at IS NULL))
  -- 连接测试账户：媒体域全部，或 chat 域只声明非对话形态的账户。
  AND (
    a.capability_domain IN ('video', 'audio', 'image')
    OR (
      a.capability_domain = 'chat'
      AND EXISTS (
        SELECT 1 FROM juhe_business.account_connections c
        JOIN juhe_business.account_endpoint_modes m ON m.connection_id = c.id
        WHERE c.account_id = a.id AND c.enabled = 1
      )
      AND NOT EXISTS (
        SELECT 1 FROM juhe_business.account_connections c
        JOIN juhe_business.account_endpoint_modes m ON m.connection_id = c.id
        WHERE c.account_id = a.id AND c.enabled = 1
          AND m.endpoint_mode IN ('chat_json', 'chat_sse', 'responses_json', 'responses_sse',
            'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse',
            'interactions_json', 'interactions_sse')
      )
    )
  )
ORDER BY CASE WHEN a.status = 'pending_test' THEN 0 ELSE 1 END,
  a.next_health_check_at ASC NULLS FIRST,
  a.last_health_check_at ASC NULLS FIRST,
  a.created_at ASC, a.id ASC
LIMIT $2`

type outcomeKind int

const (
	outcomeSkipped outcomeKind = iota
	outcomeSuccess
	outcomeAuthFailed
	outcomeTransient
	outcomeRestored
	outcomeStale
)

type candidateResult struct {
	outcome outcomeKind
}

func (r *Runner) processCandidate(ctx context.Context, item candidate, schedule healthSchedule) candidateResult {
	account, err := r.reader.LoadAccountForTest(ctx, item.ID)
	if err != nil {
		r.logger.Warn("load connectivity health account failed", "account_id", item.ID, "error", err)
		return candidateResult{outcome: outcomeSkipped}
	}
	if account == nil || strings.TrimSpace(account.BoundGroupID) == "" {
		return candidateResult{outcome: outcomeSkipped}
	}
	systemAccountID := strings.TrimSpace(account.OwnerSystemAccountID)
	if systemAccountID == "" {
		systemAccountID = strings.TrimSpace(account.SystemAccountID)
	}
	candidateAccount, err := r.reader.LoadAccountForGroup(ctx, account.BoundGroupID, item.ID, systemAccountID)
	if err != nil {
		r.logger.Warn("load connectivity health probe candidate failed", "account_id", item.ID, "error", err)
		return candidateResult{outcome: outcomeSkipped}
	}
	view := proberepo.AssembleProbeView(account, candidateAccount)
	if view == nil {
		return candidateResult{outcome: outcomeSkipped}
	}
	modes := make([]string, 0, len(view.NormalizeEndpointModes))
	for mode := range view.NormalizeEndpointModes {
		modes = append(modes, string(mode))
	}
	if !usesConnectionTest(item.CapabilityDomain, modes) {
		return candidateResult{outcome: outcomeSkipped}
	}
	if _, _, ok := accountprobe.ConnectivityProbeModeForView(view); !ok {
		// 没有可用探测规格时必须落健康事实并推进 next_health_check_at，
		// 否则 pending_test（last_health_check_at IS NULL）会每个 tick 被重新
		// 选中，批量无规格账户可占满 candidate LIMIT 造成饥饿。状态不变，
		// 只记录明确错误码等待注册表/连接配置修复后重新评估。
		now := r.now().UTC()
		nextDue := nextHealthCheckAt(now, schedule, false, false)
		failureCount, failureStartedAt := nextFailureState(item, now, false)
		update := healthUpdate{
			now:              now,
			nextDue:          nextDue,
			errorCode:        "connectivity_probe_spec_missing",
			errorMessage:     boundedMessage("账户没有可用于连接测试的请求形态"),
			failureCount:     failureCount,
			failureStartedAt: failureStartedAt,
			currentStatus:    item.Status,
			currentLastError: item.LastErrorCode,
			configRevision:   item.ConfigRevision,
			dispatchRevision: item.DispatchRevision,
			transient:        true,
		}
		affected, err := r.applyUpdate(ctx, item.ID, update)
		if err != nil {
			r.logger.Warn("record missing connectivity probe spec failed", "account_id", item.ID, "error", err)
			return candidateResult{outcome: outcomeSkipped}
		}
		if affected == 0 {
			return candidateResult{outcome: outcomeStale}
		}
		r.logger.Warn("connectivity probe spec missing", "account_id", item.ID, "preset", view.ConnectionPresetCode)
		return candidateResult{outcome: outcomeTransient}
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	observation, _, probeErr := r.prober.ManualConnectivityDiagnostics(probeCtx, view, true)
	cancel()
	now := r.now().UTC()
	if probeErr != nil {
		// 调度器停机/调用方取消不写健康事实；探测自身超时按上游不可达记录，
		// 否则 pending_test 账户会因一次挂起永久停在待检查。
		if ctx.Err() != nil && errors.Is(probeErr, context.Canceled) {
			return candidateResult{outcome: outcomeSkipped}
		}
		r.logger.Warn("connectivity health probe failed", "account_id", item.ID, "error", probeErr)
		observation = &accountquality.ProbeObservation{
			Result: accountquality.ProbeResult{
				Success:   false,
				ErrorCode: "connectivity_probe_error",
				Message:   "连接测试执行失败：" + probeErr.Error(),
			},
			Evidence: accountquality.ProbeEvidence{Connectivity: true},
		}
	}
	if observation == nil {
		return candidateResult{outcome: outcomeSkipped}
	}

	statusCode := 0
	if observation.Result.StatusCode != nil {
		statusCode = *observation.Result.StatusCode
	}
	authFailure := statusCode == 401 || statusCode == 403
	success := observation.Result.Success
	failureCount, failureStartedAt := nextFailureState(item, now, success)
	nextDue := nextHealthCheckAt(now, schedule, success, authFailure)
	update := healthUpdate{
		now:              now,
		nextDue:          nextDue,
		statusCode:       statusCode,
		errorCode:        strings.TrimSpace(observation.Result.ErrorCode),
		errorMessage:     boundedMessage(observation.Result.Message),
		traceID:          strings.TrimSpace(observation.Result.TraceID),
		failureCount:     failureCount,
		failureStartedAt: failureStartedAt,
		currentStatus:    item.Status,
		currentLastError: item.LastErrorCode,
		configRevision:   item.ConfigRevision,
		dispatchRevision: item.DispatchRevision,
	}
	switch {
	case success:
		update.success = true
		update.restore = item.Status == "pending_test" ||
			(item.Status == "error" && isConnectivityAuthError(item.LastErrorCode))
	case authFailure:
		update.authFailure = true
		update.errorCode = firstNonEmpty(update.errorCode, "connectivity_auth_failed")
	case probeErr != nil || statusCode >= 500 || statusCode == 0:
		update.transient = true
		update.errorCode = firstNonEmpty(update.errorCode, "connectivity_upstream_unavailable")
	default:
		// 非认证的 4xx：只记录健康事实，不改调度状态。
		update.transient = true
		update.errorCode = firstNonEmpty(update.errorCode, "connectivity_probe_failed")
	}
	affected, err := r.applyUpdate(ctx, item.ID, update)
	if err != nil {
		r.logger.Warn("write connectivity health outcome failed", "account_id", item.ID, "error", err)
		return candidateResult{outcome: outcomeSkipped}
	}
	if affected == 0 {
		return candidateResult{outcome: outcomeStale}
	}
	if r.stateVersion != nil {
		bumpCtx, bumpCancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := r.stateVersion.BumpAccountState(bumpCtx, item.ID); err != nil {
			r.logger.Warn("bump account state version failed", "account_id", item.ID, "error", err)
		}
		bumpCancel()
	}
	switch {
	case update.restore:
		return candidateResult{outcome: outcomeRestored}
	case update.success:
		return candidateResult{outcome: outcomeSuccess}
	case update.authFailure:
		return candidateResult{outcome: outcomeAuthFailed}
	default:
		return candidateResult{outcome: outcomeTransient}
	}
}

func usesConnectionTest(domain string, modes []string) bool {
	return capabilitydomain.TestKindForAccount(domain, modes) == capabilitydomain.TestKindConnection
}

func isConnectivityAuthError(code string) bool {
	code = strings.TrimSpace(code)
	return code == "connectivity_auth_failed"
}

func nextFailureState(item candidate, now time.Time, success bool) (int, any) {
	if success {
		return 0, nil
	}
	count := item.FailureCount + 1
	if item.FailureStartedAt.Valid && strings.TrimSpace(item.FailureStartedAt.String) != "" {
		return count, item.FailureStartedAt.String
	}
	return count, now.Format(time.RFC3339Nano)
}

type healthUpdate struct {
	now              time.Time
	nextDue          time.Time
	statusCode       int
	errorCode        string
	errorMessage     string
	traceID          string
	failureCount     int
	failureStartedAt any
	currentStatus    string
	currentLastError string
	configRevision   int64
	dispatchRevision int64
	success          bool
	authFailure      bool
	transient        bool
	restore          bool
}

func (r *Runner) applyUpdate(ctx context.Context, accountID string, update healthUpdate) (int64, error) {
	query, params := buildHealthUpdate(accountID, update)
	result, err := r.db.ExecContext(ctx, query, params...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// buildHealthUpdate 生成健康事实写入语句：基础事实对全部结果都写；成功
// 额外写 last_health_success_at，并在待测试/连通性认证失败恢复场景回到
// active；认证失败写 error 且不写冷却；网络/5xx/其他 4xx 只记录事实。
// 状态写入始终带 status/config_revision/dispatch_revision CAS，避免覆盖
// 并发编辑或调度状态迁移。
func buildHealthUpdate(accountID string, update healthUpdate) (string, []any) {
	nowText := update.now.Format(time.RFC3339Nano)
	sets := []string{
		"last_health_check_at = $1",
		"next_health_check_at = $2",
		"last_health_check_status_code = $3",
		"last_health_check_error_code = $4",
		"last_health_check_error_message = $5",
		"last_health_check_trace_id = $6",
		"health_check_failure_count = $7",
		"health_check_failure_started_at = $8",
		"updated_at = $9",
	}
	params := []any{
		nowText,
		update.nextDue.UTC().Format(time.RFC3339Nano),
		nullableInt(update.statusCode),
		nullableText(update.errorCode),
		nullableText(update.errorMessage),
		nullableText(update.traceID),
		update.failureCount,
		update.failureStartedAt,
		nowText,
	}
	if update.success {
		sets = append(sets, "last_health_success_at = $10")
		params = append(params, nowText)
		if update.currentStatus == "pending_test" || update.restore || isConnectivityAuthError(update.currentLastError) {
			sets = append(sets,
				"status = $11",
				"schedulable = 1",
				"cooldown_until = NULL",
				"last_error_code = NULL",
				"last_error_message = NULL",
				"last_error_trace_id = NULL",
			)
			params = append(params, "active")
		}
	} else if update.authFailure {
		sets = append(sets,
			"status = $10",
			"schedulable = 0",
			"cooldown_until = NULL",
			"last_error_code = $11",
			"last_error_message = $12",
			"last_error_trace_id = NULL",
		)
		params = append(params, "error", "connectivity_auth_failed", nullableText(update.errorMessage))
	}
	guard := len(params)
	query := "UPDATE juhe_business.accounts SET " + strings.Join(sets, ", ") +
		" WHERE id = $" + fmt.Sprint(guard+1) +
		" AND deleted_at IS NULL AND status = $" + fmt.Sprint(guard+2) +
		" AND config_revision = $" + fmt.Sprint(guard+3) +
		" AND dispatch_revision = $" + fmt.Sprint(guard+4)
	params = append(params, accountID, update.currentStatus, update.configRevision, update.dispatchRevision)
	return query, params
}

type healthSchedule struct {
	Interval time.Duration
	Jitter   time.Duration
}

func (r *Runner) loadSchedule(ctx context.Context) healthSchedule {
	schedule := healthSchedule{Interval: DefaultInterval}
	rows, err := r.db.QueryContext(ctx,
		`SELECT key, value_json FROM juhe_business.system_settings
		 WHERE system_account_id = 'sys_admin'
		   AND key IN ('accountHealthCheckIntervalHours', 'accountHealthCheckJitterMinutes')`)
	if err != nil {
		r.logger.Warn("read account health schedule failed; using defaults", "error", err)
		return schedule
	}
	defer rows.Close()
	values := map[string]int{}
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			continue
		}
		var value int
		if json.Unmarshal([]byte(raw), &value) == nil {
			values[key] = value
		}
	}
	if hours, ok := values["accountHealthCheckIntervalHours"]; ok && hours >= 1 && hours <= 168 {
		schedule.Interval = time.Duration(hours) * time.Hour
	}
	if minutes, ok := values["accountHealthCheckJitterMinutes"]; ok && minutes >= 0 && minutes <= 1440 {
		schedule.Jitter = time.Duration(minutes) * time.Minute
	}
	return schedule
}

func nextHealthCheckAt(now time.Time, schedule healthSchedule, success, authFailure bool) time.Time {
	if !success && !authFailure {
		// 瞬态失败固定按最多 5 分钟复测：jitter 只用于正常周期/认证失败的
		// 打散，叠加到 5 分钟上限上会把“最多 5 分钟”放大到设置上限
		// （默认 10 分钟、最大 1440 分钟）。
		delay := schedule.Interval
		if delay > transientRetryDelay {
			delay = transientRetryDelay
		}
		if delay < time.Minute {
			delay = time.Minute
		}
		return now.Add(delay)
	}
	delay := schedule.Interval
	if schedule.Jitter > 0 {
		half := int64(schedule.Jitter / 2)
		if half > 0 {
			delay += time.Duration(rand.Int63n(2*half+1) - half)
		}
	}
	if delay < time.Minute {
		delay = time.Minute
	}
	return now.Add(delay)
}

func nullableText(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func boundedMessage(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= 1000 {
		return value
	}
	return string(runes[:1000]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
