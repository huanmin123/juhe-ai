package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
)

// Kept as a small SQL manifest so the first-pass storage test can assert the
// transaction and owner-scope boundaries without requiring a live database.
const managementAccountImportSQL = `BEGIN; INSERT INTO juhe_business.accounts (system_account_id); INSERT INTO juhe_business.group_accounts; INSERT INTO juhe_business.proxy_profiles;`

func (s *Store) Import(ctx context.Context, input port.ManagementAccountImportInput) (port.ManagementAccountImportResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementAccountImportResult{}, fmt.Errorf("begin account import tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	proxyIDs := make(map[string]string, len(input.Proxies))
	for _, proxy := range input.Proxies {
		proxyIDs[proxy.Ref] = proxy.ID
		if !input.Options.CreateMissingProxies {
			continue
		}
		_, err = tx.Exec(ctx, `INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, description, type, host, port, username, password_encrypted, enabled, test_status, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),$10,'unknown',$11,$11)
ON CONFLICT (id) DO NOTHING`, proxy.ID, input.SystemAccountID, proxy.Name, optionalImportText(proxy.Description), proxy.Type, proxy.Host, proxy.Port, proxy.Username, proxy.PasswordEncrypted, proxy.Enabled, input.Now)
		if err != nil {
			return port.ManagementAccountImportResult{}, fmt.Errorf("create imported proxy: %w", err)
		}
	}

	groupIDs := map[string]string{}
	for _, account := range input.Accounts {
		if strings.TrimSpace(account.GroupID) != "" {
			groupIDs[account.GroupID] = account.GroupID
			continue
		}
		if strings.TrimSpace(account.GroupName) == "" {
			continue
		}
		if !input.Options.CreateMissingGroups {
			return port.ManagementAccountImportResult{}, fmt.Errorf("账户 %q 的分组不存在", account.Name)
		}
		key := account.ProviderCode + "\x00" + strings.ToLower(strings.TrimSpace(account.GroupName))
		if existing, ok := groupIDs[key]; ok {
			groupIDs[account.GroupName] = existing
			continue
		}
		groupID := "group_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		err = tx.QueryRow(ctx, `INSERT INTO juhe_business.groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at) VALUES ($1,$2,$3,$4,true,false,'personal',$5,$5) ON CONFLICT (system_account_id, provider_code, lower(name)) DO UPDATE SET updated_at=EXCLUDED.updated_at RETURNING id`, groupID, input.SystemAccountID, strings.TrimSpace(account.GroupName), account.ProviderCode, input.Now).Scan(&groupID)
		if err != nil {
			return port.ManagementAccountImportResult{}, fmt.Errorf("create imported group: %w", err)
		}
		groupIDs[key] = groupID
		groupIDs[account.GroupName] = groupID
	}

	imported := 0
	skipped := 0
	for _, account := range input.Accounts {
		var existingID string
		err = tx.QueryRow(ctx, `SELECT id FROM juhe_business.accounts WHERE system_account_id=$1 AND credential_fingerprint=$2 AND deleted_at IS NULL LIMIT 1`, input.SystemAccountID, account.CredentialFingerprint).Scan(&existingID)
		if err == nil {
			if input.Options.SkipDuplicates {
				skipped++
				continue
			}
			return port.ManagementAccountImportResult{}, fmt.Errorf("账户 %q 已存在", account.Name)
		}
		if err != pgx.ErrNoRows {
			return port.ManagementAccountImportResult{}, fmt.Errorf("check imported account duplicate: %w", err)
		}

		var protocolCode, protocolVersion string
		err = tx.QueryRow(ctx, `SELECT protocol_code, protocol_version FROM juhe_business.provider_protocol_profiles WHERE id=$1 AND provider_code=$2 AND enabled=true`, account.ProviderProtocolProfileID, account.ProviderCode).Scan(&protocolCode, &protocolVersion)
		if err != nil {
			return port.ManagementAccountImportResult{}, fmt.Errorf("账户 %q 的供应商协议档案不可用: %w", account.Name, err)
		}
		proxyID := strings.TrimSpace(account.ProxyProfileID)
		if proxyID == "" && account.ProxyRef != "" {
			proxyID = proxyIDs[account.ProxyRef]
		}
		_, err = tx.Exec(ctx, `INSERT INTO juhe_business.accounts (id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_fingerprint, concurrency_limit, priority, super_priority_enabled, fallback_enabled, schedulable, availability_schedule_json, account_expires_at, health_check_model, health_check_endpoint_mode, proxy_profile_id, created_at, updated_at, notes)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,true,$16,$17,$18,$19,NULLIF($20,''),$21,$21,$22)`, account.ID, input.SystemAccountID, account.ProviderCode, account.ProviderProtocolProfileID, protocolCode, protocolVersion, account.Name, account.Type, account.Status, account.CredentialsEncrypted, account.CredentialFingerprint, account.ConcurrencyLimit, account.Priority, account.SuperPriorityEnabled, account.FallbackEnabled, account.AvailabilityScheduleJSON, account.AccountExpiresAt, importHealthModel(account.HealthCheckModel), account.HealthCheckEndpointMode, proxyID, input.Now, account.Notes)
		if err != nil {
			return port.ManagementAccountImportResult{}, fmt.Errorf("insert imported account: %w", err)
		}
		for _, model := range account.SupportedModels {
			if _, err = tx.Exec(ctx, `INSERT INTO juhe_business.account_supported_models (account_id, provider_code, model, created_at) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, account.ID, account.ProviderCode, model, input.Now); err != nil {
				return port.ManagementAccountImportResult{}, fmt.Errorf("insert imported account model: %w", err)
			}
		}
		groupID := strings.TrimSpace(account.GroupID)
		if groupID == "" && strings.TrimSpace(account.GroupName) != "" {
			groupID = groupIDs[account.GroupName]
		}
		if groupID != "" {
			if _, err = tx.Exec(ctx, `INSERT INTO juhe_business.group_accounts (system_account_id, group_id, account_id, local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,true,$7,$7)`, input.SystemAccountID, groupID, account.ID, account.Priority, account.SuperPriorityEnabled, account.FallbackEnabled, input.Now); err != nil {
				return port.ManagementAccountImportResult{}, fmt.Errorf("bind imported account group: %w", err)
			}
		}
		imported++
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementAccountImportResult{}, fmt.Errorf("commit account import tx: %w", err)
	}
	committed = true
	return port.ManagementAccountImportResult{Imported: imported, Skipped: skipped}, nil
}

func optionalImportText(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func importHealthModel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	return value
}

var _ port.ManagementAccountImporter = (*Store)(nil)
