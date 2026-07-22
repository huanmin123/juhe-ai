package managementroutestrategies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	RouteStrategyCreatedReason          = "route_strategy_created"
	routeStrategyInvalidationTimeout    = 5 * time.Second
	maxRouteStrategyCreateGroupBindings = 20
)

type RuntimeInvalidator interface {
	InvalidateGatewayRuntime(ctx context.Context, reason string) error
}

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

type NameExistsError struct {
	Name string
}

func (e *NameExistsError) Error() string {
	name := routeStrategyTrimECMAScriptWhitespace(e.Name)
	if name == "" {
		return "策略路由名称已存在"
	}
	return "策略路由名称已存在：" + name
}

func ValidationMessage(err error) (string, bool) {
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		return "", false
	}
	if strings.TrimSpace(validationErr.Message) == "" {
		return "策略路由参数无效", true
	}
	return validationErr.Message, true
}

func NameExistsMessage(err error) (string, bool) {
	var existsErr *NameExistsError
	if !errors.As(err, &existsErr) {
		return "", false
	}
	return existsErr.Error(), true
}

type ConfigInput struct {
	value any
	set   bool
}

func NewConfigInput(value any, set bool) ConfigInput {
	return ConfigInput{value: value, set: set}
}

func (i ConfigInput) Set() bool {
	return i.set
}

func (i ConfigInput) Value() any {
	return i.value
}

type CreateGroupBindingInput struct {
	GroupID     string
	Priority    int
	PrioritySet bool
	Weight      int
	WeightSet   bool
	Status      string
	StatusSet   bool
}

type CreateInput struct {
	SystemAccountID            string
	IncludeSystemAccountFields bool
	Name                       string
	Description                *string
	Mode                       string
	ModeSet                    bool
	Status                     string
	StatusSet                  bool
	GroupBindings              []CreateGroupBindingInput
	NormalRoutingConfig        ConfigInput
	HybridRoutingConfig        ConfigInput
}

type normalizedCreateGroupBinding struct {
	GroupID      string
	GroupName    string
	ProviderCode string
	Priority     int
	Weight       int
	Status       string
	GroupEnabled bool
}

type normalizedCreateInput struct {
	systemAccountID string
	includeOwner    bool
	name            string
	description     *string
	mode            string
	status          string
	config          routeStrategyRuntimeConfig
	configJSON      *string
	bindings        []CreateGroupBindingInput
	now             time.Time
}

func (s *Service) Create(ctx context.Context, input CreateInput) (DetailResult, error) {
	if s.createStore == nil {
		return DetailResult{}, fmt.Errorf("management route strategy create store is required")
	}
	if s.transactor == nil {
		return DetailResult{}, fmt.Errorf("management route strategy transactor is required")
	}
	normalized, err := s.normalizeCreateInput(input)
	if err != nil {
		return DetailResult{}, err
	}

	var result DetailResult
	err = s.transactor.PublicRouteStrategyInTx(
		ctx,
		func(txCtx context.Context, store port.PublicRouteStrategyStore) error {
			target, found, err := store.FindPublicRouteStrategyTargetByID(
				txCtx,
				normalized.systemAccountID,
			)
			if err != nil {
				return err
			}
			if !found {
				return validationError("目标系统账户不存在")
			}
			bindings, err := normalizeCreateBindingsWithStore(
				txCtx,
				store,
				normalized.systemAccountID,
				normalized.mode,
				normalized.bindings,
			)
			if err != nil {
				return err
			}
			created, err := store.CreatePublicRouteStrategy(
				txCtx,
				port.PublicRouteStrategyCreateInput{
					ID:              s.newID("rts"),
					SystemAccountID: normalized.systemAccountID,
					Name:            normalized.name,
					Description:     normalized.description,
					Mode:            port.PublicRouteStrategyMode(normalized.mode),
					Status:          port.PublicRouteStrategyStatus(normalized.status),
					ConfigJSON:      normalized.configJSON,
					Bindings:        s.createBindingInputs(bindings),
					Now:             normalized.now,
				},
			)
			if errors.Is(err, port.ErrPublicRouteStrategyDuplicateName) {
				return &NameExistsError{Name: normalized.name}
			}
			if err != nil {
				return err
			}
			result = createdDetailResult(
				created,
				target,
				normalized.config,
				normalized.includeOwner,
			)
			return nil
		},
	)
	if err != nil {
		return DetailResult{}, err
	}

	s.invalidateCreatedRouteStrategy(ctx)
	return result, nil
}

func (s *Service) normalizeCreateInput(input CreateInput) (normalizedCreateInput, error) {
	systemAccountID := routeStrategyTrimECMAScriptWhitespace(input.SystemAccountID)
	if systemAccountID == "" {
		return normalizedCreateInput{}, validationError("目标系统账户不能为空")
	}
	name := routeStrategyTrimECMAScriptWhitespace(input.Name)
	if name == "" {
		return normalizedCreateInput{}, validationError("策略路由名称不能为空")
	}
	mode, err := normalizeCreateMode(input.Mode, input.ModeSet)
	if err != nil {
		return normalizedCreateInput{}, err
	}
	status, err := normalizeCreateStatus(input.Status, input.StatusSet)
	if err != nil {
		return normalizedCreateInput{}, err
	}
	config, configJSON, err := normalizeCreateConfig(
		mode,
		input.NormalRoutingConfig,
		input.HybridRoutingConfig,
	)
	if err != nil {
		return normalizedCreateInput{}, err
	}
	bindings, err := normalizeCreateBindingBasics(input.GroupBindings)
	if err != nil {
		return normalizedCreateInput{}, err
	}
	return normalizedCreateInput{
		systemAccountID: systemAccountID,
		includeOwner:    input.IncludeSystemAccountFields,
		name:            name,
		description:     normalizeCreateDescription(input.Description),
		mode:            mode,
		status:          status,
		config:          config,
		configJSON:      configJSON,
		bindings:        bindings,
		now:             s.now().UTC(),
	}, nil
}

func normalizeCreateMode(value string, set bool) (string, error) {
	if value == "" && !set {
		return "normal", nil
	}
	switch value {
	case "normal", "hybrid_smart", "weighted", "failover", "round_robin":
		return value, nil
	default:
		return "", validationError("路由策略模式无效")
	}
}

func normalizeCreateStatus(value string, set bool) (string, error) {
	if value == "" && !set {
		return "active", nil
	}
	switch value {
	case "active", "disabled":
		return value, nil
	default:
		return "", validationError("策略路由状态无效")
	}
}

func normalizeCreateDescription(value *string) *string {
	if value == nil {
		return nil
	}
	text := routeStrategyTrimECMAScriptWhitespace(*value)
	if text == "" {
		return nil
	}
	return &text
}

func normalizeCreateBindingBasics(
	inputs []CreateGroupBindingInput,
) ([]CreateGroupBindingInput, error) {
	if len(inputs) == 0 {
		return nil, validationError("策略路由至少需要绑定一个分组")
	}
	if len(inputs) > maxRouteStrategyCreateGroupBindings {
		return nil, validationError(
			fmt.Sprintf("策略路由最多绑定 %d 个分组", maxRouteStrategyCreateGroupBindings),
		)
	}

	groupIDs := make(map[string]struct{}, len(inputs))
	activePriorities := make(map[int]struct{}, len(inputs))
	bindings := make([]CreateGroupBindingInput, 0, len(inputs))
	for index, input := range inputs {
		groupID := routeStrategyTrimECMAScriptWhitespace(input.GroupID)
		if groupID == "" {
			return nil, validationError("策略路由分组无效")
		}
		if _, exists := groupIDs[groupID]; exists {
			return nil, validationError("策略路由绑定分组不能重复")
		}
		groupIDs[groupID] = struct{}{}

		priority := input.Priority
		if priority == 0 && !input.PrioritySet {
			priority = index + 1
		}
		if priority <= 0 {
			return nil, validationError("策略路由分组优先级必须是大于 0 的整数")
		}
		weight := input.Weight
		if weight == 0 && !input.WeightSet {
			weight = 1
		}
		if weight < 1 || weight > 100 {
			return nil, validationError("策略路由分组权重必须是 1-100")
		}
		status := input.Status
		if status == "" && !input.StatusSet {
			status = "active"
		}
		if status != "active" && status != "disabled" {
			return nil, validationError("策略路由分组绑定状态无效")
		}
		if status == "active" {
			if _, exists := activePriorities[priority]; exists {
				return nil, validationError("策略路由启用分组优先级不能重复")
			}
			activePriorities[priority] = struct{}{}
		}
		bindings = append(bindings, CreateGroupBindingInput{
			GroupID:  groupID,
			Priority: priority,
			Weight:   weight,
			Status:   status,
		})
	}
	if len(activePriorities) == 0 {
		return nil, validationError("策略路由至少需要一个启用分组")
	}
	return bindings, nil
}

func normalizeCreateBindingsWithStore(
	ctx context.Context,
	store port.PublicRouteStrategyStore,
	systemAccountID string,
	mode string,
	inputs []CreateGroupBindingInput,
) ([]normalizedCreateGroupBinding, error) {
	groupIDs := make([]string, 0, len(inputs))
	for _, input := range inputs {
		groupIDs = append(groupIDs, input.GroupID)
	}
	groups, err := store.FindPublicRouteStrategyBindableGroups(ctx, systemAccountID, groupIDs)
	if err != nil {
		return nil, err
	}
	return normalizeCreateBindingsFromGroups(mode, inputs, groups)
}

func normalizeCreateBindingsFromGroups(
	mode string,
	inputs []CreateGroupBindingInput,
	groups []port.PublicRouteStrategyBindableGroup,
) ([]normalizedCreateGroupBinding, error) {
	groupsByID := make(map[string]port.PublicRouteStrategyBindableGroup, len(groups))
	for _, group := range groups {
		groupsByID[group.ID] = group
	}

	bindings := make([]normalizedCreateGroupBinding, 0, len(inputs))
	for _, input := range inputs {
		group, found := groupsByID[input.GroupID]
		if !found {
			return nil, validationError(
				"策略路由只能绑定自己的分组或有效授权给自己的分组",
			)
		}
		if input.Status == "active" && !group.Enabled {
			name := strings.TrimSpace(group.Name)
			if name == "" {
				name = input.GroupID
			}
			return nil, validationError("策略路由不能启用已停用分组：" + name)
		}
		bindings = append(bindings, normalizedCreateGroupBinding{
			GroupID:      input.GroupID,
			GroupName:    group.Name,
			ProviderCode: group.ProviderCode,
			Priority:     input.Priority,
			Weight:       input.Weight,
			Status:       input.Status,
			GroupEnabled: group.Enabled,
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

func validateCreateModeBindings(
	mode string,
	bindings []normalizedCreateGroupBinding,
) error {
	activeCount := 0
	for _, binding := range bindings {
		if binding.Status == "active" {
			activeCount++
		}
	}
	switch mode {
	case "normal":
		if len(bindings) != 1 || activeCount != 1 {
			return validationError("普通路由只能绑定一个启用分组")
		}
	case "failover":
		if len(bindings) < 2 {
			return validationError("故障回退路由需要一个主用分组和至少一个备用分组")
		}
		if bindings[0].Status != "active" {
			return validationError("故障回退路由的主用分组必须启用")
		}
		for _, binding := range bindings[1:] {
			if binding.Status == "active" {
				return nil
			}
		}
		return validationError("故障回退路由至少需要一个启用备用分组")
	}
	return nil
}

func normalizeCreateConfig(
	mode string,
	normalInput ConfigInput,
	hybridInput ConfigInput,
) (routeStrategyRuntimeConfig, *string, error) {
	switch mode {
	case "normal":
		if hybridInput.Set() && hybridInput.Value() != nil {
			return routeStrategyRuntimeConfig{}, nil, validationError(
				"普通路由不能配置混合评分规则",
			)
		}
		var raw any
		if normalInput.Set() {
			raw = normalInput.Value()
		}
		raw, err := normalizeCreateNormalConfigTypes(raw)
		if err != nil {
			return routeStrategyRuntimeConfig{}, nil, err
		}
		normal, err := normalizeManagementNormalRoutingConfig(raw)
		if err != nil {
			return routeStrategyRuntimeConfig{}, nil, validationError(err.Error())
		}
		config := routeStrategyRuntimeConfig{NormalRoutingConfig: normal}
		if normal.SchedulingPreference == defaultSchedulingPreference {
			return config, nil, nil
		}
		configJSON, err := marshalCreateConfig(map[string]any{
			"normalRoutingConfig": normal,
		})
		return config, configJSON, err
	case "hybrid_smart":
		if normalInput.Set() && normalInput.Value() != nil {
			return routeStrategyRuntimeConfig{}, nil, validationError(
				"只有普通路由可以配置调度偏好",
			)
		}
		var raw any
		if hybridInput.Set() {
			raw = hybridInput.Value()
		}
		raw, err := normalizeCreateHybridConfigTypes(raw)
		if err != nil {
			return routeStrategyRuntimeConfig{}, nil, err
		}
		hybrid, err := normalizeManagementHybridRoutingConfig(raw)
		if err != nil {
			return routeStrategyRuntimeConfig{}, nil, validationError(err.Error())
		}
		config := routeStrategyRuntimeConfig{HybridRoutingConfig: hybrid}
		configJSON, err := marshalCreateConfig(map[string]any{
			"hybridRoutingConfig": hybrid,
		})
		return config, configJSON, err
	default:
		if normalInput.Set() && normalInput.Value() != nil {
			return routeStrategyRuntimeConfig{}, nil, validationError(
				"只有普通路由可以配置调度偏好",
			)
		}
		if hybridInput.Set() && hybridInput.Value() != nil {
			return routeStrategyRuntimeConfig{}, nil, validationError(
				"只有混合智能路由可以配置混合评分规则",
			)
		}
		return routeStrategyRuntimeConfig{}, nil, nil
	}
}

func normalizeCreateNormalConfigTypes(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	record, ok := value.(map[string]any)
	if !ok || record == nil {
		return nil, validationError("普通路由调度配置无效")
	}
	if err := rejectCreateConfigKeys(record, "schedulingPreference", "speedFirstConfig"); err != nil {
		return nil, err
	}
	output := cloneCreateConfigRecord(record)
	if err := validateCreateConfigStringFields(record, "schedulingPreference"); err != nil {
		return nil, err
	}
	speed, exists := record["speedFirstConfig"]
	if !exists {
		return output, nil
	}
	speedRecord, ok := speed.(map[string]any)
	if !ok || speedRecord == nil {
		return nil, validationError("速度优先配置无效")
	}
	if err := rejectCreateConfigKeys(
		speedRecord,
		"firstByteThresholdMs",
		"slowTriggerCount",
		"slowWindowSeconds",
		"recoverySuccessCount",
		"probeIntervalSeconds",
		"degradedTtlSeconds",
		"maxFirstByteRetriesPerRequest",
	); err != nil {
		return nil, err
	}
	normalizedSpeed := cloneCreateConfigRecord(speedRecord)
	if err := normalizeCreateConfigNumberFields(
		normalizedSpeed,
		speedRecord,
		"firstByteThresholdMs",
		"slowTriggerCount",
		"slowWindowSeconds",
		"recoverySuccessCount",
		"probeIntervalSeconds",
		"degradedTtlSeconds",
		"maxFirstByteRetriesPerRequest",
	); err != nil {
		return nil, err
	}
	output["speedFirstConfig"] = normalizedSpeed
	return output, nil
}

func normalizeCreateHybridConfigTypes(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	record, ok := value.(map[string]any)
	if !ok || record == nil {
		return nil, validationError("混合路由配置不能为空")
	}
	if err := rejectCreateConfigKeys(
		record,
		"scoringGroupId",
		"scoringModel",
		"scoringContextMode",
		"qualityPreference",
		"scoringTimeoutMs",
		"scoringFallbackMaxLevel",
		"scoringCacheEnabled",
		"scoringCacheTtlSeconds",
		"cacheAffinityEnabled",
		"affinityTtlSeconds",
		"switchMinLevelDelta",
		"downgradeConsecutiveLowCount",
		"levelRoutes",
		"qualityInspection",
	); err != nil {
		return nil, err
	}
	output := cloneCreateConfigRecord(record)
	if err := validateCreateConfigStringFields(
		record,
		"scoringGroupId",
		"scoringModel",
		"scoringContextMode",
		"qualityPreference",
	); err != nil {
		return nil, err
	}
	if err := validateCreateConfigBooleanFields(
		record,
		"scoringCacheEnabled",
		"cacheAffinityEnabled",
	); err != nil {
		return nil, err
	}
	if err := normalizeCreateConfigNumberFields(
		output,
		record,
		"scoringTimeoutMs",
		"scoringFallbackMaxLevel",
		"scoringCacheTtlSeconds",
		"affinityTtlSeconds",
		"switchMinLevelDelta",
		"downgradeConsecutiveLowCount",
	); err != nil {
		return nil, err
	}

	if routesValue, exists := record["levelRoutes"]; exists {
		routes, ok := routesValue.([]any)
		if !ok {
			return nil, validationError("混合路由等级范围不能为空")
		}
		normalizedRoutes := make([]any, 0, len(routes))
		for _, route := range routes {
			routeRecord, ok := route.(map[string]any)
			if !ok || routeRecord == nil {
				return nil, validationError("混合路由等级范围无效")
			}
			if err := rejectCreateConfigKeys(
				routeRecord,
				"minLevel",
				"maxLevel",
				"targetModel",
				"enabled",
			); err != nil {
				return nil, err
			}
			normalizedRoute := cloneCreateConfigRecord(routeRecord)
			if err := normalizeCreateConfigNumberFields(
				normalizedRoute,
				routeRecord,
				"minLevel",
				"maxLevel",
			); err != nil {
				return nil, err
			}
			if err := validateCreateConfigStringFields(routeRecord, "targetModel"); err != nil {
				return nil, err
			}
			if err := validateCreateConfigBooleanFields(routeRecord, "enabled"); err != nil {
				return nil, err
			}
			normalizedRoutes = append(normalizedRoutes, normalizedRoute)
		}
		output["levelRoutes"] = normalizedRoutes
	}

	if qualityValue, exists := record["qualityInspection"]; exists {
		quality, ok := qualityValue.(map[string]any)
		if !ok || quality == nil {
			return nil, validationError("混合路由质量评分配置无效")
		}
		if err := rejectCreateConfigKeys(
			quality,
			"enabled",
			"scoringGroupId",
			"scoringModel",
			"triggerMode",
			"maxTriggerLevel",
			"maxRetries",
			"failureAction",
			"unavailableAction",
		); err != nil {
			return nil, err
		}
		normalizedQuality := cloneCreateConfigRecord(quality)
		if err := validateCreateConfigBooleanFields(quality, "enabled"); err != nil {
			return nil, err
		}
		if err := validateCreateConfigStringFields(
			quality,
			"scoringGroupId",
			"scoringModel",
			"triggerMode",
			"failureAction",
			"unavailableAction",
		); err != nil {
			return nil, err
		}
		if err := normalizeCreateConfigNumberFields(
			normalizedQuality,
			quality,
			"maxTriggerLevel",
			"maxRetries",
		); err != nil {
			return nil, err
		}
		output["qualityInspection"] = normalizedQuality
	}
	return output, nil
}

func cloneCreateConfigRecord(record map[string]any) map[string]any {
	output := make(map[string]any, len(record))
	for key, value := range record {
		output[key] = value
	}
	return output
}

func validateCreateConfigStringFields(record map[string]any, fields ...string) error {
	for _, field := range fields {
		value, exists := record[field]
		if !exists {
			continue
		}
		if _, ok := value.(string); !ok {
			return validationError("策略路由配置字段类型无效：" + field)
		}
	}
	return nil
}

func validateCreateConfigBooleanFields(record map[string]any, fields ...string) error {
	for _, field := range fields {
		value, exists := record[field]
		if !exists {
			continue
		}
		if _, ok := value.(bool); !ok {
			return validationError("策略路由配置字段类型无效：" + field)
		}
	}
	return nil
}

func normalizeCreateConfigNumberFields(
	output map[string]any,
	record map[string]any,
	fields ...string,
) error {
	for _, field := range fields {
		value, exists := record[field]
		if !exists {
			continue
		}
		normalized, ok := normalizeCreateConfigNumber(value)
		if !ok {
			return validationError("策略路由配置字段类型无效：" + field)
		}
		output[field] = normalized
	}
	return nil
}

func normalizeCreateConfigNumber(value any) (any, bool) {
	if number, ok := value.(json.Number); ok {
		return number, true
	}
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return nil, false
	}
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return json.Number(strconv.FormatInt(reflected.Int(), 10)), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return json.Number(strconv.FormatUint(reflected.Uint(), 10)), true
	case reflect.Float32, reflect.Float64:
		return reflected.Convert(reflect.TypeOf(float64(0))).Float(), true
	default:
		return nil, false
	}
}

func rejectCreateConfigKeys(record map[string]any, allowed ...string) error {
	allowedKeys := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedKeys[key] = struct{}{}
	}
	unknown := make([]string, 0)
	for key := range record {
		if _, exists := allowedKeys[key]; !exists {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return validationError("策略路由配置包含未知字段：" + unknown[0])
}

func marshalCreateConfig(value map[string]any) (*string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, validationError("策略路由配置无效")
	}
	text := string(encoded)
	return &text, nil
}

func (s *Service) createBindingInputs(
	bindings []normalizedCreateGroupBinding,
) []port.PublicRouteStrategyGroupBindingCreateInput {
	inputs := make([]port.PublicRouteStrategyGroupBindingCreateInput, 0, len(bindings))
	for _, binding := range bindings {
		inputs = append(inputs, port.PublicRouteStrategyGroupBindingCreateInput{
			ID:       s.newID("rsg"),
			GroupID:  binding.GroupID,
			Priority: binding.Priority,
			Weight:   binding.Weight,
			Status:   port.PublicRouteStrategyStatus(binding.Status),
		})
	}
	return inputs
}

func createdDetailResult(
	created port.PublicRouteStrategySummary,
	target port.PublicGroupTarget,
	config routeStrategyRuntimeConfig,
	includeOwner bool,
) DetailResult {
	bindings := make([]GroupBindingSummary, 0, len(created.GroupBindings))
	for _, binding := range created.GroupBindings {
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
		ID:            created.ID,
		Name:          created.Name,
		Description:   created.Description,
		Mode:          string(created.Mode),
		Status:        string(created.Status),
		IsDefault:     false,
		GroupBindings: bindings,
		APIKeyCount:   0,
		CreatedAt:     created.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:     created.UpdatedAt.UTC().Format(time.RFC3339Nano),
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

func (s *Service) invalidateCreatedRouteStrategy(ctx context.Context) {
	s.invalidateRouteStrategy(
		ctx,
		RouteStrategyCreatedReason,
		"策略路由创建后网关运行态失效失败",
	)
}

func (s *Service) invalidateRouteStrategy(
	ctx context.Context,
	reason string,
	failureMessage string,
) {
	if s.invalidator == nil {
		return
	}
	invalidationCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		routeStrategyInvalidationTimeout,
	)
	defer cancel()
	if err := s.invalidator.InvalidateGatewayRuntime(
		invalidationCtx,
		reason,
	); err != nil {
		s.logger.Warn(
			failureMessage,
			slog.String(
				"event",
				"management_route_strategy_gateway_runtime_invalidation_failed",
			),
			slog.String("reason", reason),
			slog.Any("error", err),
		)
	}
}

func validationError(message string) error {
	return &ValidationError{Message: message}
}
