package accounts

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

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// statsTable qualifies a juhe_stats table (PostgreSQL schema-qualified, bare
// on SQLite — the Go test database keeps one file).
func (s *Store) statsTable(name string) string {
	if s.pg {
		return "juhe_stats." + name
	}
	return name
}

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

// balanceSnapshotRecord mirrors AccountBalanceSnapshotRecord.
type balanceSnapshotRecord struct {
	snapshot         map[string]any
	nextRefreshAfter sql.NullString
	updatedAt        string
}

// loadBalanceSnapshotRecord reads the relay_balance stats row.
func (s *Store) loadBalanceSnapshotRecord(ctx context.Context, accountID string) (*balanceSnapshotRecord, error) {
	var (
		snapshotJSON     string
		nextRefreshAfter sql.NullString
		updatedAt        string
	)
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT account_id, snapshot_json, next_refresh_after, updated_at
		FROM `+s.statsTable("account_usage_snapshots")+`
		WHERE kind = 'relay_balance' AND account_id = ?
		LIMIT 1`), accountID).Scan(&accountID, &snapshotJSON, &nextRefreshAfter, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record := &balanceSnapshotRecord{nextRefreshAfter: nextRefreshAfter, updatedAt: updatedAt}
	if strings.TrimSpace(snapshotJSON) != "" {
		_ = json.Unmarshal([]byte(snapshotJSON), &record.snapshot)
	}
	return record, nil
}

// balanceSnapshotTimestampMs mirrors requiredAccountBalanceTimestampMilliseconds
// without the error path: malformed instances compare as never-equal.
func balanceSnapshotTimestampMs(value string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// balanceSnapshotMatchesConfiguration mirrors accountBalanceSnapshotMatchesConfiguration:
// the snapshot must carry the current config revision and the persisted
// next_refresh_after must equal the configured due instant.
func balanceSnapshotMatchesConfiguration(nextRefreshAt string, configRevision int64, record *balanceSnapshotRecord) bool {
	if record == nil || record.snapshot == nil {
		return false
	}
	if revision, ok := record.snapshot["configRevision"].(float64); ok {
		if int64(revision) != configRevision {
			return false
		}
	} else {
		return false
	}
	configuredMs, configuredOK := balanceSnapshotTimestampMs(nextRefreshAt)
	persistedMs, persistedOK := balanceSnapshotTimestampMs(record.nextRefreshAfter.String)
	if !configuredOK || !persistedOK {
		return !configuredOK && !persistedOK && nextRefreshAt == "" && !record.nextRefreshAfter.Valid
	}
	return configuredMs == persistedMs
}

// FindBalanceDetails mirrors the GET /:id/balance/details projection: the
// scope-checked account row plus the per-Key snapshot mapping. Returns
// (nil, nil) for missing/out-of-scope accounts and the sentinel
// balanceDetailsDisabledError for the 账户未开启余额查询 branch (route 404).
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

// balanceDetailsDisabledError marks balanceQueryEnabled=false (404 copy).
var errBalanceDetailsDisabled = errors.New("账户未开启余额查询")

func (s *Store) FindBalanceDetails(ctx context.Context, accountID string, access AccessScope) (*BalanceDetails, error) {
	ctx = ensureCtx(ctx)
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil, nil
	}
	authorized := s.authorizedReadableIDs(ctx, access)[id]
	scopeClause := ""
	args := []any{id}
	if scoped := access.manageableID(); scoped != "" && !authorized {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row balanceDetailsRow
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision,
			accounts.system_account_id, accounts.type, accounts.credentials_encrypted,
			accounts.authorization_instance_authorization_id,
			accounts.authorization_instance_source_account_id,
			accounts.balance_query_enabled, accounts.balance_query_next_refresh_at,
			accounts.balance_query_config_json
		FROM `+s.table("accounts")+` accounts
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
	if !access.canAccessAll() && row.systemAccountID != access.ViewerID && !authorized {
		return nil, nil
	}
	// Node findAccountForTestAsync + the route guard: instance rows and any
	// stamped variant without the credentials permission render 403 (route);
	// the disabled query renders 404.
	if row.authorizationID.Valid && row.authorizationID.String != "" ||
		row.sourceAccountID.Valid && row.sourceAccountID.String != "" {
		return nil, errBalanceDetailsForbidden
	}
	if row.balanceQueryEnabled != 1 {
		return nil, errBalanceDetailsDisabled
	}
	credentials := Credentials{}
	if err := DecryptJSON(s.secret, row.credentialsEncryped, &credentials); err != nil {
		return nil, err
	}
	apiKeys := EffectiveAccountApiKeys(credentials)
	record, err := s.loadBalanceSnapshotRecord(ctx, row.id)
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
	if balanceSnapshotMatchesConfiguration(row.nextRefreshAt.String, row.configRevision, record) {
		currentSnapshot = record.snapshot
		details.UpdatedAt = &record.updatedAt
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
		fingerprint := s.balanceAPIKeyFingerprint(apiKey)
		if stored, ok := storedByFingerprint[fingerprint]; ok {
			details.KeyBalances = append(details.KeyBalances, balanceKeySnapshotFromMap(fingerprint, maskBalanceAPIKey(apiKey), stored))
			continue
		}
		details.KeyBalances = append(details.KeyBalances, BalanceKeySnapshot{
			KeyFingerprint: fingerprint,
			MaskedKey:      maskBalanceAPIKey(apiKey),
			Status:         "pending",
		})
	}
	return details, nil
}

// balanceKeySnapshotFromMap projects one stored per-Key entry.
func balanceKeySnapshotFromMap(fallbackFingerprint, fallbackMask string, stored map[string]any) BalanceKeySnapshot {
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

// maskBalanceAPIKey mirrors maskAccountBalanceApiKey
// (account-balance-config.ts:144-148).
func maskBalanceAPIKey(value string) string {
	key := strings.TrimSpace(value)
	runes := []rune(key)
	if len(runes) <= 8 {
		return string(runes[:minInt(2, len(runes))]) + "…" + string(runes[maxInt(0, len(runes)-2):])
	}
	return string(runes[:4]) + "…" + string(runes[len(runes)-4:])
}

// errBalanceDetailsForbidden / errBalanceRefreshForbidden mark the instance
// rows and stamped variants (route 403 copies).
var (
	errBalanceDetailsForbidden = errors.New("无权查看该账户的上游余额明细")
	errBalanceRefreshForbidden = errors.New("无权刷新该账户的上游余额")
)

// BalanceRefreshCandidate mirrors AccountBalanceRefreshCandidate
// (findAccountBalanceManualRefreshCandidateAsync): the manual refresh query
// keeps disabled/unavailable accounts eligible (the route re-checks the
// permissions against the summary row) but stays api_key + enabled-only.
type BalanceRefreshCandidate struct {
	ID                  string
	SystemAccountID     string
	ConfigRevision      int64
	CredentialsEnvelope string
	ConfigJSON          string
	NextRefreshAt       sql.NullString
	ProxyProfileID      sql.NullString
}

// FindBalanceManualRefreshCandidate mirrors
// findAccountBalanceManualRefreshCandidateAsync.
func (s *Store) FindBalanceManualRefreshCandidate(ctx context.Context, accountID string) (*BalanceRefreshCandidate, error) {
	ctx = ensureCtx(ctx)
	var row BalanceRefreshCandidate
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, config_revision,
			credentials_encrypted, balance_query_config_json, balance_query_next_refresh_at, proxy_profile_id
		FROM `+s.table("accounts")+`
		WHERE id = ?
			AND type = 'api_key'
			AND balance_query_enabled = 1
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NULL
		LIMIT 1`), strings.TrimSpace(accountID)).Scan(
		&row.ID, &row.SystemAccountID, &row.ConfigRevision,
		&row.CredentialsEnvelope, &row.ConfigJSON, &row.NextRefreshAt, &row.ProxyProfileID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
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
	// TestDraft runs the non-persisted draft probe
	// (Node testAccountBalanceCandidate): it always resolves to a snapshot
	// (status failed on error), never an error.
	TestDraft(ctx context.Context, input BalanceDraftProbeInput) (map[string]any, error)
}

// BalanceDraftProbeInput mirrors the AccountBalanceQueryCandidate shape the
// draft probe consumes (Node testAccountBalanceCandidate).
type BalanceDraftProbeInput struct {
	ID             string
	Credentials    Credentials
	Config         map[string]any
	ProxyProfileID *string
}

// SetManualBalanceRefresher wires the port (composition-root handover).
func (s *Store) SetManualBalanceRefresher(refresher ManualBalanceRefresher) {
	s.balanceRefresher = refresher
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
	AccountType          string
	Credentials          Credentials
	ProxyProfileID       *string
	SupportedModels      []string
}

// SetModelCatalogRefresher wires the port (composition-root handover).
func (s *Store) SetModelCatalogRefresher(refresher ModelCatalogRefresher) {
	s.modelCatalogRefresher = refresher
}

// balanceDraftRow is the draft-test account projection (the strict body
// account plus the group/provider resolution).
type balanceDraftRow struct {
	groupID          string
	groupName        string
	ownerID          string
	accountType      string
	providerCode     string
	providerProfile  *providerProfile
	credentials      Credentials
	proxyProfileID   *string
	supportedModels  []string
	healthCheckModel string
	healthCheckMode  string
}

// prepareBalanceDraft mirrors prepareAccountDraftTestSnapshotAsync for the
// balance probe: the group/provider contract checks plus the credentials
// normalization. Errors carry the Node 400 copy.
func (s *Store) prepareBalanceDraft(ctx context.Context, accountInput map[string]any, access AccessScope) (*balanceDraftRow, error) {
	groupID := strings.TrimSpace(textString(accountInput["groupId"]))
	providerCode := strings.TrimSpace(textString(accountInput["providerCode"]))
	accountType := strings.TrimSpace(textString(accountInput["type"]))
	if groupID == "" || providerCode == "" || accountType == "" {
		return nil, &ValidationError{Message: "账户分组无效"}
	}
	group, err := s.groupOwnerAndProvider(ctx, s.db, groupID)
	if err != nil {
		return nil, err
	}
	if group == nil || group.providerCode != providerCode {
		return nil, &ValidationError{Message: "账户分组无效"}
	}
	owner := group.systemAccountID
	if owner == "" {
		owner = access.viewerID()
	}
	if owner == "" {
		return nil, &ValidationError{Message: "账户分组缺少归属用户，无法测试"}
	}
	profileID := strings.TrimSpace(textString(accountInput["providerProtocolProfileId"]))
	if profileID == "" {
		return nil, &ValidationError{Message: "账户 providerProtocolProfileId 不能为空"}
	}
	profile, err := s.requireEnabledProviderProtocolProfile(ctx, s.db, providerCode, profileID)
	if err != nil {
		return nil, err
	}
	supported := false
	for _, item := range profile.accountTypes {
		if item == accountType {
			supported = true
			break
		}
	}
	if !supported {
		return nil, &ValidationError{Message: "供应商 " + providerCode + " 不支持账户类型 " + accountType}
	}
	credentialsInput, _ := accountInput["credentials"].(map[string]any)
	if credentialsInput == nil {
		credentialsInput = map[string]any{}
	}
	// draftAccountCredentials: the oauth draft falls back to the profile base
	// URL when the input omits base_url.
	if accountType != "oauth" || textString(credentialsInput["base_url"]) == "" {
		if accountType == "oauth" {
			fallback := profile.baseURL
			if strings.TrimSpace(fallback) == "" {
				fallback = "https://api.openai.com/v1"
			}
			credentialsInput["base_url"] = fallback
		}
	}
	credentials, err := NormalizeAccountCredentialsForWrite(accountType, Credentials(credentialsInput), &EndpointModeDefaultContext{
		ProviderCode:              providerCode,
		AccountType:               accountType,
		ProviderProtocolProfileID: profile.id,
		ProtocolCode:              profile.protocolCode,
		ProtocolVersion:           profile.protocolVersion,
	})
	if err != nil {
		return nil, err
	}
	row := &balanceDraftRow{
		groupID:          groupID,
		groupName:        group.name.String,
		ownerID:          owner,
		accountType:      accountType,
		providerCode:     providerCode,
		providerProfile:  profile,
		credentials:      credentials,
		healthCheckModel: textString(accountInput["healthCheckModel"]),
		healthCheckMode:  textString(accountInput["healthCheckEndpointMode"]),
	}
	if list, ok := accountInput["supportedModels"].([]any); ok {
		for _, item := range list {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				row.supportedModels = append(row.supportedModels, strings.TrimSpace(text))
			}
		}
	}
	if text, ok := accountInput["proxyProfileId"].(string); ok && strings.TrimSpace(text) != "" {
		row.proxyProfileID = &text
	}
	return row, nil
}

// ForceActivateResult mirrors the force-activate outcome the route consumes.
type ForceActivateResult struct {
	Account *ListItem
	Changed bool
}

// ForceActivatePending mirrors forceActivatePendingAccountAsync: the
// pending_test CAS restore (active/disabled by the availability schedule),
// the dispatch revision advance for the activated branch and the summary
// re-read. Returns (nil account, changed=false) when the row is missing or
// outside the scope; the route turns the state race into 409.
func (s *Store) ForceActivatePending(ctx context.Context, accountID string, access AccessScope) (*ForceActivateResult, error) {
	ctx = ensureCtx(ctx)
	id := strings.TrimSpace(accountID)
	if id == "" {
		return nil, nil
	}
	authorized := s.authorizedReadableIDs(ctx, access)[id]
	scopeClause := ""
	args := []any{id}
	if scoped := access.manageableID(); scoped != "" && !authorized {
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
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, config_revision, system_account_id, name,
			status, availability_schedule_json, account_expires_at,
			authorization_instance_authorization_id, authorization_instance_source_account_id
		FROM `+s.table("accounts")+`
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
	if !access.canAccessAll() && row.systemAccountID != access.ViewerID && !authorized {
		return nil, nil
	}
	// Node accountRowForManage guard: instance rows and non-pending or expired
	// accounts keep changed=false (route 400/409 branches).
	stamped := row.authorizationID.Valid && row.authorizationID.String != "" ||
		row.sourceAccountID.Valid && row.sourceAccountID.String != ""
	if stamped || row.status != "pending_test" ||
		isAccountExpired(row.accountExpiresAt.String, s.now()) {
		summary, summaryErr := s.findForceActivateSummary(ctx, row.id, row.systemAccountID)
		if summaryErr != nil {
			return nil, summaryErr
		}
		return &ForceActivateResult{Account: summary, Changed: false}, nil
	}
	now := s.now()
	checkedAt := isoMillis(now)
	nextStatus := "disabled"
	if m11ScheduleAllowed(row.scheduleJSON.String, now) {
		nextStatus = "active"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	exec, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
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
			if err := s.advanceBatchDispatchRevision(ctx, tx, row.id, newID("dispatch"), now.UnixMilli()); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	summary, err := s.findForceActivateSummary(ctx, row.id, row.systemAccountID)
	if err != nil {
		return nil, err
	}
	return &ForceActivateResult{Account: summary, Changed: changed}, nil
}

// scheduleAllows evaluates the availability schedule at the given instant; a
// blank schedule stays always-allowed (Node
// isAccountAvailabilityScheduleAllowed with a null schedule).
func m11ScheduleAllowed(scheduleJSON string, now time.Time) bool {
	trimmed := strings.TrimSpace(scheduleJSON)
	if trimmed == "" {
		return true
	}
	schedule, err := ParseScheduleJSON(trimmed)
	if err != nil || schedule == nil {
		return true
	}
	if override, ok := ScheduleStatus(schedule, now); ok {
		return override == "active"
	}
	return true
}

// findForceActivateSummary re-reads the sanitized account summary
// (Node findAccountSummaryAsync with the owner access).
func (s *Store) findForceActivateSummary(ctx context.Context, accountID, ownerID string) (*ListItem, error) {
	result, err := s.ListPage(ctx, AccessScope{ViewerID: ownerID}, ListOptions{IDs: []string{accountID}, Page: 1, PageSize: 1})
	if err != nil {
		return nil, err
	}
	if len(result.Items) == 0 {
		return nil, nil
	}
	return &result.Items[0], nil
}
