// Authorized account instance provisioning (BUG-0175 / D-64 / D-74): the Go
// create mutation used to persist only the grant row, so the grantee never
// received a schedulable instance account and the whole "authorize an
// account to a user" chain broke at creation. This file ports the archived
// Node create tail (resource-authorization-write-state.repository.ts):
//   - ensureAccountAuthorizationInstance (:259-370): reuse the live instance,
//     restore the soft-deleted one (field reset + rename + dispatch revision
//     advance) or insert a fresh instance row
//   - bindActiveAccountAuthorizationToGranteeGroup (:183-224) with the four
//     verbatim target-group rejections (groupIdForAuthorizationBinding /
//     defaultGroupIdForAuthorizationBinding :397-423)
//   - uniqueAuthorizedAccountInstanceName (:371-387)
//   - syncAccountAuthorizationInstanceNamesForSourceAccount (:225-257)
//
// Boundary notes (deliberate SQL-level ownership):
//   - The accounts/groups write primitives (revision family advance, name
//     search terms) are unexported in internal/accounts and the composition
//     root is outside this task's write scope, so the well-known business
//     tables are written directly here; column sets follow
//     maintenance/internal/schema (pg_schema.go / sqlite_schema.go).
//   - The instance credentials column is a NOT NULL placeholder only: the
//     authorized read surfaces project NULL for instance rows and read the
//     source account credentials instead (accounts/m11_reads.go CASE WHEN).
//     The archived Node arm wrote encryptJson({}); this package has no
//     credential-secret channel, so the equivalent empty JSON document goes
//     in as-is.
//   - The dispatch-revision advance mirrors the archived restore arm
//     (advanceAccountCircuitDispatchRevisionInSqliteTransaction) with the
//     shared circuit projection key, so the gateway circuit consumer sees the
//     restored instance like any other revision bump.
package authz

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	circuitcontrolplane "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
)

// queryer is the transaction/database subset the provisioning helpers need.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// authorizedInstanceProvision carries the create-tail inputs for one direct
// user grant of an account resource. RuntimeAuthorizationID is the id of the
// resource_authorizations row the instance stamps (the archived Node arm
// receives the runtime row, and every reader joins on it — NOT the
// resource_authorization_grants id).
type authorizedInstanceProvision struct {
	RuntimeAuthorizationID string
	SourceAccountID        string
	OwnerID                string
	GranteeID              string
	TargetGroupID          *string
	GrantExpiresAt         *string
}

// provisionAuthorizedAccountInstance mirrors the bind gating of
// bindActiveAccountAuthorizationToGranteeGroup (:184-188): active,
// unexpired account grants only, then ensure the instance and bind it into
// the grantee group. It runs inside the create transaction so a validation
// rejection rolls the whole grant back.
func (s *Store) provisionAuthorizedAccountInstance(ctx context.Context, tx *sql.Tx, in authorizedInstanceProvision, nowTime time.Time, now string) error {
	if in.GrantExpiresAt != nil && *in.GrantExpiresAt != "" &&
		authorizationExpiresPassed(*in.GrantExpiresAt, nowTime) {
		return nil
	}
	// The instance row is stamped with the runtime authorization id (Node
	// upsertResourceAuthorizationForUser hands the bind the resource_
	// authorizations row); without the runtime row there is nothing to stamp.
	runtimeID, err := s.findRuntimeIDForUserGrant(ctx, tx, "account", in.SourceAccountID, in.GranteeID)
	if err != nil {
		return err
	}
	if runtimeID == "" {
		return nil
	}
	in.RuntimeAuthorizationID = runtimeID
	instance, err := s.ensureAccountAuthorizationInstance(ctx, tx, in, nowTime, now)
	if err != nil {
		return err
	}
	if instance == nil || instance.id == "" || instance.providerCode == "" {
		return nil
	}
	return s.bindInstanceToGranteeGroup(ctx, tx, instance, in, now)
}

// sourceAccountColumns is the projection the restore and insert arms share
// (archived `SELECT *` narrowed to the consumed fields).
const sourceAccountColumns = `provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		name, type, concurrency_limit, temporary_unavailable_continuous_probe_enabled,
		health_check_model, health_check_endpoint_mode`

// ensureAccountAuthorizationInstance mirrors the archived :259-370: the live
// instance is reused untouched, the soft-deleted instance is restored with
// the source-derived field reset plus a dispatch-revision advance, and a
// missing instance is inserted with empty credentials and inherited
// provider/protocol/health configuration. A nil row (source deleted or the
// degenerate self-grant) silently skips provisioning exactly like the
// archived `return undefined`.
func (s *Store) ensureAccountAuthorizationInstance(ctx context.Context, tx *sql.Tx, in authorizedInstanceProvision, nowTime time.Time, now string) (*instanceAccountRef, error) {
	var liveID, liveProvider string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id, provider_code FROM `+s.table("accounts")+`
		WHERE authorization_instance_authorization_id = ? AND deleted_at IS NULL LIMIT 1`), in.RuntimeAuthorizationID).
		Scan(&liveID, &liveProvider)
	if err == nil {
		return &instanceAccountRef{id: liveID, providerCode: liveProvider}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	var source struct {
		providerCode                    string
		providerProtocolProfileID       string
		protocolCode                    string
		protocolVersion                 string
		name                            string
		accountType                     string
		concurrencyLimit                int
		continuousProbeEnabled          int
		healthCheckModel                string
		healthCheckEndpointMode         string
	}
	err = tx.QueryRowContext(ctx, s.bind(`SELECT `+sourceAccountColumns+` FROM `+s.table("accounts")+`
		WHERE id = ? AND deleted_at IS NULL LIMIT 1`), in.SourceAccountID).
		Scan(&source.providerCode, &source.providerProtocolProfileID, &source.protocolCode,
			&source.protocolVersion, &source.name, &source.accountType, &source.concurrencyLimit,
			&source.continuousProbeEnabled, &source.healthCheckModel, &source.healthCheckEndpointMode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// The archived arm guards `source.system_account_id === grantee` only; the
	// empty provider check lives in the bind step (instance.provider_code), so
	// provisioning itself never skips on provider shape.
	if in.GranteeID == "" || in.GranteeID == in.OwnerID {
		return nil, nil
	}

	// Soft-deleted instance → restore arm (:280-353). The archived field set
	// is ported verbatim: provider/protocol/type/health columns follow the
	// source, runtime failure state clears, namespace columns re-stamp, and
	// the dispatch revision advances so downstream caches invalidate.
	var deletedID string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("accounts")+`
		WHERE authorization_instance_authorization_id = ? AND deleted_at IS NOT NULL
		ORDER BY deleted_at DESC, updated_at DESC, id ASC LIMIT 1`), in.RuntimeAuthorizationID).Scan(&deletedID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if deletedID != "" {
		restoredName, err := s.uniqueAuthorizedAccountInstanceName(ctx, tx, source.name, in.GranteeID, in.RuntimeAuthorizationID, deletedID)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
			SET provider_code = ?,
				provider_protocol_profile_id = ?,
				protocol_code = ?,
				protocol_version = ?,
				name = ?,
				type = ?,
				status = 'active',
				schedulable = 1,
				cooldown_until = NULL,
				last_error_code = NULL,
				last_error_message = NULL,
				cooldown_retest_failure_count = 0,
				cooldown_retest_observation_started_at = NULL,
				cooldown_retest_last_at = NULL,
				cooldown_retest_last_status_code = NULL,
				stream_failure_count = 0,
				stream_failure_window_started_at = NULL,
				temporary_unavailable_continuous_probe_enabled = ?,
				health_check_model = ?,
				health_check_endpoint_mode = ?,
				authorization_instance_source_account_id = ?,
				authorization_instance_owner_system_account_id = ?,
				deleted_at = NULL,
				deleted_by = NULL,
				updated_at = ?
			WHERE id = ?`),
			source.providerCode, source.providerProtocolProfileID, source.protocolCode,
			source.protocolVersion, restoredName, source.accountType,
			source.continuousProbeEnabled, source.healthCheckModel, source.healthCheckEndpointMode,
			in.SourceAccountID, in.OwnerID, now, deletedID); err != nil {
			return nil, err
		}
		if err := s.replaceInstanceNameSearchTerms(ctx, tx, deletedID, in.GranteeID, restoredName, now); err != nil {
			return nil, err
		}
		if err := s.advanceInstanceDispatchRevision(ctx, tx, deletedID, nowTime.UnixMilli()); err != nil {
			return nil, err
		}
		return &instanceAccountRef{id: deletedID, providerCode: source.providerCode}, nil
	}

	// Insert arm (:355-370): a fresh instance with empty credentials and the
	// source-inherited static configuration.
	id := "acc_" + randomSuffix()
	name, err := s.uniqueAuthorizedAccountInstanceName(ctx, tx, source.name, in.GranteeID, in.RuntimeAuthorizationID, "")
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("accounts")+`
		(id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		 name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
		 proxy_profile_id, concurrency_limit,
		 priority, super_priority_enabled, fallback_enabled, schedulable, notes, account_expires_at,
		 cooldown_until, last_error_code, last_error_message,
		 cooldown_retest_observation_started_at, stream_failure_count, stream_failure_window_started_at,
		 temporary_unavailable_continuous_probe_enabled, health_check_model, health_check_endpoint_mode,
		 authorization_instance_source_account_id, authorization_instance_authorization_id,
		 authorization_instance_owner_system_account_id,
		 created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, NULL, ?, NULL, ?, ?, ?, ?, 1, NULL, NULL, NULL, NULL, NULL, NULL, 0, NULL, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, in.GranteeID, source.providerCode, source.providerProtocolProfileID, source.protocolCode,
		source.protocolVersion, name, source.accountType, instanceEmptyCredentials, "",
		source.concurrencyLimit, 0, 0, 0,
		source.continuousProbeEnabled, source.healthCheckModel, source.healthCheckEndpointMode,
		in.SourceAccountID, in.RuntimeAuthorizationID, in.OwnerID, now, now); err != nil {
		return nil, err
	}
	if err := s.replaceInstanceNameSearchTerms(ctx, tx, id, in.GranteeID, name, now); err != nil {
		return nil, err
	}
	return &instanceAccountRef{id: id, providerCode: source.providerCode}, nil
}

// instanceEmptyCredentials is the NOT NULL placeholder document discussed in
// the package comment: an empty credentials JSON, never carrying secret
// material (the archived arm wrote the encrypted equivalent encryptJson({})).
const instanceEmptyCredentials = "{}"

// instanceAccountRef is the provisioning result the bind step consumes.
type instanceAccountRef struct {
	id           string
	providerCode string
}

// uniqueAuthorizedAccountInstanceName mirrors :371-387: the trimmed source
// name (fallback 授权账户), the first six characters of the grant id suffix,
// then the `-<shortId>` and `-<shortId>-<n>` candidate ladder capped at 1000
// before the timestamp fallback.
func (s *Store) uniqueAuthorizedAccountInstanceName(ctx context.Context, q queryer, sourceName, systemAccountID, authorizationID, exceptAccountID string) (string, error) {
	baseName := strings.TrimSpace(sourceName)
	if baseName == "" {
		baseName = "授权账户"
	}
	shortID := authorizationID
	if idx := strings.LastIndex(authorizationID, "_"); idx >= 0 {
		shortID = authorizationID[idx+1:]
	}
	if len(shortID) > 6 {
		shortID = shortID[:6]
	}
	if shortID == "" {
		shortID = authorizationID
		if len(shortID) > 6 {
			shortID = shortID[len(shortID)-6:]
		}
	}
	candidates := []string{baseName, baseName + "-" + shortID}
	for _, candidate := range candidates {
		available, err := s.accountNameAvailable(ctx, q, systemAccountID, candidate, exceptAccountID)
		if err != nil {
			return "", err
		}
		if available {
			return candidate, nil
		}
	}
	for index := 2; index <= 1000; index++ {
		candidate := baseName + "-" + shortID + "-" + strconv.Itoa(index)
		available, err := s.accountNameAvailable(ctx, q, systemAccountID, candidate, exceptAccountID)
		if err != nil {
			return "", err
		}
		if available {
			return candidate, nil
		}
	}
	return baseName + "-" + shortID + "-" + strconv.FormatInt(time.Now().UnixMilli(), 10), nil
}

// accountNameAvailable mirrors isAccountNameAvailable (:388-396) against the
// partial unique index idx_accounts_owner_name_unique
// (system_account_id, name) WHERE deleted_at IS NULL.
func (s *Store) accountNameAvailable(ctx context.Context, q queryer, systemAccountID, name, exceptAccountID string) (bool, error) {
	query := `SELECT id FROM ` + s.table("accounts") + `
		WHERE system_account_id = ? AND name = ? AND deleted_at IS NULL`
	args := []any{systemAccountID, name}
	if exceptAccountID != "" {
		query += ` AND id <> ?`
		args = append(args, exceptAccountID)
	}
	query += ` LIMIT 1`
	var id string
	err := q.QueryRowContext(ctx, s.bind(query), args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

// bindInstanceToGranteeGroup mirrors bindActiveAccountAuthorizationToGranteeGroup
// (:183-224): an existing enabled binding that already matches the request is
// kept, the target group passes the four verbatim validations, a stale
// binding is replaced, and the group_accounts row lands through the
// (group_id, account_id) upsert.
func (s *Store) bindInstanceToGranteeGroup(ctx context.Context, tx *sql.Tx, instance *instanceAccountRef, in authorizedInstanceProvision, now string) error {
	requestedGroupID := ""
	if in.TargetGroupID != nil {
		requestedGroupID = strings.TrimSpace(*in.TargetGroupID)
	}
	var existingGroupID sql.NullString
	err := tx.QueryRowContext(ctx, s.bind(`SELECT group_id FROM `+s.table("group_accounts")+`
		WHERE account_id = ?
			AND system_account_id = ?
			AND account_authorization_id = ?
			AND enabled = 1
		ORDER BY updated_at DESC, group_id ASC, account_id ASC
		LIMIT 1`), instance.id, in.GranteeID, in.RuntimeAuthorizationID).Scan(&existingGroupID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existingGroupID.Valid && existingGroupID.String != "" &&
		(requestedGroupID == "" || existingGroupID.String == requestedGroupID) {
		return nil
	}
	bindGroupID, err := s.groupIDForAuthorizationBinding(ctx, tx, instance.providerCode, in.GranteeID, requestedGroupID)
	if err != nil {
		return err
	}
	if bindGroupID == "" {
		return nil
	}
	if existingGroupID.Valid && existingGroupID.String != "" && existingGroupID.String != bindGroupID {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("group_accounts")+`
			WHERE account_id = ? AND system_account_id = ? AND account_authorization_id = ?`),
			instance.id, in.GranteeID, in.RuntimeAuthorizationID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("group_accounts")+`
		(system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(group_id, account_id) DO UPDATE SET
			system_account_id = excluded.system_account_id,
			account_authorization_id = excluded.account_authorization_id,
			enabled = 1,
			updated_at = excluded.updated_at`),
		in.GranteeID, bindGroupID, instance.id, in.RuntimeAuthorizationID, now, now); err != nil {
		return err
	}
	return nil
}

// groupIDForAuthorizationBinding mirrors groupIdForAuthorizationBinding +
// defaultGroupIdForAuthorizationBinding (:397-423) with the four verbatim
// rejections; the empty target falls back to the grantee's enabled default
// group for the instance provider.
func (s *Store) groupIDForAuthorizationBinding(ctx context.Context, q queryer, providerCode, systemAccountID, targetGroupID string) (string, error) {
	if targetGroupID != "" {
		var id, owner, groupProvider string
		var enabled int
		err := q.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, provider_code, enabled
			FROM `+s.table("groups")+` WHERE id = ? LIMIT 1`), targetGroupID).
			Scan(&id, &owner, &groupProvider, &enabled)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != systemAccountID) {
			return "", failf("目标分组不存在或不属于被授权用户")
		}
		if err != nil {
			return "", err
		}
		if groupProvider != providerCode {
			return "", failf("目标分组供应商与授权账户不一致")
		}
		if enabled != 1 {
			return "", failf("目标分组已停用，请选择启用分组")
		}
		return id, nil
	}
	var id string
	err := q.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("groups")+`
		WHERE system_account_id = ? AND provider_code = ? AND is_default = 1 AND enabled = 1
		ORDER BY updated_at DESC, id ASC LIMIT 1`), systemAccountID, providerCode).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", failf("目标用户缺少启用的默认分组，请按当前数据契约修复目标用户分组后再授权")
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// advanceInstanceDispatchRevision mirrors the archived restore-arm
// advanceAccountCircuitDispatchRevisionInSqliteTransaction: a fenced
// dispatch_revision bump plus one pending dispatch_revision_changed outbox
// row under the shared circuit projection key. It runs inside the caller's
// transaction.
func (s *Store) advanceInstanceDispatchRevision(ctx context.Context, tx *sql.Tx, accountID string, nowMS int64) error {
	transitionID := "dispatch_" + randomSuffix()
	dedupeKey := "dispatch:" + transitionID
	var revision int64
	lockSuffix := ""
	if s.pg {
		lockSuffix = " FOR UPDATE"
	}
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT dispatch_revision FROM `+s.table("accounts")+`
		WHERE id = ?`+lockSuffix), accountID).Scan(&revision); err != nil {
		return err
	}
	exec, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET dispatch_revision = dispatch_revision + 1
		WHERE id = ? AND dispatch_revision = ?`), accountID, revision)
	if err != nil {
		return err
	}
	if affected, _ := exec.RowsAffected(); affected != 1 {
		return errors.New("账户 dispatch revision 推进冲突：" + accountID)
	}
	revision++
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_circuit_outbox")+`
		(event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
		 circuit_scope_key, incident_id, transition_id, dispatch_revision, generation,
		 ledger_revision, status, available_at_ms, attempt_count, created_at_ms, updated_at_ms)
		VALUES (?, ?, ?, 'dispatch_revision_changed', ?, ?, NULL, NULL, ?, ?, NULL, NULL, 'pending', ?, 0, ?, ?)`),
		"circ_"+randomSuffix(), circuitcontrolplane.ProjectionKey, dedupeKey, accountID, accountID,
		transitionID, revision, nowMS, nowMS, nowMS)
	return err
}

// replaceInstanceNameSearchTerms mirrors replaceAccountNameSearchTerms for
// the provisioned instance rows: the NFKC-normalized document plus the 1..3
// gram term set backing the account list search.
func (s *Store) replaceInstanceNameSearchTerms(ctx context.Context, q queryer, accountID, systemAccountID, name, now string) error {
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_name_search_terms")+` WHERE account_id = ?`), accountID); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_name_search_documents")+` WHERE account_id = ?`), accountID); err != nil {
		return err
	}
	normalizedName := strings.TrimSpace(norm.NFKC.String(name))
	if normalizedName == "" {
		return nil
	}
	if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_name_search_documents")+`
		(account_id, system_account_id, normalized_name, updated_at) VALUES (?, ?, ?, ?)`),
		accountID, systemAccountID, normalizedName, now); err != nil {
		return err
	}
	seen := map[string]bool{}
	runes := []rune(normalizedName)
	for length := 1; length <= 3; length++ {
		for index := 0; index+length <= len(runes); index++ {
			term := string(runes[index : index+length])
			if strings.TrimSpace(term) == "" || seen[term] {
				continue
			}
			seen[term] = true
			statement := `INSERT INTO ` + s.table("account_name_search_terms") + `
				(account_id, system_account_id, term, created_at) VALUES (?, ?, ?, ?)`
			conflict := ` ON CONFLICT (account_id, term) DO NOTHING`
			if !s.pg {
				statement = strings.Replace(statement, "INSERT INTO", "INSERT OR IGNORE INTO", 1)
				conflict = ""
			}
			if _, err := q.ExecContext(ctx, s.bind(statement+conflict), accountID, systemAccountID, term, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// SyncAccountAuthorizationInstanceNamesForSourceAccount mirrors
// syncAccountAuthorizationInstanceNamesForSourceAccount (:225-257): every
// live instance of the renamed source account follows the new source name
// through the same uniqueness ladder, and the changed ids are returned so
// callers can drive per-instance cache flushes. The archived export has no
// call sites in the Node backend (dead surface kept for verbatim parity);
// the live rename cascade is the patch-repository copy
// syncAuthorizationInstanceNamesInClient (account-management-patch.repository.ts
// :1670-1708), ported in-transaction into the accounts package rename arm
// (internal/accounts patch.go syncAuthorizationInstanceNames).
func (s *Store) SyncAccountAuthorizationInstanceNamesForSourceAccount(ctx context.Context, sourceAccountID, sourceName string) ([]string, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	changed, err := s.syncAccountAuthorizationInstanceNamesForSourceAccountTx(ctx, tx, sourceAccountID, sourceName, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	return changed, tx.Commit()
}

// syncAccountAuthorizationInstanceNamesForSourceAccountTx is the in-transaction
// form of the archived :225-257.
func (s *Store) syncAccountAuthorizationInstanceNamesForSourceAccountTx(ctx context.Context, tx *sql.Tx, sourceAccountID, sourceName, now string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id, system_account_id, authorization_instance_authorization_id, name
		FROM `+s.table("accounts")+`
		WHERE authorization_instance_source_account_id = ?
			AND deleted_at IS NULL
		ORDER BY system_account_id ASC, id ASC`), sourceAccountID)
	if err != nil {
		return nil, err
	}
	type instanceRow struct {
		id, systemAccountID, authorizationID, name string
	}
	instances := []instanceRow{}
	for rows.Next() {
		var row instanceRow
		var authorizationID, name sql.NullString
		if err := rows.Scan(&row.id, &row.systemAccountID, &authorizationID, &name); err != nil {
			rows.Close()
			return nil, err
		}
		row.authorizationID = authorizationID.String
		row.name = name.String
		instances = append(instances, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	changed := []string{}
	for _, row := range instances {
		if row.id == "" || row.systemAccountID == "" || row.authorizationID == "" {
			continue
		}
		nextName, err := s.uniqueAuthorizedAccountInstanceName(ctx, tx, sourceName, row.systemAccountID, row.authorizationID, row.id)
		if err != nil {
			return nil, err
		}
		if row.name == nextName {
			continue
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
			SET name = ?, updated_at = ? WHERE id = ?`), nextName, now, row.id); err != nil {
			return nil, err
		}
		if err := s.replaceInstanceNameSearchTerms(ctx, tx, row.id, row.systemAccountID, nextName, now); err != nil {
			return nil, err
		}
		changed = append(changed, row.id)
	}
	return changed, nil
}
