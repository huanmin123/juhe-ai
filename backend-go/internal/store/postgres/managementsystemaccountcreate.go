package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

var (
	defaultBuiltInGroups = []struct {
		Name         string
		ProviderCode string
		Description  string
	}{
		{Name: "默认 OpenAI 兼容分组", ProviderCode: "openai", Description: ""},
		{Name: "默认 GPT 分组", ProviderCode: "gpt", Description: ""},
		{Name: "默认 DeepSeek 分组", ProviderCode: "deepseek", Description: ""},
		{Name: "默认 Anthropic 分组", ProviderCode: "anthropic", Description: ""},
		{Name: "默认 Gemini 分组", ProviderCode: "gemini", Description: ""},
		{Name: "默认 GLM 分组", ProviderCode: "glm", Description: ""},
		{Name: "默认混合供应商分组", ProviderCode: "hybrid", Description: "混合供应商账户保存真实上游凭据和 Base URL，允许账户内配置跨协议入口映射"},
	}
)

const hybridProviderCode = "hybrid"

func (s *Store) CreateManagementSystemAccount(ctx context.Context, input port.ManagementSystemAccountCreateInput) (port.ManagementSystemAccountCreateResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return port.ManagementSystemAccountCreateResult{}, fmt.Errorf("begin system account create tx: %w", err)
	}
	defer tx.Rollback(ctx)

	q := postgresqueries.New(tx)

	description := pgtype.Text{}
	if input.Description != nil && *input.Description != "" {
		description = pgtype.Text{String: *input.Description, Valid: true}
	}

	row, err := q.CreateManagementSystemAccount(ctx, postgresqueries.CreateManagementSystemAccountParams{
		ID:                     input.ID,
		Username:               input.Username,
		DisplayName:            input.DisplayName,
		Description:            description,
		Role:                   input.Role,
		Status:                 input.Status,
		PasswordHash:           input.PasswordHash,
		MustChangePassword:     input.MustChangePassword,
		ImageGenerationEnabled: input.ImageGenerationEnabled,
		CreatedAt:              pgTimestamptz(input.CreatedAt),
		UpdatedAt:              pgTimestamptz(input.UpdatedAt),
	})
	if err != nil {
		if isPGUniqueViolation(err) {
			if strings.Contains(err.Error(), "idx_system_accounts_username_unique_lower") {
				return port.ManagementSystemAccountCreateResult{}, port.ErrManagementSystemAccountUsernameExists
			}
			if strings.Contains(err.Error(), "idx_system_accounts_display_name_unique_lower") {
				return port.ManagementSystemAccountCreateResult{}, port.ErrManagementSystemAccountDisplayNameExists
			}
		}
		return port.ManagementSystemAccountCreateResult{}, fmt.Errorf("create management system account: %w", err)
	}

	groupIDs, _, apiKeyIDs, err := createDefaultResources(ctx, q, input.ID, input.DefaultAPIKeys, input.CreatedAt)
	if err != nil {
		return port.ManagementSystemAccountCreateResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return port.ManagementSystemAccountCreateResult{}, fmt.Errorf("commit system account create: %w", err)
	}

	account := port.ManagementSystemAccountSummary{
		ID:                     row.ID,
		Username:               row.Username,
		DisplayName:            row.DisplayName,
		Description:            textValue(row.Description),
		Role:                   row.Role,
		Status:                 row.Status,
		MustChangePassword:     row.MustChangePassword,
		ImageGenerationEnabled: row.ImageGenerationEnabled,
		LastLoginAt:            timestamptzPtr(row.LastLoginAt),
		CreatedAt:              timestamptzValue(row.CreatedAt),
		UpdatedAt:              timestamptzValue(row.UpdatedAt),
	}

	return port.ManagementSystemAccountCreateResult{
		Account:          account,
		DefaultGroupIDs:  groupIDs,
		DefaultAPIKeyIDs: apiKeyIDs,
	}, nil
}

func createDefaultResources(
	ctx context.Context,
	q *postgresqueries.Queries,
	systemAccountID string,
	defaultAPIKeys []port.ManagementDefaultAPIKeyCreateInput,
	now time.Time,
) (groupIDs []string, strategyIDs []string, apiKeyIDs []string, err error) {
	routeGroupCount := defaultRouteGroupCount()
	if len(defaultAPIKeys) != routeGroupCount {
		return nil, nil, nil, fmt.Errorf("default api key count = %d, want %d", len(defaultAPIKeys), routeGroupCount)
	}
	apiKeyIndex := 0
	for _, group := range defaultBuiltInGroups {
		groupID := prefixedUUID("grp")
		_, err := q.CreateManagementDefaultGroup(ctx, postgresqueries.CreateManagementDefaultGroupParams{
			ID:              groupID,
			SystemAccountID: systemAccountID,
			Name:            group.Name,
			ProviderCode:    group.ProviderCode,
			Description:     pgtype.Text{String: group.Description, Valid: group.Description != ""},
			CreatedAt:       pgTimestamptz(now),
			UpdatedAt:       pgTimestamptz(now),
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("create default group %s: %w", group.ProviderCode, err)
		}
		groupIDs = append(groupIDs, groupID)

		if group.ProviderCode == hybridProviderCode {
			continue
		}

		strategyID := prefixedUUID("route_strategy")
		strategyName := defaultRouteStrategyNameForGroup(group.Name)
		strategyDesc := "系统默认普通路由，绑定" + group.Name + "。"
		_, err = q.CreateManagementDefaultRouteStrategy(ctx, postgresqueries.CreateManagementDefaultRouteStrategyParams{
			ID:              strategyID,
			SystemAccountID: systemAccountID,
			Name:            strategyName,
			Description:     pgtype.Text{String: strategyDesc, Valid: true},
			CreatedAt:       pgTimestamptz(now),
			UpdatedAt:       pgTimestamptz(now),
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("create default route strategy for %s: %w", group.ProviderCode, err)
		}
		strategyIDs = append(strategyIDs, strategyID)

		rsgID := prefixedUUID("rsg")
		err = q.CreateManagementDefaultRouteStrategyGroup(ctx, postgresqueries.CreateManagementDefaultRouteStrategyGroupParams{
			ID:              rsgID,
			RouteStrategyID: strategyID,
			SystemAccountID: systemAccountID,
			GroupID:         groupID,
			CreatedAt:       pgTimestamptz(now),
			UpdatedAt:       pgTimestamptz(now),
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("create default route strategy group: %w", err)
		}

		defaultAPIKey := defaultAPIKeys[apiKeyIndex]
		apiKeyIndex++
		apiKeyName := defaultAPIKeyNameForRouteStrategy(strategyName)
		apiKeyDesc := "系统默认 API Key，绑定" + strategyName + "。"
		_, err = q.CreateManagementDefaultAPIKey(ctx, postgresqueries.CreateManagementDefaultAPIKeyParams{
			ID:                 defaultAPIKey.ID,
			SystemAccountID:    systemAccountID,
			RouteStrategyID:    strategyID,
			Name:               apiKeyName,
			Description:        pgtype.Text{String: apiKeyDesc, Valid: true},
			KeyHash:            defaultAPIKey.KeyHash,
			KeyPrefix:          defaultAPIKey.KeyPrefix,
			KeySuffix:          defaultAPIKey.KeySuffix,
			KeySecretEncrypted: pgtype.Text{String: defaultAPIKey.KeySecretEncrypted, Valid: defaultAPIKey.KeySecretEncrypted != ""},
			CreatedAt:          pgTimestamptz(now),
			UpdatedAt:          pgTimestamptz(now),
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("create default api key: %w", err)
		}
		apiKeyIDs = append(apiKeyIDs, defaultAPIKey.ID)
	}
	return groupIDs, strategyIDs, apiKeyIDs, nil
}

func defaultRouteGroupCount() int {
	count := 0
	for _, group := range defaultBuiltInGroups {
		if group.ProviderCode != hybridProviderCode {
			count++
		}
	}
	return count
}

func defaultRouteStrategyNameForGroup(groupName string) string {
	name := strings.TrimSpace(groupName)
	if name == "" {
		return "默认路由"
	}
	return strings.TrimSuffix(name, "分组") + "路由"
}

func defaultAPIKeyNameForRouteStrategy(routeStrategyName string) string {
	return strings.TrimSuffix(routeStrategyName, "路由") + "API Key"
}

func prefixedUUID(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func isPGUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

var _ port.ManagementSystemAccountCreator = (*Store)(nil)
