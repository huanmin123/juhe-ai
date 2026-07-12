package managementroutestrategies

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	RouteStrategyUpdatedReason                  = "route_strategy_updated"
	maxRouteStrategyUpdateSnapshotTotalAttempts = 3
)

type routeStrategyBindingSnapshotChangedError struct {
	groupIDs []string
}

func (e *routeStrategyBindingSnapshotChangedError) Error() string {
	return "策略路由分组绑定发生并发变化"
}

type NotFoundError struct {
	RouteStrategyID string
}

func (e *NotFoundError) Error() string {
	return "策略路由不存在"
}

func NotFoundMessage(err error) (string, bool) {
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		return "", false
	}
	return notFoundErr.Error(), true
}

type UpdateInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	RouteStrategyID      string
	HasName              bool
	Name                 string
	HasDescription       bool
	Description          *string
	HasMode              bool
	Mode                 string
	HasStatus            bool
	Status               string
	HasGroupBindings     bool
	GroupBindings        []CreateGroupBindingInput
	NormalRoutingConfig  ConfigInput
	HybridRoutingConfig  ConfigInput
}

type UpdateResult struct {
	Before               DetailResult
	RouteStrategy        DetailResult
	OwnerSystemAccountID string
}

type normalizedUpdateScope struct {
	ownerSystemAccountID string
	includeOwner         bool
	routeStrategyID      string
}

type normalizedUpdateInput struct {
	hasName          bool
	name             string
	hasDescription   bool
	description      *string
	hasMode          bool
	mode             string
	hasStatus        bool
	status           string
	hasGroupBindings bool
	groupBindings    []CreateGroupBindingInput
	normalConfig     ConfigInput
	hybridConfig     ConfigInput
	now              time.Time
}

type routeStrategyUpdatePreload struct {
	scope          normalizedUpdateScope
	current        port.PublicRouteStrategySummary
	currentVisible bool
	target         port.PublicGroupTarget
	targetFound    bool
}

func (s *Service) PrepareUpdate(ctx context.Context, input UpdateInput) error {
	_, err := s.preloadUpdate(ctx, input)
	return err
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (UpdateResult, error) {
	if s.createStore == nil {
		return UpdateResult{}, fmt.Errorf("management route strategy update store is required")
	}
	if s.transactor == nil {
		return UpdateResult{}, fmt.Errorf("management route strategy transactor is required")
	}
	preload, err := s.preloadUpdate(ctx, input)
	if err != nil {
		return UpdateResult{}, err
	}

	normalized, err := s.normalizeUpdatePatchInput(input)
	if err != nil {
		return UpdateResult{}, err
	}
	if !preload.currentVisible || !preload.targetFound {
		return UpdateResult{}, routeStrategyNotFound(preload.scope.routeStrategyID)
	}

	prelockedGroupIDs := routeStrategyUpdatePrelockGroupIDs(
		normalized,
		preload.current.GroupBindings,
	)
	var result UpdateResult
	for attempt := 0; attempt < maxRouteStrategyUpdateSnapshotTotalAttempts; attempt++ {
		err = s.transactor.PublicRouteStrategyInTx(
			ctx,
			func(txCtx context.Context, store port.PublicRouteStrategyStore) error {
				groups, err := store.FindPublicRouteStrategyBindableGroups(
					txCtx,
					preload.current.SystemAccountID,
					prelockedGroupIDs,
				)
				if err != nil {
					return err
				}

				current, found, err := store.FindPublicRouteStrategyByID(
					txCtx,
					preload.scope.routeStrategyID,
				)
				if err != nil {
					return err
				}
				currentVisible := found &&
					(preload.scope.ownerSystemAccountID == "" ||
						current.SystemAccountID == preload.scope.ownerSystemAccountID)
				if !currentVisible ||
					current.SystemAccountID != preload.current.SystemAccountID {
					return routeStrategyNotFound(preload.scope.routeStrategyID)
				}

				currentConfig, err := routeStrategyUpdateCurrentConfig(current)
				if err != nil {
					return err
				}
				before := routeStrategySummaryDetailResult(
					current,
					preload.target,
					currentConfig,
					preload.scope.includeOwner,
				)
				nextName := current.Name
				if normalized.hasName {
					nextName = normalized.name
				}
				nextDescription := current.Description
				if normalized.hasDescription {
					nextDescription = normalized.description
				}
				nextMode := string(current.Mode)
				if normalized.hasMode {
					nextMode = normalized.mode
				}
				nextStatus := string(current.Status)
				if normalized.hasStatus {
					nextStatus = normalized.status
				}
				nextConfig, nextConfigJSON, err := normalizeUpdateConfig(
					nextMode,
					current,
					currentConfig,
					normalized.normalConfig,
					normalized.hybridConfig,
				)
				if err != nil {
					return err
				}

				var bindings []normalizedCreateGroupBinding
				if normalized.hasGroupBindings {
					bindings, err = normalizeCreateBindingsFromGroups(
						nextMode,
						normalized.groupBindings,
						groups,
					)
				} else {
					latestGroupIDs := routeStrategyUpdateBindingGroupIDs(
						current.GroupBindings,
					)
					if !routeStrategyUpdateSameGroupIDs(
						prelockedGroupIDs,
						latestGroupIDs,
					) {
						return &routeStrategyBindingSnapshotChangedError{
							groupIDs: latestGroupIDs,
						}
					}
					bindings, err = normalizeUpdateCurrentBindings(nextMode, current.GroupBindings)
				}
				if err != nil {
					return err
				}

				updated, found, err := store.UpdatePublicRouteStrategy(
					txCtx,
					port.PublicRouteStrategyUpdateInput{
						ID:              current.ID,
						SystemAccountID: current.SystemAccountID,
						Name:            nextName,
						Description:     nextDescription,
						Mode:            port.PublicRouteStrategyMode(nextMode),
						Status:          port.PublicRouteStrategyStatus(nextStatus),
						ConfigJSON:      nextConfigJSON,
						Bindings:        s.createBindingInputs(bindings),
						Now:             normalized.now,
					},
				)
				if errors.Is(err, port.ErrPublicRouteStrategyDuplicateName) {
					return &NameExistsError{Name: nextName}
				}
				if err != nil {
					return err
				}
				if !found {
					return routeStrategyNotFound(preload.scope.routeStrategyID)
				}

				result = UpdateResult{
					Before: before,
					RouteStrategy: routeStrategySummaryDetailResult(
						updated,
						preload.target,
						nextConfig,
						preload.scope.includeOwner,
					),
					OwnerSystemAccountID: current.SystemAccountID,
				}
				return nil
			},
		)
		var snapshotChanged *routeStrategyBindingSnapshotChangedError
		if !errors.As(err, &snapshotChanged) {
			break
		}
		prelockedGroupIDs = snapshotChanged.groupIDs
	}
	if err != nil {
		return UpdateResult{}, err
	}

	s.invalidateRouteStrategy(
		ctx,
		RouteStrategyUpdatedReason,
		"策略路由更新后网关运行态失效失败",
	)
	return result, nil
}

func (s *Service) preloadUpdate(
	ctx context.Context,
	input UpdateInput,
) (routeStrategyUpdatePreload, error) {
	if s.createStore == nil {
		return routeStrategyUpdatePreload{}, fmt.Errorf(
			"management route strategy update store is required",
		)
	}
	scope, err := routeStrategyUpdateScope(input)
	if err != nil {
		return routeStrategyUpdatePreload{}, err
	}
	preload := routeStrategyUpdatePreload{scope: scope}
	current, found, err := s.createStore.FindPublicRouteStrategyByID(
		ctx,
		scope.routeStrategyID,
	)
	if err != nil {
		return routeStrategyUpdatePreload{}, err
	}
	preload.current = current
	preload.currentVisible = found &&
		(scope.ownerSystemAccountID == "" ||
			current.SystemAccountID == scope.ownerSystemAccountID)
	if !preload.currentVisible {
		return preload, nil
	}

	preload.target, preload.targetFound, err =
		s.createStore.FindPublicRouteStrategyTargetByID(
			ctx,
			current.SystemAccountID,
		)
	if err != nil {
		return routeStrategyUpdatePreload{}, err
	}
	if _, err := routeStrategyUpdateCurrentConfig(current); err != nil {
		return routeStrategyUpdatePreload{}, err
	}
	return preload, nil
}

func routeStrategyUpdatePrelockGroupIDs(
	normalized normalizedUpdateInput,
	current []port.PublicRouteStrategyGroupBindingSummary,
) []string {
	if normalized.hasGroupBindings {
		groupIDs := make([]string, 0, len(normalized.groupBindings))
		for _, binding := range normalized.groupBindings {
			groupIDs = append(groupIDs, binding.GroupID)
		}
		sort.Strings(groupIDs)
		return groupIDs
	}
	return routeStrategyUpdateBindingGroupIDs(current)
}

func routeStrategyUpdateBindingGroupIDs(
	bindings []port.PublicRouteStrategyGroupBindingSummary,
) []string {
	groupIDs := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		groupIDs = append(groupIDs, binding.GroupID)
	}
	sort.Strings(groupIDs)
	return groupIDs
}

func routeStrategyUpdateSameGroupIDs(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *Service) normalizeUpdatePatchInput(input UpdateInput) (normalizedUpdateInput, error) {
	if !routeStrategyHasUpdateContent(input) {
		return normalizedUpdateInput{}, validationError("请提供要修改的策略路由内容")
	}

	normalized := normalizedUpdateInput{
		hasName:          input.HasName,
		hasDescription:   input.HasDescription,
		hasMode:          input.HasMode,
		hasStatus:        input.HasStatus,
		hasGroupBindings: input.HasGroupBindings,
		groupBindings:    input.GroupBindings,
		normalConfig:     input.NormalRoutingConfig,
		hybridConfig:     input.HybridRoutingConfig,
	}
	var err error
	if input.HasName {
		normalized.name = routeStrategyTrimECMAScriptWhitespace(input.Name)
		if normalized.name == "" {
			return normalizedUpdateInput{}, validationError("策略路由名称不能为空")
		}
	}
	if input.HasDescription {
		normalized.description = normalizeCreateDescription(input.Description)
	}
	if input.HasMode {
		normalized.mode, err = normalizeCreateMode(input.Mode, true)
		if err != nil {
			return normalizedUpdateInput{}, err
		}
	}
	if input.HasStatus {
		normalized.status, err = normalizeCreateStatus(input.Status, true)
		if err != nil {
			return normalizedUpdateInput{}, err
		}
	}
	if input.HasGroupBindings {
		normalized.groupBindings, err = normalizeCreateBindingBasics(input.GroupBindings)
		if err != nil {
			return normalizedUpdateInput{}, err
		}
	}
	normalized.normalConfig, err = normalizeUpdateNormalConfigInput(
		input.NormalRoutingConfig,
	)
	if err != nil {
		return normalizedUpdateInput{}, err
	}
	normalized.hybridConfig, err = normalizeUpdateHybridConfigInput(
		input.HybridRoutingConfig,
	)
	if err != nil {
		return normalizedUpdateInput{}, err
	}
	normalized.now = s.now().UTC()
	return normalized, nil
}

func normalizeUpdateNormalConfigInput(input ConfigInput) (ConfigInput, error) {
	if !input.Set() || input.Value() == nil {
		return input, nil
	}
	raw, err := normalizeCreateNormalConfigTypes(input.Value())
	if err != nil {
		return ConfigInput{}, err
	}
	if _, err := normalizeManagementNormalRoutingConfig(raw); err != nil {
		return ConfigInput{}, validationError(err.Error())
	}
	return NewConfigInput(raw, true), nil
}

func normalizeUpdateHybridConfigInput(input ConfigInput) (ConfigInput, error) {
	if !input.Set() || input.Value() == nil {
		return input, nil
	}
	raw, err := normalizeCreateHybridConfigTypes(input.Value())
	if err != nil {
		return ConfigInput{}, err
	}
	if _, err := normalizeManagementHybridRoutingConfig(raw); err != nil {
		return ConfigInput{}, validationError(err.Error())
	}
	return NewConfigInput(raw, true), nil
}

func routeStrategyUpdateScope(
	input UpdateInput,
) (normalizedUpdateScope, error) {
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	routeStrategyID := strings.TrimSpace(input.RouteStrategyID)
	if actorSystemAccountID == "" || routeStrategyID == "" {
		return normalizedUpdateScope{}, validationError("策略路由更新作用域无效")
	}
	if input.SelfOnly || !routeStrategyAdminRole(input.ActorRole) {
		return normalizedUpdateScope{
			ownerSystemAccountID: actorSystemAccountID,
			routeStrategyID:      routeStrategyID,
		}, nil
	}
	ownerSystemAccountID := strings.TrimSpace(input.SystemAccountID)
	if ownerSystemAccountID == "all" {
		ownerSystemAccountID = ""
	}
	return normalizedUpdateScope{
		ownerSystemAccountID: ownerSystemAccountID,
		includeOwner:         true,
		routeStrategyID:      routeStrategyID,
	}, nil
}

func routeStrategyHasUpdateContent(input UpdateInput) bool {
	return input.HasName ||
		input.HasDescription ||
		input.HasMode ||
		input.HasStatus ||
		input.HasGroupBindings ||
		input.NormalRoutingConfig.Set() ||
		input.HybridRoutingConfig.Set()
}

func routeStrategyUpdateCurrentConfig(
	current port.PublicRouteStrategySummary,
) (routeStrategyRuntimeConfig, error) {
	config, err := parseRouteStrategyRuntimeConfig(current.ConfigJSON)
	if err != nil {
		return routeStrategyRuntimeConfig{}, validationError(
			"现有策略路由配置无效：" + err.Error(),
		)
	}
	switch current.Mode {
	case port.PublicRouteStrategyModeNormal:
		if config.NormalRoutingConfig == nil {
			config.NormalRoutingConfig = &NormalRoutingConfig{
				SchedulingPreference: defaultSchedulingPreference,
			}
		}
		return config, nil
	case port.PublicRouteStrategyModeHybridSmart:
		if config.HybridRoutingConfig == nil {
			return routeStrategyRuntimeConfig{}, validationError(
				"现有策略路由配置无效：混合路由配置不能为空",
			)
		}
		return config, nil
	default:
		return routeStrategyRuntimeConfig{}, nil
	}
}

func normalizeUpdateConfig(
	nextMode string,
	current port.PublicRouteStrategySummary,
	currentConfig routeStrategyRuntimeConfig,
	normalInput ConfigInput,
	hybridInput ConfigInput,
) (routeStrategyRuntimeConfig, *string, error) {
	switch nextMode {
	case "normal":
		if hybridInput.Set() && hybridInput.Value() != nil {
			return routeStrategyRuntimeConfig{}, nil, validationError(
				"普通路由不能配置混合评分规则",
			)
		}
		if normalInput.Set() {
			return normalizeCreateConfig(nextMode, normalInput, hybridInput)
		}
		if current.Mode == port.PublicRouteStrategyModeNormal {
			return routeStrategyRuntimeConfig{
				NormalRoutingConfig: currentConfig.NormalRoutingConfig,
			}, current.ConfigJSON, nil
		}
		return normalizeCreateConfig(nextMode, normalInput, hybridInput)
	case "hybrid_smart":
		if normalInput.Set() && normalInput.Value() != nil {
			return routeStrategyRuntimeConfig{}, nil, validationError(
				"只有普通路由可以配置调度偏好",
			)
		}
		if hybridInput.Set() {
			return normalizeCreateConfig(nextMode, normalInput, hybridInput)
		}
		if current.Mode == port.PublicRouteStrategyModeHybridSmart {
			if currentConfig.HybridRoutingConfig == nil {
				return routeStrategyRuntimeConfig{}, nil, validationError(
					"混合路由配置不能为空",
				)
			}
			return routeStrategyRuntimeConfig{
				HybridRoutingConfig: currentConfig.HybridRoutingConfig,
			}, current.ConfigJSON, nil
		}
		return normalizeCreateConfig(nextMode, normalInput, hybridInput)
	default:
		return normalizeCreateConfig(nextMode, normalInput, hybridInput)
	}
}

func normalizeUpdateCurrentBindings(
	mode string,
	current []port.PublicRouteStrategyGroupBindingSummary,
) ([]normalizedCreateGroupBinding, error) {
	inputs := make([]CreateGroupBindingInput, 0, len(current))
	currentByGroupID := make(
		map[string]port.PublicRouteStrategyGroupBindingSummary,
		len(current),
	)
	for _, binding := range current {
		inputs = append(inputs, CreateGroupBindingInput{
			GroupID:     binding.GroupID,
			Priority:    binding.Priority,
			PrioritySet: true,
			Weight:      binding.Weight,
			WeightSet:   true,
			Status:      string(binding.Status),
			StatusSet:   true,
		})
		currentByGroupID[binding.GroupID] = binding
	}
	basics, err := normalizeCreateBindingBasics(inputs)
	if err != nil {
		return nil, err
	}
	bindings := make([]normalizedCreateGroupBinding, 0, len(basics))
	for _, binding := range basics {
		summary := currentByGroupID[binding.GroupID]
		bindings = append(bindings, normalizedCreateGroupBinding{
			GroupID:      binding.GroupID,
			GroupName:    summary.GroupName,
			ProviderCode: summary.ProviderCode,
			Priority:     binding.Priority,
			Weight:       binding.Weight,
			Status:       binding.Status,
			GroupEnabled: summary.GroupEnabled,
		})
	}
	sort.Slice(bindings, func(left int, right int) bool {
		if bindings[left].Priority != bindings[right].Priority {
			return bindings[left].Priority < bindings[right].Priority
		}
		return bindings[left].GroupID < bindings[right].GroupID
	})
	if err := validateCreateModeBindings(mode, bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}

func routeStrategySummaryDetailResult(
	summary port.PublicRouteStrategySummary,
	target port.PublicGroupTarget,
	config routeStrategyRuntimeConfig,
	includeOwner bool,
) DetailResult {
	bindings := make([]GroupBindingSummary, 0, len(summary.GroupBindings))
	for _, binding := range summary.GroupBindings {
		bindings = append(bindings, GroupBindingSummary{
			ID:           binding.ID,
			GroupID:      binding.GroupID,
			GroupName:    binding.GroupName,
			ProviderCode: binding.ProviderCode,
			Priority:     binding.Priority,
			Weight:       binding.Weight,
			Status:       string(binding.Status),
			GroupEnabled: binding.GroupEnabled,
		})
	}
	result := DetailResult{
		ID:            summary.ID,
		Name:          summary.Name,
		Description:   summary.Description,
		Mode:          string(summary.Mode),
		Status:        string(summary.Status),
		IsDefault:     summary.IsDefault,
		GroupBindings: bindings,
		APIKeyCount:   summary.APIKeyCount,
		CreatedAt:     summary.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:     summary.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	switch result.Mode {
	case "normal":
		result.NormalRoutingConfig = config.NormalRoutingConfig
	case "hybrid_smart":
		result.HybridRoutingConfig = config.HybridRoutingConfig
	}
	if includeOwner {
		result.SystemAccountID = target.ID
		result.SystemAccountName = target.DisplayName
	}
	return result
}

func routeStrategyNotFound(routeStrategyID string) error {
	return &NotFoundError{RouteStrategyID: strings.TrimSpace(routeStrategyID)}
}
