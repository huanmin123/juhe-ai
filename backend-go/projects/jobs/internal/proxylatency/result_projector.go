package proxylatency

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/schedulejitter"
)

const defaultResultProjectionConsumer = "juhe-ai-proxy-latency-go-projector-v1"

type ProjectionDisposition string

const (
	ProjectionApplied  ProjectionDisposition = "applied"
	ProjectionStale    ProjectionDisposition = "stale"
	ProjectionIgnored  ProjectionDisposition = "ignored"
	ProjectionRejected ProjectionDisposition = "rejected"
)

type ProjectionResult struct {
	OutcomeID    string
	ProxyID      string
	InputVersion int64
	Disposition  ProjectionDisposition
	Changed      bool
	Reason       string
}

// ResultProjectorConfig controls only the Go-owned result writer. The source
// records stay in the jobs Store; the cursor, receipts and proxy state live in
// the business schema so a committed outcome can be recovered after a crash.
type ResultProjectorConfig struct {
	ConsumerKey  string
	PollInterval time.Duration
	BatchSize    int
	Now          func() time.Time
}

// ResultProjector is the sole J3a business writer. It replaces the former
// Node outcome reader/projector and never calls the Node DB-service IPC.
type ResultProjector struct {
	store    *Store
	business *sql.DB
	mode     StoreMode
	cfg      ResultProjectorConfig
	logger   *slog.Logger

	mu          sync.RWMutex
	lastSuccess time.Time
	lastError   string
}

func NewResultProjector(store *Store, business *sql.DB, cfg ResultProjectorConfig, logger *slog.Logger) (*ResultProjector, error) {
	if store == nil || store.db == nil || business == nil {
		return nil, errors.New("J3a Go result projector 未初始化")
	}
	if store.mode != StorePostgres && store.mode != StoreSQLite {
		return nil, errors.New("J3a Go result projector Store mode 无效")
	}
	if strings.TrimSpace(cfg.ConsumerKey) == "" {
		cfg.ConsumerKey = defaultResultProjectionConsumer
	}
	if len(cfg.ConsumerKey) > 200 {
		return nil, errors.New("J3a Go result projector consumer key 无效")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.PollInterval < 100*time.Millisecond || cfg.PollInterval > time.Minute {
		return nil, errors.New("J3a Go result projector poll interval 无效")
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = defaultProxyLatencyBatchSize
	}
	if cfg.BatchSize < 1 || cfg.BatchSize > maxProxyLatencyWorkItems {
		return nil, errors.New("J3a Go result projector batch size 无效")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ResultProjector{store: store, business: business, mode: store.mode, cfg: cfg, logger: logger}, nil
}

// CheckContract proves that the Go result role can use exactly the existing
// J3a business projection tables, proxy test-state columns, and the minimal
// account-availability dirty path fired by proxy_profiles updates. It performs
// zero-row statements only; role provisioning is never repaired implicitly.
func (p *ResultProjector) CheckContract(ctx context.Context) error {
	if p == nil || p.business == nil {
		return errors.New("J3a Go result projector 未初始化")
	}
	tx, err := p.business.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始 J3a Go result projector 契约事务失败: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range p.contractStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("J3a Go result projector 契约不满足: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 J3a Go result projector 契约事务失败: %w", err)
	}
	return nil
}

func (p *ResultProjector) contractStatements() []string {
	if p.mode == StorePostgres {
		return []string{
			"SELECT id,test_status,latency_ms,outbound_ip,outbound_region,last_test_message,last_tested_at,updated_at FROM juhe_business.proxy_profiles LIMIT 0",
			"SELECT outcome_id,proxy_id,input_version,disposition,reason,applied_at FROM juhe_business.proxy_latency_projection_receipts LIMIT 0",
			"SELECT consumer_key,stored_at,outcome_id,updated_at FROM juhe_business.proxy_latency_projection_cursors LIMIT 0",
			"SELECT id,proxy_profile_id,authorization_instance_source_account_id,system_account_id FROM juhe_business.accounts LIMIT 0",
			"SELECT account_id,source_generation FROM juhe_business.account_list_availability_projections LIMIT 0",
			"SELECT account_id,generation,available_at_ms FROM juhe_business.account_list_availability_dirty LIMIT 0",
			"UPDATE juhe_business.proxy_profiles SET test_status=test_status WHERE FALSE",
			"INSERT INTO juhe_business.proxy_latency_projection_receipts(outcome_id,proxy_id,input_version,disposition,reason,applied_at) SELECT '', '', 1, 'rejected', NULL, CURRENT_TIMESTAMP WHERE FALSE",
			"INSERT INTO juhe_business.proxy_latency_projection_cursors(consumer_key,stored_at,outcome_id,updated_at) SELECT '', NULL, NULL, CURRENT_TIMESTAMP WHERE FALSE",
			"INSERT INTO juhe_business.account_list_availability_dirty(account_id,viewer_system_account_id,generation,applied_generation,reason,available_at_ms,claim_token,claimed_by,claim_until_ms,attempt_count,created_at_ms,updated_at_ms) SELECT '', '', 1, 0, 'j3a_contract_check', 0, NULL, NULL, NULL, 0, 0, 0 WHERE FALSE",
			"UPDATE juhe_business.account_list_availability_dirty SET generation=generation WHERE FALSE",
		}
	}
	return []string{
		"SELECT id,test_status,latency_ms,outbound_ip,outbound_region,last_test_message,last_tested_at,updated_at FROM proxy_profiles LIMIT 0",
		"SELECT outcome_id,proxy_id,input_version,disposition,reason,applied_at FROM proxy_latency_projection_receipts LIMIT 0",
		"SELECT consumer_key,stored_at,outcome_id,updated_at FROM proxy_latency_projection_cursors LIMIT 0",
		"UPDATE proxy_profiles SET test_status=test_status WHERE FALSE",
		"INSERT INTO proxy_latency_projection_receipts(outcome_id,proxy_id,input_version,disposition,reason,applied_at) SELECT '', '', 1, 'rejected', NULL, CURRENT_TIMESTAMP WHERE FALSE",
		"INSERT INTO proxy_latency_projection_cursors(consumer_key,stored_at,outcome_id,updated_at) SELECT '', NULL, NULL, CURRENT_TIMESTAMP WHERE FALSE",
	}
}

func (p *ResultProjector) Ready() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastError == "" && !p.lastSuccess.IsZero() && p.cfg.Now().UTC().Sub(p.lastSuccess) <= p.cfg.PollInterval*2+time.Second
}

func (p *ResultProjector) Run(ctx context.Context) error {
	if p == nil {
		return errors.New("J3a Go result projector 未初始化")
	}
	for {
		_, err := p.Drain(ctx)
		p.record(err)
		if err != nil && p.logger != nil {
			p.logger.Error("J3a Go result projection failed; immutable outcome is retained for retry", "error", err)
		}
		if err := waitRuntime(ctx, schedulejitter.Delay(p.cfg.PollInterval)); err != nil {
			return err
		}
	}
}

// Drain advances the Go-owned cursor in the same business transaction as the
// receipt and CAS result. A rejected payload deliberately remains unadvanced
// and causes fail-closed retry rather than being silently discarded.
func (p *ResultProjector) Drain(ctx context.Context) (int, error) {
	if p == nil || p.store == nil {
		return 0, errors.New("J3a Go result projector 未初始化")
	}
	cursor, err := p.currentCursor(ctx)
	if err != nil {
		return 0, err
	}
	stored, err := p.store.ListCommittedOutcomes(ctx, cursor, p.cfg.BatchSize)
	if err != nil {
		return 0, err
	}
	for index := range stored {
		result, err := p.projectStored(ctx, stored[index], true)
		if err != nil {
			return index, err
		}
		if result.Disposition == ProjectionRejected {
			return index, fmt.Errorf("J3a Go result projector rejected outcome %s: %s", result.OutcomeID, result.Reason)
		}
	}
	return len(stored), nil
}

// ProjectOutcome is used by the synchronous Go manual and periodic executor
// immediately after a durable outcome commit. It does not advance the global
// cursor out of order; the recovery loop later advances it with the receipt.
func (p *ResultProjector) ProjectOutcome(ctx context.Context, outcome Outcome) (ProjectionResult, error) {
	if p == nil || p.store == nil {
		return ProjectionResult{}, errors.New("J3a Go result projector 未初始化")
	}
	stored, found, err := p.store.FindCommittedOutcome(ctx, outcome.OutcomeID)
	if err != nil {
		return ProjectionResult{}, err
	}
	if !found || stored.RequestID != outcome.RequestID || stored.ProxyID != outcome.ProxyID || stored.InputVersion != outcome.InputVersion || stored.ConfigRevision != outcome.ConfigRevision {
		return ProjectionResult{}, errors.New("J3a Go result projector 未找到匹配的 committed outcome")
	}
	result, err := p.projectStored(ctx, stored, false)
	p.record(err)
	if err != nil {
		return ProjectionResult{}, err
	}
	if result.Disposition == ProjectionRejected {
		return result, fmt.Errorf("J3a Go result projector rejected outcome %s: %s", result.OutcomeID, result.Reason)
	}
	return result, nil
}

// ProjectManualNoTargets preserves the management API's no-provider report
// without reviving the removed Node writer. There is no durable probe outcome
// in this case, so no receipt/cursor is created; the direct CAS is explicit.
func (p *ResultProjector) ProjectManualNoTargets(ctx context.Context, request ManualRequest, observedAt time.Time) (ProjectionResult, error) {
	if p == nil || p.business == nil || observedAt.IsZero() {
		return ProjectionResult{}, errors.New("J3a Go no-target projection 参数无效")
	}
	if err := request.Validate(25 * time.Second); err != nil || len(request.Targets) != 0 {
		return ProjectionResult{}, errors.New("J3a Go no-target projection request 无效")
	}
	report := request.Report(Outcome{ProxyID: request.ProxyID, ObservedAt: observedAt.UTC(), OverallStatus: OverallUnknown})
	tx, err := p.business.BeginTx(ctx, nil)
	if err != nil {
		return ProjectionResult{}, fmt.Errorf("开始 J3a Go no-target projection 事务失败: %w", err)
	}
	defer tx.Rollback()
	proxyFound, configRevision, lastTestedAt, err := p.proxyFenceTx(ctx, tx, request.ProxyID)
	if err != nil {
		return ProjectionResult{}, err
	}
	if !proxyFound {
		if err := tx.Commit(); err != nil {
			return ProjectionResult{}, fmt.Errorf("提交 J3a Go no-target projection 事务失败: %w", err)
		}
		p.record(nil)
		return ProjectionResult{ProxyID: request.ProxyID, Disposition: ProjectionIgnored, Reason: "proxy_missing_or_deleted"}, nil
	}
	if !sameProjectionInstant(configRevision, request.ConfigRevision) {
		if err := tx.Commit(); err != nil {
			return ProjectionResult{}, fmt.Errorf("提交 J3a Go no-target projection 事务失败: %w", err)
		}
		p.record(nil)
		return ProjectionResult{ProxyID: request.ProxyID, Disposition: ProjectionStale, Reason: "config_revision_stale"}, nil
	}
	if lastTestedAt != nil && lastTestedAt.After(observedAt.UTC()) {
		if err := tx.Commit(); err != nil {
			return ProjectionResult{}, fmt.Errorf("提交 J3a Go no-target projection 事务失败: %w", err)
		}
		p.record(nil)
		return ProjectionResult{ProxyID: request.ProxyID, Disposition: ProjectionStale, Reason: "observed_at_stale"}, nil
	}
	result, err := p.applyStateUpdate(ctx, tx, request.ProxyID, request.ConfigRevision, observedAt.UTC(), projectionSummary{Status: string(report.Status), Message: report.Message})
	if err != nil {
		return ProjectionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectionResult{}, fmt.Errorf("提交 J3a Go no-target projection 事务失败: %w", err)
	}
	p.record(nil)
	projection := ProjectionResult{ProxyID: request.ProxyID, Disposition: result, Changed: result == ProjectionApplied}
	if result == ProjectionStale {
		projection.Reason = "projection_compare_and_set_missed"
	}
	return projection, nil
}

// ProjectManualOutbound persists optional outbound diagnostics only after the
// durable outcome's CAS projection. It never changes the proxy config epoch.
func (p *ResultProjector) ProjectManualOutbound(ctx context.Context, outcome Outcome, outboundIP, outboundRegion string) error {
	if p == nil || p.business == nil || (strings.TrimSpace(outboundIP) == "" && strings.TrimSpace(outboundRegion) == "") {
		return nil
	}
	tx, err := p.business.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始 J3a Go outbound projection 事务失败: %w", err)
	}
	defer tx.Rollback()
	query, args := p.manualOutboundUpdate(outcome, outboundIP, outboundRegion)
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("写入 J3a Go outbound diagnostics 失败: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取 J3a Go outbound diagnostics 写入结果失败: %w", err)
	}
	if changed != 1 {
		found, configRevision, lastTestedAt, fenceErr := p.proxyFenceTx(ctx, tx, outcome.ProxyID)
		if fenceErr != nil {
			return fenceErr
		}
		if !found {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("提交 J3a Go outbound missing 判定事务失败: %w", err)
			}
			return ErrManualProxyMissing
		}
		if !sameProjectionInstant(configRevision, outcome.ConfigRevision) || lastTestedAt == nil || !lastTestedAt.Equal(outcome.ObservedAt.UTC()) {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("提交 J3a Go outbound stale 判定事务失败: %w", err)
			}
			return ErrManualProjectionStale
		}
		return errors.New("J3a Go outbound diagnostics CAS 未命中")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 J3a Go outbound projection 事务失败: %w", err)
	}
	return nil
}

func (p *ResultProjector) projectStored(ctx context.Context, stored StoredOutcome, advance bool) (ProjectionResult, error) {
	if err := validateOutcome(stored.Outcome); err != nil {
		return ProjectionResult{}, err
	}
	tx, err := p.business.BeginTx(ctx, nil)
	if err != nil {
		return ProjectionResult{}, fmt.Errorf("开始 J3a Go result projection 事务失败: %w", err)
	}
	defer tx.Rollback()
	result, err := p.projectStoredTx(ctx, tx, stored)
	if err != nil {
		return ProjectionResult{}, err
	}
	if advance && result.Disposition != ProjectionRejected {
		if err := p.advanceCursorTx(ctx, tx, OutcomeCursor{StoredAt: stored.StoredAt, OutcomeID: stored.OutcomeID}); err != nil {
			return ProjectionResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ProjectionResult{}, fmt.Errorf("提交 J3a Go result projection 事务失败: %w", err)
	}
	return result, nil
}

func (p *ResultProjector) projectStoredTx(ctx context.Context, tx *sql.Tx, stored StoredOutcome) (ProjectionResult, error) {
	base := ProjectionResult{OutcomeID: stored.OutcomeID, ProxyID: stored.ProxyID, InputVersion: stored.InputVersion}
	existing, found, err := p.receiptTx(ctx, tx, stored.OutcomeID)
	if err != nil {
		return ProjectionResult{}, err
	}
	if found {
		return existing, nil
	}
	if reason := validateProjectionOutcome(stored.Outcome); reason != "" {
		if err := p.insertReceiptTx(ctx, tx, base, ProjectionRejected, reason); err != nil {
			return ProjectionResult{}, err
		}
		return ProjectionResult{OutcomeID: base.OutcomeID, ProxyID: base.ProxyID, InputVersion: base.InputVersion, Disposition: ProjectionRejected, Reason: reason}, nil
	}

	proxyFound, configRevision, lastTestedAt, err := p.proxyFenceTx(ctx, tx, stored.ProxyID)
	if err != nil {
		return ProjectionResult{}, err
	}
	if !proxyFound {
		return p.recordDispositionTx(ctx, tx, base, ProjectionIgnored, "proxy_missing_or_deleted")
	}
	if !sameProjectionInstant(configRevision, stored.ConfigRevision) {
		return p.recordDispositionTx(ctx, tx, base, ProjectionStale, "config_revision_stale")
	}
	if lastTestedAt != nil && lastTestedAt.After(stored.ObservedAt) {
		return p.recordDispositionTx(ctx, tx, base, ProjectionStale, "observed_at_stale")
	}
	summary := summarizeProjectionItems(stored.Items)
	disposition, err := p.applyStateUpdate(ctx, tx, stored.ProxyID, stored.ConfigRevision, stored.ObservedAt, summary)
	if err != nil {
		return ProjectionResult{}, err
	}
	if disposition == ProjectionStale {
		return p.recordDispositionTx(ctx, tx, base, ProjectionStale, "projection_compare_and_set_missed")
	}
	return p.recordDispositionTx(ctx, tx, base, ProjectionApplied, "")
}

func (p *ResultProjector) recordDispositionTx(ctx context.Context, tx *sql.Tx, base ProjectionResult, disposition ProjectionDisposition, reason string) (ProjectionResult, error) {
	if err := p.insertReceiptTx(ctx, tx, base, disposition, reason); err != nil {
		return ProjectionResult{}, err
	}
	return ProjectionResult{OutcomeID: base.OutcomeID, ProxyID: base.ProxyID, InputVersion: base.InputVersion, Disposition: disposition, Changed: disposition == ProjectionApplied, Reason: reason}, nil
}

func (p *ResultProjector) receiptTx(ctx context.Context, tx *sql.Tx, outcomeID string) (ProjectionResult, bool, error) {
	query := `SELECT outcome_id,proxy_id,input_version,disposition,reason FROM proxy_latency_projection_receipts WHERE outcome_id=?`
	if p.mode == StorePostgres {
		query = `SELECT outcome_id,proxy_id,input_version,disposition,reason FROM juhe_business.proxy_latency_projection_receipts WHERE outcome_id=$1 FOR UPDATE`
	}
	var result ProjectionResult
	var disposition string
	var reason sql.NullString
	err := tx.QueryRowContext(ctx, query, outcomeID).Scan(&result.OutcomeID, &result.ProxyID, &result.InputVersion, &disposition, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectionResult{}, false, nil
	}
	if err != nil {
		return ProjectionResult{}, false, fmt.Errorf("读取 J3a Go result receipt 失败: %w", err)
	}
	if !validProjectionDisposition(disposition) {
		return ProjectionResult{}, false, errors.New("J3a Go result receipt disposition 无效")
	}
	result.Disposition = ProjectionDisposition(disposition)
	result.Reason = reason.String
	return result, true, nil
}

func (p *ResultProjector) insertReceiptTx(ctx context.Context, tx *sql.Tx, base ProjectionResult, disposition ProjectionDisposition, reason string) error {
	if !validProjectionDisposition(string(disposition)) {
		return errors.New("J3a Go result receipt disposition 无效")
	}
	query := `INSERT INTO proxy_latency_projection_receipts(outcome_id,proxy_id,input_version,disposition,reason,applied_at) VALUES(?,?,?,?,?,?) ON CONFLICT(outcome_id) DO NOTHING`
	args := []any{base.OutcomeID, base.ProxyID, base.InputVersion, disposition, nullableString(reason), p.cfg.Now().UTC().Format(time.RFC3339Nano)}
	if p.mode == StorePostgres {
		query = `INSERT INTO juhe_business.proxy_latency_projection_receipts(outcome_id,proxy_id,input_version,disposition,reason,applied_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(outcome_id) DO NOTHING`
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("写入 J3a Go result receipt 失败: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取 J3a Go result receipt 写入结果失败: %w", err)
	}
	if changed == 1 {
		return nil
	}
	existing, found, err := p.receiptTx(ctx, tx, base.OutcomeID)
	if err != nil || !found || existing.ProxyID != base.ProxyID || existing.InputVersion != base.InputVersion || existing.Disposition != disposition || existing.Reason != reason {
		return errors.New("J3a Go result receipt 幂等性冲突")
	}
	return nil
}

func (p *ResultProjector) proxyFenceTx(ctx context.Context, tx *sql.Tx, proxyID string) (bool, string, *time.Time, error) {
	query := `SELECT id,updated_at,last_tested_at FROM proxy_profiles WHERE id=?`
	if p.mode == StorePostgres {
		query = `SELECT id,to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS updated_at,CASE WHEN last_tested_at IS NULL THEN NULL ELSE to_char(last_tested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END AS last_tested_at FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE`
	}
	var id, revision string
	var last sql.NullString
	err := tx.QueryRowContext(ctx, query, proxyID).Scan(&id, &revision, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil, nil
	}
	if err != nil {
		return false, "", nil, fmt.Errorf("读取 J3a Go proxy CAS fence 失败: %w", err)
	}
	if strings.TrimSpace(id) == "" || !validProjectionInstant(revision) {
		return false, "", nil, errors.New("J3a Go proxy CAS fence 无效")
	}
	if !last.Valid {
		return true, revision, nil, nil
	}
	parsed, err := parseProxyLatencyUTC(last.String, "last_tested_at")
	if err != nil {
		return false, "", nil, err
	}
	return true, revision, &parsed, nil
}

func (p *ResultProjector) applyStateUpdate(ctx context.Context, tx *sql.Tx, proxyID, configRevision string, observedAt time.Time, summary projectionSummary) (ProjectionDisposition, error) {
	if !validProjectionInstant(configRevision) || observedAt.IsZero() || !validProjectionStatus(summary.Status) {
		return ProjectionRejected, errors.New("J3a Go proxy state projection 参数无效")
	}
	var latency any
	if summary.LatencyMS != nil {
		latency = *summary.LatencyMS
	}
	query := `UPDATE proxy_profiles SET test_status=?,latency_ms=?,last_test_message=?,last_tested_at=? WHERE id=? AND updated_at=? AND (last_tested_at IS NULL OR last_tested_at<=?)`
	args := []any{summary.Status, latency, summary.Message, observedAt.UTC().Format(time.RFC3339Nano), proxyID, configRevision, observedAt.UTC().Format(time.RFC3339Nano)}
	if p.mode == StorePostgres {
		query = `UPDATE juhe_business.proxy_profiles SET test_status=$1,latency_ms=$2,last_test_message=$3,last_tested_at=$4 WHERE id=$5 AND updated_at=$6::timestamptz AND (last_tested_at IS NULL OR last_tested_at<=$4)`
		args = []any{summary.Status, latency, summary.Message, observedAt.UTC(), proxyID, configRevision}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return ProjectionRejected, fmt.Errorf("写入 J3a Go proxy state 失败: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ProjectionRejected, fmt.Errorf("读取 J3a Go proxy state 写入结果失败: %w", err)
	}
	if changed != 1 {
		return ProjectionStale, nil
	}
	return ProjectionApplied, nil
}

func (p *ResultProjector) manualOutboundUpdate(outcome Outcome, outboundIP, outboundRegion string) (string, []any) {
	if p.mode == StorePostgres {
		if strings.TrimSpace(outboundIP) != "" && strings.TrimSpace(outboundRegion) != "" {
			return `UPDATE juhe_business.proxy_profiles SET outbound_ip=$1,outbound_region=$2 WHERE id=$3 AND updated_at=$4::timestamptz AND last_tested_at=$5`, []any{outboundIP, outboundRegion, outcome.ProxyID, outcome.ConfigRevision, outcome.ObservedAt.UTC()}
		}
		if strings.TrimSpace(outboundIP) != "" {
			return `UPDATE juhe_business.proxy_profiles SET outbound_ip=$1 WHERE id=$2 AND updated_at=$3::timestamptz AND last_tested_at=$4`, []any{outboundIP, outcome.ProxyID, outcome.ConfigRevision, outcome.ObservedAt.UTC()}
		}
		return `UPDATE juhe_business.proxy_profiles SET outbound_region=$1 WHERE id=$2 AND updated_at=$3::timestamptz AND last_tested_at=$4`, []any{outboundRegion, outcome.ProxyID, outcome.ConfigRevision, outcome.ObservedAt.UTC()}
	}
	observed := outcome.ObservedAt.UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(outboundIP) != "" && strings.TrimSpace(outboundRegion) != "" {
		return `UPDATE proxy_profiles SET outbound_ip=?,outbound_region=? WHERE id=? AND updated_at=? AND last_tested_at=?`, []any{outboundIP, outboundRegion, outcome.ProxyID, outcome.ConfigRevision, observed}
	}
	if strings.TrimSpace(outboundIP) != "" {
		return `UPDATE proxy_profiles SET outbound_ip=? WHERE id=? AND updated_at=? AND last_tested_at=?`, []any{outboundIP, outcome.ProxyID, outcome.ConfigRevision, observed}
	}
	return `UPDATE proxy_profiles SET outbound_region=? WHERE id=? AND updated_at=? AND last_tested_at=?`, []any{outboundRegion, outcome.ProxyID, outcome.ConfigRevision, observed}
}

func (p *ResultProjector) currentCursor(ctx context.Context) (*OutcomeCursor, error) {
	query := `SELECT stored_at,outcome_id FROM proxy_latency_projection_cursors WHERE consumer_key=?`
	if p.mode == StorePostgres {
		query = `SELECT stored_at,outcome_id FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1`
	}
	var storedAt, outcomeID sql.NullString
	err := p.business.QueryRowContext(ctx, query, p.cfg.ConsumerKey).Scan(&storedAt, &outcomeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 J3a Go result cursor 失败: %w", err)
	}
	if !storedAt.Valid && !outcomeID.Valid {
		return nil, nil
	}
	if !storedAt.Valid || !outcomeID.Valid || strings.TrimSpace(outcomeID.String) == "" {
		return nil, errors.New("J3a Go result cursor 存储损坏")
	}
	parsed, err := parseProxyLatencyUTC(storedAt.String, "stored_at")
	if err != nil {
		return nil, err
	}
	return &OutcomeCursor{StoredAt: parsed, OutcomeID: outcomeID.String}, nil
}

func (p *ResultProjector) advanceCursorTx(ctx context.Context, tx *sql.Tx, next OutcomeCursor) error {
	if next.StoredAt.IsZero() || strings.TrimSpace(next.OutcomeID) == "" {
		return errors.New("J3a Go result cursor 参数无效")
	}
	query := `SELECT stored_at,outcome_id FROM proxy_latency_projection_cursors WHERE consumer_key=?`
	if p.mode == StorePostgres {
		query = `SELECT stored_at,outcome_id FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1 FOR UPDATE`
	}
	var storedAt, outcomeID sql.NullString
	err := tx.QueryRowContext(ctx, query, p.cfg.ConsumerKey).Scan(&storedAt, &outcomeID)
	if !errors.Is(err, sql.ErrNoRows) && err != nil {
		return fmt.Errorf("锁定 J3a Go result cursor 失败: %w", err)
	}
	nextStoredAt := next.StoredAt.UTC().Format(time.RFC3339Nano)
	now := p.cfg.Now().UTC().Format(time.RFC3339Nano)
	if errors.Is(err, sql.ErrNoRows) {
		insert := `INSERT INTO proxy_latency_projection_cursors(consumer_key,stored_at,outcome_id,updated_at) VALUES(?,?,?,?)`
		if p.mode == StorePostgres {
			insert = `INSERT INTO juhe_business.proxy_latency_projection_cursors(consumer_key,stored_at,outcome_id,updated_at) VALUES($1,$2,$3,$4)`
		}
		if _, err := tx.ExecContext(ctx, insert, p.cfg.ConsumerKey, nextStoredAt, next.OutcomeID, now); err != nil {
			return fmt.Errorf("创建 J3a Go result cursor 失败: %w", err)
		}
		return nil
	}
	if !storedAt.Valid || !outcomeID.Valid {
		return errors.New("J3a Go result cursor 存储损坏")
	}
	currentAt, err := parseProxyLatencyUTC(storedAt.String, "stored_at")
	if err != nil {
		return err
	}
	comparison := compareOutcomeCursor(OutcomeCursor{StoredAt: currentAt, OutcomeID: outcomeID.String}, next)
	if comparison > 0 {
		return errors.New("J3a Go result cursor 不允许倒退")
	}
	if comparison == 0 {
		return nil
	}
	update := `UPDATE proxy_latency_projection_cursors SET stored_at=?,outcome_id=?,updated_at=? WHERE consumer_key=?`
	args := []any{nextStoredAt, next.OutcomeID, now, p.cfg.ConsumerKey}
	if p.mode == StorePostgres {
		update = `UPDATE juhe_business.proxy_latency_projection_cursors SET stored_at=$1,outcome_id=$2,updated_at=$3 WHERE consumer_key=$4`
	}
	result, err := tx.ExecContext(ctx, update, args...)
	if err != nil {
		return fmt.Errorf("推进 J3a Go result cursor 失败: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errors.New("J3a Go result cursor 未前进")
	}
	return nil
}

type projectionSummary struct {
	Status    string
	LatencyMS *int64
	Message   string
}

func summarizeProjectionItems(items []ItemResult) projectionSummary {
	baseStatus, average := projectionBase(items)
	all := make([]ItemStatus, 0, len(items)+1)
	all = append(all, baseStatus)
	for _, item := range items {
		all = append(all, item.Status)
	}
	passed, warning, failed, unknown := 0, 0, 0, 0
	for _, status := range all {
		switch status {
		case ItemPassed:
			passed++
		case ItemWarning:
			warning++
		case ItemFailed:
			failed++
		default:
			unknown++
		}
	}
	status := OverallUnknown
	if failed > 0 {
		status = OverallFailed
	} else if warning > 0 || (passed > 0 && unknown > 0) {
		status = OverallWarning
	} else if unknown == 0 && passed > 0 {
		status = OverallPassed
	}
	message := "代理检测未形成有效传输尝试"
	switch status {
	case OverallPassed:
		message = "代理质量检测通过"
	case OverallWarning:
		message = fmt.Sprintf("代理可用，存在 %d 项告警", warning)
	case OverallFailed:
		message = fmt.Sprintf("代理检测存在 %d 项失败", failed)
	}
	return projectionSummary{Status: string(status), LatencyMS: average, Message: message}
}

func projectionBase(items []ItemResult) (ItemStatus, *int64) {
	failed, unknown, reachable := 0, 0, 0
	var total int64
	latencies := int64(0)
	for _, item := range items {
		switch item.Status {
		case ItemFailed:
			failed++
		case ItemUnknown:
			unknown++
		case ItemPassed:
			reachable++
		}
		if (item.Status == ItemPassed || item.Status == ItemWarning) && item.LatencyMS >= 0 {
			total += item.LatencyMS
			latencies++
		}
	}
	status := ItemUnknown
	if failed == 0 && unknown == 0 {
		status = ItemPassed
	} else if reachable > 0 {
		status = ItemWarning
	} else if failed > 0 {
		status = ItemFailed
	}
	if latencies == 0 {
		return status, nil
	}
	average := (total + latencies/2) / latencies
	return status, &average
}

func validateProjectionOutcome(outcome Outcome) string {
	if err := validateOutcome(outcome); err != nil {
		return "outcome_contract_invalid"
	}
	if outcome.Trigger != TriggerPeriodic && outcome.Trigger != TriggerManual {
		return "trigger_not_allowed"
	}
	if len(outcome.Items) == 0 {
		return "outcome_items_missing"
	}
	if SummarizeItems(outcome.Items) != outcome.OverallStatus {
		return "overall_status_mismatch"
	}
	return ""
}

func sameProjectionInstant(left, right string) bool {
	leftAt, leftErr := parseProxyLatencyUTC(left, "timestamp")
	rightAt, rightErr := parseProxyLatencyUTC(right, "timestamp")
	return leftErr == nil && rightErr == nil && leftAt.Equal(rightAt)
}

func validProjectionInstant(value string) bool {
	_, err := parseProxyLatencyUTC(value, "timestamp")
	return err == nil
}

func validProjectionStatus(value string) bool {
	return value == string(OverallPassed) || value == string(OverallWarning) || value == string(OverallFailed) || value == string(OverallUnknown)
}

func validProjectionDisposition(value string) bool {
	return value == string(ProjectionApplied) || value == string(ProjectionStale) || value == string(ProjectionIgnored) || value == string(ProjectionRejected)
}

func compareOutcomeCursor(left, right OutcomeCursor) int {
	if left.StoredAt.Before(right.StoredAt) {
		return -1
	}
	if left.StoredAt.After(right.StoredAt) {
		return 1
	}
	return strings.Compare(left.OutcomeID, right.OutcomeID)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (p *ResultProjector) record(err error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.lastError = err.Error()
		return
	}
	p.lastError = ""
	p.lastSuccess = p.cfg.Now().UTC()
}
