// Downstream effects that trail every grant runtime sync in Node:
//   - syncResourceAuthorizationRequestQuotaHourlyWindowScopeBindingsAsync
//     (request-quota-hourly-windows.repository.ts:122-144; sqlite variant
//     :108-120, identical bindings semantics minus the stats dirty mark)
//   - enqueueAccountHealthJobsInputsForAuthorizationSourceInTransactionAsync
//     (account-health-jobs-input-authorization-fanout.repository.ts:51-76)
//     with reserveAndEnqueueAccountHealthJobsInputInTransactionAsync
//     (account-health-jobs-input-outbox.repository.ts:90-121) and
//     reserveAccountHealthJobsInputVersionInTransactionAsync
//
// Node call sites mirrored here:
//   - syncResourceAuthorizationGrantRuntimeAsync tail
//     (resource-authorization-write.repository.ts:1001-1002) → syncGrantRuntime
//     (patch :809, expire sweep :957, revoke :992)
//   - returnResourceAuthorizationGrantAsync tail
//     (resource-authorization-return.repository.ts:587-596) → Return /
//     ReturnGroupForGrantee
//   - create mutations (:219/:233/:402/:439) → Create (bindings only; Node
//     create never enqueues health inputs)
package authz

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
)

// AuthorizationGrantHealthFanoutReason mirrors the Node reason constant
// ('authorization_grant_changed') passed by every grant-terminating call site.
const AuthorizationGrantHealthFanoutReason = "authorization_grant_changed"

// authorizationFanoutProviderCodes mirrors the Node whitelist
// (account-health-jobs-input-authorization-fanout.repository.ts:36/:62).
var authorizationFanoutProviderCodes = []string{
	"gpt", "openai", "xai", "anthropic", "deepseek", "glm", "gemini", "hybrid",
}

// authorizationFanoutAccountTypes mirrors the Node type whitelist
// (account-health-jobs-input-authorization-fanout.repository.ts:37/:63).
var authorizationFanoutAccountTypes = []string{"api_key", "oauth", "google_oauth"}

func sqlPlaceholders(count int) string {
	return strings.TrimRight(strings.Repeat("?, ", count), ", ")
}

func sqlInOutList(prefix string, count int) string {
	return prefix + " (" + sqlPlaceholders(count) + ")"
}

// statsTable qualifies a juhe_stats table (PostgreSQL schema-qualified, bare
// on SQLite), matching the gateway accounts/apikeys stats table helpers.
func (s *Store) statsTable(name string) string {
	if s.pg {
		return "juhe_stats." + name
	}
	return name
}

// syncGrantQuotaScopeBindings mirrors
// syncResourceAuthorizationRequestQuotaHourlyWindowScopeBindingsAsync:
// replace every scope binding sourced by this grant, reloaded from the
// post-sync runtime rows inside the same transaction. Must run after the
// runtime projection so revoked/paused runtimes drop their bindings.
func (s *Store) syncGrantQuotaScopeBindings(ctx context.Context, tx *sql.Tx, grant *grantRow, now string) error {
	// Previous direct-authorization scope ids (:128-135 sqlite :169-178): they
	// stay reload candidates so a runtime that just left this grant still gets
	// re-evaluated (and its stale binding removed) in the rebuild below.
	previousRows, err := tx.QueryContext(ctx, s.bind(`SELECT scope_id, scope_type
		FROM `+s.table("request_quota_hourly_window_scope_bindings")+`
		WHERE source_type = 'resource_authorization_grant' AND source_id = ?`), grant.ID)
	if err != nil {
		return err
	}
	var previousAuthorizationIDs []string
	for previousRows.Next() {
		var scopeID, scopeType string
		if err := previousRows.Scan(&scopeID, &scopeType); err != nil {
			previousRows.Close()
			return err
		}
		if scopeType == "account_authorization" || scopeType == "group_authorization" {
			previousAuthorizationIDs = append(previousAuthorizationIDs, scopeID)
		}
	}
	previousRows.Close()
	if err := previousRows.Err(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("request_quota_hourly_window_scope_bindings")+`
		WHERE source_type = 'resource_authorization_grant' AND source_id = ?`), grant.ID); err != nil {
		return err
	}
	bindings, err := s.loadAuthorizationScopeBindings(ctx, tx, grant, previousAuthorizationIDs)
	if err != nil {
		return err
	}
	if err := s.insertScopeBindings(ctx, tx, bindings, now); err != nil {
		return err
	}
	if s.pg {
		// markPostgresRequestQuotaHourlyWindowDirtyScopes (:384-405): the stats
		// hourly window rebuild only exists on PostgreSQL; the sqlite variant
		// (:108-120) never marks dirty scopes.
		return s.markQuotaHourlyWindowDirtyScopes(ctx, tx, bindings, now)
	}
	return nil
}

// loadAuthorizationScopeBindings mirrors
// loadAffectedAuthorizationBindingRowsAsync (:238-292). The IN-list spelling
// of the previousAuthorizationIds clause matches the sqlite variant
// (:185-187); PostgreSQL's `= ANY(?::text[])` is the same membership test.
func (s *Store) loadAuthorizationScopeBindings(ctx context.Context, tx *sql.Tx, grant *grantRow, previousAuthorizationIDs []string) ([]scopeBinding, error) {
	query := `SELECT ra.id, ra.resource_type, ra.resource_id, ra.resource_owner_system_account_id,
		ra.grantee_system_account_id, ra.status, ra.limits_json, ra.effective_source_type,
		ra.effective_source_team_id, effective_grant.id AS effective_grant_id,
		instance_accounts.id AS authorization_instance_account_id
	FROM ` + s.table("resource_authorizations") + ` ra
	LEFT JOIN ` + s.table("resource_authorization_grants") + ` effective_grant
		ON effective_grant.resource_type = ra.resource_type
		AND effective_grant.resource_id = ra.resource_id
		AND effective_grant.status = 'active'
		AND (
			(ra.effective_source_type = 'manual'
				AND effective_grant.grantee_type = 'system_account'
				AND effective_grant.grantee_system_account_id = ra.grantee_system_account_id)
			OR
			(ra.effective_source_type = 'team'
				AND effective_grant.grantee_type = 'team'
				AND effective_grant.grantee_team_id = ra.effective_source_team_id)
		)
	LEFT JOIN ` + s.table("accounts") + ` instance_accounts
		ON ra.resource_type = 'account'
		AND instance_accounts.authorization_instance_authorization_id = ra.id
		AND instance_accounts.system_account_id = ra.grantee_system_account_id
		AND instance_accounts.authorization_instance_source_account_id = ra.resource_id
		AND instance_accounts.deleted_at IS NULL
	WHERE ra.resource_type = ?
		AND ra.resource_id = ?
		AND (
			(? = 'system_account' AND ra.grantee_system_account_id = ?)
			OR
			(? = 'team' AND EXISTS (
				SELECT 1 FROM ` + s.table("resource_authorization_sources") + ` ras
				WHERE ras.authorization_id = ra.id
					AND ras.source_type = 'team'
					AND ras.source_team_id = ?
			))`
	args := []any{grant.ResourceType, grant.ResourceID,
		grant.GranteeType, grant.GranteeUserID.String,
		grant.GranteeType, grant.GranteeTeamID.String}
	if len(previousAuthorizationIDs) > 0 {
		query += "\n\t\t\tOR ra.id IN (" + sqlPlaceholders(len(previousAuthorizationIDs)) + ")"
		for _, id := range previousAuthorizationIDs {
			args = append(args, id)
		}
	}
	query += "\n\t\t)\n\tORDER BY ra.id ASC"
	rows, err := tx.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type authorizationBindingRow struct {
		id                             string
		resourceType                   string
		resourceID                     string
		resourceOwnerSystemAccountID   string
		granteeSystemAccountID         string
		status                         string
		limitsJSON                     sql.NullString
		effectiveSourceType            sql.NullString
		effectiveSourceTeamID          sql.NullString
		effectiveGrantID               sql.NullString
		authorizationInstanceAccountID sql.NullString
	}
	var affected []authorizationBindingRow
	for rows.Next() {
		var row authorizationBindingRow
		if err := rows.Scan(&row.id, &row.resourceType, &row.resourceID, &row.resourceOwnerSystemAccountID,
			&row.granteeSystemAccountID, &row.status, &row.limitsJSON, &row.effectiveSourceType,
			&row.effectiveSourceTeamID, &row.effectiveGrantID,
			&row.authorizationInstanceAccountID); err != nil {
			return nil, err
		}
		affected = append(affected, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// buildAuthorizationScopeBindings (:294-333): only active runtimes still
	// carried by an active effective grant contribute bindings.
	bindings := []scopeBinding{}
	for _, row := range affected {
		if row.status != StatusActive || !row.effectiveGrantID.Valid || row.effectiveGrantID.String == "" {
			continue
		}
		windowHours, err := activeAuthorizationQuotaHourlyWindowHours(row.limitsJSON.String)
		if err != nil {
			return nil, err
		}
		if windowHours == 0 {
			continue
		}
		systemAccountID := row.resourceOwnerSystemAccountID
		scopeType := "group_authorization"
		if row.resourceType == "account" {
			systemAccountID = row.granteeSystemAccountID
			scopeType = "account_authorization"
		}
		bindings = append(bindings, scopeBinding{
			systemAccountID: systemAccountID,
			scopeType:       scopeType,
			scopeID:         row.id,
			sourceType:      "resource_authorization_grant",
			sourceID:        row.effectiveGrantID.String,
			windowHours:     windowHours,
		})
		if row.effectiveSourceType.String != "team" || row.effectiveSourceTeamID.String == "" {
			continue
		}
		if row.resourceType == "account" {
			if !row.authorizationInstanceAccountID.Valid || row.authorizationInstanceAccountID.String == "" {
				continue
			}
			bindings = append(bindings, scopeBinding{
				systemAccountID: row.granteeSystemAccountID,
				scopeType:       "account_authorization_team",
				scopeID:         row.authorizationInstanceAccountID.String + ":" + row.effectiveSourceTeamID.String,
				sourceType:      "resource_authorization_grant",
				sourceID:        row.effectiveGrantID.String,
				windowHours:     windowHours,
			})
		} else {
			bindings = append(bindings, scopeBinding{
				systemAccountID: row.resourceOwnerSystemAccountID,
				scopeType:       "group_authorization_team",
				scopeID:         row.resourceID + ":" + row.effectiveSourceTeamID.String,
				sourceType:      "resource_authorization_grant",
				sourceID:        row.effectiveGrantID.String,
				windowHours:     windowHours,
			})
		}
	}
	return uniqueScopeBindings(bindings), nil
}

// scopeBinding mirrors StoredScopeBinding (request-quota-hourly-windows
// .repository.ts:34-37).
type scopeBinding struct {
	systemAccountID string
	scopeType       string
	scopeID         string
	sourceType      string
	sourceID        string
	windowHours     int
}

// insertScopeBindings mirrors insertPostgresScopeBindings (:360-382) / the
// per-row sqlite variant (:335-358): same upsert columns and conflict target.
func (s *Store) insertScopeBindings(ctx context.Context, tx *sql.Tx, bindings []scopeBinding, now string) error {
	if len(bindings) == 0 {
		return nil
	}
	values := strings.TrimRight(strings.Repeat("(?, ?, ?, ?, ?, ?, ?, ?), ", len(bindings)), ", ")
	query := `INSERT INTO ` + s.table("request_quota_hourly_window_scope_bindings") + ` (
		system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
	) VALUES ` + values + `
	ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
		source_type = EXCLUDED.source_type,
		source_id = EXCLUDED.source_id,
		window_hours = EXCLUDED.window_hours,
		updated_at = EXCLUDED.updated_at`
	args := make([]any, 0, len(bindings)*8)
	for _, binding := range bindings {
		args = append(args, binding.systemAccountID, binding.scopeType, binding.scopeID,
			binding.sourceType, binding.sourceID, binding.windowHours, now, now)
	}
	_, err := tx.ExecContext(ctx, s.bind(query), args...)
	return err
}

// markQuotaHourlyWindowDirtyScopes mirrors
// markPostgresRequestQuotaHourlyWindowDirtyScopes (:384-405).
func (s *Store) markQuotaHourlyWindowDirtyScopes(ctx context.Context, tx *sql.Tx, bindings []scopeBinding, now string) error {
	if len(bindings) == 0 {
		return nil
	}
	values := strings.TrimRight(strings.Repeat("(?, ?, ?, 1, ?, ?), ", len(bindings)), ", ")
	query := `INSERT INTO ` + s.statsTable("usage_quota_hourly_window_dirty_scopes") + ` (
		system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
	) VALUES ` + values + `
	ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
		generation = ` + s.statsTable("usage_quota_hourly_window_dirty_scopes") + `.generation + 1,
		updated_at = EXCLUDED.updated_at`
	args := make([]any, 0, len(bindings)*5)
	for _, binding := range bindings {
		args = append(args, binding.systemAccountID, binding.scopeType, binding.scopeID, now, now)
	}
	_, err := tx.ExecContext(ctx, s.bind(query), args...)
	return err
}

func uniqueScopeBindings(bindings []scopeBinding) []scopeBinding {
	seen := make(map[string]struct{}, len(bindings))
	unique := make([]scopeBinding, 0, len(bindings))
	for _, binding := range bindings {
		key := binding.systemAccountID + "\x00" + binding.scopeType + "\x00" + binding.scopeID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, binding)
	}
	return unique
}

// activeAuthorizationQuotaHourlyWindowHours mirrors
// activeRequestQuotaHourlyWindowHours (:415-420) + isValidRequestQuotaHourly
// WindowHours (:422-424): enabled hourly limits with an integer 1..720 window
// yield the hours; anything else yields 0 (no binding). Parse failures
// propagate like the Node throw.
func activeAuthorizationQuotaHourlyWindowHours(limitsJSON string) (int, error) {
	limits, err := gatewayquota.ParseRequestQuotaLimitsJSON(limitsJSON)
	if err != nil {
		return 0, err
	}
	if limits.Hourly == nil || !limits.Hourly.Enabled {
		return 0, nil
	}
	if limits.Hourly.Hours < 1 || limits.Hourly.Hours > gatewayquota.MaxRequestQuotaHourlyWindowHours {
		return 0, nil
	}
	return limits.Hourly.Hours, nil
}

// enqueueGrantAccountHealthInputs mirrors
// enqueueAccountHealthJobsInputsForAuthorizationSourceInTransactionAsync:
// non-account resources return immediately; otherwise every whitelisted
// authorization-instance account reserves its next input epoch and writes a
// pending snapshot outbox row inside the caller's transaction (jobs side
// counterpart: internal/oauthrefresh/healthfanout.go). now is the caller's
// transaction timestamp, exactly like the Node nowIso() flowing through the
// whole grant mutation. Must run inside the grant mutation transaction so the
// epoch reservation can never commit without its durable publish intent.
func (s *Store) enqueueGrantAccountHealthInputs(ctx context.Context, tx *sql.Tx, grant *grantRow, now string) (int, error) {
	if strings.TrimSpace(grant.ResourceType) != "account" {
		return 0, nil
	}
	lockSuffix := ""
	if s.pg {
		lockSuffix = "\n\tFOR UPDATE"
	}
	query := fmt.Sprintf(`SELECT id, config_revision, dispatch_revision
		FROM %s
		WHERE authorization_instance_source_account_id = ?
			AND deleted_at IS NULL
			AND provider_code IN (%s)
			AND type IN (%s)
		ORDER BY id ASC%s`, s.table("accounts"),
		sqlPlaceholders(len(authorizationFanoutProviderCodes)),
		sqlPlaceholders(len(authorizationFanoutAccountTypes)), lockSuffix)
	args := []any{strings.TrimSpace(grant.ResourceID)}
	for _, code := range authorizationFanoutProviderCodes {
		args = append(args, code)
	}
	for _, accountType := range authorizationFanoutAccountTypes {
		args = append(args, accountType)
	}
	rows, err := tx.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return 0, err
	}
	type instanceAccount struct {
		id               string
		configRevision   sql.NullInt64
		dispatchRevision sql.NullInt64
	}
	instances := []instanceAccount{}
	for rows.Next() {
		var instance instanceAccount
		if err := rows.Scan(&instance.id, &instance.configRevision, &instance.dispatchRevision); err != nil {
			rows.Close()
			return 0, err
		}
		instances = append(instances, instance)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	enqueued := 0
	for _, instance := range instances {
		version, err := s.reserveAccountHealthInputVersion(ctx, tx, instance.id, now)
		if err != nil {
			return enqueued, err
		}
		// event_id: Node uses randomUUID; the jobs-side same-source port and T1
		// already render it as random 16-byte hex (registered non-blocking
		// difference), reused here via randomSuffix.
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_health_jobs_input_outbox")+`
			(event_id, account_id, input_version, event_kind, reason,
			 config_revision, dispatch_revision, status, available_at,
			 created_at, updated_at)
			VALUES (?, ?, ?, 'snapshot', ?, ?, ?, 'pending', ?, ?, ?)`),
			randomSuffix(), instance.id, version, AuthorizationGrantHealthFanoutReason,
			instance.configRevision.Int64, instance.dispatchRevision.Int64, now, now, now); err != nil {
			return enqueued, err
		}
		enqueued++
	}
	return enqueued, nil
}

// reserveAccountHealthInputVersion mirrors
// reserveAccountHealthJobsInputVersionInTransactionAsync via the jobs-side
// same-source port (internal/oauthrefresh/healthfanout.go): row-locked read of
// the account's current epoch, then update-or-insert at current+1.
func (s *Store) reserveAccountHealthInputVersion(ctx context.Context, tx *sql.Tx, accountID, now string) (int64, error) {
	normalized := strings.TrimSpace(accountID)
	if normalized == "" {
		return 0, failf("缺少健康任务输入的账户 ID")
	}
	lockSuffix := ""
	if s.pg {
		lockSuffix = " FOR UPDATE"
	}
	var current sql.NullInt64
	err := tx.QueryRowContext(ctx, s.bind("SELECT current_version FROM "+s.table("account_health_jobs_input_versions")+" WHERE account_id = ?"+lockSuffix), normalized).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	next := int64(1)
	if err == nil && current.Valid {
		next = current.Int64 + 1
		if _, err := tx.ExecContext(ctx, s.bind("UPDATE "+s.table("account_health_jobs_input_versions")+" SET current_version = ?, reserved_at = ? WHERE account_id = ?"), next, now, normalized); err != nil {
			return 0, err
		}
		return next, nil
	}
	if _, err := tx.ExecContext(ctx, s.bind("INSERT INTO "+s.table("account_health_jobs_input_versions")+" (account_id, current_version, reserved_at) VALUES (?, ?, ?)"), normalized, next, now); err != nil {
		return 0, err
	}
	return next, nil
}
