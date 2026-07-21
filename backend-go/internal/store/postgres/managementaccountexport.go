package postgres

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

//go:embed queries/w2_management_account_export.sql
var managementAccountExportSQL string

func (s *Store) ListManagementAccountExportBatch(ctx context.Context, input port.ManagementAccountExportInput, afterID string, limit int) (port.ManagementAccountExportPage, error) {
	if limit <= 0 || limit > port.ManagementAccountExportMaxAccounts {
		limit = port.ManagementAccountExportMaxAccounts
	}
	filter := input.Filter
	ids := nullableTextArray(input.AccountIDs)
	statuses := nullableTextArray(filter.Statuses)
	rows, err := s.pool.Query(ctx, managementAccountExportSQL,
		strings.TrimSpace(input.SystemAccountID), ids, strings.TrimSpace(filter.Keyword),
		strings.TrimSpace(filter.ProviderCode), strings.TrimSpace(filter.GroupID),
		nullableTextArray(filter.TagIDs), strings.TrimSpace(filter.Type), statuses,
		strings.TrimSpace(filter.Schedulable), strings.TrimSpace(afterID), limit+1,
	)
	if err != nil {
		return port.ManagementAccountExportPage{}, fmt.Errorf("list management account export batch: %w", err)
	}
	defer rows.Close()
	page := port.ManagementAccountExportPage{Items: make([]port.ManagementAccountExportAccount, 0, limit)}
	for rows.Next() {
		var item port.ManagementAccountExportAccount
		var matched int
		if err := rows.Scan(
			&item.ID, &item.Name, &item.ProviderCode, &item.ProviderProtocolProfileID,
			&item.ProtocolCode, &item.ProtocolVersion, &item.Type, &item.Status,
			&item.SystemAccountID, &item.CredentialsEncrypted, &item.GroupID, &item.GroupName,
			&item.ProxyProfileID, &item.ProxyName, &item.ProxyType, &item.ProxyHost, &item.ProxyPort,
			&item.ProxyUsername, &item.ProxyPasswordEncrypted, &item.ProxyDescription, &item.ProxyEnabled,
			&item.ConcurrencyLimit, &item.Priority, &item.SuperPriorityEnabled, &item.FallbackEnabled,
			&item.Schedulable, &item.SupportedModelsJSON, &item.HealthCheckModel,
			&item.HealthCheckEndpointMode, &item.TemporaryUnavailableContinuousProbe,
			&item.ModelMappingsJSON, &item.TagsJSON, &item.AccountExpiresAt,
			&item.AvailabilityScheduleJSON, &item.Notes, &matched,
		); err != nil {
			return port.ManagementAccountExportPage{}, fmt.Errorf("scan management account export batch: %w", err)
		}
		if len(page.Items) == limit {
			page.HasMore = true
			break
		}
		page.Items = append(page.Items, item)
		page.NextID = item.ID
		page.Matched = matched
	}
	if err := rows.Err(); err != nil {
		return port.ManagementAccountExportPage{}, fmt.Errorf("read management account export batch: %w", err)
	}
	return page, nil
}

func nullableTextArray(values []string) []string {
	cleaned := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		cleaned = append(cleaned, value)
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

var _ port.ManagementAccountExportReader = (*Store)(nil)
