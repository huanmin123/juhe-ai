package accounts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// REFACTOR-0005 阶段 0：错误三件套、RevisionConflictMessage、AccessScope、
// queryer 已下沉中立包 accountscore；根包保留别名，外部 accounts.XXX 消费面
// 零改动。
type (
	// ConflictError maps to the Node route family 409 paths: the owner-scoped
	// duplicate account name error.
	ConflictError = accountscore.ConflictError
	// ValidationError maps to the Node 400 mutation message set surfaced through
	// the body validation and repository normalization layers.
	ValidationError = accountscore.ValidationError
	// RevisionConflictError maps to AccountManagementPatchRevisionConflictError /
	// the lock config-revision CAS failures: the route family renders 409 with the
	// Node copy ('账户配置已被其他操作更新，请刷新后重试' for the patch, the lock
	// copies for the lock family).
	RevisionConflictError = accountscore.RevisionConflictError
	// AccessScope mirrors storage/access-scope.ts for the accounts slice: admins
	// see every row unless a systemAccountId filter narrows the view; users are
	// pinned to their own rows (forceSelfAccessScope).
	AccessScope = accountscore.AccessScope
	// queryer abstracts *sql.DB / *sql.Tx so transactional paths never touch
	// s.db while a transaction is in flight.
	queryer = accountscore.Queryer
)

// RevisionConflictMessage mirrors the accounts.routes.ts PATCH catch copy.
const RevisionConflictMessage = accountscore.RevisionConflictMessage

// Lock messages mirror account-lock.routes.ts.
const (
	lockNotFoundMessage       = "账户不存在或无权操作"
	lockConfigConflictMessage = "账户配置已发生并发变更，请刷新列表后重试"
	lockStateConflictMessage  = "账户锁死状态已发生并发变更，请刷新列表后重试"
)

// Tag messages mirror account-tags.routes.ts.
const (
	tagNotFoundMessage   = "标签不存在"
	tagInUseMessage      = "标签已绑定账户，不能删除"
	maxTagsPerAccount    = accountscore.MaxTagsPerAccount
	maxTagNameLength     = accountscore.MaxTagNameLength
	maxAccountNameLength = 128
)

// Store is the accounts persistence over PostgreSQL. secret
// is the Node runtimeConfig.secret material: accounts.credentials_encrypted
// rows written by Node must stay decryptable.
type Store struct {
	db     *sql.DB
	secret string
	now    func() time.Time
	newI   func(prefix string) string
	// statsDB is the dedicated juhe_stats handle behind the
	// account_usage_snapshots reads/writes (oauth_usage_snapshot,
	// balance snapshot record, superseded-snapshot cleanup). Nil until
	// AttachStatsDatabase wires it; nil keeps those statements on the
	// business handle (PostgreSQL shares the pool; single-file tests).
	statsDB *sql.DB
	// authorized is the M10 authorized-instance reader (authz slice, narrow
	// interface). Nil until SetAuthorizedReader / Deps.Mount wires it.
	authorized AuthorizedAccountReader
	// invalidator is the batch-edit post-commit cache invalidation port
	// (batch_effects.go). Nil until SetCacheInvalidator wires it; a nil port
	// keeps the batch self-contained.
	invalidator CacheInvalidator
	// runtimeEffects is the runtime-reset port (runtime_reset_effects.go). Nil
	// until SetRuntimeResetEffects wires it; a nil port keeps the endpoint
	// self-contained (tests) and reports the runtime surfaces unavailable.
	runtimeEffects RuntimeResetEffects
	// backgroundProbeRuntimeCleanup is the background-probe disable-arm
	// runtime cleanup port (background_probe_disable_effects.go). Nil until
	// SetBackgroundProbeRuntimeCleanup wires it; a nil port keeps the disable
	// arm DB-only and the audit notes the degradation.
	backgroundProbeRuntimeCleanup BackgroundProbeRuntimeCleanup
	// testEffects is the manual-test dispatch port (test_effects.go). Nil
	// until SetTestDispatchEffects wires it; a nil port keeps the diagnostic
	// family self-contained (tests) and renders dispatch unavailable.
	testEffects TestDispatchEffects
	// m11 advanced/balance/traffic/return ports (nil until wired via the
	// matching setters; each keeps its endpoint self-contained with the Node
	// failure shapes).
	apiKeyRuntimeDetails   APIKeyRuntimeDetailsReader
	balanceRefresher       ManualBalanceRefresher
	modelCatalogRefresher  ModelCatalogRefresher
	returner               AuthorizationGrantReturner
	trafficRuntimeMigrator TrafficRuntimeMigrator
	// balanceSnapshotCleaner is the post-commit balance snapshot cleanup port
	// (balance_snapshot_cleanup.go). Nil until SetBalanceSnapshotCleaner
	// wires it; a nil port keeps PATCH cache/snapshot silent.
	balanceSnapshotCleaner BalanceSnapshotCleaner
	// runtimeCooldownSettings is the narrow settings port behind the
	// rate_limited re-arm of the edit-path runtime-state normalization
	// (patch_runtime_state.go). Nil until SetRuntimeCooldownSettings wires
	// it; a nil port falls back to the Node schema default (2 minutes).
	runtimeCooldownSettings RuntimeCooldownSettings
	// concurrencyReader is the runtime concurrency read port behind the list
	// currentConcurrency overlay (list.go). Nil until SetConcurrencyReader
	// wires it; nil keeps every row at 0.
	concurrencyReader AccountConcurrencyReader
	// aiAccountLimitSettings is the narrow settings port behind the creation
	// limit assertion (write.go, BUG-0174 M-8). Nil until
	// SetAiAccountLimitSettings wires it; a nil port falls back to the Node
	// schema default (100).
	aiAccountLimitSettings AiAccountCreationLimitSettings
	// usage is the stats-database read port behind the list todayUsage/usage
	// hydration (list_usage.go, BUG-0175 D-126 账户面). Nil until
	// SetUsageSource wires it; a nil port keeps the zero summaries.
	usage UsageSource
	// authorizationStats is the authz-slice read port behind the list
	// authorizationCount/authorizationTeamCount/authorizationUsageAvailable
	// projection (authorization_stats.go, Node account-summary.repository.ts
	// :1608-1610). Nil until SetAuthorizationStatsSource wires it; a nil port
	// keeps the zero fields.
	authorizationStats AuthorizationStatsSource
	// 运行态 overlay 三端口（list_runtime_overlay.go，缺陷修复批次一）：
	// runtimeAvailability / circuitSummary / apiKeyRuntime 列表叠加的事实源。
	// Nil until the matching setters wire them（组合根在网关链启用分支注入）；
	// nil 端口保持字段缺席（前端按 undefined 回退基线展示，零回归）。
	runtimeAvailabilitySource  RuntimeAvailabilitySource
	circuitSummarySource       CircuitSummarySource
	apiKeyRuntimeSummarySource APIKeyRuntimeSummarySource
	// log 是降级日志落点（BUG-0248：authorized 读路径曾用裸 println 绕过
	// JSONL 管道）。Nil until SetLogger wires it（Deps.Mount 注入
	// Deps.Log）；nil 保持静默。
	log *slog.Logger
}

// NewStore builds the store.
func NewStore(db *sql.DB, secret string, now func() time.Time, newID func(string) string) (*Store, error) {
	if db == nil {
		return nil, errors.New("accounts store requires a database")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("accounts store requires the runtime secret")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return newRandomID(prefix) }
	}
	return &Store{db: db, secret: secret, now: now, newI: newID}, nil
}

// newRandomID mirrors Node newId(prefix): "{prefix}_{millis}_{8 hex}".
func newRandomID(prefix string) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return prefix + "_" + itoa64(time.Now().UnixMilli()) + "_" + hex.EncodeToString(buf)[:8]
}

// AttachStatsDatabase injects the dedicated juhe_stats handle for the
// account_usage_snapshots reads/writes. PostgreSQL shares the business pool
// (juhe_stats schema-qualified), so the nil fallback keeps stats statements
// on the business handle; the injection point stays for deployments that
// split the stats database onto its own pool.
func (s *Store) AttachStatsDatabase(db *sql.DB) { s.statsDB = db }

// statsQueryDB resolves the handle stats-table statements must run on: the
// attached stats database when present, else the shared handle.
func (s *Store) statsQueryDB() *sql.DB {
	if s.statsDB != nil {
		return s.statsDB
	}
	return s.db
}

func (s *Store) table(name string) string {
	return accountscore.SQLTable(name)
}

// bind rewrites ? placeholders into $N for PostgreSQL.
func (s *Store) bind(query string) string {
	return accountscore.SQLBind(query)
}

// boolTrueLiteral renders the dialect TRUE literal（原 test_store.go 定义，
// 阶段 0 逻辑下沉 accountscore，根包保留方法转发）.
func (s *Store) boolTrueLiteral() string {
	return accountscore.BoolTrueLiteral()
}

// boolFalseLiteral renders the dialect FALSE literal.
func (s *Store) boolFalseLiteral() string {
	return accountscore.BoolFalseLiteral()
}

func itoa(v int) string {
	return accountscore.Itoa(v)
}

func ensureCtx(ctx context.Context) context.Context {
	return accountscore.EnsureCtx(ctx)
}

// isoMillis mirrors Node toISOString() millisecond precision.
func isoMillis(t time.Time) string {
	return accountscore.IsoMillis(t)
}

// canonicalRFC3339 mirrors canonicalizeRfc3339Instant (offset required, UTC/Z
// output with millisecond precision).
func canonicalRFC3339(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return isoMillis(parsed), true
}

// insertIgnore appends the explicit ON CONFLICT clause to an INSERT
// statement.
func (s *Store) insertIgnore(statement, conflictClause string) string {
	return statement + conflictClause
}

// ownerEffectiveStatusSQL mirrors the owner branch of
// accountManagementEffectiveStatusSql: expiry/error → disabled, non-active
// statuses surface verbatim, live cooldown → temporary_unavailable, paused
// scheduling → disabled. The comparison instant is inlined as a quoted ISO
// literal exactly like Node (safe: the value comes from the store clock).
func ownerEffectiveStatusSQL(alias, nowLiteral string) string {
	if alias == "" {
		alias = "accounts"
	}
	return `CASE
		WHEN ` + alias + `.last_error_code = 'account_expired'
			OR (` + alias + `.account_expires_at IS NOT NULL AND ` + alias + `.account_expires_at <= ` + nowLiteral + `)
		THEN 'disabled'
		WHEN ` + alias + `.status <> 'active' THEN ` + alias + `.status
		WHEN ` + alias + `.cooldown_until IS NOT NULL AND ` + alias + `.cooldown_until > ` + nowLiteral + ` THEN 'temporary_unavailable'
		-- 时间计划闸门（时间计划闸门与账户状态解耦设计 §5.4）：active + 时段外
		-- 是正常状态的子集，独立子状态，不并入 disabled（排序在 schedulable 之前）。
		WHEN ` + alias + `.availability_schedule_gate = 0 THEN 'schedule_out_of_window'
		WHEN ` + alias + `.schedulable <> 1 THEN 'disabled'
		ELSE ` + alias + `.status
	END`
}

// sqlQuoteISO renders an ISO instant as a single-quoted SQL literal.
func sqlQuoteISO(iso string) string {
	return "'" + strings.ReplaceAll(iso, "'", "''") + "'"
}

// statusRankSQL mirrors the accountManagementListOrderClause status CASE.
// schedule_out_of_window 是正常状态的子集（设计 §5.4），与 active 同档排序。
func statusRankSQL(effective string) string {
	return `CASE ` + effective + `
		WHEN 'active' THEN 1
		WHEN 'schedule_out_of_window' THEN 1
		WHEN 'temporary_unavailable' THEN 2
		WHEN 'rate_limited' THEN 3
		WHEN 'pending_test' THEN 4
		WHEN 'quality_isolated' THEN 5
		WHEN 'error' THEN 6
		WHEN 'disabled' THEN 7
		ELSE 8
	END`
}

// accountStatusFilterValues mirrors accountStatusFilterValues: comma
// separated, 'all' dropped, deduplicated.
func accountStatusFilterValues(status string) []string {
	if strings.TrimSpace(status) == "" {
		return nil
	}
	seen := map[string]bool{}
	values := []string{}
	for _, item := range strings.Split(status, ",") {
		text := strings.TrimSpace(item)
		if text == "" || text == "all" || seen[text] {
			continue
		}
		seen[text] = true
		values = append(values, text)
	}
	return values
}

// expandScheduleOutOfWindowStatuses（2026-11-07 裁决，时间计划闸门与账户状态
// 解耦设计 §5.4 修正）：状态筛选词表里的 active 按「正常状态」语义命中——
// 生效状态派生把 active+gate=0 写为 schedule_out_of_window（正常状态子集），
// 筛选随展示口径展开命中；「停用」等其余词目不展开。
func expandScheduleOutOfWindowStatuses(statuses []string) []string {
	hasActive, hasDerived := false, false
	for _, status := range statuses {
		switch status {
		case "active":
			hasActive = true
		case "schedule_out_of_window":
			hasDerived = true
		}
	}
	if !hasActive || hasDerived {
		return statuses
	}
	return append(statuses, "schedule_out_of_window")
}

// normalizeTextList mirrors normalizeTextList: trimmed, deduplicated, sorted,
// capped.
func normalizeTextList(values []string, cap int) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	sortStrings(out)
	if len(out) > cap {
		out = out[:cap]
	}
	return out
}

// sortStrings 阶段 B 下沉 accountscore，根包保留同名转发.
func sortStrings(values []string) {
	accountscore.SortStrings(values)
}

// itoa64 阶段 B 下沉 accountscore，根包保留同名转发.
func itoa64(v int64) string {
	return accountscore.Itoa64(v)
}

// minInt / maxInt 阶段 B 下沉 accountscore，根包保留同名转发.
func minInt(a, b int) int {
	return accountscore.MinInt(a, b)
}

func maxInt(a, b int) int {
	return accountscore.MaxInt(a, b)
}

// nullPtrString renders NULL/empty SQL text as an omitted JSON field.
func nullPtrString(value sql.NullString) *string {
	return accountscore.NullPtrString(value)
}
