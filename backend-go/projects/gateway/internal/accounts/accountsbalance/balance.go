package accountsbalance

// M11 balance family: GET /{id}/balance/details, POST /{id}/balance/refresh,
// POST /balance/test-draft, POST /model-catalog/refresh and POST
// /{id}/force-activate (Node account-balance.routes.ts +
// account-balance.repository.ts + account-balance-query.service.ts +
// account-model-catalog-refresh.service.ts + account-force-activate.routes.ts
// + account-runtime-mutation.repository.ts forceActivatePendingAccount).
//
// The pure DB projections (details, refresh candidate localization, the
// force-activate CAS) live in this package. The live upstream executions
// (manual refresh commit, draft probe, model catalog discovery) ride the
// narrow ManualBalanceRefresher / ModelCatalogRefresher ports injected by the
// composition root — J2 keeps the balance execution in the Go jobs service and
// the gateway must not duplicate it. A nil port degrades exactly like the
// Node failure shapes (details/force-activate stay fully functional).
//
// REFACTOR-0005 阶段 B：原 m11_balance.go 随迁；prepareBalanceDraft /
// balanceDraftRow（门面路由按字段读取其私有结构，且输入全部是门面
// write-path helper）、findForceActivateSummary（ListPage 深读，经
// Deps.FindAccountSummary 注入）、m11ScheduleAllowed（schedule.go 门面面，
// 经 Deps.ScheduleGate 注入）留在门面。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// BalanceKeySnapshot mirrors AccountBalanceKeySnapshot
// (account-balance.types.ts).
type BalanceKeySnapshot struct {
	KeyFingerprint string  `json:"keyFingerprint"`
	MaskedKey      string  `json:"maskedKey"`
	Status         string  `json:"status"`
	RemainingUsd   *string `json:"remainingUsd,omitempty"`
	RawUnit        *string `json:"rawUnit,omitempty"`
	Scope          *string `json:"scope,omitempty"`
	Basis          any     `json:"basis,omitempty"`
	ErrorMessage   *string `json:"errorMessage,omitempty"`
	LastAttemptAt  *string `json:"lastAttemptAt,omitempty"`
	LastSuccessAt  *string `json:"lastSuccessAt,omitempty"`
}

// balanceDetailsRow is the account projection the details endpoint reads
// (Node findAccountForTestAsync guard + the balance columns).
type balanceDetailsRow struct {
	id                  string
	configRevision      int64
	systemAccountID     string
	accountType         string
	credentialsEncryped string
	authorizationID     sql.NullString
	sourceAccountID     sql.NullString
	balanceQueryEnabled int
	nextRefreshAt       sql.NullString
	balanceConfigJSON   string
}

// BalanceSnapshotRecord mirrors AccountBalanceSnapshotRecord. 字段导出仅供
// 门面桥别名与根包既有测试字面量沿用；读取路径全部在本包内。
type BalanceSnapshotRecord struct {
	Snapshot         map[string]any
	NextRefreshAfter sql.NullString
	UpdatedAt        string
}

// LoadBalanceSnapshotRecord reads the relay_balance stats row.
func (s *Service) LoadBalanceSnapshotRecord(ctx context.Context, accountID string) (*BalanceSnapshotRecord, error) {
	var (
		snapshotJSON     string
		nextRefreshAfter sql.NullString
		updatedAt        string
	)
	err := s.statsDB().QueryRowContext(ctx, s.store.Bind(`SELECT account_id, snapshot_json, next_refresh_after, updated_at
		FROM `+s.StatsTable("account_usage_snapshots")+`
		WHERE kind = 'relay_balance' AND account_id = ?
		LIMIT 1`), accountID).Scan(&accountID, &snapshotJSON, &nextRefreshAfter, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record := &BalanceSnapshotRecord{NextRefreshAfter: nextRefreshAfter, UpdatedAt: updatedAt}
	if strings.TrimSpace(snapshotJSON) != "" {
		_ = json.Unmarshal([]byte(snapshotJSON), &record.Snapshot)
	}
	return record, nil
}

// BalanceSnapshotTimestampMs mirrors requiredAccountBalanceTimestampMilliseconds
// without the error path: malformed instances compare as never-equal.
func BalanceSnapshotTimestampMs(value string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// BalanceSnapshotMatchesConfiguration mirrors accountBalanceSnapshotMatchesConfiguration:
// the snapshot must carry the current config revision and the persisted
// next_refresh_after must equal the configured due instant.
func BalanceSnapshotMatchesConfiguration(nextRefreshAt string, configRevision int64, record *BalanceSnapshotRecord) bool {
	if record == nil || record.Snapshot == nil {
		return false
	}
	if revision, ok := record.Snapshot["configRevision"].(float64); ok {
		if int64(revision) != configRevision {
			return false
		}
	} else {
		return false
	}
	configuredMs, configuredOK := BalanceSnapshotTimestampMs(nextRefreshAt)
	persistedMs, persistedOK := BalanceSnapshotTimestampMs(record.NextRefreshAfter.String)
	if !configuredOK || !persistedOK {
		return !configuredOK && !persistedOK && nextRefreshAt == "" && !record.NextRefreshAfter.Valid
	}
	return configuredMs == persistedMs
}

// FindBalanceDetails mirrors the GET /:id/balance/details projection: the
// scope-checked account row plus the per-Key snapshot mapping. Returns
// (nil, nil) for missing/out-of-scope accounts and the sentinel
// ErrBalanceDetailsDisabled for the 账户未开启余额查询 branch (route 404).
type BalanceDetails struct {
	AccountID        string               `json:"accountId"`
	ConfigRevision   int64                `json:"configRevision,omitempty"`
	SnapshotRevision any                  `json:"-"`
	KeyCount         int                  `json:"keyCount"`
	QueriedKeyCount  int                  `json:"queriedKeyCount"`
	Scope            string               `json:"scope"`
	Aggregation      string               `json:"aggregation"`
	UpdatedAt        *string              `json:"updatedAt,omitempty"`
	KeyBalances      []BalanceKeySnapshot `json:"keyBalances"`
}

// ErrBalanceDetailsDisabled marks balanceQueryEnabled=false (404 copy).
var ErrBalanceDetailsDisabled = errors.New("账户未开启余额查询")

func (s *Service) FindBalanceDetails(ctx context.Context, accountID string, access accountscore.AccessScope) (*BalanceDetails, error) {
	ctx = accountscore.EnsureCtx(ctx)
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil, nil
	}
	authorized := s.deps.AuthorizedReadableIDs(ctx, access)[id]
	scopeClause := ""
	args := []any{id}
	if scoped := access.ManageableID(); scoped != "" && !authorized {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row balanceDetailsRow
	err := s.store.DB().QueryRowContext(ctx, s.store.Bind(`SELECT accounts.id, accounts.config_revision,
			accounts.system_account_id, accounts.type, accounts.credentials_encrypted,
			accounts.authorization_instance_authorization_id,
			accounts.authorization_instance_source_account_id,
			accounts.balance_query_enabled, accounts.balance_query_next_refresh_at,
			accounts.balance_query_config_json
		FROM `+s.store.Table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.configRevision, &row.systemAccountID, &row.accountType,
		&row.credentialsEncryped, &row.authorizationID, &row.sourceAccountID,
		&row.balanceQueryEnabled, &row.nextRefreshAt, &row.balanceConfigJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !access.CanAccessAll() && row.systemAccountID != access.ViewerID && !authorized {
		return nil, nil
	}
	// Node findAccountForTestAsync + the route guard: instance rows and any
	// stamped variant without the credentials permission render 403 (route);
	// the disabled query renders 404.
	if row.authorizationID.Valid && row.authorizationID.String != "" ||
		row.sourceAccountID.Valid && row.sourceAccountID.String != "" {
		return nil, ErrBalanceDetailsForbidden
	}
	if row.balanceQueryEnabled != 1 {
		return nil, ErrBalanceDetailsDisabled
	}
	credentials := accountscore.Credentials{}
	if err := accountscore.DecryptJSON(s.store.Secret(), row.credentialsEncryped, &credentials); err != nil {
		return nil, err
	}
	apiKeys := EffectiveAccountApiKeys(credentials)
	record, err := s.LoadBalanceSnapshotRecord(ctx, row.id)
	if err != nil {
		return nil, err
	}
	details := &BalanceDetails{
		AccountID:      row.id,
		ConfigRevision: row.configRevision,
		KeyCount:       len(apiKeys),
		Scope:          "unknown",
		Aggregation:    "unknown",
		KeyBalances:    []BalanceKeySnapshot{},
	}
	var currentSnapshot map[string]any
	if BalanceSnapshotMatchesConfiguration(row.nextRefreshAt.String, row.configRevision, record) {
		currentSnapshot = record.Snapshot
		details.UpdatedAt = &record.UpdatedAt
	}
	if currentSnapshot != nil {
		if revision, ok := currentSnapshot["configRevision"].(float64); ok {
			details.SnapshotRevision = int64(revision)
		}
		if queried, ok := currentSnapshot["queriedKeyCount"].(float64); ok {
			details.QueriedKeyCount = int(queried)
		}
		if scope, ok := currentSnapshot["scope"].(string); ok && scope != "" {
			details.Scope = scope
		}
		if aggregation, ok := currentSnapshot["aggregation"].(string); ok && aggregation != "" {
			details.Aggregation = aggregation
		}
	}
	storedByFingerprint := map[string]map[string]any{}
	if currentSnapshot != nil {
		if keyBalances, ok := currentSnapshot["keyBalances"].([]any); ok {
			for _, item := range keyBalances {
				if typed, ok := item.(map[string]any); ok {
					if fingerprint, ok := typed["keyFingerprint"].(string); ok {
						storedByFingerprint[fingerprint] = typed
					}
				}
			}
		}
	}
	for _, apiKey := range apiKeys {
		fingerprint := s.BalanceAPIKeyFingerprint(apiKey)
		if stored, ok := storedByFingerprint[fingerprint]; ok {
			details.KeyBalances = append(details.KeyBalances, BalanceKeySnapshotFromMap(fingerprint, MaskBalanceAPIKey(apiKey), stored))
			continue
		}
		details.KeyBalances = append(details.KeyBalances, BalanceKeySnapshot{
			KeyFingerprint: fingerprint,
			MaskedKey:      MaskBalanceAPIKey(apiKey),
			Status:         "pending",
		})
	}
	return details, nil
}

// BalanceKeySnapshotFromMap projects one stored per-Key entry.
func BalanceKeySnapshotFromMap(fallbackFingerprint, fallbackMask string, stored map[string]any) BalanceKeySnapshot {
	snapshot := BalanceKeySnapshot{Status: "pending"}
	if text, ok := stored["keyFingerprint"].(string); ok && text != "" {
		snapshot.KeyFingerprint = text
	} else {
		snapshot.KeyFingerprint = fallbackFingerprint
	}
	if text, ok := stored["maskedKey"].(string); ok && text != "" {
		snapshot.MaskedKey = text
	} else {
		snapshot.MaskedKey = fallbackMask
	}
	if text, ok := stored["status"].(string); ok && text != "" {
		snapshot.Status = text
	}
	if text, ok := stored["remainingUsd"].(string); ok {
		snapshot.RemainingUsd = &text
	}
	if text, ok := stored["rawUnit"].(string); ok {
		snapshot.RawUnit = &text
	}
	if text, ok := stored["scope"].(string); ok {
		snapshot.Scope = &text
	}
	snapshot.Basis = stored["basis"]
	if text, ok := stored["errorMessage"].(string); ok {
		snapshot.ErrorMessage = &text
	}
	if text, ok := stored["lastAttemptAt"].(string); ok {
		snapshot.LastAttemptAt = &text
	}
	if text, ok := stored["lastSuccessAt"].(string); ok {
		snapshot.LastSuccessAt = &text
	}
	return snapshot
}

// MaskBalanceAPIKey mirrors maskAccountBalanceApiKey
// (account-balance-config.ts:144-148).
func MaskBalanceAPIKey(value string) string {
	key := strings.TrimSpace(value)
	runes := []rune(key)
	if len(runes) <= 8 {
		return string(runes[:accountscore.MinInt(2, len(runes))]) + "…" + string(runes[accountscore.MaxInt(0, len(runes)-2):])
	}
	return string(runes[:4]) + "…" + string(runes[len(runes)-4:])
}

// ErrBalanceDetailsForbidden / ErrBalanceRefreshForbidden mark the instance
// rows and stamped variants (route 403 copies).
var (
	ErrBalanceDetailsForbidden = errors.New("无权查看该账户的上游余额明细")
	ErrBalanceRefreshForbidden = errors.New("无权刷新该账户的上游余额")
)

// BalanceRefreshCandidate mirrors AccountBalanceRefreshCandidate
// (findAccountBalanceManualRefreshCandidateAsync): the manual refresh query
// keeps disabled/unavailable accounts eligible (the route re-checks the
// permissions against the summary row) but stays api_key + enabled-only.
type BalanceRefreshCandidate struct {
	ID                  string
	SystemAccountID     string
	ProviderCode        string
	Type                string
	Status              string
	Schedulable         bool
	ConfigRevision      int64
	DispatchRevision    int64
	CredentialsEnvelope string
	ConfigJSON          string
	NextRefreshAt       sql.NullString
	ProxyProfileID      sql.NullString
}

// FindBalanceManualRefreshCandidate mirrors
// findAccountBalanceManualRefreshCandidateAsync (the SELECT column set follows
// the archived Node repository account-balance.repository.ts:315-334, the row
// mapping :938-971; dispatch_revision is the manual InputVersion fence).
func (s *Service) FindBalanceManualRefreshCandidate(ctx context.Context, accountID string) (*BalanceRefreshCandidate, error) {
	ctx = accountscore.EnsureCtx(ctx)
	var row BalanceRefreshCandidate
	var schedulable int
	err := s.store.DB().QueryRowContext(ctx, s.store.Bind(`SELECT id, system_account_id, provider_code, type, status, schedulable,
			config_revision, dispatch_revision, credentials_encrypted, balance_query_config_json,
			balance_query_next_refresh_at, proxy_profile_id
		FROM `+s.store.Table("accounts")+`
		WHERE id = ?
			AND type = 'api_key'
			AND balance_query_enabled = 1
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NULL
		LIMIT 1`), strings.TrimSpace(accountID)).Scan(
		&row.ID, &row.SystemAccountID, &row.ProviderCode, &row.Type, &row.Status, &schedulable,
		&row.ConfigRevision, &row.DispatchRevision, &row.CredentialsEnvelope, &row.ConfigJSON,
		&row.NextRefreshAt, &row.ProxyProfileID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.Schedulable = schedulable != 0
	return &row, nil
}

// BalanceManualRefreshOutcome mirrors the manual refresh outcome tuple the
// route consumes (refreshAccountBalanceCandidateWithOutcome /
// runAccountBalanceManualViaGo).
type BalanceManualRefreshOutcome struct {
	Persisted bool
	Outcome   string
	Snapshot  map[string]any
}

// ManualBalanceRefresher is the narrow execution port for the live upstream
// balance paths. The J2 Go jobs service owns the adapters; the composition
// root bridges it. A nil port keeps the endpoint contract: refresh renders the
// Node 500 shape and the draft test renders the failed-snapshot shape.
type ManualBalanceRefresher interface {
	// RefreshManual runs the manual refresh: upstream query + snapshot CAS
	// commit. Persisted=false with Outcome 'lease_busy' or 'stale' mirrors the
	// Node 409 branches.
	RefreshManual(ctx context.Context, candidate BalanceRefreshCandidate) (BalanceManualRefreshOutcome, error)
	// TestDraft runs the non-persisted draft probe (Node
	// testAccountBalanceCandidate): it always resolves to a snapshot
	// (status failed on error), never an error.
	TestDraft(ctx context.Context, input BalanceDraftProbeInput) (map[string]any, error)
}

// BalanceDraftProbeInput mirrors the AccountBalanceQueryCandidate shape the
// draft probe consumes (Node testAccountBalanceCandidate).
type BalanceDraftProbeInput struct {
	ID             string
	Credentials    accountscore.Credentials
	Config         map[string]any
	ProxyProfileID *string
}

// ModelCatalogRefresher is the narrow execution port of
// refreshAccountDraftModelCatalogAsync (the live upstream model discovery).
type ModelCatalogRefresher interface {
	RefreshDraftModelCatalog(ctx context.Context, input ModelCatalogDiscoveryInput) (map[string]any, error)
}

// ModelCatalogDiscoveryInput carries the prepared draft account plus the
// discovery context to the port.
type ModelCatalogDiscoveryInput struct {
	OwnerSystemAccountID string
	ProviderCode         string
	ProviderProfileID    string
	// ProtocolCode is the provider protocol profile protocol the upstream
	// models request rides (openai / anthropic / gemini path + header shape).
	ProtocolCode     string
	AccountType      string
	Credentials      accountscore.Credentials
	ProxyProfileID   *string
	HealthCheckModel string
	SupportedModels  []string
}

// ForceActivateOutcome carries the force-activate result across the facade
// bridge: Account is the sanitized summary re-read behind the injected
// FindAccountSummary port (the facade *ListItem; nil when the row is gone).
type ForceActivateOutcome struct {
	Account any
	Changed bool
}

// ForceActivatePending mirrors forceActivatePendingAccountAsync: the
// pending_test CAS restore (active/disabled by the availability schedule),
// the dispatch revision advance for the activated branch and the summary
// re-read. Returns (nil outcome) when the row is missing or outside the
// scope; the route turns the state race into 409.
func (s *Service) ForceActivatePending(ctx context.Context, accountID string, access accountscore.AccessScope) (*ForceActivateOutcome, error) {
	ctx = accountscore.EnsureCtx(ctx)
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil, nil
	}
	authorized := s.deps.AuthorizedReadableIDs(ctx, access)[id]
	scopeClause := ""
	args := []any{id}
	if scoped := access.ManageableID(); scoped != "" && !authorized {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row struct {
		id               string
		configRevision   int64
		systemAccountID  string
		name             string
		status           string
		scheduleJSON     sql.NullString
		accountExpiresAt sql.NullString
		authorizationID  sql.NullString
		sourceAccountID  sql.NullString
	}
	err := s.store.DB().QueryRowContext(ctx, s.store.Bind(`SELECT id, config_revision, system_account_id, name,
			status, availability_schedule_json, account_expires_at,
			authorization_instance_authorization_id, authorization_instance_source_account_id
		FROM `+s.store.Table("accounts")+`
		WHERE id = ?
			AND deleted_at IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.configRevision, &row.systemAccountID, &row.name,
		&row.status, &row.scheduleJSON, &row.accountExpiresAt,
		&row.authorizationID, &row.sourceAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !access.CanAccessAll() && row.systemAccountID != access.ViewerID && !authorized {
		return nil, nil
	}
	// Node accountRowForManage guard: instance rows and non-pending or expired
	// accounts keep changed=false (route 400/409 branches).
	stamped := row.authorizationID.Valid && row.authorizationID.String != "" ||
		row.sourceAccountID.Valid && row.sourceAccountID.String != ""
	if stamped || row.status != "pending_test" ||
		accountscore.IsAccountExpired(row.accountExpiresAt.String, s.store.Now()) {
		summary, summaryErr := s.deps.FindAccountSummary(ctx, row.id, row.systemAccountID)
		if summaryErr != nil {
			return nil, summaryErr
		}
		return &ForceActivateOutcome{Account: summary, Changed: false}, nil
	}
	now := s.store.Now()
	checkedAt := accountscore.IsoMillis(now)
	nextStatus := "disabled"
	if s.deps.ScheduleGate(row.scheduleJSON.String, now) {
		nextStatus = "active"
	}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	exec, err := tx.ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("accounts")+`
		SET status = ?,
			schedulable = 1,
			cooldown_until = NULL,
			last_error_code = NULL,
			last_error_message = NULL,
			last_error_trace_id = NULL,
			cooldown_retest_failure_count = 0,
			cooldown_retest_observation_started_at = NULL,
			cooldown_retest_last_at = NULL,
			cooldown_retest_last_status_code = NULL,
			next_health_check_at = NULL,
			health_check_failure_count = 0,
			health_check_failure_started_at = NULL,
			last_health_check_error_code = NULL,
			last_health_check_error_message = NULL,
			stream_failure_count = 0,
			stream_failure_window_started_at = NULL,
			updated_at = ?
		WHERE id = ?
			AND system_account_id = ?
			AND authorization_instance_authorization_id IS NULL
			AND deleted_at IS NULL
			AND status = 'pending_test'
			AND config_revision = ?
			AND (account_expires_at IS NULL OR account_expires_at > ?)`),
		nextStatus, checkedAt, row.id, row.systemAccountID, row.configRevision, checkedAt)
	if err != nil {
		return nil, err
	}
	changed := false
	if affected, _ := exec.RowsAffected(); affected > 0 {
		changed = true
		if nextStatus == "active" {
			if err := s.deps.AdvanceBatchDispatchRevision(ctx, tx, row.id, s.deps.NewDispatchID(), now.UnixMilli()); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	summary, err := s.deps.FindAccountSummary(ctx, row.id, row.systemAccountID)
	if err != nil {
		return nil, err
	}
	return &ForceActivateOutcome{Account: summary, Changed: changed}, nil
}
