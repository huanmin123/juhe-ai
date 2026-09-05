package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// M09 batch edit slice: the POST /accounts/batch-edit-context +
// POST /accounts/batch-update family ported from
// backend/src/modules/accounts/account-batch-edit.routes.ts,
// account-batch-edit.service.ts, storage/account-batch-edit-context.repository.ts
// and storage/account-batch-update.repository.ts.

// batchAccessError mirrors AccountBatchUpdateAccessError: the default message
// renders 404, the same-owner-scope variant renders 400 (route mapping).
type batchAccessError struct{ Message string }

func (e *batchAccessError) Error() string { return e.Message }

func (e *batchAccessError) sameScope() bool {
	return strings.Contains(e.Message, "同一系统账户作用域")
}

const (
	batchAccessDefaultMessage  = "批量编辑账户不存在、不可编辑或不属于同一作用域"
	batchSameScopeMessage      = "批量编辑账户必须属于同一系统账户作用域"
	batchRangeMessage          = "批量编辑账户数量必须在 2-100 个之间"
	batchRangeUniqueMessage    = "批量编辑账户数量必须在 2-100 个之间且不能重复"
	batchDuplicateMessage      = "批量编辑账户不能为空或重复"
	batchRevisionInvalidPrompt = "批量编辑账户配置版本无效"
	batchFieldsPrompt          = "批量编辑参数无效"
	batchContextPrompt         = "批量编辑上下文参数无效"
)

// batchVersionConflictError mirrors AccountBatchUpdateVersionConflictError
// (409 账户配置已发生变化，请刷新后重试：{id}).
type batchVersionConflictError struct{ AccountID string }

func (e *batchVersionConflictError) Error() string {
	return fmt.Sprintf("账户配置已发生变化，请刷新后重试：%s", e.AccountID)
}

// BatchEditContextField is one AccountBatchEditContextField.
var batchEditContextFields = map[string]bool{
	"supportedModels":        true,
	"modelMappings":          true,
	"supportedEndpointModes": true,
}

// BatchEditContextItem mirrors AccountBatchEditContextItem. Requested fields
// are always rendered (empty arrays included); unrequested fields stay
// omitted, exactly like the Node service, so presence rides on pointers.
type BatchEditContextItem struct {
	ID                        string          `json:"id"`
	ConfigRevision            int64           `json:"configRevision"`
	ProviderCode              string          `json:"providerCode"`
	ProviderProtocolProfileID string          `json:"providerProtocolProfileId"`
	ProtocolCode              string          `json:"protocolCode"`
	ProtocolVersion           string          `json:"protocolVersion"`
	Type                      string          `json:"type"`
	SupportedModels           *[]string       `json:"supportedModels,omitempty"`
	ModelMappings             *[]ModelMapping `json:"modelMappings,omitempty"`
	SupportedEndpointModes    *[]string       `json:"supportedEndpointModes,omitempty"`
}

// LoadBatchEditContext mirrors loadAccountBatchEditContextAsync: the trimmed
// id set must resolve to scope-visible, non-authorization owner rows that all
// share one owner. Returns the items in request order (owner fields stripped,
// exactly like the Node service).
func (s *Store) LoadBatchEditContext(ctx context.Context, accountIDs []string, fields []string, access AccessScope) ([]BatchEditContextItem, error) {
	ctx = ensureCtx(ctx)
	ids := make([]string, 0, len(accountIDs))
	seen := map[string]bool{}
	for _, id := range accountIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		ids = append(ids, trimmed)
	}
	if len(accountIDs) < 2 || len(accountIDs) > 100 || len(ids) != len(accountIDs) {
		return nil, &ValidationError{Message: batchRangeUniqueMessage}
	}
	fieldSet := map[string]bool{}
	for _, field := range fields {
		if !batchEditContextFields[field] {
			return nil, &ValidationError{Message: "不支持的批量编辑上下文字段：" + field}
		}
		fieldSet[field] = true
	}
	scoped := access.manageableID()
	if scoped == "" && !access.canAccessAll() {
		return nil, &batchAccessError{Message: batchAccessDefaultMessage}
	}

	scopeClause := ""
	args := anySlice(ids)
	if scoped != "" {
		scopeClause = " AND system_account_id = ?"
		args = append(args, scoped)
	}
	columns := `id, config_revision, system_account_id, provider_code,
		provider_protocol_profile_id, protocol_code, protocol_version, type`
	if fieldSet["supportedEndpointModes"] {
		columns += ", credentials_encrypted"
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+columns+` FROM `+s.table("accounts")+`
		WHERE id IN (`+placeholders(len(ids))+`)
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NULL
			AND authorization_instance_source_account_id IS NULL`+scopeClause+`
		ORDER BY id ASC`), args...)
	if err != nil {
		return nil, err
	}
	type contextRow struct {
		id              string
		configRevision  int64
		systemAccountID string
		providerCode    string
		profileID       string
		protocolCode    string
		protocolVersion string
		accountType     string
		credentials     sql.NullString
	}
	records := map[string]*contextRow{}
	for rows.Next() {
		row := &contextRow{}
		targets := []any{&row.id, &row.configRevision, &row.systemAccountID, &row.providerCode,
			&row.profileID, &row.protocolCode, &row.protocolVersion, &row.accountType}
		if fieldSet["supportedEndpointModes"] {
			targets = append(targets, &row.credentials)
		}
		if err := rows.Scan(targets...); err != nil {
			rows.Close()
			return nil, err
		}
		records[row.id] = row
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var (
		models   map[string][]string
		mappings map[string][]ModelMapping
	)
	if fieldSet["supportedModels"] {
		if models, err = s.loadBatchSupportedModels(ctx, s.db, ids); err != nil {
			return nil, err
		}
	}
	if fieldSet["modelMappings"] {
		if mappings, err = s.loadBatchModelMappings(ctx, s.db, ids); err != nil {
			return nil, err
		}
	}

	items := []BatchEditContextItem{}
	owners := map[string]bool{}
	for _, id := range ids {
		row := records[id]
		if row == nil {
			continue
		}
		owners[row.systemAccountID] = true
		item := BatchEditContextItem{
			ID: row.id, ConfigRevision: row.configRevision,
			ProviderCode: row.providerCode, ProviderProtocolProfileID: row.profileID,
			ProtocolCode: row.protocolCode, ProtocolVersion: row.protocolVersion,
			Type: row.accountType,
		}
		if fieldSet["supportedModels"] {
			fields := models[row.id]
			if fields == nil {
				fields = []string{}
			}
			item.SupportedModels = &fields
		}
		if fieldSet["modelMappings"] {
			relations := mappings[row.id]
			if relations == nil {
				relations = []ModelMapping{}
			}
			item.ModelMappings = &relations
		}
		if fieldSet["supportedEndpointModes"] {
			var credentials Credentials
			if row.credentials.Valid && strings.TrimSpace(row.credentials.String) != "" {
				if err := DecryptJSON(s.secret, row.credentials.String, &credentials); err != nil {
					return nil, err
				}
			}
			modes := storedEndpointModes(credentials["supported_endpoint_modes"])
			item.SupportedEndpointModes = &modes
		}
		items = append(items, item)
	}
	if len(items) != len(ids) {
		return nil, &batchAccessError{Message: batchAccessDefaultMessage}
	}
	if len(owners) > 1 {
		return nil, &batchAccessError{Message: batchSameScopeMessage}
	}
	return items, nil
}

func storedEndpointModes(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return []string{}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, item := range list {
		if text, ok := item.(string); ok && !seen[text] {
			seen[text] = true
			out = append(out, text)
		}
	}
	return out
}

// loadBatchSupportedModels mirrors the context repository supported-models
// branch (account_id ASC, model ASC). The queryer stays explicit so
// transactional callers never touch s.db while the tx holds the only SQLite
// connection.
func (s *Store) loadBatchSupportedModels(ctx context.Context, q queryer, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, s.bind(`SELECT account_id, model FROM `+s.table("account_supported_models")+`
		WHERE account_id IN (`+placeholders(len(ids))+`) ORDER BY account_id ASC, model ASC`), anySlice(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, model string
		if err := rows.Scan(&accountID, &model); err != nil {
			return nil, err
		}
		out[accountID] = append(out[accountID], model)
	}
	return out, rows.Err()
}

// loadBatchModelMappings mirrors the context repository model-mappings branch
// (account_id ASC, source_model ASC, source_endpoint_family ASC).
func (s *Store) loadBatchModelMappings(ctx context.Context, q queryer, ids []string) (map[string][]ModelMapping, error) {
	out := map[string][]ModelMapping{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, s.bind(`SELECT account_id, source_model, source_endpoint_family,
			upstream_model, upstream_endpoint_family, enabled
		FROM `+s.table("account_model_mappings")+`
		WHERE account_id IN (`+placeholders(len(ids))+`)
		ORDER BY account_id ASC, source_model ASC, source_endpoint_family ASC`), anySlice(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID string
		var mapping ModelMapping
		var enabled int64
		if err := rows.Scan(&accountID, &mapping.SourceModel, &mapping.SourceEndpointFamily,
			&mapping.UpstreamModel, &mapping.UpstreamEndpointFamily, &enabled); err != nil {
			return nil, err
		}
		mapping.Enabled = boolPtr(enabled == 1)
		out[accountID] = append(out[accountID], mapping)
	}
	return out, rows.Err()
}

func boolPtr(value bool) *bool { return &value }

// BatchUpdateTarget mirrors AccountBatchUpdateTarget.
type BatchUpdateTarget struct {
	AccountID      string
	ConfigRevision int64
}

// BatchUpdateField mirrors the batchUpdateFieldSchema discriminated union.
type BatchUpdateField struct {
	Enabled bool
	Value   any
}

// BatchUpdateInput is the parsed POST /accounts/batch-update payload.
type BatchUpdateInput struct {
	Targets []BatchUpdateTarget
	Updates map[string]BatchUpdateField
}

// BatchUpdateItem mirrors AccountBatchUpdateItemResult.
type BatchUpdateItem struct {
	ID             string   `json:"id"`
	ConfigRevision int64    `json:"configRevision"`
	ChangedFields  []string `json:"changedFields"`
}

// BatchUpdateResult mirrors the route response payload (the owner scope stays
// internal for the operation log, like the Node service destructure).
type BatchUpdateResult struct {
	BatchID       string            `json:"batchId"`
	ChangedFields []string          `json:"changedFields"`
	Items         []BatchUpdateItem `json:"items"`

	OwnerSystemAccountID string `json:"-"`
}

// batchLockedAccount mirrors AccountBatchUpdateLockedAccount.
type batchLockedAccount struct {
	id                                 string
	configRevision                     int64
	systemAccountID                    string
	providerCode                       string
	providerProtocolProfileID          string
	protocolCode                       string
	protocolVersion                    string
	accountType                        string
	status                             string
	credentials                        Credentials
	proxyProfileID                     sql.NullString
	balanceQueryEnabled                bool
	concurrencyLimit                   int
	priority                           int
	superPriorityEnabled               bool
	fallbackEnabled                    bool
	clientCompatibility                string
	schedulable                        bool
	availabilitySchedule               *AvailabilitySchedule
	accountExpiresAt                   sql.NullString
	notes                              sql.NullString
	cooldownUntil                      sql.NullString
	lastErrorCode                      sql.NullString
	lastErrorMessage                   sql.NullString
	cooldownRetestFailureCount         int
	cooldownRetestObservationStartedAt sql.NullString
	cooldownRetestLastAt               sql.NullString
	cooldownRetestLastStatusCode       sql.NullInt64
	healthCheckModel                   string
	healthCheckEndpointMode            string
	supportedModels                    []string
	modelMappings                      []ModelMapping
	tags                               []string
}

// batchPreparedAccount mirrors AccountBatchUpdatePreparedAccount.
type batchPreparedAccount struct {
	accountID               string
	expectedConfigRevision  int64
	changedFields           []string
	sets                    []string
	setArgs                 []any
	supportedModels         []string
	modelMappings           []ModelMapping
	hasModelMappings        bool
	tags                    []string
	hasTags                 bool
	dispatchBinding         *batchDispatchBinding
	dispatchRevisionChanged bool
	groupStatsAffected      bool
	gatewayRuntimeAffected  bool
}

// batchCredentialFieldMap mirrors the Node credentialFieldMap: batch update
// key → credentials record key for the five credential-config overrides.
var batchCredentialFieldMap = [][2]string{
	{"errorHandlingRules", "error_handling_rules"},
	{"responseInspectionRules", "response_inspection_rules"},
	{"supportedEndpointModes", "supported_endpoint_modes"},
	{"serviceTierOverride", "service_tier_override"},
	{"reasoningEffortOverride", "reasoning_effort_override"},
}

type batchDispatchBinding struct {
	priority             int
	superPriorityEnabled bool
	fallbackEnabled      bool
}

// BatchUpdate mirrors updateAccountsBatchAsync + prepareBatchUpdatesAsync:
// scope/revision-checked locked load, per-account field preparation with
// change tracking, per-account config_revision CAS increments and the
// satellite writes. Any failure aborts the whole transaction (all-or-nothing
// per request, exactly like the Node repository transaction).
func (s *Store) BatchUpdate(ctx context.Context, input BatchUpdateInput, access AccessScope) (*BatchUpdateResult, error) {
	ctx = ensureCtx(ctx)
	if err := assertBatchTargets(input.Targets); err != nil {
		return nil, err
	}
	requestedFields := make([]string, 0, len(input.Updates))
	for field, update := range input.Updates {
		if update.Enabled {
			requestedFields = append(requestedFields, field)
		}
	}
	if len(requestedFields) == 0 {
		return nil, &ValidationError{Message: "请至少选择一项需要覆盖的配置"}
	}
	sortStrings(requestedFields)

	scoped := access.manageableID()
	if scoped == "" && !access.canAccessAll() {
		return nil, &batchAccessError{Message: batchAccessDefaultMessage}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	accounts, err := s.loadBatchLockedAccounts(ctx, tx, input.Targets, requestedFields, scoped, access)
	if err != nil {
		return nil, err
	}
	if err := assertLockedAccountsMatchTargets(accounts, input.Targets); err != nil {
		return nil, err
	}
	prepared, err := s.prepareBatchUpdates(ctx, tx, accounts, input.Updates)
	if err != nil {
		return nil, err
	}
	if err := assertPreparedAccountsMatchTargets(prepared, input.Targets); err != nil {
		return nil, err
	}

	now := s.now()
	nowISO := isoMillis(now)
	batchID := s.newI("account_batch")
	revisionByAccount := map[string]int64{}
	for _, target := range input.Targets {
		revisionByAccount[target.AccountID] = target.ConfigRevision
	}
	items := []BatchUpdateItem{}
	changedFields := map[string]bool{}
	// Post-commit side-effect audiences (Node transactionResult
	// changedAccountIds/statsAccountIds/gatewayAccountIds,
	// account-batch-update.repository.ts:195-213).
	changedAccountIDs := []string{}
	statsAccountIDs := []string{}
	gatewayAccountIDs := []string{}
	owner := ""
	if len(accounts) > 0 {
		owner = accounts[0].systemAccountID
	}
	byID := map[string]*batchLockedAccount{}
	for index := range accounts {
		byID[accounts[index].id] = &accounts[index]
	}
	for _, account := range prepared {
		current := byID[account.accountID]
		if current == nil {
			return nil, &batchAccessError{Message: batchAccessDefaultMessage}
		}
		expected, ok := revisionByAccount[account.accountID]
		if !ok || expected != account.expectedConfigRevision {
			return nil, &batchVersionConflictError{AccountID: account.accountID}
		}
		if len(account.changedFields) == 0 {
			items = append(items, BatchUpdateItem{ID: account.accountID, ConfigRevision: current.configRevision, ChangedFields: []string{}})
			continue
		}
		if err := s.executeBatchCasUpdate(ctx, tx, &account, current.systemAccountID, nowISO); err != nil {
			return nil, err
		}
		if account.supportedModels != nil {
			if err := s.replaceAccountSupportedModels(ctx, tx, account.accountID, current.providerCode, account.supportedModels, nowISO); err != nil {
				return nil, err
			}
		}
		if account.hasModelMappings {
			if err := s.replaceAccountModelMappings(ctx, tx, account.accountID, current.providerCode, account.modelMappings, nowISO); err != nil {
				return nil, err
			}
		}
		if account.hasTags {
			if _, err := s.replaceAccountTags(ctx, tx, account.accountID, current.systemAccountID, account.tags, nowISO); err != nil {
				return nil, err
			}
		}
		if account.dispatchRevisionChanged {
			// In-transaction dispatch family advance (Node
			// account-batch-update.repository.ts:167-174): any error aborts
			// the whole batch with the transaction.
			if err := s.advanceBatchDispatchRevisionFamily(ctx, tx, batchDispatchRevision{
				accountID:    account.accountID,
				transitionID: batchID + ":" + account.accountID,
				nowMS:        now.UnixMilli(),
			}); err != nil {
				return nil, err
			}
		}
		if account.dispatchBinding != nil {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("group_accounts")+`
				SET local_priority = ?, local_super_priority_enabled = ?, local_fallback_enabled = ?, updated_at = ?
				WHERE account_id = ? AND system_account_id = ? AND enabled = 1`),
				account.dispatchBinding.priority, boolInt(account.dispatchBinding.superPriorityEnabled),
				boolInt(account.dispatchBinding.fallbackEnabled), nowISO,
				account.accountID, current.systemAccountID); err != nil {
				return nil, err
			}
		}
		item := BatchUpdateItem{ID: account.accountID, ConfigRevision: account.expectedConfigRevision + 1, ChangedFields: []string{}}
		for _, field := range account.changedFields {
			item.ChangedFields = append(item.ChangedFields, field)
			changedFields[field] = true
		}
		items = append(items, item)
		changedAccountIDs = append(changedAccountIDs, account.accountID)
		if account.groupStatsAffected {
			statsAccountIDs = append(statsAccountIDs, account.accountID)
		}
		if account.gatewayRuntimeAffected {
			gatewayAccountIDs = append(gatewayAccountIDs, account.accountID)
		}
	}

	result := &BatchUpdateResult{BatchID: batchID, Items: items, OwnerSystemAccountID: owner}
	for field := range changedFields {
		result.ChangedFields = append(result.ChangedFields, field)
	}
	sortStrings(result.ChangedFields)
	if result.ChangedFields == nil {
		result.ChangedFields = []string{}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Post-commit best-effort chain (Node account-batch-update.repository.ts:
	// 216-241): lookup + stats + runtime invalidation, failures logged only.
	// Node also calls cleanupChangedBalanceSnapshots here for proxy-changed
	// accounts (account-batch-edit.service.ts:97,398-412); the Go side has no
	// balance snapshot mechanism, so that step stays unported.
	s.finishBatchUpdateSideEffects(ctx, batchID, changedAccountIDs, statsAccountIDs, gatewayAccountIDs)
	return result, nil
}

// assertBatchTargets mirrors assertBatchTargets.
func assertBatchTargets(targets []BatchUpdateTarget) error {
	if len(targets) < 2 || len(targets) > 100 {
		return &ValidationError{Message: batchRangeMessage}
	}
	seen := map[string]bool{}
	for _, target := range targets {
		id := strings.TrimSpace(target.AccountID)
		if id == "" || seen[id] {
			return &ValidationError{Message: batchDuplicateMessage}
		}
		seen[id] = true
		if target.ConfigRevision < 1 {
			return &ValidationError{Message: batchRevisionInvalidPrompt}
		}
	}
	return nil
}

// loadBatchLockedAccounts mirrors loadLockedAccountsAsync (full column
// projection; the Node field projection is a read-optimization only).
func (s *Store) loadBatchLockedAccounts(ctx context.Context, q queryer, targets []BatchUpdateTarget, requestedFields []string, scoped string, access AccessScope) ([]batchLockedAccount, error) {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, strings.TrimSpace(target.AccountID))
	}
	rows, err := s.queryBatchAccountRows(ctx, q, ids, scoped)
	if err != nil {
		return nil, err
	}
	fields := map[string]bool{}
	for _, field := range requestedFields {
		fields[field] = true
	}
	needModels := fields["supportedModels"] || fields["healthCheckModel"] || fields["modelMappings"] ||
		fields["supportedEndpointModes"] || fields["serviceTierOverride"] || fields["reasoningEffortOverride"]
	needMappings := fields["modelMappings"] || fields["supportedModels"] || fields["supportedEndpointModes"]
	needTags := fields["tags"]
	models := map[string][]string{}
	mappings := map[string][]ModelMapping{}
	tags := map[string][]string{}
	if needModels {
		if models, err = s.loadBatchSupportedModels(ctx, q, ids); err != nil {
			return nil, err
		}
	}
	if needMappings {
		if mappings, err = s.loadBatchModelMappings(ctx, q, ids); err != nil {
			return nil, err
		}
	}
	if needTags {
		if tags, err = s.loadBatchTags(ctx, q, ids); err != nil {
			return nil, err
		}
	}
	for index := range rows {
		rows[index].supportedModels = models[rows[index].id]
		if rows[index].supportedModels == nil {
			rows[index].supportedModels = []string{}
		}
		rows[index].modelMappings = mappings[rows[index].id]
		rows[index].tags = tags[rows[index].id]
		if rows[index].tags == nil {
			rows[index].tags = []string{}
		}
	}
	return rows, nil
}

func (s *Store) queryBatchAccountRows(ctx context.Context, q queryer, ids []string, scoped string) ([]batchLockedAccount, error) {
	scopeClause := ""
	args := anySlice(ids)
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	rows, err := q.QueryContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision, accounts.system_account_id,
			accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code,
			accounts.protocol_version, accounts.type, accounts.status, accounts.credentials_encrypted,
			accounts.proxy_profile_id, accounts.balance_query_enabled, accounts.concurrency_limit,
			accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
			accounts.client_compatibility, accounts.schedulable, accounts.availability_schedule_json,
			accounts.account_expires_at, accounts.notes, accounts.cooldown_until, accounts.last_error_code,
			accounts.last_error_message, accounts.cooldown_retest_failure_count,
			accounts.cooldown_retest_observation_started_at, accounts.cooldown_retest_last_at,
			accounts.cooldown_retest_last_status_code, accounts.health_check_model,
			accounts.health_check_endpoint_mode
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id IN (`+placeholders(len(ids))+`)
			AND accounts.deleted_at IS NULL
			AND accounts.authorization_instance_authorization_id IS NULL
			AND accounts.authorization_instance_source_account_id IS NULL`+scopeClause+`
		ORDER BY accounts.id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []batchLockedAccount{}
	for rows.Next() {
		var account batchLockedAccount
		var credentialsEncrypted string
		var superPriority, fallback, schedulable, balanceEnabled int64
		var scheduleJSON sql.NullString
		if err := rows.Scan(&account.id, &account.configRevision, &account.systemAccountID,
			&account.providerCode, &account.providerProtocolProfileID, &account.protocolCode,
			&account.protocolVersion, &account.accountType, &account.status, &credentialsEncrypted,
			&account.proxyProfileID, &balanceEnabled, &account.concurrencyLimit,
			&account.priority, &superPriority, &fallback,
			&account.clientCompatibility, &schedulable, &scheduleJSON,
			&account.accountExpiresAt, &account.notes, &account.cooldownUntil, &account.lastErrorCode,
			&account.lastErrorMessage, &account.cooldownRetestFailureCount,
			&account.cooldownRetestObservationStartedAt, &account.cooldownRetestLastAt,
			&account.cooldownRetestLastStatusCode, &account.healthCheckModel,
			&account.healthCheckEndpointMode); err != nil {
			return nil, err
		}
		account.credentials = Credentials{}
		if strings.TrimSpace(credentialsEncrypted) != "" {
			if err := DecryptJSON(s.secret, credentialsEncrypted, &account.credentials); err != nil {
				return nil, err
			}
		}
		account.balanceQueryEnabled = balanceEnabled == 1
		account.superPriorityEnabled = superPriority == 1
		account.fallbackEnabled = fallback == 1
		account.schedulable = schedulable == 1
		if schedule, err := ParseScheduleJSON(scheduleJSON.String); err == nil {
			account.availabilitySchedule = schedule
		}
		out = append(out, account)
	}
	return out, rows.Err()
}

// loadBatchTags mirrors the repository tags branch (binding join, name ASC).
func (s *Store) loadBatchTags(ctx context.Context, q queryer, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, s.bind(`SELECT account_tag_bindings.account_id, account_tags.name
		FROM `+s.table("account_tag_bindings")+` account_tag_bindings
		INNER JOIN `+s.table("account_tags")+` account_tags
			ON account_tags.id = account_tag_bindings.tag_id
		WHERE account_tag_bindings.account_id IN (`+placeholders(len(ids))+`)
		ORDER BY account_tag_bindings.account_id ASC, account_tags.name ASC`), anySlice(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, name string
		if err := rows.Scan(&accountID, &name); err != nil {
			return nil, err
		}
		out[accountID] = append(out[accountID], name)
	}
	return out, rows.Err()
}

// assertLockedAccountsMatchTargets mirrors the repository target assertions.
func assertLockedAccountsMatchTargets(accounts []batchLockedAccount, targets []BatchUpdateTarget) error {
	if len(accounts) != len(targets) {
		return &batchAccessError{Message: batchAccessDefaultMessage}
	}
	byID := map[string]BatchUpdateTarget{}
	for _, target := range targets {
		byID[strings.TrimSpace(target.AccountID)] = target
	}
	owners := map[string]bool{}
	for _, account := range accounts {
		target, ok := byID[account.id]
		if !ok {
			return &batchAccessError{Message: batchAccessDefaultMessage}
		}
		if account.configRevision != target.ConfigRevision {
			return &batchVersionConflictError{AccountID: account.id}
		}
		owners[account.systemAccountID] = true
	}
	if len(owners) > 1 {
		return &batchAccessError{Message: batchSameScopeMessage}
	}
	return nil
}

func assertPreparedAccountsMatchTargets(prepared []batchPreparedAccount, targets []BatchUpdateTarget) error {
	if len(prepared) != len(targets) {
		return &ValidationError{Message: "批量编辑最终配置数量不匹配"}
	}
	ids := map[string]bool{}
	for _, account := range prepared {
		ids[account.accountID] = true
	}
	for _, target := range targets {
		if !ids[strings.TrimSpace(target.AccountID)] {
			return &ValidationError{Message: "批量编辑最终配置目标不匹配"}
		}
	}
	return nil
}

// batchModelConfigurationFields mirrors modelConfigurationFields.
var batchModelConfigurationFields = map[string]bool{
	"supportedModels": true, "healthCheckModel": true, "healthCheckEndpointMode": true,
	"modelMappings": true, "supportedEndpointModes": true,
	"serviceTierOverride": true, "reasoningEffortOverride": true,
}

// prepareBatchUpdates mirrors prepareBatchUpdatesAsync: all 16 request fields
// are honored (the five credential-config overrides landed with the
// credential-normalization slice); the provider-catalog validations Node runs
// for model mappings and the gpt request overrides stay with the
// model-validation companion slice (registered M09 deferral).
func (s *Store) prepareBatchUpdates(ctx context.Context, q queryer, accounts []batchLockedAccount, updates map[string]BatchUpdateField) ([]batchPreparedAccount, error) {
	homogeneous := false
	for field := range updates {
		if updates[field].Enabled && batchModelConfigurationFields[field] {
			homogeneous = true
			break
		}
	}
	if homogeneous {
		signatures := map[string]bool{}
		for _, account := range accounts {
			signatures[strings.Join([]string{account.providerCode, account.providerProtocolProfileID, account.accountType}, "\x00")] = true
		}
		if len(signatures) > 1 {
			return nil, &ValidationError{Message: "模型与协议配置只能批量覆盖到相同供应商、协议档案和账户类型的账户"}
		}
	}
	resolvedProxy := ""
	resolveProxy := false
	if update, ok := updates["proxyProfileId"]; ok && update.Enabled {
		resolveProxy = true
		if update.Value != nil {
			text := strings.TrimSpace(textString(update.Value))
			if text == "" {
				return nil, &ValidationError{Message: "代理配置不能为空"}
			}
			owner := ""
			if len(accounts) > 0 {
				owner = accounts[0].systemAccountID
			}
			if strings.TrimSpace(owner) == "" {
				return nil, &ValidationError{Message: "账户归属不能为空"}
			}
			var id string
			var enabled int64
			err := q.QueryRowContext(ctx, s.bind(`SELECT id, enabled FROM `+s.table("proxy_profiles")+`
				WHERE id = ? AND system_account_id = ? LIMIT 1`), text, owner).Scan(&id, &enabled)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && enabled != 1) {
				return nil, &ValidationError{Message: "代理不存在或已停用，请选择一个已启用的代理"}
			}
			if err != nil {
				return nil, err
			}
			resolvedProxy = id
		}
	}

	prepared := make([]batchPreparedAccount, 0, len(accounts))
	for index := range accounts {
		result, err := s.prepareBatchAccount(&accounts[index], updates, resolvedProxy, resolveProxy)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, result)
	}
	return prepared, nil
}

// applyNullableCredentialOverride mirrors applyNullableCredentialOverride: a
// null/” batch value deletes the credential key, everything else copies.
func applyNullableCredentialOverride(credentials Credentials, updates map[string]BatchUpdateField, updateKey, credentialKey string) {
	update, ok := updates[updateKey]
	if !ok || !update.Enabled {
		return
	}
	if update.Value == nil || update.Value == "" {
		delete(credentials, credentialKey)
		return
	}
	credentials[credentialKey] = update.Value
}

// jsonValueDeepEqual mirrors Node isDeepStrictEqual for decoded-JSON values:
// both sides render through a canonical JSON round-trip before comparison.
func jsonValueDeepEqual(left, right any) bool {
	leftEncoded, err := json.Marshal(canonicalizeJSONValue(left))
	if err != nil {
		return false
	}
	rightEncoded, err := json.Marshal(canonicalizeJSONValue(right))
	if err != nil {
		return false
	}
	return string(leftEncoded) == string(rightEncoded)
}

// prepareBatchAccount mirrors prepareAccountUpdateAsync for the supported
// field subset. changedFields are collected and sorted; main-column writes use
// the same physical columns the Node repository assigns.
func (s *Store) prepareBatchAccount(account *batchLockedAccount, updates map[string]BatchUpdateField, resolvedProxy string, resolveProxy bool) (batchPreparedAccount, error) {
	result := batchPreparedAccount{
		accountID:              account.id,
		expectedConfigRevision: account.configRevision,
		changedFields:          []string{},
	}
	addChange := func(field string) { result.changedFields = append(result.changedFields, field) }
	setColumn := func(column string, value any) {
		result.sets = append(result.sets, column+" = ?")
		result.setArgs = append(result.setArgs, value)
	}
	hasOwn := func(field string) bool {
		update, ok := updates[field]
		return ok && update.Enabled
	}
	fieldValue := func(field string) any {
		return updates[field].Value
	}

	// Credential-config overrides (account-batch-edit.service.ts:139-183):
	// merge the five fields into the decrypted credentials record, normalize
	// through NormalizeAccountCredentialsForWrite and re-seal on change.
	nextCredentials := account.credentials
	credentialsChangedFields := map[string]bool{}
	credentialsChanged := false
	hasCredentialConfigUpdate := false
	for _, pair := range batchCredentialFieldMap {
		if hasOwn(pair[0]) {
			hasCredentialConfigUpdate = true
			break
		}
	}
	if hasCredentialConfigUpdate {
		merged := Credentials{}
		for key, value := range account.credentials {
			merged[key] = value
		}
		if hasOwn("errorHandlingRules") {
			rules, err := normalizeAccountErrorHandlingRules(fieldValue("errorHandlingRules"))
			if err != nil {
				return result, err
			}
			merged["error_handling_rules"] = rules
		}
		if hasOwn("responseInspectionRules") {
			rules, err := normalizeAccountResponseInspectionRules(fieldValue("responseInspectionRules"))
			if err != nil {
				return result, err
			}
			merged["response_inspection_rules"] = rules
		}
		if hasOwn("supportedEndpointModes") {
			merged["supported_endpoint_modes"] = fieldValue("supportedEndpointModes")
		}
		applyNullableCredentialOverride(merged, updates, "serviceTierOverride", "service_tier_override")
		applyNullableCredentialOverride(merged, updates, "reasoningEffortOverride", "reasoning_effort_override")
		normalized, err := NormalizeAccountCredentialsForWrite(account.accountType, merged, &EndpointModeDefaultContext{
			ProviderCode:              account.providerCode,
			AccountType:               account.accountType,
			ClientCompatibility:       account.clientCompatibility,
			ProviderProtocolProfileID: account.providerProtocolProfileID,
			ProtocolCode:              account.protocolCode,
			ProtocolVersion:           account.protocolVersion,
		})
		if err != nil {
			return result, err
		}
		nextCredentials = normalized
		for _, pair := range batchCredentialFieldMap {
			if !hasOwn(pair[0]) {
				continue
			}
			if !jsonValueDeepEqual(account.credentials[pair[1]], nextCredentials[pair[1]]) {
				credentialsChangedFields[pair[0]] = true
				credentialsChanged = true
			}
		}
		if credentialsChanged {
			sealed, err := EncryptJSON(s.secret, map[string]any(nextCredentials))
			if err != nil {
				return result, err
			}
			setColumn("credentials_encrypted", sealed)
		}
	}

	// supportedModels.
	nextSupportedModels := account.supportedModels
	supportedModelsChanged := false
	if hasOwn("supportedModels") {
		models, err := normalizeSupportedModelsInput(fieldValue("supportedModels"))
		if err != nil {
			return result, err
		}
		if len(models) == 0 {
			models = []string{}
		}
		nextSupportedModels = models
	}
	// Node asserts the required supported-model set only when a model
	// configuration field participates in the batch.
	modelConfigurationRelevant := hasOwn("supportedModels") || hasOwn("healthCheckModel") ||
		hasOwn("modelMappings") || hasOwn("supportedEndpointModes") ||
		hasOwn("serviceTierOverride") || hasOwn("reasoningEffortOverride")
	if modelConfigurationRelevant {
		if err := assertSupportedModelsRequired(nextSupportedModels); err != nil {
			return result, err
		}
	}
	supportedModelsChanged = hasOwn("supportedModels") && !unorderedStringListEqual(account.supportedModels, nextSupportedModels)
	if supportedModelsChanged {
		addChange("supportedModels")
		result.supportedModels = nextSupportedModels
	}

	// healthCheckModel.
	nextHealthCheckModel := strings.TrimSpace(account.healthCheckModel)
	if hasOwn("healthCheckModel") {
		text := strings.TrimSpace(textString(fieldValue("healthCheckModel")))
		if text == "" {
			return result, &ValidationError{Message: "账户检查模型不能为空"}
		}
		nextHealthCheckModel = text
	}
	if hasOwn("healthCheckModel") || supportedModelsChanged {
		found := false
		for _, model := range nextSupportedModels {
			if model == nextHealthCheckModel {
				found = true
				break
			}
		}
		if !found {
			return result, &ValidationError{Message: "账户 " + account.id + " 的检查模型必须属于最终支持模型"}
		}
	}
	healthCheckModelChanged := hasOwn("healthCheckModel") && nextHealthCheckModel != strings.TrimSpace(account.healthCheckModel)
	if healthCheckModelChanged {
		addChange("healthCheckModel")
		setColumn("health_check_model", nextHealthCheckModel)
	}

	// healthCheckEndpointMode (account-batch-edit.service.ts:223-253): the
	// explicit value resolves against the final enabled endpoint modes. The
	// images_json model-catalog confirmation stays with the model-validation
	// companion slice, so the pre-slice fallback
	// (account.healthCheckEndpointMode === 'images_json') stands in.
	nextHealthCheckEndpointMode := account.healthCheckEndpointMode
	resolveHealthMode := hasOwn("healthCheckEndpointMode") || hasOwn("supportedEndpointModes")
	if hasOwn("healthCheckEndpointMode") {
		nextHealthCheckEndpointMode = textString(fieldValue("healthCheckEndpointMode"))
	}
	if resolveHealthMode {
		resolved, err := resolveHealthCheckEndpointMode(
			&nextHealthCheckEndpointMode,
			account.providerCode,
			account.providerProtocolProfileID,
			storedEndpointModes(nextCredentials["supported_endpoint_modes"]),
			boolPtr(account.healthCheckEndpointMode == "images_json"),
		)
		if err != nil {
			return result, err
		}
		nextHealthCheckEndpointMode = resolved
	}
	if nextHealthCheckEndpointMode != account.healthCheckEndpointMode {
		addChange("healthCheckEndpointMode")
		setColumn("health_check_endpoint_mode", nextHealthCheckEndpointMode)
	}

	// modelMappings.
	nextModelMappings := account.modelMappings
	shouldValidateMappings := hasOwn("modelMappings") || hasOwn("supportedEndpointModes")
	if hasOwn("modelMappings") {
		mappings, err := normalizeBatchModelMappings(fieldValue("modelMappings"))
		if err != nil {
			return result, err
		}
		nextModelMappings = mappings
	}
	if shouldValidateMappings || supportedModelsChanged {
		if err := assertMappingUpstreamsAllowed(nextModelMappings, nextSupportedModels); err != nil {
			return result, err
		}
	}
	// Node also runs assertEndpointModesCompatible here against the final
	// credential endpoint modes.
	if shouldValidateMappings || hasOwn("healthCheckEndpointMode") {
		if err := assertEndpointModesCompatible(account.providerCode, account.accountType, account.clientCompatibility,
			protocolPredicateInput{
				providerCode:              account.providerCode,
				protocolCode:              account.protocolCode,
				protocolVersion:           account.protocolVersion,
				providerProtocolProfileID: account.providerProtocolProfileID,
			},
			storedEndpointModes(nextCredentials["supported_endpoint_modes"])); err != nil {
			return result, err
		}
	}
	modelMappingsChanged := shouldValidateMappings && !modelMappingsEqual(account.modelMappings, nextModelMappings)
	if modelMappingsChanged {
		addChange("modelMappings")
		result.modelMappings = nextModelMappings
		result.hasModelMappings = true
	}

	// tags.
	nextTags := account.tags
	if hasOwn("tags") {
		names, err := normalizeAccountTagNamesInput(fieldValue("tags"))
		if err != nil {
			return result, err
		}
		nextTags = names
		if nextTags == nil {
			nextTags = []string{}
		}
	}
	tagsChanged := hasOwn("tags") && !unorderedStringListEqual(account.tags, nextTags)
	if tagsChanged {
		addChange("tags")
		result.tags = nextTags
		result.hasTags = true
	}

	// proxyProfileId.
	var nextProxy *string
	proxyChanged := false
	if resolveProxy {
		if resolvedProxy != "" {
			text := resolvedProxy
			nextProxy = &text
		}
		currentProxy := nullPtrString(account.proxyProfileID)
		proxyChanged = !nullableTextEqual(nextProxy, currentProxy)
		if proxyChanged {
			addChange("proxyProfileId")
			if nextProxy == nil {
				setColumn("proxy_profile_id", nil)
			} else {
				setColumn("proxy_profile_id", *nextProxy)
			}
			if account.balanceQueryEnabled {
				setColumn("balance_query_next_refresh_at", isoMillis(s.now()))
			}
		}
	}
	// Node account-batch-edit.service.ts:391: dispatchRevisionChanged: proxyChanged —
	// the same field comparison that drives the changedFields entry.
	result.dispatchRevisionChanged = proxyChanged

	// Dispatch fields.
	nextConcurrency := account.concurrencyLimit
	if hasOwn("concurrencyLimit") {
		nextConcurrency = intValue(fieldValue("concurrencyLimit"), account.concurrencyLimit)
	}
	concurrencyChanged := hasOwn("concurrencyLimit") && nextConcurrency != account.concurrencyLimit
	if concurrencyChanged {
		addChange("concurrencyLimit")
		setColumn("concurrency_limit", nextConcurrency)
	}
	nextPriority := account.priority
	if hasOwn("priority") {
		nextPriority = intValue(fieldValue("priority"), account.priority)
	}
	nextSuper := account.superPriorityEnabled
	if hasOwn("superPriorityEnabled") {
		nextSuper = boolValue(fieldValue("superPriorityEnabled"), account.superPriorityEnabled)
	}
	nextFallback := account.fallbackEnabled
	if hasOwn("fallbackEnabled") {
		nextFallback = boolValue(fieldValue("fallbackEnabled"), account.fallbackEnabled)
	}
	if nextSuper && nextFallback {
		if hasOwn("superPriorityEnabled") && boolValue(fieldValue("superPriorityEnabled"), false) && !hasOwn("fallbackEnabled") {
			nextFallback = false
		} else if hasOwn("fallbackEnabled") && boolValue(fieldValue("fallbackEnabled"), false) && !hasOwn("superPriorityEnabled") {
			nextSuper = false
		} else {
			return result, &ValidationError{Message: "超级优先和降级备用不能同时开启"}
		}
	}
	priorityChanged := nextPriority != account.priority
	superChanged := nextSuper != account.superPriorityEnabled
	fallbackChanged := nextFallback != account.fallbackEnabled
	if priorityChanged {
		addChange("priority")
		setColumn("priority", nextPriority)
	}
	if superChanged {
		addChange("superPriorityEnabled")
		setColumn("super_priority_enabled", boolInt(nextSuper))
	}
	if fallbackChanged {
		addChange("fallbackEnabled")
		setColumn("fallback_enabled", boolInt(nextFallback))
	}
	dispatchChanged := priorityChanged || superChanged || fallbackChanged
	if dispatchChanged {
		result.dispatchBinding = &batchDispatchBinding{
			priority: nextPriority, superPriorityEnabled: nextSuper, fallbackEnabled: nextFallback,
		}
	}

	// accountExpiresAt.
	nextExpiresAt := account.accountExpiresAt
	if hasOwn("accountExpiresAt") {
		value := fieldValue("accountExpiresAt")
		if value == nil {
			nextExpiresAt = sql.NullString{}
		} else {
			canonical, valid := canonicalRFC3339(textString(value))
			if !valid {
				return result, &ValidationError{Message: "账户套餐到期时间必须是有效时间字符串"}
			}
			nextExpiresAt = sql.NullString{String: canonical, Valid: true}
		}
	}
	expiresAtChanged := hasOwn("accountExpiresAt") &&
		(nextExpiresAt.Valid != account.accountExpiresAt.Valid || nextExpiresAt.String != account.accountExpiresAt.String)
	if expiresAtChanged {
		addChange("accountExpiresAt")
		if nextExpiresAt.Valid {
			setColumn("account_expires_at", nextExpiresAt.String)
		} else {
			setColumn("account_expires_at", nil)
		}
	}

	// availabilitySchedule.
	nextSchedule := account.availabilitySchedule
	scheduleChanged := false
	if hasOwn("availabilitySchedule") {
		schedule, err := NormalizeSchedule(fieldValue("availabilitySchedule"))
		if err != nil {
			return result, accountScheduleError(err)
		}
		nextSchedule = schedule
		currentJSON, _ := ScheduleJSON(account.availabilitySchedule)
		nextJSON, _ := ScheduleJSON(nextSchedule)
		scheduleChanged = currentJSON != nextJSON
		if scheduleChanged {
			addChange("availabilitySchedule")
			if raw, ok := ScheduleJSON(nextSchedule); ok {
				setColumn("availability_schedule_json", raw)
			} else {
				setColumn("availability_schedule_json", nil)
			}
			if nextCheck, ok := NextScheduleCheckAt(nextSchedule, s.now()); ok {
				setColumn("availability_schedule_next_check_at", nextCheck)
			} else {
				setColumn("availability_schedule_next_check_at", nil)
			}
		}
	}

	// notes.
	if hasOwn("notes") {
		text := strings.TrimSpace(textString(fieldValue("notes")))
		var next sql.NullString
		if text != "" {
			next = sql.NullString{String: text, Valid: true}
		}
		if next.Valid != account.notes.Valid || next.String != account.notes.String {
			addChange("notes")
			if next.Valid {
				setColumn("notes", next.String)
			} else {
				setColumn("notes", nil)
			}
		}
	}

	// Package expiry / schedule driven runtime state.
	nextStatus := account.status
	nextSchedulable := account.schedulable
	expiredByChangedPackage := expiresAtChanged && isAccountExpired(nextExpiresAt.String, s.now())
	if expiredByChangedPackage {
		nextStatus = "disabled"
		nextSchedulable = false
		setColumn("cooldown_until", nil)
		setColumn("last_error_code", "account_expired")
		setColumn("last_error_message", "账户套餐已过期，已自动停用")
		setColumn("cooldown_retest_failure_count", 0)
		setColumn("cooldown_retest_observation_started_at", nil)
		setColumn("cooldown_retest_last_at", nil)
		setColumn("cooldown_retest_last_status_code", nil)
	} else if scheduleChanged {
		if override, ok := ScheduleStatus(nextSchedule, s.now()); ok &&
			(account.status == "active" || account.status == "disabled") {
			nextStatus = override
		}
		if nextStatus != account.status && batchStatusForcesSchedulableOff(nextStatus) {
			nextSchedulable = false
		}
	}
	if nextStatus != account.status {
		addChange("status")
		setColumn("status", nextStatus)
	}
	if nextSchedulable != account.schedulable {
		addChange("schedulable")
		setColumn("schedulable", boolInt(nextSchedulable))
	}

	// Health check reschedule marker (column exists in the maintenance schema).
	shouldScheduleHealthCheck := proxyChanged || supportedModelsChanged || healthCheckModelChanged ||
		nextHealthCheckEndpointMode != account.healthCheckEndpointMode || modelMappingsChanged ||
		(credentialsChanged && hasOwn("supportedEndpointModes"))
	if shouldScheduleHealthCheck && nextStatus != "disabled" {
		setColumn("next_health_check_at", nil)
	}

	// Record the credential-config field changes after the status/health block
	// so changedFields stay sorted with the rest (Node collects into a Set and
	// sorts at the end).
	for _, pair := range batchCredentialFieldMap {
		if credentialsChangedFields[pair[0]] {
			addChange(pair[0])
		}
	}

	sortStrings(result.changedFields)
	groupStatsFields := map[string]bool{"status": true, "schedulable": true, "concurrencyLimit": true}
	gatewayFields := map[string]bool{
		"status": true, "schedulable": true, "concurrencyLimit": true, "priority": true,
		"superPriorityEnabled": true, "fallbackEnabled": true, "proxyProfileId": true,
		"supportedModels": true, "modelMappings": true, "healthCheckModel": true,
		"healthCheckEndpointMode": true, "availabilitySchedule": true, "accountExpiresAt": true,
		"errorHandlingRules": true, "responseInspectionRules": true, "supportedEndpointModes": true,
		"serviceTierOverride": true, "reasoningEffortOverride": true,
	}
	for _, field := range result.changedFields {
		if groupStatsFields[field] {
			result.groupStatsAffected = true
		}
		if gatewayFields[field] {
			result.gatewayRuntimeAffected = true
		}
	}
	return result, nil
}

func batchStatusForcesSchedulableOff(status string) bool {
	return status == "pending_test" || status == "error" || status == "rate_limited" || status == "temporary_unavailable"
}

// nullableTextEqual compares two optional text values (nil vs empty collapse
// is avoided: SQL NULL and ” compare as written).
func nullableTextEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

// accountScheduleError mirrors normalizeAccountAvailabilitySchedule: the API
// Key schedule copy is rebranded to the account copy.
func accountScheduleError(err error) error {
	var validation *ValidationError
	if errors.As(err, &validation) {
		return &ValidationError{Message: strings.ReplaceAll(validation.Message, "API Key 时间计划", "账户时间计划")}
	}
	return err
}

// executeBatchCasUpdate mirrors executeAccountBatchCasUpdate.
func (s *Store) executeBatchCasUpdate(ctx context.Context, q queryer, prepared *batchPreparedAccount, ownerSystemAccountID, updatedAt string) error {
	assignments := append([]string{}, prepared.sets...)
	args := append([]any{}, prepared.setArgs...)
	assignments = append(assignments, "config_revision = config_revision + 1", "updated_at = ?")
	args = append(args, updatedAt, prepared.accountID, prepared.expectedConfigRevision, ownerSystemAccountID)
	exec, err := q.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET
		`+strings.Join(assignments, ", ")+`
		WHERE id = ?
			AND config_revision = ?
			AND system_account_id = ?
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NULL
			AND authorization_instance_source_account_id IS NULL`), args...)
	if err != nil {
		return err
	}
	if affected, _ := exec.RowsAffected(); affected != 1 {
		return &batchVersionConflictError{AccountID: prepared.accountID}
	}
	return nil
}

// normalizeBatchModelMappings mirrors the import parser contract the batch
// payload shares: shape validation, identity mappings dropped, source dedupe.
func normalizeBatchModelMappings(value any) ([]ModelMapping, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, &ValidationError{Message: "账户 modelMappings 必须是模型映射数组"}
	}
	out := []ModelMapping{}
	seenSources := map[string]bool{}
	for _, item := range list {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, &ValidationError{Message: "账户 modelMappings 条目必须是对象"}
		}
		mapping := ModelMapping{
			SourceModel:            strings.TrimSpace(textString(record["sourceModel"])),
			SourceEndpointFamily:   strings.TrimSpace(textString(record["sourceEndpointFamily"])),
			UpstreamModel:          strings.TrimSpace(textString(record["upstreamModel"])),
			UpstreamEndpointFamily: strings.TrimSpace(textString(record["upstreamEndpointFamily"])),
		}
		if mapping.SourceModel == "" || mapping.SourceEndpointFamily == "" ||
			mapping.UpstreamModel == "" || mapping.UpstreamEndpointFamily == "" {
			return nil, &ValidationError{Message: "账户 modelMappings 条目必须包含 sourceModel、sourceEndpointFamily、upstreamModel 和 upstreamEndpointFamily"}
		}
		if !batchMappingSourceFamilies[mapping.SourceEndpointFamily] || !batchMappingUpstreamFamilies[mapping.UpstreamEndpointFamily] {
			return nil, &ValidationError{Message: batchFieldsPrompt}
		}
		if mapping.SourceModel == mapping.UpstreamModel && mapping.SourceEndpointFamily == mapping.UpstreamEndpointFamily {
			continue
		}
		sourceKey := mapping.SourceEndpointFamily + "\n" + mapping.SourceModel
		if seenSources[sourceKey] {
			return nil, &ValidationError{Message: "账户 modelMappings 不能重复配置同一个 sourceModel 和 sourceEndpointFamily：" + mapping.SourceModel + " / " + mapping.SourceEndpointFamily}
		}
		seenSources[sourceKey] = true
		enabled := true
		if value, ok := record["enabled"].(bool); ok {
			enabled = value
		}
		mapping.Enabled = &enabled
		out = append(out, mapping)
	}
	return out, nil
}

var batchMappingSourceFamilies = map[string]bool{
	"chat_completions": true, "responses": true, "messages": true,
	"generate_content": true, "stream_generate_content": true,
}

var batchMappingUpstreamFamilies = map[string]bool{
	"chat_completions": true, "responses": true, "messages": true, "generate_content": true,
}

// assertMappingUpstreamsAllowed mirrors
// assertAccountModelMappingUpstreamsAllowedBySupportedModels.
func assertMappingUpstreamsAllowed(mappings []ModelMapping, supportedModels []string) error {
	supported := map[string]bool{}
	for _, model := range supportedModels {
		trimmed := strings.TrimSpace(model)
		if trimmed != "" {
			supported[trimmed] = true
		}
	}
	if len(supported) == 0 || len(mappings) == 0 {
		return nil
	}
	invalid := []string{}
	for _, mapping := range mappings {
		if !supported[strings.TrimSpace(mapping.UpstreamModel)] {
			invalid = append(invalid, strings.TrimSpace(mapping.UpstreamModel))
		}
	}
	if len(invalid) > 0 {
		limit := minInt(5, len(invalid))
		return &ValidationError{Message: "映射上游模型只能选择账户支持模型：" + strings.Join(invalid[:limit], "、")}
	}
	return nil
}

func unorderedStringListEqual(left, right []string) bool {
	sortedLeft := append([]string{}, left...)
	sortedRight := append([]string{}, right...)
	sort.Strings(sortedLeft)
	sort.Strings(sortedRight)
	if len(sortedLeft) != len(sortedRight) {
		return false
	}
	for index := range sortedLeft {
		if sortedLeft[index] != sortedRight[index] {
			return false
		}
	}
	return true
}

func modelMappingsEqual(left, right []ModelMapping) bool {
	keys := func(mappings []ModelMapping) []string {
		out := make([]string, 0, len(mappings))
		for _, mapping := range mappings {
			enabled := "1"
			if mapping.Enabled != nil && !*mapping.Enabled {
				enabled = "0"
			}
			out = append(out, strings.Join([]string{
				mapping.SourceEndpointFamily, mapping.SourceModel,
				mapping.UpstreamEndpointFamily, mapping.UpstreamModel, enabled,
			}, "\x00"))
		}
		sort.Strings(out)
		return out
	}
	leftKeys, rightKeys := keys(left), keys(right)
	if len(leftKeys) != len(rightKeys) {
		return false
	}
	for index := range leftKeys {
		if leftKeys[index] != rightKeys[index] {
			return false
		}
	}
	return true
}

func intValue(value any, fallback int) int {
	if number, ok := value.(float64); ok && number == float64(int(number)) {
		return int(number)
	}
	return fallback
}

func boolValue(value any, fallback bool) bool {
	if enabled, ok := value.(bool); ok {
		return enabled
	}
	return fallback
}

// batchUpdateBody mirrors accountBatchEditSchema.strict(): strict target and
// update key sets with the discriminated {enabled, value} union per field.
func batchUpdateBody(body map[string]any) (BatchUpdateInput, string) {
	input := BatchUpdateInput{Updates: map[string]BatchUpdateField{}}
	for key := range body {
		switch key {
		case "targets", "updates":
		default:
			return BatchUpdateInput{}, batchFieldsPrompt
		}
	}
	rawTargets, ok := body["targets"].([]any)
	if !ok {
		return BatchUpdateInput{}, batchFieldsPrompt
	}
	if len(rawTargets) < 2 || len(rawTargets) > 100 {
		return BatchUpdateInput{}, "批量编辑账户不能重复"
	}
	seen := map[string]bool{}
	for _, item := range rawTargets {
		record, ok := item.(map[string]any)
		if !ok {
			return BatchUpdateInput{}, batchFieldsPrompt
		}
		for key := range record {
			switch key {
			case "accountId", "configRevision":
			default:
				return BatchUpdateInput{}, batchFieldsPrompt
			}
		}
		accountID := strings.TrimSpace(textString(record["accountId"]))
		if accountID == "" {
			return BatchUpdateInput{}, batchFieldsPrompt
		}
		revision, ok := record["configRevision"].(float64)
		if !ok || revision != float64(int64(revision)) || revision < 1 {
			return BatchUpdateInput{}, batchFieldsPrompt
		}
		if seen[accountID] {
			return BatchUpdateInput{}, "批量编辑账户不能重复"
		}
		seen[accountID] = true
		input.Targets = append(input.Targets, BatchUpdateTarget{AccountID: accountID, ConfigRevision: int64(revision)})
	}
	rawUpdates, ok := body["updates"].(map[string]any)
	if !ok {
		return BatchUpdateInput{}, batchFieldsPrompt
	}
	anyEnabled := false
	for field, value := range rawUpdates {
		if !batchUpdateFields[field] {
			return BatchUpdateInput{}, batchFieldsPrompt
		}
		record, ok := value.(map[string]any)
		if !ok {
			return BatchUpdateInput{}, batchFieldsPrompt
		}
		for key := range record {
			switch key {
			case "enabled", "value":
			default:
				return BatchUpdateInput{}, batchFieldsPrompt
			}
		}
		enabled, ok := record["enabled"].(bool)
		if !ok {
			return BatchUpdateInput{}, batchFieldsPrompt
		}
		field2 := BatchUpdateField{Enabled: enabled}
		if enabled {
			rawValue, exists := record["value"]
			if !exists {
				return BatchUpdateInput{}, batchFieldsPrompt
			}
			if message := validateBatchUpdateValue(field, rawValue); message != "" {
				return BatchUpdateInput{}, message
			}
			field2.Value = rawValue
			anyEnabled = true
		}
		input.Updates[field] = field2
	}
	if !anyEnabled {
		return BatchUpdateInput{}, "请至少选择一项需要覆盖的配置"
	}
	return input, ""
}

var batchUpdateFields = map[string]bool{
	"tags": true, "proxyProfileId": true, "concurrencyLimit": true, "priority": true,
	"superPriorityEnabled": true, "fallbackEnabled": true, "accountExpiresAt": true,
	"availabilitySchedule": true, "notes": true, "errorHandlingRules": true,
	"responseInspectionRules": true, "supportedModels": true, "healthCheckModel": true,
	"healthCheckEndpointMode": true, "modelMappings": true, "supportedEndpointModes": true,
	"serviceTierOverride": true, "reasoningEffortOverride": true,
}

var batchHealthCheckEndpointModes = map[string]bool{
	"images_json": true, "chat_json": true, "chat_sse": true,
	"responses_json": true, "responses_sse": true,
	"messages_json": true, "messages_sse": true,
	"generate_content_json": true, "generate_content_sse": true,
	"interactions_json": true, "interactions_sse": true,
}

// validateBatchUpdateValue mirrors the per-field value schema subset the store
// consumes (the credential-config fields keep their structural checks here and
// are rejected in the store with the companion-slice notice).
func validateBatchUpdateValue(field string, value any) string {
	switch field {
	case "tags":
		list, ok := value.([]any)
		if !ok || len(list) > 24 {
			return batchFieldsPrompt
		}
	case "proxyProfileId":
		if value == nil {
			return ""
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return batchFieldsPrompt
		}
	case "concurrencyLimit":
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) || number < 1 {
			return batchFieldsPrompt
		}
	case "priority":
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) || number < 0 {
			return batchFieldsPrompt
		}
	case "superPriorityEnabled", "fallbackEnabled":
		if _, ok := value.(bool); !ok {
			return batchFieldsPrompt
		}
	case "accountExpiresAt":
		if value == nil {
			return ""
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return batchFieldsPrompt
		}
	case "availabilitySchedule":
		if value == nil {
			return ""
		}
		if _, ok := value.(map[string]any); !ok {
			return batchFieldsPrompt
		}
	case "notes":
		if _, ok := value.(string); !ok {
			return batchFieldsPrompt
		}
	case "errorHandlingRules":
		list, ok := value.([]any)
		if !ok || len(list) > 100 {
			return batchFieldsPrompt
		}
	case "responseInspectionRules":
		list, ok := value.([]any)
		if !ok || len(list) > 20 {
			return batchFieldsPrompt
		}
	case "supportedModels":
		list, ok := value.([]any)
		if !ok || len(list) < 1 || len(list) > 500 {
			return batchFieldsPrompt
		}
		for _, item := range list {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return batchFieldsPrompt
			}
		}
	case "healthCheckModel":
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return batchFieldsPrompt
		}
	case "healthCheckEndpointMode":
		text, ok := value.(string)
		if !ok || !batchHealthCheckEndpointModes[text] {
			return batchFieldsPrompt
		}
	case "modelMappings":
		if _, ok := value.([]any); !ok {
			return batchFieldsPrompt
		}
	case "supportedEndpointModes":
		list, ok := value.([]any)
		if !ok || len(list) < 1 || len(list) > 13 {
			return batchFieldsPrompt
		}
		for _, item := range list {
			if _, ok := item.(string); !ok {
				return batchFieldsPrompt
			}
		}
	case "serviceTierOverride":
		if value == nil {
			return ""
		}
		text, ok := value.(string)
		if !ok {
			return batchFieldsPrompt
		}
		switch text {
		case "default", "priority", "flex", "":
		default:
			return batchFieldsPrompt
		}
	case "reasoningEffortOverride":
		if value == nil {
			return ""
		}
		text, ok := value.(string)
		if !ok {
			return batchFieldsPrompt
		}
		switch text {
		case "none", "minimal", "low", "medium", "high", "xhigh", "max", "":
		default:
			return batchFieldsPrompt
		}
	}
	return ""
}

// batchEditContextBody mirrors accountBatchEditContextSchema.strict().
func batchEditContextBody(body map[string]any) ([]string, []string, string) {
	for key := range body {
		switch key {
		case "accountIds", "fields":
		default:
			return nil, nil, batchContextPrompt
		}
	}
	rawIDs, ok := body["accountIds"].([]any)
	if !ok {
		return nil, nil, batchContextPrompt
	}
	if len(rawIDs) < 2 || len(rawIDs) > 100 {
		return nil, nil, "批量编辑账户不能重复"
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, item := range rawIDs {
		text := strings.TrimSpace(textString(item))
		if text == "" {
			return nil, nil, batchContextPrompt
		}
		if seen[text] {
			return nil, nil, "批量编辑账户不能重复"
		}
		seen[text] = true
		ids = append(ids, text)
	}
	fields := []string{}
	rawFields, hasFields := body["fields"]
	if !hasFields || rawFields == nil {
		return nil, nil, batchContextPrompt
	}
	{
		list, ok := rawFields.([]any)
		if !ok {
			return nil, nil, batchContextPrompt
		}
		if len(list) > 3 {
			return nil, nil, batchContextPrompt
		}
		fieldSeen := map[string]bool{}
		for _, item := range list {
			text, ok := item.(string)
			if !ok || !batchEditContextFields[text] {
				return nil, nil, batchContextPrompt
			}
			if fieldSeen[text] {
				return nil, nil, "批量编辑上下文字段不能重复"
			}
			fieldSeen[text] = true
			fields = append(fields, text)
		}
	}
	return ids, fields, ""
}

// marshalJSONValue is a small helper used by handlers that embed structured
// values into log entries.
func marshalJSONValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}
