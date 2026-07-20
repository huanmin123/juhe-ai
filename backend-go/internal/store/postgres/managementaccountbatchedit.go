package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) LoadManagementAccountBatchEditContext(ctx context.Context, systemAccountID string, ids []string) ([]port.ManagementAccountBatchEditAccount, bool, error) {
	rows, err := s.pool.Query(ctx, loadManagementAccountBatchEditContextSQL, ids, strings.TrimSpace(systemAccountID))
	if err != nil {
		return nil, false, fmt.Errorf("load account batch edit context: %w", err)
	}
	defer rows.Close()
	accounts := make([]port.ManagementAccountBatchEditAccount, 0, len(ids))
	for rows.Next() {
		var a port.ManagementAccountBatchEditAccount
		var schedule, notes *string
		if err := rows.Scan(&a.ID, &a.SystemAccountID, &a.Name, &a.ProviderCode, &a.ProtocolCode, &a.ProtocolVersion,
			&a.Type, &a.Status, &a.ConcurrencyLimit, &a.Priority, &a.SuperPriority, &a.FallbackEnabled,
			&a.Schedulable, &a.HealthCheckModel, &a.HealthCheckEndpoint, &a.AccountExpiresAt, &schedule, &notes, &a.ConfigRevision); err != nil {
			return nil, false, fmt.Errorf("scan account batch edit context: %w", err)
		}
		a.Notes = notes
		if schedule != nil && strings.TrimSpace(*schedule) != "" {
			_ = json.Unmarshal([]byte(*schedule), &a.Availability)
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return accounts, len(accounts) == len(ids), nil
}

func (s *Store) UpdateManagementAccountsBatch(ctx context.Context, input port.ManagementAccountBatchEditInput) (port.ManagementAccountBatchEditResult, bool, error) {
	fields := make([]string, 0, len(input.Updates))
	for field := range input.Updates {
		if batchEditableFields[field] {
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 {
		return port.ManagementAccountBatchEditResult{}, false, nil
	}
	sort.Strings(fields)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementAccountBatchEditResult{}, false, fmt.Errorf("begin account batch edit tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	ids := make([]string, 0, len(input.Targets))
	// Lock and validate each target before applying the shared update expression.
	for _, target := range input.Targets {
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM juhe_business.accounts WHERE id=$1 AND system_account_id=$2 AND deleted_at IS NULL AND config_revision=$3 FOR UPDATE`, target.AccountID, input.SystemAccountID, target.ConfigRevision).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return port.ManagementAccountBatchEditResult{}, false, nil
		}
		if err != nil {
			return port.ManagementAccountBatchEditResult{}, false, fmt.Errorf("lock account for batch edit: %w", err)
		}
		ids = append(ids, id)
	}
	setArgs := []any{input.Now}
	set := make([]string, 0, len(fields)+1)
	for _, field := range fields {
		set = append(set, field+" = $"+fmt.Sprint(len(setArgs)+1))
		setArgs = append(setArgs, input.Updates[field])
	}
	set = append(set, "updated_at = $1", "config_revision = config_revision + 1")
	setArgs = append(setArgs, ids)
	query := `UPDATE juhe_business.accounts SET ` + strings.Join(set, ", ") + ` WHERE id = ANY($` + fmt.Sprint(len(setArgs)) + `::text[]) RETURNING id, system_account_id, name, provider_code, protocol_code, protocol_version, type, status, concurrency_limit, priority, super_priority_enabled, fallback_enabled, schedulable, health_check_model, health_check_endpoint_mode, account_expires_at, availability_schedule_json, notes, config_revision`
	rows, err := tx.Query(ctx, query, setArgs...)
	if err != nil {
		return port.ManagementAccountBatchEditResult{}, false, fmt.Errorf("update accounts batch: %w", err)
	}
	accounts, err := scanBatchEditAccounts(rows)
	rows.Close()
	if err != nil {
		return port.ManagementAccountBatchEditResult{}, false, err
	}
	if len(accounts) != len(ids) {
		return port.ManagementAccountBatchEditResult{}, false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementAccountBatchEditResult{}, false, fmt.Errorf("commit account batch edit: %w", err)
	}
	committed = true
	return port.ManagementAccountBatchEditResult{BatchID: fmt.Sprintf("batch_%d", input.Now.UnixNano()), ChangedFields: fields, Accounts: accounts}, true, nil
}

var batchEditableFields = map[string]bool{"concurrency_limit": true, "priority": true, "super_priority_enabled": true, "fallback_enabled": true, "schedulable": true, "health_check_model": true, "health_check_endpoint_mode": true, "account_expires_at": true, "availability_schedule_json": true, "notes": true}

func scanBatchEditAccounts(rows pgx.Rows) ([]port.ManagementAccountBatchEditAccount, error) {
	result := []port.ManagementAccountBatchEditAccount{}
	for rows.Next() {
		var a port.ManagementAccountBatchEditAccount
		var schedule, notes *string
		if err := rows.Scan(&a.ID, &a.SystemAccountID, &a.Name, &a.ProviderCode, &a.ProtocolCode, &a.ProtocolVersion, &a.Type, &a.Status, &a.ConcurrencyLimit, &a.Priority, &a.SuperPriority, &a.FallbackEnabled, &a.Schedulable, &a.HealthCheckModel, &a.HealthCheckEndpoint, &a.AccountExpiresAt, &schedule, &notes, &a.ConfigRevision); err != nil {
			return nil, err
		}
		a.Notes = notes
		if schedule != nil {
			_ = json.Unmarshal([]byte(*schedule), &a.Availability)
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

var _ port.ManagementAccountBatchEditReader = (*Store)(nil)
var _ port.ManagementAccountBatchEditor = (*Store)(nil)
