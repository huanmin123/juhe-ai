package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) CreateManagementAccount(ctx context.Context, input port.ManagementAccountCreateInput) (port.ManagementAccountCreateResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementAccountCreateResult{}, fmt.Errorf("begin account create tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()
	var account map[string]any = map[string]any{}
	var id, owner, name, provider, profile, protocol, version, accountType, status, fingerprint, healthModel, endpoint string
	var concurrency, priority int
	var superPriority, fallback, schedulable bool
	var proxy, schedule, notes *string
	var expires, createdAt, updatedAt *time.Time
	err = tx.QueryRow(ctx, managementAccountCreateSQL, input.SystemAccountID, input.ProviderCode, input.ProviderProtocolProfileID, input.ID, input.Type, input.Name, input.Status, input.CredentialsEncrypted, input.CredentialFingerprint, input.ConcurrencyLimit, input.Priority, input.SuperPriorityEnabled, input.FallbackEnabled, input.Schedulable, input.AvailabilityScheduleJSON, input.HealthCheckModel, input.GroupID, input.HealthCheckEndpointMode, input.ProxyProfileID, input.AccountExpiresAt, input.TemporaryUnavailableContinuousProbe, input.Notes, input.CreatedAt, input.UpdatedAt).Scan(&id, &owner, &name, &provider, &profile, &protocol, &version, &accountType, &status, &fingerprint, &concurrency, &priority, &superPriority, &fallback, &schedulable, &healthModel, &endpoint, &proxy, &expires, &schedule, &notes, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountCreateResult{}, fmt.Errorf("provider profile or group invalid")
	}
	if err != nil {
		return port.ManagementAccountCreateResult{}, fmt.Errorf("insert management account: %w", err)
	}
	for _, model := range input.SupportedModels {
		if _, err := tx.Exec(ctx, managementAccountCreateSupportedModelSQL, id, provider, model, input.CreatedAt); err != nil {
			return port.ManagementAccountCreateResult{}, fmt.Errorf("insert account supported model: %w", err)
		}
	}
	if input.GroupID != "" {
		if _, err := tx.Exec(ctx, managementAccountCreateGroupBindingSQL, owner, input.GroupID, id, input.Priority, input.SuperPriorityEnabled, input.FallbackEnabled, input.CreatedAt); err != nil {
			return port.ManagementAccountCreateResult{}, fmt.Errorf("insert account group binding: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementAccountCreateResult{}, fmt.Errorf("commit account create tx: %w", err)
	}
	committed = true
	account["id"], account["systemAccountId"], account["name"], account["providerCode"], account["providerProtocolProfileId"] = id, owner, name, provider, profile
	account["protocolCode"], account["protocolVersion"], account["type"], account["status"] = protocol, version, accountType, status
	account["credentialFingerprint"], account["concurrencyLimit"], account["priority"] = fingerprint, concurrency, priority
	account["superPriorityEnabled"], account["fallbackEnabled"], account["schedulable"] = superPriority, fallback, schedulable
	account["healthCheckModel"], account["healthCheckEndpointMode"] = healthModel, endpoint
	if proxy != nil {
		account["proxyProfileId"] = *proxy
	}
	if schedule != nil {
		account["availabilitySchedule"] = *schedule
	}
	if notes != nil {
		account["notes"] = *notes
	}
	if expires != nil {
		account["accountExpiresAt"] = expires
	}
	if createdAt != nil {
		account["createdAt"] = createdAt
	}
	if updatedAt != nil {
		account["updatedAt"] = updatedAt
	}
	account["supportedModels"] = input.SupportedModels
	account["boundGroupId"] = input.GroupID
	return port.ManagementAccountCreateResult{Account: account}, nil
}

var _ port.ManagementAccountCreator = (*Store)(nil)
