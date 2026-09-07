package accounthealth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/schedulejitter"
)

// J1 outcome → 业务账户投影器（BUG-0174 M-1）。行为基线是 Node 归档的
// db-service 投影链：modules/background/account-health-jobs-outcome-drain.service.ts
// （drain 消费者）+ storage/account-health-projection.repository.ts（fenced CAS
// 投影 + receipts）与 storage/account-health-projection-cursor.repository.ts
// （monotonic cursor），并包含 57535d3ad（availability_schedule_json 进投影
// activationPlan + restoresDispatch 推进 dispatch revision）。
//
// 归属依据：归档的投影运行时是独立持续轮询循环
// （account-health-jobs-outcome-projection-runtime.service.ts 的
// runProjectionLoop，projectionPollMs + passive jitter），不属于 J1 runCycle；
// 因此 Go 侧实现为独立 supervisor 组件（OutcomeProjector.Run），与 outbox
// drain / probe drain（runCycle 内）并存而非合并。
//
// 只读 jobs store 的不可变 outcome（payload 携带 projection 时才可投影，
// store.AppendOutcome 已保证非冷却投影仅在 jobs current-state CAS 成功后保留
// projection）；业务库写入逐条在一个事务内完成 receipt 幂等检查、fence 校验、
// CAS 更新、dispatch revision 推进与 receipt 落行。全部 CAS 失败 = stale 终态
// 跳过（幂等，不重试）；SQL/解码错误保留游标整批重试。

// DefaultProjectionConsumerKey 与归档 runtime 的 consumerKey 一致。
const DefaultProjectionConsumerKey = "juhe-ai-account-health-jobs-projector-v1"

// accountHealthProjectionAdvisoryLockKey 归档 PG 事务 advisory lock 键。
const accountHealthProjectionAdvisoryLockKey = "juhe-ai:account-health-projection:v1"

const accountCircuitProjectionKey = "account_circuit_runtime_v1"

// OutcomeCursor 是 drain 游标（jobs store 的 durable 排序键）。ObservedAtText
// 保留存储原文（SQLite 文本精度回写无损），ObservedAt 供单调比较与查询绑定。
type OutcomeCursor struct {
	ObservedAtText string
	ObservedAt     time.Time
	OutcomeID      string
}

// DrainedOutcome 耦合解码后的 outcome 与其 durable 排序时间戳。
type DrainedOutcome struct {
	StorageObservedAtText string
	StorageObservedAt     time.Time
	Outcome               Outcome
}

// ProjectionDisposition 与归档 receipts 的 disposition 枚举一致。
type ProjectionDisposition string

const (
	ProjectionApplied  ProjectionDisposition = "applied"
	ProjectionStale    ProjectionDisposition = "stale"
	ProjectionIgnored  ProjectionDisposition = "ignored"
	ProjectionRejected ProjectionDisposition = "rejected"
)

// ProjectionResult 是单条 outcome 的投影结论（归档 AccountHealthProjectionResult）。
type ProjectionResult struct {
	OutcomeID    string
	AccountID    string
	InputVersion int64
	Disposition  ProjectionDisposition
	Changed      bool
	Reason       string
}

// ProjectionDrainResult 是一轮 drain 的计数。
type ProjectionDrainResult struct {
	Processed  int
	LastCursor *OutcomeCursor
}

// ProjectionBusinessDB 是投影器专用的双模业务库句柄（SQLite/PG；业务表在
// juhe_business schema）。组合根复用 worker 的业务库连接（worker_business_db
// 同一开库约定），投影器自身不做 schema DDL（receipts/cursors 由 maintenance
// bootstrap 负责，与归档一致）。
type ProjectionBusinessDB struct {
	db       *sql.DB
	postgres bool
}

func NewProjectionBusinessDB(db *sql.DB, postgres bool) (*ProjectionBusinessDB, error) {
	if db == nil {
		return nil, errors.New("J1 outcome 投影缺少业务库句柄")
	}
	return &ProjectionBusinessDB{db: db, postgres: postgres}, nil
}

func (b *ProjectionBusinessDB) table(name string) string {
	if b.postgres {
		return "juhe_business." + name
	}
	return name
}

// bind 在 PostgreSQL 方言下把 ? 占位符改写为 $n（worker_business_db 同约定）。
func (b *ProjectionBusinessDB) bind(query string) string {
	if !b.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

// GroupStatsDirtyMarker 是 statsverify.Store 全量脏标记的窄 port（可 Mock）。
// 归档对 availability 变化投影按 accountId 标脏
// （markGroupAccountStatsDirtyByAccountIds）；Go jobs stats 家族当前只提供全量
// 标脏能力，按现有 oauth sweep 先例采用全量语义（仅统计新鲜度窗口差异，不
// 影响探活恢复闭环），marker 缺席时由装配方显式 warn、运行期跳过。
type GroupStatsDirtyMarker interface {
	MarkAllGroupAccountStatsDirty(ctx context.Context, reason string, now time.Time) error
}

// OutcomeProjectorConfig 组合投影器依赖。
type OutcomeProjectorConfig struct {
	Business         *ProjectionBusinessDB
	CredentialSecret string
	// PollInterval 是归档 projectionPollMs 的等价物（持续轮询节拍）。
	PollInterval time.Duration
	// BatchSize 是归档 projectionBatchSize（单轮 drain 的 outcome 上限）。
	BatchSize   int
	ConsumerKey string
	Stats       GroupStatsDirtyMarker
	Logger      *slog.Logger
	Now         func() time.Time
}

const (
	// DefaultProjectionPollInterval / DefaultProjectionBatchSize 对齐归档
	// runtime 配置默认值（runtime.ts projectionPollMs=1000,
	// projectionBatchSize=100 及其边界）。
	DefaultProjectionPollInterval = time.Second
	minProjectionPollInterval     = 100 * time.Millisecond
	maxProjectionPollInterval     = time.Minute
	DefaultProjectionBatchSize    = 100
	maxProjectionBatchSize        = 1000
	// drainPageLimit 是归档 drain 的单页上限（每页最多 100 条）。
	drainPageLimit = 100
)

// OutcomeProjector 是独立的 J1 outcome 投影组件（supervisor.Component.Run）。
type OutcomeProjector struct {
	store       *Store
	business    *ProjectionBusinessDB
	secret      string
	poll        time.Duration
	batch       int
	consumerKey string
	stats       GroupStatsDirtyMarker
	logger      *slog.Logger
	now         func() time.Time
}

func NewOutcomeProjector(store *Store, config OutcomeProjectorConfig) (*OutcomeProjector, error) {
	if store == nil {
		return nil, errors.New("J1 outcome 投影缺少 jobs store")
	}
	if config.Business == nil || config.Business.db == nil {
		return nil, errors.New("J1 outcome 投影缺少业务库句柄")
	}
	if strings.TrimSpace(config.CredentialSecret) == "" {
		return nil, errors.New("J1 outcome 投影缺少凭据 secret")
	}
	poll := config.PollInterval
	if poll <= 0 {
		poll = DefaultProjectionPollInterval
	}
	if poll < minProjectionPollInterval || poll > maxProjectionPollInterval {
		return nil, fmt.Errorf("J1 outcome 投影轮询间隔必须在 %s..%s", minProjectionPollInterval, maxProjectionPollInterval)
	}
	batch := config.BatchSize
	if batch <= 0 {
		batch = DefaultProjectionBatchSize
	}
	if batch < 1 || batch > maxProjectionBatchSize {
		return nil, fmt.Errorf("J1 outcome 投影批量必须在 1..%d", maxProjectionBatchSize)
	}
	consumerKey := strings.TrimSpace(config.ConsumerKey)
	if consumerKey == "" {
		consumerKey = DefaultProjectionConsumerKey
	}
	if len(consumerKey) > 200 {
		return nil, errors.New("J1 projection consumer key 无效")
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &OutcomeProjector{store: store, business: config.Business, secret: config.CredentialSecret, poll: poll, batch: batch, consumerKey: consumerKey, stats: config.Stats, logger: logger, now: now}, nil
}

// Run 是持续轮询投影循环（归档 runProjectionLoop 的 Go 等价；passive jitter
// 节拍与包内既有循环一致）。单轮失败保留游标下一轮重试，不终止循环。
func (p *OutcomeProjector) Run(ctx context.Context) error {
	timer := time.NewTimer(schedulejitter.Delay(p.poll))
	defer timer.Stop()
	for {
		if _, err := p.DrainOnce(ctx); err != nil {
			p.logger.Warn("J1 outcome 投影失败，保留游标等待下一轮",
				"event", "account_health_outcome_projection_failed", "consumerKey", p.consumerKey, "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			timer.Reset(schedulejitter.Delay(p.poll))
		}
	}
}

// DrainOnce 执行一轮有界 drain（归档 drainAccountHealthJobsOutcomes 语义）：
// 按 cursor 分页读取未投影 outcome，逐条先投影后推进游标；游标推进失败即停
// （归档唯一停止分支）。
func (p *OutcomeProjector) DrainOnce(ctx context.Context) (ProjectionDrainResult, error) {
	result := ProjectionDrainResult{}
	cursor, err := p.loadCursor(ctx)
	if err != nil {
		return result, err
	}
	result.LastCursor = cursor
	for result.Processed < p.batch {
		pageLimit := p.batch - result.Processed
		if pageLimit > drainPageLimit {
			pageLimit = drainPageLimit
		}
		page, err := p.store.listProjectedOutcomes(ctx, cursor, pageLimit)
		if err != nil {
			return result, err
		}
		if len(page) == 0 {
			break
		}
		for _, item := range page {
			next := &OutcomeCursor{ObservedAtText: item.StorageObservedAtText, ObservedAt: item.StorageObservedAt, OutcomeID: item.Outcome.OutcomeID}
			if _, err := p.projectOutcome(ctx, item.Outcome); err != nil {
				return result, err
			}
			advanced, err := p.advanceCursor(ctx, next)
			if err != nil {
				return result, err
			}
			if !advanced {
				return result, nil
			}
			cursor = next
			result.Processed++
		}
		if len(page) < pageLimit {
			break
		}
	}
	result.LastCursor = cursor
	return result, nil
}

// projectionDBTime 是投影读取侧可空时间列扫描器；SQLite 文本列保留存储原文，
// 保证游标回写与 WHERE 比较无损。
type projectionDBTime struct {
	Time    time.Time
	Valid   bool
	RawText string
}

func (value *projectionDBTime) Scan(source any) error {
	if source == nil {
		return nil
	}
	switch text := source.(type) {
	case time.Time:
		value.Time, value.Valid = text.UTC(), true
		value.RawText = text.UTC().Format(time.RFC3339Nano)
		return nil
	case string:
		return value.parseText(text)
	case []byte:
		return value.parseText(string(text))
	default:
		return fmt.Errorf("不支持的时间列类型 %T", source)
	}
}

func (value *projectionDBTime) parseText(text string) error {
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return err
	}
	value.Time, value.Valid, value.RawText = parsed.UTC(), true, text
	return nil
}

// listProjectedOutcomes 按 durable 排序读取 outcome 页（归档
// listAccountHealthJobsOutcomes 的只读窄口；含 stale/审计行——无 projection 的
// 行经 validate 落 ignored receipt 后由游标推进收敛）。Store 的只读附加方法，
// 不改动既有 store 逻辑。
func (s *Store) listProjectedOutcomes(ctx context.Context, after *OutcomeCursor, limit int) ([]DrainedOutcome, error) {
	if limit < 1 {
		return nil, errors.New("J1 outcome 读取 limit 无效")
	}
	var query string
	var args []any
	if s.mode == StorePostgres {
		query = `SELECT payload, observed_at FROM juhe_jobs.account_health_outcomes`
		if after != nil {
			query += ` WHERE observed_at > $1 OR (observed_at = $1 AND outcome_id > $2)`
			args = append(args, after.ObservedAt, after.OutcomeID)
		}
		query += ` ORDER BY observed_at ASC, outcome_id ASC LIMIT $` + strconv.Itoa(len(args)+1)
		args = append(args, limit)
	} else {
		query = `SELECT payload, observed_at FROM account_health_outcomes`
		if after != nil {
			query += ` WHERE observed_at > ? OR (observed_at = ? AND outcome_id > ?)`
			args = append(args, after.ObservedAtText, after.ObservedAtText, after.OutcomeID)
		}
		query += ` ORDER BY observed_at ASC, outcome_id ASC LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取 J1 outcome 投影页失败: %w", err)
	}
	defer rows.Close()
	result := make([]DrainedOutcome, 0, limit)
	for rows.Next() {
		var payload any
		var observed projectionDBTime
		if err := rows.Scan(&payload, &observed); err != nil {
			return nil, err
		}
		if !observed.Valid {
			return nil, errors.New("J1 jobs outcome storage observed_at 无效")
		}
		var outcome Outcome
		switch value := payload.(type) {
		case []byte:
			if err := json.Unmarshal(value, &outcome); err != nil {
				return nil, fmt.Errorf("解码 J1 outcome payload 失败: %w", err)
			}
		case string:
			if err := json.Unmarshal([]byte(value), &outcome); err != nil {
				return nil, fmt.Errorf("解码 J1 outcome payload 失败: %w", err)
			}
		default:
			return nil, fmt.Errorf("J1 jobs outcome payload 列类型无效: %T", payload)
		}
		result = append(result, DrainedOutcome{StorageObservedAtText: observed.RawText, StorageObservedAt: observed.Time.UTC(), Outcome: outcome})
	}
	return result, rows.Err()
}

// loadCursor 读取业务库游标（归档 currentAccountHealthProjectionCursor）并按
// outcome_id 重新锚定存储时间戳（归档 resolveCursor：容忍游标精度漂移）。
func (p *OutcomeProjector) loadCursor(ctx context.Context) (*OutcomeCursor, error) {
	var observedAtText, outcomeID sql.NullString
	err := p.business.db.QueryRowContext(ctx, p.business.bind(`SELECT observed_at, outcome_id FROM `+p.business.table("account_health_projection_cursors")+` WHERE consumer_key = ?`), p.consumerKey).Scan(&observedAtText, &outcomeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 J1 投影游标失败: %w", err)
	}
	if !observedAtText.Valid && !outcomeID.Valid {
		return nil, nil
	}
	if !observedAtText.Valid || !outcomeID.Valid {
		return nil, errors.New("J1 projection cursor 存储损坏")
	}
	cursor, err := normalizeProjectionCursor(observedAtText.String, outcomeID.String)
	if err != nil {
		return nil, err
	}
	if resolved, resolveErr := p.resolveCursorStorage(ctx, cursor.OutcomeID); resolveErr == nil && resolved != "" && resolved != cursor.ObservedAtText {
		if anchored, anchorErr := normalizeProjectionCursor(resolved, cursor.OutcomeID); anchorErr == nil {
			cursor = anchored
		}
	}
	return cursor, nil
}

// resolveCursorStorage 返回 outcome_id 当前的存储排序时间文本（缺失时返回空，
// 保持原游标；归档 resolvePostgresCursor/resolveSqliteCursor）。
func (p *OutcomeProjector) resolveCursorStorage(ctx context.Context, outcomeID string) (string, error) {
	query := `SELECT observed_at FROM account_health_outcomes WHERE outcome_id = ?`
	if p.store.mode == StorePostgres {
		query = `SELECT observed_at FROM juhe_jobs.account_health_outcomes WHERE outcome_id = ?`
	}
	var observed projectionDBTime
	err := p.store.db.QueryRowContext(ctx, query, outcomeID).Scan(&observed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("锚定 J1 投影游标失败: %w", err)
	}
	if !observed.Valid {
		return "", nil
	}
	return observed.RawText, nil
}

// advanceCursor 单调推进游标（归档 advanceAccountHealthProjectionCursor）。
// 独立小事务：投影事务已提交后执行，与归档 projectAndAdvance 顺序一致。
func (p *OutcomeProjector) advanceCursor(ctx context.Context, next *OutcomeCursor) (bool, error) {
	tx, err := p.business.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("开始 J1 投影游标事务失败: %w", err)
	}
	defer tx.Rollback()
	table := p.business.table("account_health_projection_cursors")
	var observedAtText, outcomeID sql.NullString
	err = tx.QueryRowContext(ctx, p.business.bind(`SELECT observed_at, outcome_id FROM `+table+` WHERE consumer_key = ?`), p.consumerKey).Scan(&observedAtText, &outcomeID)
	var existing *OutcomeCursor
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// 首次写入。
	case err != nil:
		return false, fmt.Errorf("读取 J1 投影游标失败: %w", err)
	case observedAtText.Valid && outcomeID.Valid:
		if existing, err = normalizeProjectionCursor(observedAtText.String, outcomeID.String); err != nil {
			return false, err
		}
	case !observedAtText.Valid && !outcomeID.Valid:
		existing = nil
	default:
		return false, errors.New("J1 projection cursor 存储损坏")
	}
	nowText := p.now().UTC().Format(time.RFC3339Nano)
	updated := false
	switch {
	case existing != nil && compareProjectionCursor(existing, next) >= 0:
		// 已越过或同位：仅同一不可变 outcome ID 允许一次性规范精度表示。
		if existing.OutcomeID == next.OutcomeID && existing.ObservedAtText != next.ObservedAtText {
			result, execErr := tx.ExecContext(ctx, p.business.bind(`UPDATE `+table+` SET observed_at = ?, updated_at = ? WHERE consumer_key = ? AND outcome_id = ?`), next.ObservedAtText, nowText, p.consumerKey, existing.OutcomeID)
			if execErr != nil {
				return false, execErr
			}
			affected, execErr := result.RowsAffected()
			if execErr != nil {
				return false, execErr
			}
			updated = affected == 1
		}
	case existing != nil:
		result, execErr := tx.ExecContext(ctx, p.business.bind(`UPDATE `+table+` SET observed_at = ?, outcome_id = ?, updated_at = ? WHERE consumer_key = ?`), next.ObservedAtText, next.OutcomeID, nowText, p.consumerKey)
		if execErr != nil {
			return false, execErr
		}
		affected, execErr := result.RowsAffected()
		if execErr != nil {
			return false, execErr
		}
		updated = affected == 1
	default:
		if _, execErr := tx.ExecContext(ctx, p.business.bind(`INSERT INTO `+table+`(consumer_key, observed_at, outcome_id, updated_at) VALUES (?, ?, ?, ?)`), p.consumerKey, next.ObservedAtText, next.OutcomeID, nowText); execErr != nil {
			return false, execErr
		}
		updated = true
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return updated, nil
}

func normalizeProjectionCursor(observedAtText, outcomeID string) (*OutcomeCursor, error) {
	observedAtText = strings.TrimSpace(observedAtText)
	outcomeID = strings.TrimSpace(outcomeID)
	if outcomeID == "" || len(outcomeID) > 4096 {
		return nil, errors.New("J1 projection cursor outcomeId 无效")
	}
	observed, err := time.Parse(time.RFC3339Nano, observedAtText)
	if err != nil {
		return nil, errors.New("J1 projection cursor observedAt 必须是 RFC3339 时间")
	}
	return &OutcomeCursor{ObservedAtText: observedAtText, ObservedAt: observed.UTC(), OutcomeID: outcomeID}, nil
}

func compareProjectionCursor(left, right *OutcomeCursor) int {
	if left.ObservedAt.Before(right.ObservedAt) {
		return -1
	}
	if left.ObservedAt.After(right.ObservedAt) {
		return 1
	}
	return strings.Compare(left.OutcomeID, right.OutcomeID)
}

// projectOutcome 对单条 outcome 执行投影事务（归档
// projectAccountHealthJobsOutcome/projectAccountHealthJobsOutcomeAsync）。
func (p *OutcomeProjector) projectOutcome(ctx context.Context, outcome Outcome) (ProjectionResult, error) {
	base := ProjectionResult{OutcomeID: outcome.OutcomeID, AccountID: outcome.AccountID, InputVersion: outcome.InputVersion}
	tx, err := p.business.db.BeginTx(ctx, nil)
	if err != nil {
		return base, fmt.Errorf("开始 J1 投影事务失败: %w", err)
	}
	defer tx.Rollback()
	if p.business.postgres {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", accountHealthProjectionAdvisoryLockKey); err != nil {
			return base, err
		}
	}
	existing, err := p.findReceipt(ctx, tx, outcome.OutcomeID)
	if err != nil {
		return base, err
	}
	if existing != nil {
		return *existing, nil
	}
	validation := validateProjection(outcome)
	if validation.terminal {
		if err := p.insertReceipt(ctx, tx, base, validation.disposition, validation.reason); err != nil {
			return base, err
		}
		return ProjectionResult{OutcomeID: base.OutcomeID, AccountID: base.AccountID, InputVersion: base.InputVersion, Disposition: validation.disposition, Reason: validation.reason}, tx.Commit()
	}
	record := validation.outcome
	if p.business.postgres {
		if _, err := p.lockDispatchFamilyRoot(ctx, tx, record.AccountID); err != nil {
			return base, err
		}
	}
	account, err := p.findAccountFence(ctx, tx, record.AccountID)
	if err != nil {
		return base, err
	}
	fenceReason, err := p.fenceMismatchReason(ctx, tx, account, record)
	if err != nil {
		return base, err
	}
	if fenceReason != "" {
		if err := p.insertReceipt(ctx, tx, base, ProjectionStale, fenceReason); err != nil {
			return base, err
		}
		return ProjectionResult{OutcomeID: base.OutcomeID, AccountID: base.AccountID, InputVersion: base.InputVersion, Disposition: ProjectionStale, Reason: fenceReason}, tx.Commit()
	}
	plan, err := p.activationPlanForProjection(account, record)
	if err != nil {
		return base, err
	}
	applied, err := p.applyProjectionUpdate(ctx, tx, record, account, plan)
	if err != nil {
		return base, err
	}
	if !applied {
		if err := p.insertReceipt(ctx, tx, base, ProjectionStale, "projection_compare_and_set_missed"); err != nil {
			return base, err
		}
		return ProjectionResult{OutcomeID: base.OutcomeID, AccountID: base.AccountID, InputVersion: base.InputVersion, Disposition: ProjectionStale, Reason: "projection_compare_and_set_missed"}, tx.Commit()
	}
	if plan.restoresDispatch {
		if err := p.advanceFamilyOrSelfDispatch(ctx, tx, record.AccountID, newProjectionDispatchTransitionID()); err != nil {
			return base, err
		}
	}
	if projectionChangesAvailability(record.Projection.TransitionKind) {
		p.markGroupStatsDirty(ctx, record.AccountID)
	}
	if err := p.insertReceipt(ctx, tx, base, ProjectionApplied, ""); err != nil {
		return base, err
	}
	return ProjectionResult{OutcomeID: base.OutcomeID, AccountID: base.AccountID, InputVersion: base.InputVersion, Disposition: ProjectionApplied, Changed: true}, tx.Commit()
}

// markGroupStatsDirty 对齐归档 availability 变化后的 group stats 失效；marker
// 缺席（stats 家族未装配）时跳过（装配方已在装配期显式 warn，不逐事件刷日志）。
func (p *OutcomeProjector) markGroupStatsDirty(ctx context.Context, accountID string) {
	if p.stats == nil {
		return
	}
	if err := p.stats.MarkAllGroupAccountStatsDirty(ctx, "j1_account_health_projection", p.now()); err != nil {
		p.logger.Warn("J1 投影后 group stats 脏标记失败", "event", "account_health_projection_stats_dirty_failed", "accountId", accountID, "error", err.Error())
	}
}

func (p *OutcomeProjector) findReceipt(ctx context.Context, tx *sql.Tx, receiptOutcomeID string) (*ProjectionResult, error) {
	var rowOutcomeID, accountID, disposition, reason sql.NullString
	var inputVersion int64
	err := tx.QueryRowContext(ctx, p.business.bind(`SELECT outcome_id, account_id, input_version, disposition, reason FROM `+p.business.table("account_health_projection_receipts")+` WHERE outcome_id = ?`), receiptOutcomeID).Scan(&rowOutcomeID, &accountID, &inputVersion, &disposition, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 J1 投影 receipt 失败: %w", err)
	}
	return &ProjectionResult{OutcomeID: rowOutcomeID.String, AccountID: accountID.String, InputVersion: inputVersion, Disposition: ProjectionDisposition(disposition.String), Reason: reason.String}, nil
}

func (p *OutcomeProjector) insertReceipt(ctx context.Context, tx *sql.Tx, base ProjectionResult, disposition ProjectionDisposition, reason string) error {
	var reasonValue any
	if reason != "" {
		reasonValue = reason
	}
	_, err := tx.ExecContext(ctx, p.business.bind(`INSERT INTO `+p.business.table("account_health_projection_receipts")+`(outcome_id, account_id, input_version, disposition, reason, applied_at) VALUES (?, ?, ?, ?, ?, ?)`), base.OutcomeID, base.AccountID, base.InputVersion, string(disposition), reasonValue, p.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("写入 J1 投影 receipt 失败: %w", err)
	}
	return nil
}

// projectionAccountFence 是 fence 读取的窄投影（归档 AccountFenceRow）。
type projectionAccountFence struct {
	id                            string
	status                        string
	configRevision                int64
	dispatchRevision              int64
	authorizationInstanceSourceID sql.NullString
	cooldownRetestObservation     projectionDBTime
	cooldownRetestGeneration      sql.NullString
	accountType                   string
	credentialsEncrypted          string
	balanceQueryEnabled           sql.NullInt64
	balanceQueryConfigJSON        string
	availabilityScheduleJSON      sql.NullString
}

func (p *OutcomeProjector) findAccountFence(ctx context.Context, tx *sql.Tx, accountID string) (*projectionAccountFence, error) {
	query := p.business.bind(`SELECT id, status, config_revision, dispatch_revision, authorization_instance_source_account_id, cooldown_retest_observation_started_at, cooldown_retest_generation, type, credentials_encrypted, balance_query_enabled, balance_query_config_json, availability_schedule_json FROM ` + p.business.table("accounts") + ` WHERE id = ? AND deleted_at IS NULL` + p.forUpdate())
	var account projectionAccountFence
	err := tx.QueryRowContext(ctx, query, accountID).Scan(&account.id, &account.status, &account.configRevision, &account.dispatchRevision, &account.authorizationInstanceSourceID, &account.cooldownRetestObservation, &account.cooldownRetestGeneration, &account.accountType, &account.credentialsEncrypted, &account.balanceQueryEnabled, &account.balanceQueryConfigJSON, &account.availabilityScheduleJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 J1 投影账户 fence 失败: %w", err)
	}
	return &account, nil
}

func (p *OutcomeProjector) forUpdate() string {
	if p.business.postgres {
		return " FOR UPDATE"
	}
	return ""
}

// fenceMismatchReason 对齐归档 fenceMismatchReason 的 stale 真值表（reason
// 文本保持一致）。
func (p *OutcomeProjector) fenceMismatchReason(ctx context.Context, tx *sql.Tx, account *projectionAccountFence, outcome Outcome) (string, error) {
	if account == nil {
		return "account_missing_or_deleted", nil
	}
	projection := outcome.Projection
	var currentVersion sql.NullInt64
	err := tx.QueryRowContext(ctx, p.business.bind(`SELECT current_version FROM `+p.business.table("account_health_jobs_input_versions")+` WHERE account_id = ?`+p.forUpdate()), outcome.AccountID).Scan(&currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return "input_version_stale", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取 J1 投影 input version 失败: %w", err)
	}
	if !currentVersion.Valid || currentVersion.Int64 != outcome.InputVersion {
		return "input_version_stale", nil
	}
	if account.configRevision != outcome.ConfigRevision {
		return "config_revision_stale", nil
	}
	if account.dispatchRevision != outcome.DispatchRevision {
		return "dispatch_revision_stale", nil
	}
	if account.status != projection.ExpectedAccountStatus {
		return "expected_account_status_stale", nil
	}
	if projection.SourceRevision != nil {
		if !account.authorizationInstanceSourceID.Valid {
			return "source_account_missing", nil
		}
		var sourceRevision sql.NullInt64
		err := tx.QueryRowContext(ctx, p.business.bind(`SELECT config_revision FROM `+p.business.table("accounts")+` WHERE id = ? AND deleted_at IS NULL`+p.forUpdate()), account.authorizationInstanceSourceID.String).Scan(&sourceRevision)
		if errors.Is(err, sql.ErrNoRows) {
			return "source_account_missing_or_deleted", nil
		}
		if err != nil {
			return "", fmt.Errorf("读取 J1 投影 source 账户失败: %w", err)
		}
		if !sourceRevision.Valid || sourceRevision.Int64 != *projection.SourceRevision {
			return "source_config_revision_stale", nil
		}
	} else if account.authorizationInstanceSourceID.Valid {
		return "source_config_revision_missing", nil
	}
	if projection.ExpectedCooldownFence != nil {
		if !account.cooldownRetestObservation.Valid || !account.cooldownRetestObservation.Time.UTC().Equal(projection.ExpectedCooldownFence.ObservationStartedAt.UTC()) {
			return "cooldown_observation_stale", nil
		}
		if !account.cooldownRetestGeneration.Valid || account.cooldownRetestGeneration.String != projection.ExpectedCooldownFence.Generation {
			return "cooldown_generation_stale", nil
		}
	}
	return "", nil
}

// activationPlanForProjection 移植归档 57535d3ad 的 activationPlan：仅
// activation_success / cooldown_success 读取账户 availability schedule；
// 计划缺失或未启用视同 active（归档 `?? 'active'`），解析失败 fail closed
// （disabled / 不恢复 dispatch）。
func (p *OutcomeProjector) activationPlanForProjection(account *projectionAccountFence, outcome Outcome) (activationProjectionPlan, error) {
	transition := outcome.Projection.TransitionKind
	if transition != "activation_success" && transition != "cooldown_success" {
		return activationProjectionPlan{status: "active", restoresDispatch: false}, nil
	}
	schedule, err := oauthrefresh.ParseScheduleJSON(account.availabilityScheduleJSON.String)
	if err != nil {
		// 归档 catch 分支：解析失败按 disabled 处理，不恢复 dispatch。
		return activationProjectionPlan{status: "disabled", restoresDispatch: false}, nil
	}
	status, ok := oauthrefresh.ScheduleStatus(schedule, p.now().UTC())
	if !ok {
		status = "active"
	}
	nextCheckAt, _ := oauthrefresh.NextScheduleCheckAt(schedule, p.now().UTC())
	return activationProjectionPlan{status: status, nextCheckAt: nextCheckAt, restoresDispatch: status == "active"}, nil
}

type activationProjectionPlan struct {
	status           string
	nextCheckAt      string
	restoresDispatch bool
}

// applyProjectionUpdate 构造并执行 fenced CAS UPDATE（归档 buildProjectionUpdate
// 的分支真值表）。返回 RowsAffected==1。
func (p *OutcomeProjector) applyProjectionUpdate(ctx context.Context, tx *sql.Tx, outcome Outcome, account *projectionAccountFence, plan activationProjectionPlan) (bool, error) {
	projection := outcome.Projection
	requiresOutputFence := projection.TransitionKind == "temporary_unavailable" || projection.TransitionKind == "cooldown_defer" || projection.TransitionKind == "cooldown_failure"
	if requiresOutputFence && projection.CooldownFence == nil {
		// validateProjection 已拒绝该形态；到达此处属于实现缺陷。
		return false, errors.New("J1 projection 缺少输出 cooldown fence")
	}
	updates := []string{"updated_at = ?"}
	params := []any{p.now().UTC().Format(time.RFC3339Nano)}
	set := func(column string, value any) {
		updates = append(updates, column+" = ?")
		params = append(params, value)
	}
	textOrNil := func(value string) any {
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return value
	}
	timeTextOrNil := func(value *time.Time) any {
		if value == nil || value.IsZero() {
			return nil
		}
		return value.UTC().Format(time.RFC3339Nano)
	}
	intOrNil := func(value int) any {
		if value == 0 {
			return nil
		}
		return value
	}
	observedText := outcome.ObservedAt.UTC().Format(time.RFC3339Nano)
	health := func() {
		set("last_health_check_at", observedText)
		set("next_health_check_at", timeTextOrNil(outcome.NextDueAt))
		set("last_health_check_status_code", intOrNil(outcome.StatusCode))
		set("last_health_check_trace_id", textOrNil(limitedText(outcome.RequestID, 200)))
	}
	healthSuccess := func() {
		health()
		set("last_health_success_at", observedText)
		set("health_check_failure_count", 0)
		set("health_check_failure_started_at", nil)
		set("last_health_check_error_code", nil)
		set("last_health_check_error_message", nil)
	}
	healthFailure := func() {
		health()
		set("health_check_failure_count", projectedHealthFailureCount(outcome))
		failureStarted := outcome.FailureStartedAt
		if failureStarted == nil {
			failureStarted = &outcome.ObservedAt
		}
		set("health_check_failure_started_at", timeTextOrNil(failureStarted))
		set("last_health_check_error_code", textOrNil(limitedText(outcome.ErrorCode, 200)))
		set("last_health_check_error_message", textOrNil(limitedText(outcome.ErrorMessage, 2000)))
	}
	clearCooldown := func() {
		set("cooldown_until", nil)
		set("cooldown_retest_failure_count", 0)
		set("cooldown_retest_observation_started_at", nil)
		set("cooldown_retest_generation", nil)
		set("cooldown_retest_last_at", nil)
		set("cooldown_retest_last_status_code", nil)
	}
	setCooldown := func(fence *CooldownFence) {
		set("cooldown_until", timeTextOrNil(outcome.NextDueAt))
		set("cooldown_retest_failure_count", outcome.FailureCount)
		set("cooldown_retest_observation_started_at", fenceGuardText(fence.ObservationStartedAt))
		set("cooldown_retest_generation", fence.Generation)
		set("cooldown_retest_last_at", observedText)
		set("cooldown_retest_last_status_code", intOrNil(outcome.StatusCode))
	}
	setLastError := func() {
		set("last_error_code", textOrNil(limitedText(outcome.ErrorCode, 200)))
		set("last_error_message", textOrNil(limitedText(outcome.ErrorMessage, 2000)))
		set("last_error_trace_id", textOrNil(limitedText(outcome.RequestID, 200)))
	}
	clearLastError := func() {
		set("last_error_code", nil)
		set("last_error_message", nil)
		set("last_error_trace_id", nil)
	}
	switch projection.TransitionKind {
	case "activation_success":
		set("status", plan.status)
		set("schedulable", 1)
		set("availability_schedule_next_check_at", textOrNil(plan.nextCheckAt))
		scheduleBalance, err := p.shouldScheduleBalanceAutoDetection(account, outcome, plan)
		if err != nil {
			return false, err
		}
		if scheduleBalance {
			set("balance_query_next_refresh_at", observedText)
		}
		clearCooldown()
		clearLastError()
		healthSuccess()
	case "health_success":
		healthSuccess()
	case "health_failure":
		healthFailure()
	case "activation_error":
		set("status", "error")
		set("schedulable", 0)
		clearCooldown()
		setLastError()
		healthFailure()
	case "temporary_unavailable":
		set("status", "temporary_unavailable")
		set("schedulable", 1)
		setCooldown(projection.CooldownFence)
		setLastError()
		healthFailure()
	case "cooldown_success":
		set("status", plan.status)
		set("schedulable", 1)
		set("availability_schedule_next_check_at", textOrNil(plan.nextCheckAt))
		clearCooldown()
		clearLastError()
		healthSuccess()
	case "cooldown_defer":
		setCooldown(projection.CooldownFence)
	case "cooldown_failure":
		setCooldown(projection.CooldownFence)
		setLastError()
	case "cooldown_error":
		set("status", "error")
		set("schedulable", 0)
		// 终态冷却停止调度但保留审计记录；无输出 fence 的 legacy 行清空冷却
		// （归档滚动升级兼容分支）。
		if projection.CooldownFence != nil {
			setCooldown(projection.CooldownFence)
		} else {
			set("cooldown_until", nil)
		}
		setLastError()
	default:
		return false, fmt.Errorf("J1 未知 projection transition: %s", projection.TransitionKind)
	}
	accountsTable := p.business.table("accounts")
	versionsTable := p.business.table("account_health_jobs_input_versions")
	guards := []string{
		"target.id = ?",
		"target.deleted_at IS NULL",
		"target.status = ?",
		"target.config_revision = ?",
		"target.dispatch_revision = ?",
		"EXISTS (SELECT 1 FROM " + versionsTable + " AS input_version WHERE input_version.account_id = target.id AND input_version.current_version = ?)",
	}
	params = append(params, outcome.AccountID, projection.ExpectedAccountStatus, outcome.ConfigRevision, outcome.DispatchRevision, outcome.InputVersion)
	if projection.SourceRevision == nil {
		guards = append(guards, "target.authorization_instance_source_account_id IS NULL")
	} else {
		guards = append(guards, "EXISTS (SELECT 1 FROM "+accountsTable+" AS source_account WHERE source_account.id = target.authorization_instance_source_account_id AND source_account.deleted_at IS NULL AND source_account.config_revision = ?)")
		params = append(params, *projection.SourceRevision)
	}
	if projection.ExpectedCooldownFence != nil {
		guards = append(guards, "target.cooldown_retest_observation_started_at = ?", "target.cooldown_retest_generation = ?")
		params = append(params, fenceGuardText(projection.ExpectedCooldownFence.ObservationStartedAt), projection.ExpectedCooldownFence.Generation)
	}
	query := p.business.bind(`UPDATE ` + accountsTable + ` AS target SET ` + strings.Join(updates, ", ") + ` WHERE ` + strings.Join(guards, " AND "))
	result, err := tx.ExecContext(ctx, query, params...)
	if err != nil {
		return false, fmt.Errorf("执行 J1 投影 CAS 失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// shouldScheduleBalanceAutoDetection 移植归档同名函数：仅 activation_success
// 且计划判定 active 时，为无余额配置的 api_key 账户安排余额探测。凭据解密
// 失败与归档 decryptJson 一致上抛（中止投影事务、保留游标重试），不静默降级。
func (p *OutcomeProjector) shouldScheduleBalanceAutoDetection(account *projectionAccountFence, outcome Outcome, plan activationProjectionPlan) (bool, error) {
	if outcome.Projection.TransitionKind != "activation_success" {
		return false, nil
	}
	if plan.status != "active" {
		return false, nil
	}
	if account.accountType != "api_key" || account.balanceQueryEnabled.Int64 != 0 || account.balanceQueryConfigJSON != "{}" {
		return false, nil
	}
	plaintext, err := DecryptV1Envelope(p.secret, account.credentialsEncrypted)
	if err != nil {
		return false, fmt.Errorf("解密 J1 投影账户凭据失败: %w", err)
	}
	var credentials struct {
		APIKeys []string `json:"api_keys"`
		APIKey  string   `json:"api_key"`
	}
	if err := json.Unmarshal(plaintext, &credentials); err != nil {
		return false, fmt.Errorf("解析 J1 投影账户凭据失败: %w", err)
	}
	return effectiveProjectionAPIKeyCount(credentials.APIKeys, credentials.APIKey) >= 1, nil
}

// effectiveProjectionAPIKeyCount 镜像 effectiveAccountApiKeyCount：去重非空
// api_keys，缺省回退 legacy api_key。
func effectiveProjectionAPIKeyCount(apiKeys []string, legacyKey string) int {
	seen := map[string]bool{}
	count := 0
	for _, value := range apiKeys {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		count++
	}
	if count > 0 {
		return count
	}
	if strings.TrimSpace(legacyKey) != "" {
		return 1
	}
	return 0
}

// projectedHealthFailureCount 镜像归档同名函数：values 内的安全非负整数优先，
// 否则回退 outcome.failure_count ?? 0。
func projectedHealthFailureCount(outcome Outcome) int {
	if outcome.Projection != nil {
		if value, ok := outcome.Projection.Values["health_check_failure_count"]; ok {
			if number, ok := value.(float64); ok && number >= 0 && number == float64(int64(number)) {
				return int(number)
			}
		}
	}
	return outcome.FailureCount
}

// fenceGuardText 输出与业务库 text 列一致的 fence 时间文本（accounts 相关列在
// SQLite/PG 均为 TEXT）。format/parse 往返稳定，保证投影自写 fence 的后续
// cooldown 复测 fence 守卫文本逐字一致。
func fenceGuardText(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

// limitedText 镜像归档 limited：trim 后超长截断，空文本返回空串（调用方
// 统一转 NULL）。
func limitedText(value string, maximum int) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return ""
	}
	if len(normalized) > maximum {
		return normalized[:maximum]
	}
	return normalized
}

// projectionChangesAvailability 归档同名函数：这些 transition 影响分组可用性。
func projectionChangesAvailability(transition string) bool {
	return transition == "activation_success" || transition == "activation_error" ||
		transition == "temporary_unavailable" || transition == "cooldown_success" ||
		transition == "cooldown_error"
}

// projectionValidation 是 validateProjection 的结论。
type projectionValidation struct {
	terminal    bool
	disposition ProjectionDisposition
	reason      string
	outcome     Outcome
}

func terminalProjection(disposition ProjectionDisposition, reason string) projectionValidation {
	return projectionValidation{terminal: true, disposition: disposition, reason: reason}
}

// validateProjection 逐条对齐归档同名函数的拒绝真值表（reason 文本保持一致，
// 供 receipts 审计与跨语言回归对照）。
func validateProjection(outcome Outcome) projectionValidation {
	projection := outcome.Projection
	if projection == nil {
		return terminalProjection(ProjectionIgnored, "outcome_has_no_account_projection")
	}
	if !projectionTransitionAllowed(projection.TransitionKind) {
		return terminalProjection(ProjectionRejected, "projection_transition_not_allowed")
	}
	if projection.TargetAccountID != outcome.AccountID || projection.InputVersion != outcome.InputVersion || projection.ConfigRevision != outcome.ConfigRevision || projection.DispatchRevision != outcome.DispatchRevision {
		return terminalProjection(ProjectionRejected, "projection_top_level_fence_mismatch")
	}
	if strings.TrimSpace(projection.ExpectedAccountStatus) == "" {
		return terminalProjection(ProjectionRejected, "projection_expected_account_status_missing")
	}
	for key := range projection.Values {
		if !projectionValueAllowed(key) {
			return terminalProjection(ProjectionRejected, "projection_value_not_allowed:"+key)
		}
	}
	transition := projection.TransitionKind
	if (transition == "activation_success" || transition == "activation_error") && projection.ExpectedAccountStatus != "pending_test" {
		return terminalProjection(ProjectionRejected, "projection_activation_expected_status_invalid")
	}
	if (transition == "health_success" || transition == "temporary_unavailable") && projection.ExpectedAccountStatus != "active" {
		return terminalProjection(ProjectionRejected, "projection_active_expected_status_invalid")
	}
	if transition == "health_failure" && projection.ExpectedAccountStatus != "active" && projection.ExpectedAccountStatus != "pending_test" {
		return terminalProjection(ProjectionRejected, "projection_health_failure_expected_status_invalid")
	}
	if strings.HasPrefix(transition, "cooldown_") && projection.ExpectedAccountStatus != "temporary_unavailable" && projection.ExpectedAccountStatus != "rate_limited" {
		return terminalProjection(ProjectionRejected, "projection_cooldown_expected_status_invalid")
	}
	requiresNextDue := transition != "activation_error" && transition != "cooldown_error"
	if requiresNextDue && outcome.NextDueAt == nil {
		return terminalProjection(ProjectionRejected, "projection_next_due_missing")
	}
	if (transition == "temporary_unavailable" || transition == "cooldown_defer" || transition == "cooldown_failure") && projection.CooldownFence == nil {
		return terminalProjection(ProjectionRejected, "projection_output_cooldown_fence_missing")
	}
	if strings.HasPrefix(transition, "cooldown_") && projection.ExpectedCooldownFence == nil {
		return terminalProjection(ProjectionRejected, "projection_expected_cooldown_fence_missing")
	}
	if (transition == "cooldown_defer" || transition == "cooldown_failure" || (transition == "cooldown_error" && projection.CooldownFence != nil)) &&
		!sameCooldownFence(projection.ExpectedCooldownFence, projection.CooldownFence) {
		return terminalProjection(ProjectionRejected, "projection_cooldown_fence_mismatch")
	}
	if projection.ExpectedCooldownFence != nil && projection.SourceRevision != nil &&
		!sameOptionalInt64(projection.ExpectedCooldownFence.SourceConfigRevision, projection.SourceRevision) {
		return terminalProjection(ProjectionRejected, "projection_expected_cooldown_source_fence_mismatch")
	}
	if projection.CooldownFence != nil && projection.SourceRevision != nil &&
		!sameOptionalInt64(projection.CooldownFence.SourceConfigRevision, projection.SourceRevision) {
		return terminalProjection(ProjectionRejected, "projection_output_cooldown_source_fence_mismatch")
	}
	if !outcomeMatchesTransition(outcome, transition) {
		return terminalProjection(ProjectionRejected, "projection_outcome_transition_mismatch")
	}
	return projectionValidation{outcome: outcome}
}

func projectionTransitionAllowed(transition string) bool {
	switch transition {
	case "activation_success", "health_success", "health_failure", "activation_error",
		"temporary_unavailable", "cooldown_success", "cooldown_defer", "cooldown_failure", "cooldown_error":
		return true
	default:
		return false
	}
}

func projectionValueAllowed(key string) bool {
	switch key {
	case "last_health_check_at", "last_health_success_at", "last_health_check_status_code",
		"last_health_check_error_code", "last_health_check_error_message", "health_check_failure_count":
		return true
	default:
		return false
	}
}

// outcomeMatchesTransition 归档同名函数的分支真值表。
func outcomeMatchesTransition(outcome Outcome, transition string) bool {
	switch {
	case transition == "activation_success" || transition == "health_success" || transition == "cooldown_success":
		return outcome.Outcome == OutcomeSuccess
	case transition == "cooldown_defer":
		return outcome.Outcome == OutcomeNeutral || outcome.Outcome == OutcomeTaskFailed
	case transition == "cooldown_failure" || transition == "cooldown_error":
		return outcome.Outcome == OutcomeUpstreamFailed
	default:
		return outcome.Outcome == OutcomeNeutral || outcome.Outcome == OutcomeUpstreamFailed
	}
}

// advanceFamilyOrSelfDispatch 归档 restoresDispatch 的最终语义（归档文件与
// 57535d3ad 的合并态）：授权实例只推进自身；源账户恢复推进整个家族。
func (p *OutcomeProjector) advanceFamilyOrSelfDispatch(ctx context.Context, tx *sql.Tx, accountID, transitionID string) error {
	var sourceID sql.NullString
	err := tx.QueryRowContext(ctx, p.business.bind(`SELECT authorization_instance_source_account_id FROM `+p.business.table("accounts")+` WHERE id = ?`), accountID).Scan(&sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("AI 账户不存在：" + accountID)
	}
	if err != nil {
		return fmt.Errorf("读取 J1 投影账户授权关系失败: %w", err)
	}
	if sourceID.Valid && strings.TrimSpace(sourceID.String) != "" {
		// 授权实例的恢复只失效自身；家族级 fence 由源账户恢复负责。
		return p.advanceDispatchRevision(ctx, tx, accountID, transitionID)
	}
	return p.advanceDispatchRevisionFamily(ctx, tx, accountID, transitionID)
}

// advanceDispatchRevisionFamily 推进家族根 + 全部授权实例（归档
// advanceAccountCircuitDispatchRevisionFamily：根优先，子账户派生 transitionId）。
func (p *OutcomeProjector) advanceDispatchRevisionFamily(ctx context.Context, tx *sql.Tx, accountID, transitionID string) error {
	var sourceID sql.NullString
	err := tx.QueryRowContext(ctx, p.business.bind(`SELECT authorization_instance_source_account_id FROM `+p.business.table("accounts")+` WHERE id = ?`), accountID).Scan(&sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("AI 账户不存在：" + accountID)
	}
	if err != nil {
		return fmt.Errorf("读取 J1 投影账户授权关系失败: %w", err)
	}
	familyRootID := accountID
	if sourceID.Valid && strings.TrimSpace(sourceID.String) != "" {
		familyRootID = sourceID.String
	}
	var rootID string
	err = tx.QueryRowContext(ctx, p.business.bind(`SELECT id FROM `+p.business.table("accounts")+` WHERE id = ?`+p.forUpdate()), familyRootID).Scan(&rootID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("AI 账户不存在：" + familyRootID)
	}
	if err != nil {
		return fmt.Errorf("锁定 J1 投影家族根失败: %w", err)
	}
	if p.business.postgres && familyRootID != accountID {
		var childID string
		if err := tx.QueryRowContext(ctx, p.business.bind(`SELECT id FROM `+p.business.table("accounts")+` WHERE id = ?`+p.forUpdate()), accountID).Scan(&childID); err != nil {
			return fmt.Errorf("锁定 J1 投影授权实例失败: %w", err)
		}
	}
	instanceIDs, err := p.lockedDispatchInstances(ctx, tx, familyRootID)
	if err != nil {
		return err
	}
	if err := p.advanceDispatchRevision(ctx, tx, familyRootID, transitionID); err != nil {
		return err
	}
	for _, instanceID := range instanceIDs {
		if err := p.advanceDispatchRevision(ctx, tx, instanceID, familyDispatchTransitionID(transitionID, instanceID)); err != nil {
			return err
		}
	}
	return nil
}

func (p *OutcomeProjector) lockedDispatchInstances(ctx context.Context, tx *sql.Tx, familyRootID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, p.business.bind(`SELECT id FROM `+p.business.table("accounts")+` WHERE authorization_instance_source_account_id = ? AND deleted_at IS NULL ORDER BY id ASC`+p.forUpdate()), familyRootID)
	if err != nil {
		return nil, fmt.Errorf("读取 J1 投影授权实例列表失败: %w", err)
	}
	defer rows.Close()
	instanceIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		instanceIDs = append(instanceIDs, id)
	}
	return instanceIDs, rows.Err()
}

// advanceDispatchRevision 在当前业务事务内推进单个账户的 dispatch revision 并
// 写 circuit outbox（对齐 gateway circuit_control_plane.AdvanceDispatchRevision
// 与归档 advanceAccountCircuitDispatchRevision 的幂等/replay 语义；gateway 侧
// bridge 消费该 outbox 行同步运行态）。
func (p *OutcomeProjector) advanceDispatchRevision(ctx context.Context, tx *sql.Tx, accountID, transitionID string) error {
	outboxTable := p.business.table("account_circuit_outbox")
	dedupeKey := "dispatch:" + transitionID
	var eventType, replayAccountID, replayRuntimeKey string
	err := tx.QueryRowContext(ctx, p.business.bind(`SELECT event_type, account_id, account_runtime_key FROM `+outboxTable+` WHERE projection_key = ? AND dedupe_key = ?`), accountCircuitProjectionKey, dedupeKey).Scan(&eventType, &replayAccountID, &replayRuntimeKey)
	if err == nil {
		if eventType != "dispatch_revision_changed" || replayAccountID != accountID || replayRuntimeKey != accountID {
			return errors.New("account circuit replay identity conflict")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("读取 J1 投影 circuit outbox replay 失败: %w", err)
	}
	var revision int64
	err = tx.QueryRowContext(ctx, p.business.bind(`UPDATE `+p.business.table("accounts")+` SET dispatch_revision = dispatch_revision + 1 WHERE id = ? RETURNING dispatch_revision`), accountID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("AI 账户不存在：" + accountID)
	}
	if err != nil {
		return fmt.Errorf("推进 J1 投影 dispatch revision 失败: %w", err)
	}
	nowMS := p.now().UTC().UnixMilli()
	_, err = tx.ExecContext(ctx, p.business.bind(`INSERT INTO `+outboxTable+` (event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key, circuit_scope_key, incident_id, transition_id, dispatch_revision, generation, ledger_revision, status, available_at_ms, attempt_count, created_at_ms, updated_at_ms) VALUES (?, ?, ?, 'dispatch_revision_changed', ?, ?, NULL, NULL, ?, ?, NULL, NULL, 'pending', ?, 0, ?, ?)`),
		newProjectionEventID(), accountCircuitProjectionKey, dedupeKey, accountID, accountID, transitionID, revision, nowMS, nowMS, nowMS)
	if err != nil {
		return fmt.Errorf("写入 J1 投影 circuit outbox 失败: %w", err)
	}
	return nil
}

// lockDispatchFamilyRoot 归档 PG 路径的事务首锁（父→子顺序，防死锁；SQLite
// 单 writer 不需要，归档 sqlite 路径同样无此锁）。
func (p *OutcomeProjector) lockDispatchFamilyRoot(ctx context.Context, tx *sql.Tx, accountID string) (string, error) {
	var sourceID sql.NullString
	err := tx.QueryRowContext(ctx, p.business.bind(`SELECT authorization_instance_source_account_id FROM `+p.business.table("accounts")+` WHERE id = ?`), accountID).Scan(&sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取 J1 投影账户授权关系失败: %w", err)
	}
	rootID := accountID
	if sourceID.Valid && strings.TrimSpace(sourceID.String) != "" {
		rootID = sourceID.String
	}
	var locked string
	if err := tx.QueryRowContext(ctx, p.business.bind(`SELECT id FROM `+p.business.table("accounts")+` WHERE id = ?`+p.forUpdate()), rootID).Scan(&locked); err != nil {
		return "", fmt.Errorf("锁定 J1 投影家族根失败: %w", err)
	}
	if rootID != accountID {
		var child string
		if err := tx.QueryRowContext(ctx, p.business.bind(`SELECT id FROM `+p.business.table("accounts")+` WHERE id = ?`+p.forUpdate()), accountID).Scan(&child); err != nil {
			return "", fmt.Errorf("锁定 J1 投影授权实例失败: %w", err)
		}
	}
	var lockedSource sql.NullString
	if err := tx.QueryRowContext(ctx, p.business.bind(`SELECT authorization_instance_source_account_id FROM `+p.business.table("accounts")+` WHERE id = ?`), accountID).Scan(&lockedSource); err != nil {
		return "", fmt.Errorf("复核 J1 投影授权关系失败: %w", err)
	}
	lockedRoot := accountID
	if lockedSource.Valid && strings.TrimSpace(lockedSource.String) != "" {
		lockedRoot = lockedSource.String
	}
	if lockedRoot != rootID {
		return "", errors.New("账户授权关系在锁定期间发生变化，请重试")
	}
	return rootID, nil
}

// familyDispatchTransitionID 镜像归档 familyDispatchTransitionId。
func familyDispatchTransitionID(transitionID, accountID string) string {
	digest := sha256.New()
	digest.Write([]byte(transitionID))
	digest.Write([]byte{0})
	digest.Write([]byte(accountID))
	return "dispatch-family:" + hex.EncodeToString(digest.Sum(nil))
}

func newProjectionDispatchTransitionID() string {
	return "dispatch-" + newProjectionEventID()
}

func newProjectionEventID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("projection-fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
