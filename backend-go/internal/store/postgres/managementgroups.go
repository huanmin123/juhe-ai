package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultManagementGroupOptionLimit          = 50
	maxManagementGroupOptionLimit              = 50
	maxManagementGroupUpdateRouteStrategyCount = 100
)

var requiredHighConcurrencySchedulingPolicyKeys = []string{
	"mode",
	"defaultSoftConcurrency",
	"fastFirstEnabled",
	"fallbackOnQueueEnabled",
	"breakAffinityOnSoftLimit",
	"breakAffinityOnQueueWaitMs",
	"slowRequestThresholdMs",
	"firstOutputSlowThresholdMs",
	"recentTimeoutWindowSeconds",
	"recentTimeoutPenaltyThreshold",
	"maxQueueWaitMs",
	"maxQueueSize",
	"perApiKeyQueueLimit",
	"clientIpConcurrencyLimit",
	"clientIpConcurrencyOverflowMode",
	"imageLaneMaxConcurrency",
}

func (s *Store) ListManagementGroupOptions(ctx context.Context, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupOption, error) {
	return listManagementGroupOptions(ctx, s.queries(), input)
}

func (s *Store) ListManagementGroupAuthorizationOptions(ctx context.Context, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupAuthorizationOptionRow, error) {
	return listManagementGroupAuthorizationOptions(ctx, s.queries(), input)
}

func (s *Store) ListManagementGroupAccountOptions(ctx context.Context, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupAccountOption, error) {
	return listManagementGroupAccountOptions(ctx, s.queries(), input)
}

func (s *Store) CreateManagementGroup(ctx context.Context, input port.ManagementGroupCreateInput) (port.ManagementGroupSummary, error) {
	return createManagementGroup(ctx, s.queries(), input)
}

func (s *Store) UpdateManagementGroup(ctx context.Context, input port.ManagementGroupUpdateInput) (port.ManagementGroupUpdateResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementGroupUpdateResult{}, fmt.Errorf("begin management group update tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	result, err := updateManagementGroup(ctx, s.queries().WithTx(tx), input)
	if err != nil {
		return port.ManagementGroupUpdateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return port.ManagementGroupUpdateResult{}, fmt.Errorf("commit management group update tx rolled back: %w", err)
		}
		return port.ManagementGroupUpdateResult{}, fmt.Errorf("commit management group update tx: %w", err)
	}
	committed = true
	return result, nil
}

func createManagementGroup(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementGroupCreateInput,
) (port.ManagementGroupSummary, error) {
	row, err := q.CreateManagementGroup(ctx, postgresqueries.CreateManagementGroupParams{
		SystemAccountID:      input.SystemAccountID,
		ProviderCode:         input.ProviderCode,
		ID:                   input.ID,
		Name:                 input.Name,
		Description:          pgTextPtr(input.Description),
		Enabled:              input.Enabled,
		GroupType:            input.GroupType,
		SchedulingPolicyJson: pgTextPtr(input.SchedulingPolicyJSON),
		CreatedAt:            pgTimestamptz(input.CreatedAt),
		UpdatedAt:            pgTimestamptz(input.UpdatedAt),
	})
	if err != nil {
		switch {
		case managementGroupDuplicateNameError(err):
			return port.ManagementGroupSummary{}, port.ErrManagementGroupNameExists
		case managementGroupSystemAccountForeignKeyError(err):
			return port.ManagementGroupSummary{}, port.ErrManagementGroupSystemAccountNotFound
		case managementGroupProviderForeignKeyError(err):
			return port.ManagementGroupSummary{}, port.ErrManagementGroupProviderNotFound
		default:
			return port.ManagementGroupSummary{}, fmt.Errorf("create management group: %w", err)
		}
	}
	if dependencyErr := managementGroupCreateDependencyError(
		row.SystemAccountExists,
		row.ProviderExists,
		row.ProviderEnabled,
	); dependencyErr != nil {
		return port.ManagementGroupSummary{}, dependencyErr
	}
	if !row.ID.Valid ||
		!row.SystemAccountID.Valid ||
		!row.Name.Valid ||
		!row.ProviderCode.Valid ||
		!row.Enabled.Valid ||
		!row.IsDefault.Valid ||
		!row.GroupType.Valid {
		return port.ManagementGroupSummary{}, fmt.Errorf("create management group returned no inserted row")
	}
	return port.ManagementGroupSummary{
		ID:                   row.ID.String,
		SystemAccountID:      row.SystemAccountID.String,
		Name:                 row.Name.String,
		ProviderCode:         row.ProviderCode.String,
		Description:          textPtr(row.Description),
		Enabled:              row.Enabled.Bool,
		IsDefault:            row.IsDefault.Bool,
		GroupType:            row.GroupType.String,
		SchedulingPolicyJSON: textPtr(row.SchedulingPolicyJson),
	}, nil
}

func updateManagementGroup(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementGroupUpdateInput,
) (port.ManagementGroupUpdateResult, error) {
	if input.HasProviderCode {
		provider, err := q.FindManagementGroupUpdateProvider(ctx, strings.TrimSpace(input.ProviderCode))
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return port.ManagementGroupUpdateResult{}, port.ErrManagementGroupProviderNotFound
		case err != nil:
			return port.ManagementGroupUpdateResult{}, fmt.Errorf("find management group update provider: %w", err)
		case !provider.Enabled:
			return port.ManagementGroupUpdateResult{}, port.ErrManagementGroupProviderDisabled
		}
	}

	if strings.TrimSpace(input.ActorSystemAccountID) == "" {
		return port.ManagementGroupUpdateResult{}, port.ErrManagementGroupNotFound
	}
	effectiveSystemAccountID := managementGroupUpdateEffectiveSystemAccountID(input)
	current, err := q.LockManagementGroupUpdateTarget(ctx, postgresqueries.LockManagementGroupUpdateTargetParams{
		CanAccessAll:             input.CanAccessAll,
		EffectiveSystemAccountID: effectiveSystemAccountID,
		GroupID:                  input.GroupID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementGroupUpdateResult{}, port.ErrManagementGroupNotFound
	}
	if err != nil {
		return port.ManagementGroupUpdateResult{}, fmt.Errorf("lock management group update target: %w", err)
	}
	if current.AccessType == "authorized" {
		return updateAuthorizedManagementGroup(ctx, q, input, current, effectiveSystemAccountID)
	}
	return updateOwnedManagementGroup(ctx, q, input, current, effectiveSystemAccountID)
}

func updateOwnedManagementGroup(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementGroupUpdateInput,
	current postgresqueries.LockManagementGroupUpdateTargetRow,
	effectiveSystemAccountID string,
) (port.ManagementGroupUpdateResult, error) {
	if current.IsDefault {
		return port.ManagementGroupUpdateResult{}, port.ErrManagementGroupDefaultReadonly
	}
	before := managementGroupMutationSummary(
		current.ID,
		current.Name,
		current.ProviderCode,
		current.Description,
		current.Enabled,
		current.IsDefault,
		current.GroupType,
		current.SchedulingPolicyJson,
	)
	next := before
	if input.HasName {
		next.Name = input.Name
	}
	if input.HasProviderCode {
		next.ProviderCode = strings.TrimSpace(input.ProviderCode)
	}
	if input.HasDescription {
		next.Description = input.Description
	}
	if input.HasEnabled {
		next.Enabled = input.Enabled
	}
	if input.HasGroupType {
		next.GroupType = input.GroupType
	}
	next.SchedulingPolicyJSON = managementGroupUpdateSchedulingPolicy(
		before.GroupType,
		before.SchedulingPolicyJSON,
		next.GroupType,
		input,
	)

	if next.ProviderCode != current.ProviderCode {
		accountCount, err := q.CountManagementGroupUpdateAccounts(ctx, postgresqueries.CountManagementGroupUpdateAccountsParams{
			GroupID:              current.ID,
			OwnerSystemAccountID: current.SystemAccountID,
		})
		if err != nil {
			return port.ManagementGroupUpdateResult{}, fmt.Errorf("count management group update accounts: %w", err)
		}
		if accountCount > 0 {
			return port.ManagementGroupUpdateResult{}, port.ErrManagementGroupProviderHasAccounts
		}
	}

	scopeSystemAccountID := effectiveSystemAccountID
	if scopeSystemAccountID == "" {
		scopeSystemAccountID = current.SystemAccountID
	}
	if current.Enabled && !next.Enabled {
		if err := guardManagementGroupUpdateRouteStrategies(
			ctx,
			q,
			current.ID,
			current.Name,
			managementGroupUpdateOwnerRouteStrategyScope(),
			input.UpdatedAt,
			"停用分组",
		); err != nil {
			return port.ManagementGroupUpdateResult{}, err
		}
	}

	updated, err := q.UpdateManagementGroupOwner(ctx, postgresqueries.UpdateManagementGroupOwnerParams{
		Name:                 next.Name,
		ProviderCode:         next.ProviderCode,
		Description:          pgTextPtr(next.Description),
		Enabled:              next.Enabled,
		GroupType:            next.GroupType,
		SchedulingPolicyJson: pgTextPtr(next.SchedulingPolicyJSON),
		UpdatedAt:            pgTimestamptz(input.UpdatedAt),
		GroupID:              current.ID,
		OwnerSystemAccountID: current.SystemAccountID,
	})
	switch {
	case managementGroupDuplicateNameError(err):
		return port.ManagementGroupUpdateResult{}, managementGroupUpdateNameExistsError(next.Name)
	case errors.Is(err, pgx.ErrNoRows):
		return port.ManagementGroupUpdateResult{}, port.ErrManagementGroupNotFound
	case err != nil:
		return port.ManagementGroupUpdateResult{}, fmt.Errorf("update owned management group: %w", err)
	}
	after := managementGroupMutationSummary(
		updated.ID,
		updated.Name,
		updated.ProviderCode,
		updated.Description,
		updated.Enabled,
		updated.IsDefault,
		updated.GroupType,
		updated.SchedulingPolicyJson,
	)
	return port.ManagementGroupUpdateResult{
		Before:                   before,
		After:                    after,
		AccessType:               "owner",
		OwnerSystemAccountID:     current.SystemAccountID,
		EffectiveSystemAccountID: scopeSystemAccountID,
	}, nil
}

func updateAuthorizedManagementGroup(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementGroupUpdateInput,
	current postgresqueries.LockManagementGroupUpdateTargetRow,
	effectiveSystemAccountID string,
) (port.ManagementGroupUpdateResult, error) {
	if err := managementGroupAuthorizedFieldsError(input); err != nil {
		return port.ManagementGroupUpdateResult{}, err
	}
	authorization, err := q.LockManagementGroupUpdateAuthorization(ctx, postgresqueries.LockManagementGroupUpdateAuthorizationParams{
		AuthorizationID:          current.GroupAuthorizationID,
		GroupID:                  current.ID,
		OwnerSystemAccountID:     current.SystemAccountID,
		EffectiveSystemAccountID: effectiveSystemAccountID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementGroupUpdateResult{}, port.ErrManagementGroupNotFound
	}
	if err != nil {
		return port.ManagementGroupUpdateResult{}, fmt.Errorf("lock management group update authorization: %w", err)
	}

	settingsEnabled := true
	settingsGroupType := current.GroupType
	settingsSchedulingPolicyJSON := textPtr(current.SchedulingPolicyJson)
	settings, err := q.LockManagementGroupUpdateAuthorizationSettings(
		ctx,
		authorization.ID,
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return port.ManagementGroupUpdateResult{}, fmt.Errorf("lock management group update authorization settings: %w", err)
	default:
		if settings.SystemAccountID != effectiveSystemAccountID || settings.GroupID != current.ID {
			return port.ManagementGroupUpdateResult{}, fmt.Errorf(
				"management group authorization settings %q scope mismatch",
				authorization.ID,
			)
		}
		settingsEnabled = settings.Enabled
		settingsGroupType = settings.GroupType
		settingsSchedulingPolicyJSON = textPtr(settings.SchedulingPolicyJson)
	}

	before := port.ManagementGroupMutationSummary{
		ID:                   current.ID,
		Name:                 current.Name,
		ProviderCode:         current.ProviderCode,
		Description:          textPtr(current.Description),
		Enabled:              current.Enabled && settingsEnabled,
		IsDefault:            false,
		GroupType:            settingsGroupType,
		SchedulingPolicyJSON: settingsSchedulingPolicyJSON,
	}
	normalizeManagementGroupMutationSchedulingPolicy(&before)
	next := before
	nextSettingsEnabled := settingsEnabled
	if input.HasEnabled {
		nextSettingsEnabled = input.Enabled
	}
	next.Enabled = current.Enabled && nextSettingsEnabled
	if input.HasGroupType {
		next.GroupType = input.GroupType
	}
	next.SchedulingPolicyJSON = managementGroupUpdateSchedulingPolicy(
		before.GroupType,
		before.SchedulingPolicyJSON,
		next.GroupType,
		input,
	)

	if before.Enabled && !next.Enabled {
		if err := guardManagementGroupUpdateRouteStrategies(
			ctx,
			q,
			current.ID,
			current.Name,
			managementGroupUpdateAuthorizedRouteStrategyScope(effectiveSystemAccountID),
			input.UpdatedAt,
			"停用授权分组",
		); err != nil {
			return port.ManagementGroupUpdateResult{}, err
		}
	}

	updated, err := q.UpsertManagementGroupAuthorizationSettings(
		ctx,
		postgresqueries.UpsertManagementGroupAuthorizationSettingsParams{
			AuthorizationID:          authorization.ID,
			EffectiveSystemAccountID: effectiveSystemAccountID,
			GroupID:                  current.ID,
			Enabled:                  nextSettingsEnabled,
			GroupType:                next.GroupType,
			SchedulingPolicyJson:     pgTextPtr(next.SchedulingPolicyJSON),
			UpdatedAt:                pgTimestamptz(input.UpdatedAt),
		},
	)
	if err != nil {
		return port.ManagementGroupUpdateResult{}, fmt.Errorf("upsert management group authorization settings: %w", err)
	}
	after := port.ManagementGroupMutationSummary{
		ID:                   current.ID,
		Name:                 current.Name,
		ProviderCode:         current.ProviderCode,
		Description:          textPtr(current.Description),
		Enabled:              current.Enabled && updated.Enabled,
		IsDefault:            false,
		GroupType:            updated.GroupType,
		SchedulingPolicyJSON: textPtr(updated.SchedulingPolicyJson),
	}
	normalizeManagementGroupMutationSchedulingPolicy(&after)
	return port.ManagementGroupUpdateResult{
		Before:                   before,
		After:                    after,
		AccessType:               "authorized",
		OwnerSystemAccountID:     current.SystemAccountID,
		EffectiveSystemAccountID: effectiveSystemAccountID,
		GroupAuthorizationID:     authorization.ID,
	}, nil
}

func managementGroupUpdateEffectiveSystemAccountID(input port.ManagementGroupUpdateInput) string {
	effectiveSystemAccountID := strings.TrimSpace(input.EffectiveSystemAccountID)
	if effectiveSystemAccountID != "" {
		return effectiveSystemAccountID
	}
	if input.CanAccessAll {
		return ""
	}
	return strings.TrimSpace(input.ActorSystemAccountID)
}

func managementGroupAuthorizedFieldsError(input port.ManagementGroupUpdateInput) error {
	fields := make([]string, 0, 3)
	if input.HasName {
		fields = append(fields, "name")
	}
	if input.HasProviderCode {
		fields = append(fields, "providerCode")
	}
	if input.HasDescription {
		fields = append(fields, "description")
	}
	if len(fields) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: 授权分组使用配置包含未知字段：%s",
		port.ErrManagementGroupAuthorizedFields,
		strings.Join(fields, "、"),
	)
}

func managementGroupUpdateNameExistsError(name string) error {
	return fmt.Errorf("%w: %s", port.ErrManagementGroupNameExists, name)
}

func managementGroupUpdateRouteStrategyLimitError(count int, action string) error {
	if count <= maxManagementGroupUpdateRouteStrategyCount {
		return nil
	}
	return fmt.Errorf(
		"%w: 该分组关联的策略路由超过 %d 个，请先分批解除绑定后再%s",
		port.ErrManagementGroupRouteStrategyWouldLose,
		maxManagementGroupUpdateRouteStrategyCount,
		action,
	)
}

type managementGroupUpdateRouteStrategyScope struct {
	allScopes                bool
	effectiveSystemAccountID string
}

func managementGroupUpdateOwnerRouteStrategyScope() managementGroupUpdateRouteStrategyScope {
	return managementGroupUpdateRouteStrategyScope{allScopes: true}
}

func managementGroupUpdateAuthorizedRouteStrategyScope(
	effectiveSystemAccountID string,
) managementGroupUpdateRouteStrategyScope {
	return managementGroupUpdateRouteStrategyScope{
		effectiveSystemAccountID: effectiveSystemAccountID,
	}
}

func guardManagementGroupUpdateRouteStrategies(
	ctx context.Context,
	q *postgresqueries.Queries,
	groupID string,
	groupName string,
	scope managementGroupUpdateRouteStrategyScope,
	now time.Time,
	action string,
) error {
	params := postgresqueries.LockManagementGroupUpdateRouteStrategiesParams{
		NowAt:                    pgTimestamptz(now),
		GroupID:                  groupID,
		AllScopes:                scope.allScopes,
		EffectiveSystemAccountID: scope.effectiveSystemAccountID,
	}
	routeStrategyIDs, err := q.LockManagementGroupUpdateRouteStrategies(ctx, params)
	if err != nil {
		return fmt.Errorf("lock management group update route strategies: %w", err)
	}
	if err := managementGroupUpdateRouteStrategyLimitError(len(routeStrategyIDs), action); err != nil {
		return err
	}
	if len(routeStrategyIDs) == 0 {
		return nil
	}
	count, err := q.CountManagementGroupUpdateRouteStrategyLoss(
		ctx,
		postgresqueries.CountManagementGroupUpdateRouteStrategyLossParams{
			NowAt:                    params.NowAt,
			GroupID:                  params.GroupID,
			AllScopes:                params.AllScopes,
			EffectiveSystemAccountID: params.EffectiveSystemAccountID,
			RouteStrategyIds:         routeStrategyIDs,
		},
	)
	if err != nil {
		return fmt.Errorf("count management group update route strategy loss: %w", err)
	}
	if count == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: 无法%s“%s”：该分组仍是当前范围内活跃策略路由的唯一可用启用分组",
		port.ErrManagementGroupRouteStrategyWouldLose,
		action,
		groupName,
	)
}

func managementGroupMutationSummary(
	id string,
	name string,
	providerCode string,
	description pgtype.Text,
	enabled bool,
	isDefault bool,
	groupType string,
	schedulingPolicyJSON pgtype.Text,
) port.ManagementGroupMutationSummary {
	return port.ManagementGroupMutationSummary{
		ID:                   id,
		Name:                 name,
		ProviderCode:         providerCode,
		Description:          textPtr(description),
		Enabled:              enabled,
		IsDefault:            isDefault,
		GroupType:            groupType,
		SchedulingPolicyJSON: textPtr(schedulingPolicyJSON),
	}
}

func normalizeManagementGroupMutationSchedulingPolicy(summary *port.ManagementGroupMutationSummary) {
	if summary.GroupType != "high_concurrency" {
		summary.SchedulingPolicyJSON = nil
	}
}

func managementGroupUpdateSchedulingPolicy(
	currentGroupType string,
	currentSchedulingPolicyJSON *string,
	nextGroupType string,
	input port.ManagementGroupUpdateInput,
) *string {
	if nextGroupType != "high_concurrency" {
		return nil
	}
	if input.HasSchedulingPolicy {
		return input.SchedulingPolicyJSON
	}
	if currentGroupType == "high_concurrency" {
		return currentSchedulingPolicyJSON
	}
	value := input.DefaultSchedulingPolicyJSON
	return &value
}

func listManagementGroupOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListManagementGroupOptions(ctx, postgresqueries.ListManagementGroupOptionsParams{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		Ids:             uniqueStrings(input.IDs, 50),
		ProviderCode:    strings.TrimSpace(input.ProviderCode),
		HasKeyword:      keyword != "",
		Keyword:         keyword,
		KeywordUpper:    keywordUpper,
		PreferDefault:   input.PreferDefault,
		RowLimit:        int32(managementGroupOptionLimit(input.Limit)),
		ManageableOnly:  input.ManageableOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("list management group options: %w", err)
	}
	options := make([]port.ManagementGroupOption, 0, len(rows))
	for _, row := range rows {
		schedulingPolicy, err := managementGroupSchedulingPolicy(row.ID, row.GroupType, row.SchedulingPolicyJson)
		if err != nil {
			return nil, err
		}
		authorizationLimits, err := managementGroupAuthorizationLimits(row.ID, row.AuthorizationLimitsJson)
		if err != nil {
			return nil, err
		}
		option := port.ManagementGroupOption{
			ID:                                 row.ID,
			OwnerSystemAccountID:               row.SystemAccountID,
			OwnerSystemAccountName:             textValue(row.SystemAccountName),
			Name:                               row.Name,
			ProviderCode:                       row.ProviderCode,
			Enabled:                            row.Enabled,
			IsDefault:                          row.IsDefault,
			GroupType:                          managementGroupType(row.GroupType),
			SchedulingPolicy:                   schedulingPolicy,
			AccessType:                         managementGroupAccessType(row.AccessType),
			GroupAuthorizationID:               textValue(row.GroupAuthorizationID),
			AuthorizationStatus:                textValue(row.AuthorizationStatus),
			AuthorizationExpiresAt:             timestamptzPtr(row.AuthorizationExpiresAt),
			AuthorizationLimits:                authorizationLimits,
			HasActiveManualAuthorizationSource: row.HasActiveManualAuthorizationSource,
		}
		if input.IncludeSystemAccountFields {
			option.SystemAccountID = row.SystemAccountID
			option.SystemAccountName = textValue(row.SystemAccountName)
		}
		if option.AccessType != "authorized" && !input.IncludeSystemAccountFields {
			option.OwnerSystemAccountName = ""
		}
		options = append(options, option)
	}
	return options, nil
}

func listManagementGroupAuthorizationOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupAuthorizationOptionRow, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListManagementGroupAuthorizationOptions(ctx, postgresqueries.ListManagementGroupAuthorizationOptionsParams{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		Ids:             uniqueStrings(input.IDs, 50),
		ProviderCode:    strings.TrimSpace(input.ProviderCode),
		HasKeyword:      keyword != "",
		Keyword:         keyword,
		KeywordUpper:    keywordUpper,
		PreferDefault:   input.PreferDefault,
		RowLimit:        int32(managementGroupOptionLimit(input.Limit)),
		ManageableOnly:  input.ManageableOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("list management group authorization options: %w", err)
	}
	items := make([]port.ManagementGroupAuthorizationOptionRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementGroupAuthorizationOptionRow{ID: row.ID, Name: row.Name, AccessType: row.AccessType})
	}
	return items, nil
}

func listManagementGroupAccountOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupAccountOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListManagementGroupAccountOptions(ctx, postgresqueries.ListManagementGroupAccountOptionsParams{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		Ids:             uniqueStrings(input.IDs, 50),
		ProviderCode:    strings.TrimSpace(input.ProviderCode),
		HasKeyword:      keyword != "",
		Keyword:         keyword,
		KeywordUpper:    keywordUpper,
		PreferDefault:   input.PreferDefault,
		RowLimit:        int32(managementGroupOptionLimit(input.Limit)),
		ManageableOnly:  input.ManageableOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("list management account group options: %w", err)
	}
	groupIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		groupIDs = append(groupIDs, row.ID)
	}
	accountIDsByGroupID := map[string][]string{}
	if len(groupIDs) > 0 {
		accountIDRows, err := q.ListManagementGroupAccountOptionIDs(ctx, postgresqueries.ListManagementGroupAccountOptionIDsParams{
			GroupIds:        groupIDs,
			SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		})
		if err != nil {
			return nil, fmt.Errorf("list management group account option ids: %w", err)
		}
		for _, row := range accountIDRows {
			accountIDsByGroupID[row.GroupID] = append(accountIDsByGroupID[row.GroupID], row.AccountID)
		}
	}
	options := make([]port.ManagementGroupAccountOption, 0, len(rows))
	for _, row := range rows {
		schedulingPolicy, err := managementGroupSchedulingPolicy(row.ID, row.GroupType, row.SchedulingPolicyJson)
		if err != nil {
			return nil, err
		}
		authorizationLimits, err := managementGroupAuthorizationLimits(row.ID, row.AuthorizationLimitsJson)
		if err != nil {
			return nil, err
		}
		accessType := managementGroupAccessType(row.AccessType)
		accountIDs := append([]string{}, accountIDsByGroupID[row.ID]...)
		if accessType == "authorized" {
			accountIDs = []string{}
		}
		option := port.ManagementGroupAccountOption{
			ID:                                 row.ID,
			OwnerSystemAccountID:               row.SystemAccountID,
			OwnerSystemAccountName:             textValue(row.SystemAccountName),
			Name:                               row.Name,
			ProviderCode:                       row.ProviderCode,
			Enabled:                            row.Enabled,
			IsDefault:                          row.IsDefault,
			GroupType:                          managementGroupType(row.GroupType),
			SchedulingPolicy:                   schedulingPolicy,
			AccessType:                         accessType,
			GroupAuthorizationID:               textValue(row.GroupAuthorizationID),
			AuthorizationStatus:                textValue(row.AuthorizationStatus),
			AuthorizationExpiresAt:             timestamptzPtr(row.AuthorizationExpiresAt),
			AuthorizationLimits:                authorizationLimits,
			HasActiveManualAuthorizationSource: row.HasActiveManualAuthorizationSource,
			AccountIDs:                         accountIDs,
		}
		if input.IncludeSystemAccountFields {
			option.SystemAccountID = row.SystemAccountID
			option.SystemAccountName = textValue(row.SystemAccountName)
		}
		if option.AccessType != "authorized" && !input.IncludeSystemAccountFields {
			option.OwnerSystemAccountName = ""
		}
		options = append(options, option)
	}
	return options, nil
}

func managementGroupOptionLimit(limit int) int {
	if limit <= 0 {
		return defaultManagementGroupOptionLimit
	}
	return min(limit, maxManagementGroupOptionLimit)
}

func managementGroupType(value string) string {
	if strings.TrimSpace(value) == "" {
		return "personal"
	}
	return value
}

func managementGroupAccessType(value string) string {
	if strings.TrimSpace(value) == "authorized" {
		return "authorized"
	}
	return "owner"
}

func managementGroupAuthorizationLimits(groupID string, value pgtype.Text) (map[string]any, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	var limits map[string]any
	if err := json.Unmarshal([]byte(value.String), &limits); err != nil {
		return nil, fmt.Errorf("group %s authorization limits json is invalid: %w", groupID, err)
	}
	return limits, nil
}

func managementGroupSchedulingPolicy(groupID string, groupType string, value pgtype.Text) (map[string]any, error) {
	if managementGroupType(groupType) != "high_concurrency" {
		return nil, nil
	}
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, fmt.Errorf("group %s high concurrency scheduling policy is missing", groupID)
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(value.String), &policy); err != nil {
		return nil, fmt.Errorf("group %s high concurrency scheduling policy is invalid: %w", groupID, err)
	}
	for _, key := range requiredHighConcurrencySchedulingPolicyKeys {
		if policy[key] == nil {
			return nil, fmt.Errorf("group %s high concurrency scheduling policy missing %s", groupID, key)
		}
	}
	return policy, nil
}

func managementGroupCreateDependencyError(
	systemAccountExists bool,
	providerExists bool,
	providerEnabled bool,
) error {
	switch {
	case !providerExists:
		return port.ErrManagementGroupProviderNotFound
	case !providerEnabled:
		return port.ErrManagementGroupProviderDisabled
	case !systemAccountExists:
		return port.ErrManagementGroupSystemAccountNotFound
	default:
		return nil
	}
}

func managementGroupDuplicateNameError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		(pgErr.ConstraintName == "idx_groups_owner_provider_name_unique" ||
			pgErr.ConstraintName == "idx_groups_owner_provider_name_unique_lower")
}

func managementGroupSystemAccountForeignKeyError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23503" &&
		pgErr.ConstraintName == "groups_system_account_id_fkey"
}

func managementGroupProviderForeignKeyError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23503" &&
		pgErr.ConstraintName == "groups_provider_code_fkey"
}

var _ port.ManagementGroupOptionReader = (*Store)(nil)
var _ port.ManagementGroupCreator = (*Store)(nil)
var _ port.ManagementGroupUpdater = (*Store)(nil)
