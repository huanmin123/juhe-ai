package accountquality

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"

	_ "modernc.org/sqlite"
)

// StatsStoreMode 双模存储。
type StatsStoreMode string

const (
	StatsSQLite   StatsStoreMode = "sqlite"
	StatsPostgres StatsStoreMode = "postgres"
)

// BusinessLookup 提供业务库账户元数据读取（等价 Node loadQualityAccountMetadataByIds：
// PostgreSQL 时业务与统计同库跨 schema；SQLite 时业务库独立文件，由宿主注入第二个连接）。
type BusinessLookup interface {
	// LoadAccountMetadataByIds 返回 id -> (systemAccountID, providerCode)。
	LoadAccountMetadataByIds(ctx context.Context, ids []string) (map[string]AccountMetadata, error)
}

// AccountMetadata 是 accounts 表的最小元数据。
type AccountMetadata struct {
	SystemAccountID string
	ProviderCode    string
}

// StatsStoreConfig 组合统计库与业务元数据来源。
type StatsStoreConfig struct {
	Mode                 StatsStoreMode
	DatabasePath         string
	PostgresURL          string
	PostgresMaxOpenConns int
	PostgresMaxIdleConns int
	PostgresPool         *pgpool.Handle
	Business             BusinessLookup
	Clock                Clock
}

// StatsStore 承载 account_quality_* 统计读写（等价 Node account-quality.repository）。
type StatsStore struct {
	db       *sql.DB
	mode     StatsStoreMode
	pool     *pgpool.Handle
	business BusinessLookup
	clock    Clock
}

// OpenStatsStore 打开双模统计存储（SQLite 单 writer；PG 经 pgpool）。
func OpenStatsStore(config StatsStoreConfig) (*StatsStore, error) {
	clock := config.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	switch config.Mode {
	case StatsSQLite:
		path := strings.TrimSpace(config.DatabasePath)
		if path == "" {
			return nil, errors.New("accountquality sqlite 缺少数据库路径")
		}
		dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_txlock=immediate"
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("配置 accountquality sqlite 单 writer 失败: %w", err)
		}
		return &StatsStore{db: db, mode: StatsSQLite, business: config.Business, clock: clock}, nil
	case StatsPostgres:
		if strings.TrimSpace(config.PostgresURL) == "" {
			return nil, errors.New("accountquality postgres 缺少连接 URL")
		}
		maxOpen := config.PostgresMaxOpenConns
		if maxOpen == 0 {
			maxOpen = 4
		}
		maxIdle := config.PostgresMaxIdleConns
		if maxIdle == 0 {
			maxIdle = maxOpen
		}
		pool := config.PostgresPool
		if pool == nil {
			registry := pgpool.NewRegistry()
			var err error
			pool, err = registry.Acquire("pgx", config.PostgresURL, "accountquality-store", maxOpen, maxIdle)
			if err != nil {
				return nil, err
			}
		}
		return &StatsStore{db: pool.DB(), mode: StatsPostgres, pool: pool, business: config.Business, clock: clock}, nil
	default:
		return nil, errors.New("accountquality stats store mode 必须为 sqlite 或 postgres")
	}
}

// Close 释放连接。
func (s *StatsStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.pool != nil {
		return s.pool.Close()
	}
	return s.db.Close()
}

// SetBusiness 注入业务元数据来源。
func (s *StatsStore) SetBusiness(lookup BusinessLookup) { s.business = lookup }

// SetClock 注入时间源（测试用）。
func (s *StatsStore) SetClock(clock Clock) { s.clock = clock }

// EnsureSchema 创建测试/SQLite 所需的 account_quality_* 表与索引
// （DDL 与 Node stats-schema.ts 一致；PG 的冻结 DDL 属 maintenance 项目，
// 这里同样提供 IF NOT EXISTS 兜底以便独立部署）。
func (s *StatsStore) EnsureSchema(ctx context.Context) error {
	var script string
	if s.mode == StatsSQLite {
		script = sqliteQualitySchema
	} else {
		script = postgresQualitySchema
	}
	if _, err := s.db.ExecContext(ctx, script); err != nil {
		return fmt.Errorf("初始化 accountquality schema 失败: %w", err)
	}
	return nil
}

func (s *StatsStore) now() time.Time { return s.clock.Now().UTC() }

func (s *StatsStore) scoresTable() string {
	if s.mode == StatsPostgres {
		return "juhe_stats.account_quality_scores"
	}
	return "account_quality_scores"
}

func (s *StatsStore) minuteTable() string {
	if s.mode == StatsPostgres {
		return "juhe_stats.account_quality_minute_stats"
	}
	return "account_quality_minute_stats"
}

func (s *StatsStore) dirtyTable() string {
	if s.mode == StatsPostgres {
		return "juhe_stats.account_quality_dirty_accounts"
	}
	return "account_quality_dirty_accounts"
}

// ---------------------------------------------------------------------------
// 刷新（refreshAccountQualityFromUsage / Async 的移植）

// RefreshInput 控制一次刷新。
type RefreshInput struct {
	WindowMinutes int
	DirtyLimit    int
	// Timezone 为 IANA 名称；空值回落 UTC（Node 从系统设置读取）。
	Timezone string
	// Fence 非空时在写事务内校验租约（等价 pinScheduledJobLeaseInTransaction）。
	Fence *FenceToken
}

// FenceToken 是对 taskruns 租约 fence 的窄引用（避免包间依赖反转：
// 由宿主把 taskruns.LeaseFence 展平传入）。
type FenceToken struct {
	LeaseKey     string
	OwnerID      string
	FencingToken int64
	// Assert 在事务外校验租约仍有效；失败返回错误。
	Assert func(ctx context.Context) error
}

// RefreshFromUsage 是 refreshAccountQualityFromUsage 的移植：
// 脏账户认领 → 分钟表聚合 → EWMA/成功率/质量分 → stale 降级 → 清理 → 删除脏行。
func (s *StatsStore) RefreshFromUsage(ctx context.Context, input RefreshInput) (RefreshResult, error) {
	if s.business == nil {
		return RefreshResult{}, errors.New("accountquality 刷新缺少业务账户元数据来源")
	}
	timezone, err := ResolveTimezone(input.Timezone)
	if err != nil {
		return RefreshResult{}, err
	}
	now := s.now()
	windowMinutes := input.WindowMinutes
	if windowMinutes < 1 {
		windowMinutes = 1
	}
	if windowMinutes > 24*60 {
		windowMinutes = 24 * 60
	}
	windowMs := time.Duration(windowMinutes) * time.Minute
	windowStartedAt := now.Add(-windowMs)
	windowEndedAt := now
	windowStartedMinute := MinuteKey(windowStartedAt, timezone)
	retentionCutoffMinute := MinuteKey(now.Add(-24*time.Hour), timezone)
	updatedAt := FormatMillis(now)
	dirtyLimit := normalizeDirtyLimit(input.DirtyLimit)

	dirtyAccountIds, err := s.loadDirtyAccountIds(ctx, dirtyLimit)
	if err != nil {
		return RefreshResult{}, err
	}
	rows, err := s.loadAggregates(ctx, dirtyAccountIds, windowStartedMinute)
	if err != nil {
		return RefreshResult{}, err
	}
	sampledIds := uniqueIds(rows.ids())
	activeAccounts, err := s.business.LoadAccountMetadataByIds(ctx, sampledIds)
	if err != nil {
		return RefreshResult{}, err
	}
	previousQuality, err := s.loadQualityRowsByAccountIds(ctx, sampledIds)
	if err != nil {
		return RefreshResult{}, err
	}

	var removed int64
	txErr := s.withTx(ctx, func(tx *sql.Tx) error {
		if input.Fence != nil && input.Fence.Assert != nil {
			if err := input.Fence.Assert(ctx); err != nil {
				return err
			}
		}
		removedDeleted, err := s.cleanupInactiveQualityRows(ctx, tx, QualityCleanupBatchLimit)
		if err != nil {
			return err
		}
		removed = removedDeleted
		if err := s.cleanupInactiveQualityMinuteRows(ctx, tx, QualityCleanupBatchLimit); err != nil {
			return err
		}
		if err := s.cleanupOldQualityMinuteRows(ctx, tx, retentionCutoffMinute, QualityCleanupBatchLimit); err != nil {
			return err
		}
		staleRows, err := s.loadStaleQualityRows(ctx, tx, sampledIds, dirtyAccountIds, QualityCleanupBatchLimit)
		if err != nil {
			return err
		}
		for _, previous := range staleRows {
			if err := s.markQualityStale(ctx, tx, previous, FormatMillis(windowStartedAt), FormatMillis(windowEndedAt), updatedAt); err != nil {
				return err
			}
		}
		for _, row := range rows {
			metadata, ok := activeAccounts[row.AccountID]
			if !ok {
				continue
			}
			previous, hasPrevious := previousQuality[row.AccountID]
			recentAvg := integerOrNull(row.RecentAvgFirstTokenMs)
			var previousEwma *int64
			if hasPrevious {
				previousEwma = previous.EwmaFirstTokenMs
			}
			ewma := NextEwma(previousEwma, recentAvg)
			var previousRate *float64
			if hasPrevious {
				previousRate = previous.SuccessRate
			}
			successRate := SuccessRateAfterWindow(row.RecentRequestCount, row.RecentSuccessCount, previousRate)
			var qualityState QualityState
			if row.RecentFirstTokenSampleCount > 0 {
				qualityState = QualityFresh
			} else {
				qualityState = QualityUnknown
			}
			qualityScore := ComputeQualityScore(ewma, successRate, qualityState, parseMillis(updatedAt), now)
			if err := s.upsertQualityRow(ctx, tx, QualityUpsertInput{
				AccountID:                   row.AccountID,
				SystemAccountID:             metadata.SystemAccountID,
				ProviderCode:                metadata.ProviderCode,
				QualityScore:                qualityScore,
				QualityState:                qualityState,
				RecentRequestCount:          row.RecentRequestCount,
				RecentSuccessCount:          row.RecentSuccessCount,
				RecentErrorCount:            row.RecentErrorCount,
				RecentFirstTokenSampleCount: row.RecentFirstTokenSampleCount,
				RecentAvgFirstTokenMs:       recentAvg,
				EwmaFirstTokenMs:            ewma,
				SuccessRate:                 successRate,
				WindowStartedAt:             FormatMillis(windowStartedAt),
				WindowEndedAt:               FormatMillis(windowEndedAt),
				LastSampleAt:                coalesce(row.LastSampleAt, previousLast(hasPrevious, previous, previous.LastSampleAt)),
				LastSuccessAt:               coalesce(row.LastSuccessAt, previousLast(hasPrevious, previous, previous.LastSuccessAt)),
				LastErrorAt:                 coalesce(row.LastErrorAt, previousLast(hasPrevious, previous, previous.LastErrorAt)),
				LastErrorMessage:            coalesce(row.LastErrorMessage, previousLast(hasPrevious, previous, previous.LastErrorMessage)),
				UpdatedAt:                   updatedAt,
			}); err != nil {
				return err
			}
		}
		return s.deleteDirtyAccountRows(ctx, tx, dirtyAccountIds)
	})
	if txErr != nil {
		return RefreshResult{}, txErr
	}
	return RefreshResult{
		Refreshed:       len(rows),
		Removed:         removed,
		WindowStartedAt: FormatMillis(windowStartedAt),
		WindowEndedAt:   FormatMillis(windowEndedAt),
	}, nil
}

func previousLast(hasPrevious bool, _ QualityRow, value *string) *string {
	if !hasPrevious {
		return nil
	}
	return value
}

func coalesce(values ...*string) *string {
	for _, value := range values {
		if value != nil && *value != "" {
			return value
		}
	}
	return nil
}

func (s *StatsStore) markQualityStale(ctx context.Context, tx *sql.Tx, previous QualityRow, windowStartedAt, windowEndedAt, updatedAt string) error {
	state := previous.QualityState
	switch state {
	case QualityFresh:
		state = QualityStale
	case QualityFailed:
		state = QualityUnknown
	}
	zero := int64(0)
	score := ComputeQualityScore(previous.EwmaFirstTokenMs, previous.SuccessRate, state, parseMillis(updatedAt), s.now())
	return s.upsertQualityRow(ctx, tx, QualityUpsertInput{
		AccountID:                   previous.AccountID,
		SystemAccountID:             previous.SystemAccountID,
		ProviderCode:                previous.ProviderCode,
		QualityScore:                score,
		QualityState:                state,
		RecentRequestCount:          zero,
		RecentSuccessCount:          zero,
		RecentErrorCount:            zero,
		RecentFirstTokenSampleCount: zero,
		RecentAvgFirstTokenMs:       nil,
		EwmaFirstTokenMs:            previous.EwmaFirstTokenMs,
		SuccessRate:                 previous.SuccessRate,
		WindowStartedAt:             windowStartedAt,
		WindowEndedAt:               windowEndedAt,
		LastSampleAt:                previous.LastSampleAt,
		LastSuccessAt:               previous.LastSuccessAt,
		LastErrorAt:                 previous.LastErrorAt,
		LastErrorMessage:            previous.LastErrorMessage,
		UpdatedAt:                   updatedAt,
	})
}

// QualityUpsertInput 等价 Node AccountQualityUpsertInput。
type QualityUpsertInput struct {
	AccountID                   string
	SystemAccountID             string
	ProviderCode                string
	QualityScore                int64
	QualityState                QualityState
	RecentRequestCount          int64
	RecentSuccessCount          int64
	RecentErrorCount            int64
	RecentFirstTokenSampleCount int64
	RecentAvgFirstTokenMs       *int64
	EwmaFirstTokenMs            *int64
	SuccessRate                 *float64
	WindowStartedAt             string
	WindowEndedAt               string
	LastSampleAt                *string
	LastSuccessAt               *string
	LastErrorAt                 *string
	LastErrorMessage            *string
	UpdatedAt                   string
}

const qualityUpsertSQL = `
INSERT INTO %s (
  account_id, system_account_id, provider_code, quality_score, quality_state,
  recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
  recent_avg_first_token_ms, ewma_first_token_ms, success_rate,
  window_started_at, window_ended_at, last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(account_id) DO UPDATE SET
  system_account_id = excluded.system_account_id,
  provider_code = excluded.provider_code,
  quality_score = excluded.quality_score,
  quality_state = excluded.quality_state,
  recent_request_count = excluded.recent_request_count,
  recent_success_count = excluded.recent_success_count,
  recent_error_count = excluded.recent_error_count,
  recent_first_token_sample_count = excluded.recent_first_token_sample_count,
  recent_avg_first_token_ms = excluded.recent_avg_first_token_ms,
  ewma_first_token_ms = excluded.ewma_first_token_ms,
  success_rate = excluded.success_rate,
  window_started_at = excluded.window_started_at,
  window_ended_at = excluded.window_ended_at,
  last_sample_at = excluded.last_sample_at,
  last_success_at = excluded.last_success_at,
  last_error_at = excluded.last_error_at,
  last_error_message = excluded.last_error_message,
  updated_at = excluded.updated_at
`

func (s *StatsStore) upsertQualityRow(ctx context.Context, tx *sql.Tx, input QualityUpsertInput) error {
	query := fmt.Sprintf(qualityUpsertSQL, s.scoresTable())
	_, err := tx.ExecContext(ctx, query,
		input.AccountID, input.SystemAccountID, input.ProviderCode,
		clampNonNegative(input.QualityScore), string(input.QualityState),
		clampNonNegative(input.RecentRequestCount), clampNonNegative(input.RecentSuccessCount),
		clampNonNegative(input.RecentErrorCount), clampNonNegative(input.RecentFirstTokenSampleCount),
		nullableInteger(input.RecentAvgFirstTokenMs), nullableInteger(input.EwmaFirstTokenMs),
		nullableRate(input.SuccessRate),
		input.WindowStartedAt, input.WindowEndedAt,
		input.LastSampleAt, input.LastSuccessAt, input.LastErrorAt, input.LastErrorMessage,
		input.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("写入账户质量行失败: %w", err)
	}
	return nil
}

func clampNonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// integerOrNull 与 Node integerOrNull 一致：nil 或负值归一化为 nil/0。
func integerOrNull(value *float64) *int64 {
	if value == nil {
		return nil
	}
	rounded := int64(math.Round(*value))
	if rounded < 0 {
		rounded = 0
	}
	return &rounded
}

func nullableInteger(value *int64) any {
	if value == nil {
		return nil
	}
	return clampNonNegative(*value)
}

func nullableRate(value *float64) any {
	if value == nil {
		return nil
	}
	rate := *value
	if rate < 0 {
		rate = 0
	}
	if rate > 1 {
		rate = 1
	}
	return rate
}

// ---------------------------------------------------------------------------
// 失败前置确认候选

// ListFailurePrecheckCandidates 是 listAccountQualityFailurePrecheckCandidates 的
// 移植：阈值、排序、截断与 Node 一致。
func (s *StatsStore) ListFailurePrecheckCandidates(ctx context.Context, limit, offset int) ([]FailurePrecheckCandidate, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > PrecheckCandidateLimitMax {
		limit = PrecheckCandidateLimitMax
	}
	if offset < 0 {
		offset = 0
	}
	if offset > PrecheckOffsetMax {
		offset = PrecheckOffsetMax
	}
	query := fmt.Sprintf(`
	SELECT
	  account_id,
	  system_account_id,
	  provider_code,
	  recent_request_count,
	  recent_success_count,
	  recent_error_count,
	  success_rate,
	  last_error_at,
	  last_error_message,
	  updated_at
	FROM %s
	WHERE recent_request_count >= %d
	  AND recent_error_count >= %d
	  AND (
	    recent_error_count >= %d
	    OR (success_rate IS NOT NULL AND success_rate <= %g)
	  )
	ORDER BY recent_error_count DESC,
	  COALESCE(success_rate, 1) ASC,
	  updated_at DESC,
	  account_id ASC
	LIMIT ? OFFSET ?
	`, s.scoresTable(), PrecheckMinRequests, PrecheckMinErrors, PrecheckFrequentErrors, PrecheckMaxSuccessRate)
	rows, err := s.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("读取质量失败前置确认候选失败: %w", err)
	}
	defer rows.Close()
	candidates := make([]FailurePrecheckCandidate, 0)
	for rows.Next() {
		var (
			accountID, systemAccountID, providerCode sql.NullString
			recentRequest, recentSuccess, recentErr  sql.NullInt64
			successRate                              sql.NullFloat64
			lastErrorAt, lastErrorMessage            sql.NullString
			updatedAt                                sql.NullString
		)
		if err := rows.Scan(&accountID, &systemAccountID, &providerCode, &recentRequest, &recentSuccess, &recentErr, &successRate, &lastErrorAt, &lastErrorMessage, &updatedAt); err != nil {
			return nil, err
		}
		accountIDText := strings.TrimSpace(accountID.String)
		systemAccountIDText := strings.TrimSpace(systemAccountID.String)
		providerCodeText := strings.TrimSpace(providerCode.String)
		updatedAtText := strings.TrimSpace(updatedAt.String)
		if accountIDText == "" || systemAccountIDText == "" || providerCodeText == "" {
			continue
		}
		if _, err := ParseMillisField(updatedAtText, "account_quality_scores.updated_at"); err != nil {
			return nil, err
		}
		candidate := FailurePrecheckCandidate{
			AccountID:          accountIDText,
			SystemAccountID:    systemAccountIDText,
			ProviderCode:       providerCodeText,
			RecentRequestCount: int(clampNonNegative(recentRequest.Int64)),
			RecentSuccessCount: int(clampNonNegative(recentSuccess.Int64)),
			RecentErrorCount:   int(clampNonNegative(recentErr.Int64)),
			LastErrorAt:        strings.TrimSpace(lastErrorAt.String),
			LastErrorMessage:   strings.TrimSpace(lastErrorMessage.String),
			UpdatedAt:          updatedAtText,
		}
		if successRate.Valid {
			rate := clampRate(successRate.Float64)
			candidate.SuccessRate = &rate
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

func clampRate(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// loadQualityRowsByAccountIds 等价 loadAccountQualityRowsByAccountIds。
func (s *StatsStore) loadQualityRowsByAccountIds(ctx context.Context, accountIds []string) (map[string]QualityRow, error) {
	out := map[string]QualityRow{}
	for _, chunk := range chunkStrings(accountIds, QualityLookupChunkSize) {
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(chunk)), ", ")
		query := fmt.Sprintf(`SELECT %s FROM %s WHERE account_id IN (%s)`, qualitySelectColumns, s.scoresTable(), placeholders)
		rows, err := s.db.QueryContext(ctx, query, toAnySlice(chunk)...)
		if err != nil {
			return nil, fmt.Errorf("读取账户质量行失败: %w", err)
		}
		for rows.Next() {
			row, err := scanQualityRow(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[row.AccountID] = row
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 内部查询

func normalizeDirtyLimit(limit int) int {
	if limit < 1 {
		limit = 1
	}
	if limit > DirtyAccountBatchLimit {
		limit = DirtyAccountBatchLimit
	}
	return limit
}

func (s *StatsStore) loadDirtyAccountIds(ctx context.Context, limit int) ([]string, error) {
	query := fmt.Sprintf(`
	SELECT account_id
	FROM %s
	ORDER BY first_dirty_at ASC, account_id ASC
	LIMIT ?
	`, s.dirtyTable())
	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("读取质量脏账户失败: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id.String)
	}
	return uniqueIds(ids), rows.Err()
}

type aggregateRow struct {
	AccountID                   string
	RecentRequestCount          int64
	RecentSuccessCount          int64
	RecentErrorCount            int64
	RecentFirstTokenSampleCount int64
	RecentAvgFirstTokenMs       *float64
	LastSampleAt                *string
	LastSuccessAt               *string
	LastErrorAt                 *string
	LastErrorMessage            *string
}

type aggregateRows []aggregateRow

func (r aggregateRows) ids() []string {
	out := make([]string, len(r))
	for i, row := range r {
		out[i] = row.AccountID
	}
	return out
}

// loadAggregates 按 500 一批分块聚合分钟表（与 Node loadAccountQualityAggregates
// 的 SQL 与 last_error_message 子查询一致）。
func (s *StatsStore) loadAggregates(ctx context.Context, accountIds []string, windowStartedMinute string) (aggregateRows, error) {
	var out aggregateRows
	for _, chunk := range chunkStrings(accountIds, QualityLookupChunkSize) {
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(chunk)), ", ")
		query := fmt.Sprintf(`
		SELECT
		  quality_stats.account_id,
		  SUM(quality_stats.request_count) AS recent_request_count,
		  SUM(quality_stats.success_count) AS recent_success_count,
		  SUM(quality_stats.error_count) AS recent_error_count,
		  SUM(quality_stats.first_token_ms_count) AS recent_first_token_sample_count,
		  CASE
		    WHEN SUM(quality_stats.first_token_ms_count) > 0
		    THEN SUM(quality_stats.first_token_ms_sum) * 1.0 / SUM(quality_stats.first_token_ms_count)
		    ELSE NULL
		  END AS recent_avg_first_token_ms,
		  MAX(quality_stats.last_sample_at) AS last_sample_at,
		  MAX(quality_stats.last_success_at) AS last_success_at,
		  MAX(quality_stats.last_error_at) AS last_error_at,
		  (
		    SELECT latest_error.last_error_message
		    FROM %s latest_error
		    WHERE latest_error.account_id = quality_stats.account_id
		      AND latest_error.stat_minute >= ?
		      AND latest_error.last_error_at IS NOT NULL
		    ORDER BY latest_error.last_error_at DESC, latest_error.stat_minute DESC
		    LIMIT 1
		  ) AS last_error_message
		FROM %s quality_stats
		WHERE quality_stats.account_id IN (%s)
		  AND quality_stats.stat_minute >= ?
		GROUP BY quality_stats.account_id
		ORDER BY quality_stats.account_id ASC
		`, s.minuteTable(), s.minuteTable(), placeholders)
		args := make([]any, 0, len(chunk)+2)
		args = append(args, windowStartedMinute)
		for _, id := range chunk {
			args = append(args, id)
		}
		args = append(args, windowStartedMinute)
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("聚合账户质量分钟统计失败: %w", err)
		}
		for rows.Next() {
			var (
				row                                                 aggregateRow
				accountID                                           sql.NullString
				requests, successes, errors, samples                sql.NullInt64
				avg                                                 sql.NullFloat64
				lastSampleAt, lastSuccessAt, lastErrorAt, lastError sql.NullString
			)
			if err := rows.Scan(&accountID, &requests, &successes, &errors, &samples, &avg, &lastSampleAt, &lastSuccessAt, &lastErrorAt, &lastError); err != nil {
				rows.Close()
				return nil, err
			}
			row.AccountID = strings.TrimSpace(accountID.String)
			row.RecentRequestCount = clampNonNegative(requests.Int64)
			row.RecentSuccessCount = clampNonNegative(successes.Int64)
			row.RecentErrorCount = clampNonNegative(errors.Int64)
			row.RecentFirstTokenSampleCount = clampNonNegative(samples.Int64)
			if avg.Valid {
				value := avg.Float64
				row.RecentAvgFirstTokenMs = &value
			}
			row.LastSampleAt = nullableText(lastSampleAt)
			row.LastSuccessAt = nullableText(lastSuccessAt)
			row.LastErrorAt = nullableText(lastErrorAt)
			row.LastErrorMessage = nullableText(lastError)
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

func nullableText(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := strings.TrimSpace(value.String)
	if text == "" {
		return nil
	}
	return &text
}

// loadStaleQualityRows 等价 loadStaleAccountQualityRows：排除本轮已刷新账户；
// 存在脏标记但本轮未认领的账户不参与 stale 降级；认领过的优先。
func (s *StatsStore) loadStaleQualityRows(ctx context.Context, tx *sql.Tx, refreshedIds, claimedIds []string, limit int) ([]QualityRow, error) {
	refreshedSet := toSet(refreshedIds)
	claimedSet := toSet(claimedIds)
	// Node 在 SQL 内以 temp 表过滤；这里读取上限 = limit + 已刷新数 + 全量脏数，
	// 保证过滤后仍有足够行参与排序截断。
	allDirty, err := s.loadAllDirtyIds(ctx, tx)
	if err != nil {
		return nil, err
	}
	dirtySet := toSet(allDirty)
	fetchLimit := clampLimit(limit + len(refreshedIds) + len(allDirty))
	query := fmt.Sprintf(`
	SELECT %s
	FROM %s
	WHERE quality_state IN ('fresh', 'failed')
	ORDER BY updated_at ASC, account_id ASC
	LIMIT ?
	`, qualitySelectColumns, s.scoresTable())
	rows, err := tx.QueryContext(ctx, query, fetchLimit)
	if err != nil {
		return nil, fmt.Errorf("读取待降级质量行失败: %w", err)
	}
	defer rows.Close()
	var out []QualityRow
	for rows.Next() {
		row, err := scanQualityRow(rows)
		if err != nil {
			return nil, err
		}
		if _, refreshed := refreshedSet[row.AccountID]; refreshed {
			continue
		}
		if _, dirty := dirtySet[row.AccountID]; dirty {
			if _, claimed := claimedSet[row.AccountID]; !claimed {
				continue
			}
		}
		out = append(out, row)
		if len(out) >= fetchLimit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 排序：认领过的在前，其次 updated_at ASC、account_id ASC（与 Node
	// ORDER BY 一致），随后截断到 limit。
	claimedOrder := map[string]int{}
	for i, id := range claimedIds {
		if _, ok := claimedOrder[id]; !ok {
			claimedOrder[id] = i
		}
	}
	sortQualityRowsStable(out, claimedOrder)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *StatsStore) loadAllDirtyIds(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT account_id FROM %s ORDER BY first_dirty_at ASC, account_id ASC LIMIT 10000`, s.dirtyTable()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, strings.TrimSpace(id.String))
	}
	return ids, rows.Err()
}

func sortQualityRowsStable(rows []QualityRow, claimedOrder map[string]int) {
	// 简单插入排序（limit ≤ 1000，稳定且无需引入排序接口差异）。
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			a, b := rows[j-1], rows[j]
			aClaim, aHas := claimedOrder[a.AccountID]
			bClaim, bHas := claimedOrder[b.AccountID]
			if !aHas {
				aClaim = math.MaxInt32
			}
			if !bHas {
				bClaim = math.MaxInt32
			}
			less := false
			if aClaim != bClaim {
				less = aClaim < bClaim
			} else if a.UpdatedAt != b.UpdatedAt {
				less = a.UpdatedAt < b.UpdatedAt
			} else {
				less = a.AccountID < b.AccountID
			}
			if !less {
				rows[j-1], rows[j] = rows[j], rows[j-1]
				continue
			}
			break
		}
	}
}

const qualitySelectColumns = `account_id, system_account_id, provider_code, quality_score, quality_state,
  recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
  recent_avg_first_token_ms, ewma_first_token_ms, success_rate,
  window_started_at, window_ended_at, last_sample_at, last_success_at, last_error_at, last_error_message, updated_at`

func scanQualityRow(rows *sql.Rows) (QualityRow, error) {
	var (
		row                               QualityRow
		accountID, systemID, providerCode sql.NullString
		score, state                      sql.NullString
		requests, successes, errs, smpl   sql.NullInt64
		avg, ewma                         sql.NullInt64
		rate                              sql.NullFloat64
		windowStarted, windowEnded        sql.NullString
		lastSample, lastSuccess           sql.NullString
		lastErrorAt, lastErrorMessage     sql.NullString
		updatedAt                         sql.NullString
	)
	if err := rows.Scan(&accountID, &systemID, &providerCode, &score, &state, &requests, &successes, &errs, &smpl, &avg, &ewma, &rate, &windowStarted, &windowEnded, &lastSample, &lastSuccess, &lastErrorAt, &lastErrorMessage, &updatedAt); err != nil {
		return row, err
	}
	row.AccountID = accountID.String
	row.SystemAccountID = systemID.String
	row.ProviderCode = providerCode.String
	row.QualityScore = integerOrDefault(score, UnknownQualityScore)
	row.QualityState = normalizeQualityState(state.String)
	row.RecentRequestCount = integerOrDefaultInt(requests, 0)
	row.RecentSuccessCount = integerOrDefaultInt(successes, 0)
	row.RecentErrorCount = integerOrDefaultInt(errs, 0)
	row.RecentFirstTokenSampleCount = integerOrDefaultInt(smpl, 0)
	if avg.Valid {
		v := avg.Int64
		row.RecentAvgFirstTokenMs = &v
	}
	if ewma.Valid {
		v := ewma.Int64
		row.EwmaFirstTokenMs = &v
	}
	if rate.Valid {
		v := clampRate(rate.Float64)
		row.SuccessRate = &v
	}
	row.WindowStartedAt = windowStarted.String
	row.WindowEndedAt = windowEnded.String
	row.LastSampleAt = nullableText(lastSample)
	row.LastSuccessAt = nullableText(lastSuccess)
	row.LastErrorAt = nullableText(lastErrorAt)
	row.LastErrorMessage = nullableText(lastErrorMessage)
	row.UpdatedAt = updatedAt.String
	return row, nil
}

func integerOrDefault(value sql.NullString, fallback int64) int64 {
	if !value.Valid {
		return fallback
	}
	return integerOrDefaultText(value.String, fallback)
}

func integerOrDefaultInt(value sql.NullInt64, fallback int64) int64 {
	if !value.Valid {
		return fallback
	}
	if value.Int64 < 0 {
		return 0
	}
	return value.Int64
}

func integerOrDefaultText(text string, fallback int64) int64 {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return fallback
	}
	var value float64
	if _, err := fmt.Sscanf(trimmed, "%g", &value); err != nil {
		return fallback
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	rounded := int64(math.Round(value))
	if rounded < 0 {
		return 0
	}
	return rounded
}

func normalizeQualityState(value string) QualityState {
	switch QualityState(value) {
	case QualityFresh, QualityStale, QualityFailed, QualityUnknown:
		return QualityState(value)
	default:
		return QualityUnknown
	}
}

// cleanupInactiveQualityRows / cleanupInactiveQualityMinuteRows /
// cleanupOldQualityMinuteRows：与 Node 同名函数一致——候选读出后按业务
// 元数据过滤，仅删除业务库已不存在的账户。
func (s *StatsStore) cleanupInactiveQualityRows(ctx context.Context, tx *sql.Tx, limit int) (int64, error) {
	query := fmt.Sprintf(`SELECT account_id FROM %s ORDER BY updated_at ASC, account_id ASC LIMIT ?`, s.scoresTable())
	rows, err := tx.QueryContext(ctx, query, clampLimit(limit))
	if err != nil {
		return 0, fmt.Errorf("读取质量行候选失败: %w", err)
	}
	var candidateIds []string
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		candidateIds = append(candidateIds, id.String)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	candidateIds = uniqueIds(candidateIds)
	if len(candidateIds) == 0 {
		return 0, nil
	}
	active, err := s.business.LoadAccountMetadataByIds(ctx, candidateIds)
	if err != nil {
		return 0, err
	}
	var changes int64
	for _, chunk := range chunkStrings(candidateIds, QualityLookupChunkSize) {
		var inactive []string
		for _, id := range chunk {
			if _, ok := active[id]; !ok {
				inactive = append(inactive, id)
			}
		}
		if len(inactive) == 0 {
			continue
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(inactive)), ", ")
		result, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE account_id IN (%s)`, s.scoresTable(), placeholders), toAnySlice(inactive)...)
		if err != nil {
			return changes, fmt.Errorf("清理失效账户质量行失败: %w", err)
		}
		affected, _ := result.RowsAffected()
		changes += affected
	}
	return changes, nil
}

func (s *StatsStore) cleanupInactiveQualityMinuteRows(ctx context.Context, tx *sql.Tx, limit int) error {
	query := fmt.Sprintf(`SELECT DISTINCT account_id FROM %s ORDER BY account_id ASC LIMIT ?`, s.minuteTable())
	rows, err := tx.QueryContext(ctx, query, clampLimit(limit))
	if err != nil {
		return fmt.Errorf("读取质量分钟行候选失败: %w", err)
	}
	var candidateIds []string
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		candidateIds = append(candidateIds, id.String)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	candidateIds = uniqueIds(candidateIds)
	if len(candidateIds) == 0 {
		return nil
	}
	active, err := s.business.LoadAccountMetadataByIds(ctx, candidateIds)
	if err != nil {
		return err
	}
	for _, chunk := range chunkStrings(candidateIds, QualityLookupChunkSize) {
		var inactive []string
		for _, id := range chunk {
			if _, ok := active[id]; !ok {
				inactive = append(inactive, id)
			}
		}
		if len(inactive) == 0 {
			continue
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(inactive)), ", ")
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE account_id IN (%s)`, s.minuteTable(), placeholders), toAnySlice(inactive)...); err != nil {
			return fmt.Errorf("清理失效账户质量分钟行失败: %w", err)
		}
	}
	return nil
}

func (s *StatsStore) cleanupOldQualityMinuteRows(ctx context.Context, tx *sql.Tx, cutoffMinute string, limit int) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE stat_minute < ?`, s.minuteTable())
	if _, err := tx.ExecContext(ctx, query, cutoffMinute); err != nil {
		return fmt.Errorf("清理过期质量分钟行失败: %w", err)
	}
	return nil
}

func (s *StatsStore) deleteDirtyAccountRows(ctx context.Context, tx *sql.Tx, accountIds []string) error {
	for _, chunk := range chunkStrings(accountIds, QualityLookupChunkSize) {
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(chunk)), ", ")
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE account_id IN (%s)`, s.dirtyTable(), placeholders), toAnySlice(chunk)...); err != nil {
			return fmt.Errorf("删除质量脏账户失败: %w", err)
		}
	}
	return nil
}

// MarkQualityDirty 供测试与运行时标脏使用（等价网关侧的 dirty 写入）。
func (s *StatsStore) MarkQualityDirty(ctx context.Context, accountID string) error {
	now := FormatMillis(s.now())
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
	INSERT INTO %s (account_id, first_dirty_at, updated_at) VALUES (?, ?, ?)
	ON CONFLICT(account_id) DO NOTHING
	`, s.dirtyTable()), accountID, now, now)
	return err
}

// LoadQualityRow 供测试与运行态读取单行。
func (s *StatsStore) LoadQualityRow(ctx context.Context, accountID string) (*QualityRow, error) {
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE account_id = ? LIMIT 1`, qualitySelectColumns, s.scoresTable())
	rows, err := s.db.QueryContext(ctx, query, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		row, err := scanQualityRow(rows)
		return &row, err
	}
	return nil, rows.Err()
}

// ---------------------------------------------------------------------------
// 底层辅助

func (s *StatsStore) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func clampLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	return limit
}

func uniqueIds(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func toSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func chunkStrings(values []string, size int) [][]string {
	if len(values) == 0 {
		return nil
	}
	var out [][]string
	for i := 0; i < len(values); i += size {
		end := i + size
		if end > len(values) {
			end = len(values)
		}
		out = append(out, values[i:end])
	}
	return out
}

func toAnySlice(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// FormatMillis 等价 Node toISOString()。
func FormatMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// ParseMillisField 解析 RFC3339 文本并校验。
func ParseMillisField(value, field string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s 必须是带 Z 或数值 offset 的 RFC3339 时间：%s", field, value)
	}
	return t, nil
}

func parseMillis(value string) time.Time {
	t, _ := time.Parse(time.RFC3339, value)
	return t
}


// 质量统计 schema（与 Node stats-schema.ts 一致；PG 版对应 maintenance 冻结 DDL）。
const sqliteQualitySchema = `
CREATE TABLE IF NOT EXISTS account_quality_minute_stats (
  account_id TEXT NOT NULL,
  system_account_id TEXT NOT NULL,
  provider_code TEXT NOT NULL,
  stat_minute TEXT NOT NULL,
  request_count INTEGER NOT NULL DEFAULT 0,
  success_count INTEGER NOT NULL DEFAULT 0,
  error_count INTEGER NOT NULL DEFAULT 0,
  first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
  first_token_ms_count INTEGER NOT NULL DEFAULT 0,
  last_sample_at TEXT,
  last_success_at TEXT,
  last_error_at TEXT,
  last_error_message TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (account_id, stat_minute)
);

CREATE TABLE IF NOT EXISTS account_quality_scores (
  account_id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL,
  provider_code TEXT NOT NULL,
  quality_score INTEGER NOT NULL DEFAULT 1000000,
  quality_state TEXT NOT NULL DEFAULT 'unknown',
  recent_request_count INTEGER NOT NULL DEFAULT 0,
  recent_success_count INTEGER NOT NULL DEFAULT 0,
  recent_error_count INTEGER NOT NULL DEFAULT 0,
  recent_first_token_sample_count INTEGER NOT NULL DEFAULT 0,
  recent_avg_first_token_ms INTEGER,
  ewma_first_token_ms INTEGER,
  success_rate REAL,
  window_started_at TEXT NOT NULL,
  window_ended_at TEXT NOT NULL,
  last_sample_at TEXT,
  last_success_at TEXT,
  last_error_at TEXT,
  last_error_message TEXT,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS account_quality_dirty_accounts (
  account_id TEXT PRIMARY KEY,
  first_dirty_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_account_quality_minute_stats_minute ON account_quality_minute_stats(stat_minute, account_id);

CREATE INDEX IF NOT EXISTS idx_account_quality_scores_sort ON account_quality_scores(provider_code, quality_score, quality_state);

CREATE INDEX IF NOT EXISTS idx_account_quality_scores_failure_precheck
  ON account_quality_scores(recent_error_count DESC, success_rate, updated_at DESC, account_id)
  WHERE recent_request_count >= 5 AND recent_error_count >= 2;

CREATE INDEX IF NOT EXISTS idx_account_quality_dirty_accounts_first_dirty ON account_quality_dirty_accounts(first_dirty_at, account_id);
`

const postgresQualitySchema = `
CREATE TABLE IF NOT EXISTS juhe_stats.account_quality_minute_stats (
  account_id text NOT NULL,
  system_account_id text NOT NULL,
  provider_code text NOT NULL,
  stat_minute text NOT NULL,
  request_count integer NOT NULL DEFAULT 0,
  success_count integer NOT NULL DEFAULT 0,
  error_count integer NOT NULL DEFAULT 0,
  first_token_ms_sum integer NOT NULL DEFAULT 0,
  first_token_ms_count integer NOT NULL DEFAULT 0,
  last_sample_at text,
  last_success_at text,
  last_error_at text,
  last_error_message text,
  updated_at text NOT NULL,
  PRIMARY KEY (account_id, stat_minute)
);

CREATE TABLE IF NOT EXISTS juhe_stats.account_quality_scores (
  account_id text PRIMARY KEY,
  system_account_id text NOT NULL,
  provider_code text NOT NULL,
  quality_score integer NOT NULL DEFAULT 1000000,
  quality_state text NOT NULL DEFAULT 'unknown',
  recent_request_count integer NOT NULL DEFAULT 0,
  recent_success_count integer NOT NULL DEFAULT 0,
  recent_error_count integer NOT NULL DEFAULT 0,
  recent_first_token_sample_count integer NOT NULL DEFAULT 0,
  recent_avg_first_token_ms integer,
  ewma_first_token_ms integer,
  success_rate double precision,
  window_started_at text NOT NULL,
  window_ended_at text NOT NULL,
  last_sample_at text,
  last_success_at text,
  last_error_at text,
  last_error_message text,
  updated_at text NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_stats.account_quality_dirty_accounts (
  account_id text PRIMARY KEY,
  first_dirty_at text NOT NULL,
  updated_at text NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_account_quality_minute_stats_minute ON juhe_stats.account_quality_minute_stats(stat_minute, account_id);
CREATE INDEX IF NOT EXISTS idx_account_quality_scores_sort ON juhe_stats.account_quality_scores(provider_code, quality_score, quality_state);
CREATE INDEX IF NOT EXISTS idx_account_quality_dirty_accounts_first_dirty ON juhe_stats.account_quality_dirty_accounts(first_dirty_at, account_id);
`
