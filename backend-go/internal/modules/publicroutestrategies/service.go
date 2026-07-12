package publicroutestrategies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	ModeNormal      = "normal"
	ModeHybridSmart = "hybrid_smart"
	ModeWeighted    = "weighted"
	ModeFailover    = "failover"
	ModeRoundRobin  = "round_robin"

	StatusActive   = "active"
	StatusDisabled = "disabled"

	defaultSchedulingPreference          = "cost_first"
	schedulingPreferenceSpeedFirst       = "speed_first"
	defaultFirstByteThresholdMs          = 30000
	defaultSlowTriggerCount              = 3
	defaultSlowWindowSeconds             = 120
	defaultRecoverySuccessCount          = 3
	defaultProbeIntervalSeconds          = 30
	defaultDegradedTTLSeconds            = 300
	defaultMaxFirstByteRetriesPerRequest = 2
	maxRouteStrategyGroupBindings        = 20
)

var (
	ErrTargetNotFound             = errors.New("public route strategy target not found")
	ErrTargetDisabled             = errors.New("public route strategy target disabled")
	ErrRouteStrategyNotFound      = errors.New("public route strategy not found")
	ErrDuplicateRouteStrategyName = errors.New("public route strategy duplicate name")
	ErrDefaultRouteStrategyDelete = errors.New("public route strategy default delete")
	ErrRouteStrategyAPIKeysInUse  = errors.New("public route strategy api keys in use")
	ErrGroupBoundary              = errors.New("public route strategy group boundary")
	ErrInvalidBinding             = errors.New("public route strategy invalid binding")
	ErrInvalidConfig              = errors.New("public route strategy invalid config")
)

type Service struct {
	store      port.PublicRouteStrategyStore
	transactor port.PublicRouteStrategyTransactor
	now        func() time.Time
	newID      func(prefix string) string
}

type Options struct {
	Store      port.PublicRouteStrategyStore
	Transactor port.PublicRouteStrategyTransactor
	Now        func() time.Time
	NewID      func(prefix string) string
}

type Target struct {
	Username        string `json:"username"`
	DisplayName     string `json:"displayName"`
	SystemAccountID string `json:"systemAccountId"`
	Created         bool   `json:"created"`
}

type SpeedFirstConfig struct {
	FirstByteThresholdMs          int `json:"firstByteThresholdMs"`
	SlowTriggerCount              int `json:"slowTriggerCount"`
	SlowWindowSeconds             int `json:"slowWindowSeconds"`
	RecoverySuccessCount          int `json:"recoverySuccessCount"`
	ProbeIntervalSeconds          int `json:"probeIntervalSeconds"`
	DegradedTTLSeconds            int `json:"degradedTtlSeconds"`
	MaxFirstByteRetriesPerRequest int `json:"maxFirstByteRetriesPerRequest"`
}

type NormalRoutingConfig struct {
	SchedulingPreference string            `json:"schedulingPreference"`
	SpeedFirstConfig     *SpeedFirstConfig `json:"speedFirstConfig,omitempty"`
}

type GroupBindingSummary struct {
	ID           string `json:"id"`
	GroupID      string `json:"groupId"`
	GroupName    string `json:"groupName,omitempty"`
	ProviderCode string `json:"providerCode,omitempty"`
	Priority     int    `json:"priority"`
	Weight       int    `json:"weight"`
	Status       string `json:"status"`
	GroupEnabled bool   `json:"groupEnabled"`
}

type RouteStrategySummary struct {
	ID                  string                `json:"id"`
	Name                string                `json:"name"`
	Description         *string               `json:"description,omitempty"`
	Mode                string                `json:"mode"`
	Status              string                `json:"status"`
	IsDefault           bool                  `json:"isDefault"`
	NormalRoutingConfig *NormalRoutingConfig  `json:"normalRoutingConfig,omitempty"`
	HybridRoutingConfig map[string]any        `json:"hybridRoutingConfig,omitempty"`
	GroupBindings       []GroupBindingSummary `json:"groupBindings"`
	APIKeyCount         int64                 `json:"apiKeyCount"`
	CreatedAt           string                `json:"createdAt"`
	UpdatedAt           string                `json:"updatedAt"`
}

type RouteStrategyResponse struct {
	Source        string                `json:"source"`
	GeneratedAt   string                `json:"generatedAt"`
	Action        string                `json:"action"`
	Target        Target                `json:"target"`
	RouteStrategy *RouteStrategySummary `json:"routeStrategy"`
}

type RouteStrategyListResponse struct {
	Source         string                 `json:"source"`
	GeneratedAt    string                 `json:"generatedAt"`
	Target         Target                 `json:"target"`
	Page           int                    `json:"page"`
	PageSize       int                    `json:"pageSize"`
	PageUpperBound int                    `json:"pageUpperBound"`
	HasMore        bool                   `json:"hasMore"`
	Items          []RouteStrategySummary `json:"items"`
}

type ConfigValue struct {
	value any
	set   bool
}

type OptionalString struct {
	value *string
	set   bool
}

type ListInput struct {
	TargetUsername string
	Keyword        string
	Mode           string
	Status         string
	Page           int
	PageSize       int
}

type AddInput struct {
	TargetUsername      string
	Name                string
	Description         *string
	Mode                string
	Status              string
	GroupBindings       []GroupBindingInput
	NormalRoutingConfig ConfigValue
	HybridRoutingConfig ConfigValue
}

type UpdateInput struct {
	TargetUsername      *string
	RouteStrategyID     string
	Name                *string
	Description         OptionalString
	Mode                *string
	Status              *string
	GroupBindings       OptionalGroupBindings
	NormalRoutingConfig ConfigValue
	HybridRoutingConfig ConfigValue
}

type DeleteInput struct {
	TargetUsername  *string
	RouteStrategyID string
}

type GroupBindingInput struct {
	GroupID  string
	Priority int
	Weight   int
	Status   string
}

type OptionalGroupBindings struct {
	value []GroupBindingInput
	set   bool
}

type routeStrategyConfig struct {
	NormalRoutingConfig *NormalRoutingConfig `json:"normalRoutingConfig,omitempty"`
	HybridRoutingConfig map[string]any       `json:"hybridRoutingConfig,omitempty"`
}

type normalizedBinding struct {
	GroupID      string
	GroupName    string
	ProviderCode string
	Priority     int
	Weight       int
	Status       string
	GroupEnabled bool
}

func NewService(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newID := opts.NewID
	if newID == nil {
		newID = func(prefix string) string {
			return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	return &Service{
		store:      opts.Store,
		transactor: opts.Transactor,
		now:        now,
		newID:      newID,
	}
}

func NewConfigValue(value any, set bool) ConfigValue {
	return ConfigValue{value: value, set: set}
}

func (v ConfigValue) Set() bool {
	return v.set
}

func (v ConfigValue) Value() any {
	return v.value
}

func NewOptionalString(value *string, set bool) OptionalString {
	return OptionalString{value: value, set: set}
}

func (s OptionalString) Set() bool {
	return s.set
}

func (s OptionalString) Value() *string {
	return s.value
}

func NewOptionalGroupBindings(value []GroupBindingInput, set bool) OptionalGroupBindings {
	return OptionalGroupBindings{value: value, set: set}
}

func (b OptionalGroupBindings) Set() bool {
	return b.set
}

func (b OptionalGroupBindings) Value() []GroupBindingInput {
	return b.value
}

func (s *Service) List(ctx context.Context, input ListInput) (RouteStrategyListResponse, error) {
	target, err := s.requireTarget(ctx, input.TargetUsername)
	if err != nil {
		return RouteStrategyListResponse{}, err
	}
	page, err := s.store.ListPublicRouteStrategies(ctx, port.PublicRouteStrategyListInput{
		SystemAccountID: target.ID,
		Keyword:         strings.TrimSpace(input.Keyword),
		Mode:            normalizeListMode(input.Mode),
		Status:          normalizeListStatus(input.Status),
		Page:            input.Page,
		PageSize:        input.PageSize,
	})
	if err != nil {
		return RouteStrategyListResponse{}, err
	}
	items := make([]RouteStrategySummary, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, routeStrategySummary(item))
	}
	return RouteStrategyListResponse{
		Source:         "stats",
		GeneratedAt:    s.generatedAt(),
		Target:         publicTarget(target),
		Page:           page.Page,
		PageSize:       page.PageSize,
		PageUpperBound: page.PageUpperBound,
		HasMore:        page.HasMore,
		Items:          items,
	}, nil
}

func (s *Service) Add(ctx context.Context, input AddInput) (RouteStrategyResponse, error) {
	var response RouteStrategyResponse
	err := s.inTx(ctx, func(ctx context.Context, store port.PublicRouteStrategyStore) error {
		target, err := s.requireTargetWithStore(ctx, store, input.TargetUsername)
		if err != nil {
			return err
		}
		mode := normalizeMode(input.Mode)
		status := normalizeStatus(input.Status, StatusActive)
		configJSON, err := configJSONForCreate(mode, input.NormalRoutingConfig, input.HybridRoutingConfig)
		if err != nil {
			return err
		}
		bindings, err := s.normalizeBindings(ctx, store, target.ID, mode, input.GroupBindings)
		if err != nil {
			return err
		}
		created, err := store.CreatePublicRouteStrategy(ctx, port.PublicRouteStrategyCreateInput{
			ID:              s.newID("rts"),
			SystemAccountID: target.ID,
			Name:            strings.TrimSpace(input.Name),
			Description:     input.Description,
			Mode:            port.PublicRouteStrategyMode(mode),
			Status:          port.PublicRouteStrategyStatus(status),
			ConfigJSON:      configJSON,
			Bindings:        s.bindingCreateInputs(bindings),
			Now:             s.now().UTC(),
		})
		if errors.Is(err, port.ErrPublicRouteStrategyDuplicateName) {
			return fmt.Errorf("%w: %s", ErrDuplicateRouteStrategyName, strings.TrimSpace(input.Name))
		}
		if err != nil {
			return err
		}
		response = routeStrategyResponse("created", target, created, s.generatedAt())
		return nil
	})
	return response, err
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (RouteStrategyResponse, error) {
	var response RouteStrategyResponse
	err := s.inTx(ctx, func(ctx context.Context, store port.PublicRouteStrategyStore) error {
		current, target, err := s.routeStrategyAndTargetForWrite(ctx, store, input.RouteStrategyID, input.TargetUsername)
		if err != nil {
			return err
		}
		currentConfig := parseConfig(current.ConfigJSON)
		nextMode := string(current.Mode)
		if input.Mode != nil {
			nextMode = normalizeMode(*input.Mode)
		}
		nextStatus := string(current.Status)
		if input.Status != nil {
			nextStatus = normalizeStatus(*input.Status, string(current.Status))
		}
		configJSON, err := configJSONForUpdate(nextMode, current.Mode, currentConfig, input.NormalRoutingConfig, input.HybridRoutingConfig)
		if err != nil {
			return err
		}
		var bindings []normalizedBinding
		if input.GroupBindings.Set() {
			bindings, err = s.normalizeBindings(ctx, store, current.SystemAccountID, nextMode, input.GroupBindings.Value())
			if err != nil {
				return err
			}
		} else {
			bindings = bindingsFromSummary(current.GroupBindings)
			if err := validateModeBindings(nextMode, bindings); err != nil {
				return err
			}
		}
		next := current
		if input.Name != nil {
			next.Name = strings.TrimSpace(*input.Name)
		}
		if input.Description.Set() {
			next.Description = input.Description.Value()
		}
		next.Mode = port.PublicRouteStrategyMode(nextMode)
		next.Status = port.PublicRouteStrategyStatus(nextStatus)
		next.ConfigJSON = configJSON
		updated, ok, err := store.UpdatePublicRouteStrategy(ctx, port.PublicRouteStrategyUpdateInput{
			ID:              current.ID,
			SystemAccountID: current.SystemAccountID,
			Name:            next.Name,
			Description:     next.Description,
			Mode:            next.Mode,
			Status:          next.Status,
			ConfigJSON:      next.ConfigJSON,
			Bindings:        s.bindingCreateInputs(bindings),
			Now:             s.now().UTC(),
		})
		if errors.Is(err, port.ErrPublicRouteStrategyDuplicateName) {
			return fmt.Errorf("%w: %s", ErrDuplicateRouteStrategyName, next.Name)
		}
		if err != nil {
			return err
		}
		if !ok {
			return ErrRouteStrategyNotFound
		}
		response = routeStrategyResponse("updated", target, updated, s.generatedAt())
		return nil
	})
	return response, err
}

func (s *Service) Delete(ctx context.Context, input DeleteInput) (RouteStrategyResponse, error) {
	var response RouteStrategyResponse
	err := s.inTx(ctx, func(ctx context.Context, store port.PublicRouteStrategyStore) error {
		current, target, err := s.routeStrategyAndTargetForWrite(ctx, store, input.RouteStrategyID, input.TargetUsername)
		if err != nil {
			return err
		}
		if current.IsDefault {
			return ErrDefaultRouteStrategyDelete
		}
		count, err := store.PublicRouteStrategyAPIKeyCount(ctx, current.ID, current.SystemAccountID)
		if err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("%w: %d", ErrRouteStrategyAPIKeysInUse, count)
		}
		deleted, err := store.DeletePublicRouteStrategy(ctx, current.ID, current.SystemAccountID)
		if err != nil {
			return err
		}
		if !deleted {
			return ErrRouteStrategyNotFound
		}
		response = routeStrategyResponse("deleted", target, current, s.generatedAt())
		return nil
	})
	return response, err
}

func (s *Service) requireTarget(ctx context.Context, username string) (port.PublicGroupTarget, error) {
	return s.requireTargetWithStore(ctx, s.store, username)
}

func (s *Service) requireTargetWithStore(ctx context.Context, store port.PublicRouteStrategyStore, username string) (port.PublicGroupTarget, error) {
	username = strings.TrimSpace(username)
	target, ok, err := store.FindPublicRouteStrategyTargetByUsername(ctx, username)
	if err != nil {
		return port.PublicGroupTarget{}, err
	}
	if !ok {
		return port.PublicGroupTarget{}, fmt.Errorf("%w: %s", ErrTargetNotFound, username)
	}
	if err := assertTargetActive(target); err != nil {
		return port.PublicGroupTarget{}, err
	}
	return target, nil
}

func (s *Service) routeStrategyAndTargetForWrite(ctx context.Context, store port.PublicRouteStrategyStore, routeStrategyID string, targetUsername *string) (port.PublicRouteStrategySummary, port.PublicGroupTarget, error) {
	routeStrategy, ok, err := store.FindPublicRouteStrategyByID(ctx, strings.TrimSpace(routeStrategyID))
	if err != nil {
		return port.PublicRouteStrategySummary{}, port.PublicGroupTarget{}, err
	}
	if !ok {
		return port.PublicRouteStrategySummary{}, port.PublicGroupTarget{}, ErrRouteStrategyNotFound
	}
	target, ok, err := routeStrategyTargetByIDOrUsername(ctx, store, routeStrategy.SystemAccountID, targetUsername)
	if err != nil {
		return port.PublicRouteStrategySummary{}, port.PublicGroupTarget{}, err
	}
	if !ok {
		return port.PublicRouteStrategySummary{}, port.PublicGroupTarget{}, ErrRouteStrategyNotFound
	}
	if err := assertTargetActive(target); err != nil {
		return port.PublicRouteStrategySummary{}, port.PublicGroupTarget{}, err
	}
	return routeStrategy, target, nil
}

func routeStrategyTargetByIDOrUsername(ctx context.Context, store port.PublicRouteStrategyStore, ownerSystemAccountID string, targetUsername *string) (port.PublicGroupTarget, bool, error) {
	if targetUsername != nil {
		target, ok, err := store.FindPublicRouteStrategyTargetByUsername(ctx, *targetUsername)
		if err != nil || !ok {
			return port.PublicGroupTarget{}, false, err
		}
		if target.ID != ownerSystemAccountID {
			return port.PublicGroupTarget{}, false, nil
		}
		return target, true, nil
	}
	return store.FindPublicRouteStrategyTargetByID(ctx, ownerSystemAccountID)
}

func (s *Service) normalizeBindings(ctx context.Context, store port.PublicRouteStrategyStore, systemAccountID string, mode string, inputs []GroupBindingInput) ([]normalizedBinding, error) {
	basics, err := normalizeBindingBasics(inputs)
	if err != nil {
		return nil, err
	}
	groupIDs := make([]string, 0, len(basics))
	for _, binding := range basics {
		groupIDs = append(groupIDs, binding.GroupID)
	}
	groups, err := store.FindPublicRouteStrategyBindableGroups(ctx, systemAccountID, groupIDs)
	if err != nil {
		return nil, err
	}
	groupsByID := make(map[string]port.PublicRouteStrategyBindableGroup, len(groups))
	for _, group := range groups {
		groupsByID[group.ID] = group
	}
	out := make([]normalizedBinding, 0, len(basics))
	for _, binding := range basics {
		group, ok := groupsByID[binding.GroupID]
		if !ok {
			return nil, fmt.Errorf("%w: 策略路由只能绑定自己的分组或有效授权给自己的分组", ErrGroupBoundary)
		}
		if binding.Status == StatusActive && !group.Enabled {
			return nil, fmt.Errorf("%w: 策略路由不能启用已停用分组：%s", ErrInvalidBinding, group.Name)
		}
		out = append(out, normalizedBinding{
			GroupID:      binding.GroupID,
			GroupName:    group.Name,
			ProviderCode: group.ProviderCode,
			Priority:     binding.Priority,
			Weight:       binding.Weight,
			Status:       binding.Status,
			GroupEnabled: group.Enabled,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].GroupID < out[j].GroupID
	})
	if err := validateModeBindings(mode, out); err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeBindingBasics(inputs []GroupBindingInput) ([]GroupBindingInput, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("%w: 策略路由至少需要绑定一个分组", ErrInvalidBinding)
	}
	if len(inputs) > maxRouteStrategyGroupBindings {
		return nil, fmt.Errorf("%w: 策略路由最多绑定 %d 个分组", ErrInvalidBinding, maxRouteStrategyGroupBindings)
	}
	seenGroupIDs := map[string]struct{}{}
	activePriorities := map[int]struct{}{}
	out := make([]GroupBindingInput, 0, len(inputs))
	for index, input := range inputs {
		groupID := strings.TrimSpace(input.GroupID)
		if groupID == "" {
			return nil, fmt.Errorf("%w: 策略路由分组无效", ErrInvalidBinding)
		}
		if _, ok := seenGroupIDs[groupID]; ok {
			return nil, fmt.Errorf("%w: 策略路由绑定分组不能重复", ErrInvalidBinding)
		}
		seenGroupIDs[groupID] = struct{}{}
		priority := input.Priority
		if priority == 0 {
			priority = index + 1
		}
		if priority <= 0 {
			return nil, fmt.Errorf("%w: 策略路由分组优先级必须是大于 0 的整数", ErrInvalidBinding)
		}
		weight := input.Weight
		if weight == 0 {
			weight = 1
		}
		if weight < 1 || weight > 100 {
			return nil, fmt.Errorf("%w: 策略路由分组权重必须是 1-100", ErrInvalidBinding)
		}
		status := strings.TrimSpace(input.Status)
		if status == "" {
			status = StatusActive
		}
		if status != StatusActive && status != StatusDisabled {
			return nil, fmt.Errorf("%w: 策略路由分组绑定状态无效", ErrInvalidBinding)
		}
		if status == StatusActive {
			if _, ok := activePriorities[priority]; ok {
				return nil, fmt.Errorf("%w: 策略路由启用分组优先级不能重复", ErrInvalidBinding)
			}
			activePriorities[priority] = struct{}{}
		}
		out = append(out, GroupBindingInput{GroupID: groupID, Priority: priority, Weight: weight, Status: status})
	}
	if len(activePriorities) == 0 {
		return nil, fmt.Errorf("%w: 策略路由至少需要一个启用分组", ErrInvalidBinding)
	}
	return out, nil
}

func validateModeBindings(mode string, bindings []normalizedBinding) error {
	activeCount := 0
	for _, binding := range bindings {
		if binding.Status == StatusActive {
			activeCount++
		}
	}
	switch mode {
	case ModeNormal:
		if len(bindings) != 1 || activeCount != 1 {
			return fmt.Errorf("%w: 普通路由只能绑定一个启用分组", ErrInvalidBinding)
		}
	case ModeFailover:
		if len(bindings) < 2 {
			return fmt.Errorf("%w: 故障回退路由需要一个主用分组和至少一个备用分组", ErrInvalidBinding)
		}
		if bindings[0].Status != StatusActive {
			return fmt.Errorf("%w: 故障回退路由的主用分组必须启用", ErrInvalidBinding)
		}
		hasActiveBackup := false
		for _, binding := range bindings[1:] {
			if binding.Status == StatusActive {
				hasActiveBackup = true
				break
			}
		}
		if !hasActiveBackup {
			return fmt.Errorf("%w: 故障回退路由至少需要一个启用备用分组", ErrInvalidBinding)
		}
	}
	return nil
}

func (s *Service) bindingCreateInputs(bindings []normalizedBinding) []port.PublicRouteStrategyGroupBindingCreateInput {
	out := make([]port.PublicRouteStrategyGroupBindingCreateInput, 0, len(bindings))
	for _, binding := range bindings {
		out = append(out, port.PublicRouteStrategyGroupBindingCreateInput{
			ID:       s.newID("rsg"),
			GroupID:  binding.GroupID,
			Priority: binding.Priority,
			Weight:   binding.Weight,
			Status:   port.PublicRouteStrategyStatus(binding.Status),
		})
	}
	return out
}

func bindingsFromSummary(bindings []port.PublicRouteStrategyGroupBindingSummary) []normalizedBinding {
	out := make([]normalizedBinding, 0, len(bindings))
	for _, binding := range bindings {
		out = append(out, normalizedBinding{
			GroupID:      binding.GroupID,
			GroupName:    binding.GroupName,
			ProviderCode: binding.ProviderCode,
			Priority:     binding.Priority,
			Weight:       binding.Weight,
			Status:       string(binding.Status),
			GroupEnabled: binding.GroupEnabled,
		})
	}
	return out
}

func configJSONForCreate(mode string, normal ConfigValue, hybrid ConfigValue) (*string, error) {
	config, err := normalizeConfigForWrite(mode, port.PublicRouteStrategyMode(""), routeStrategyConfig{}, normal, hybrid)
	if err != nil {
		return nil, err
	}
	return configJSON(config)
}

func configJSONForUpdate(mode string, currentMode port.PublicRouteStrategyMode, current routeStrategyConfig, normal ConfigValue, hybrid ConfigValue) (*string, error) {
	config, err := normalizeConfigForWrite(mode, currentMode, current, normal, hybrid)
	if err != nil {
		return nil, err
	}
	return configJSON(config)
}

func normalizeConfigForWrite(mode string, currentMode port.PublicRouteStrategyMode, current routeStrategyConfig, normal ConfigValue, hybrid ConfigValue) (routeStrategyConfig, error) {
	switch mode {
	case ModeNormal:
		if hybrid.Set() && hybrid.Value() != nil {
			return routeStrategyConfig{}, fmt.Errorf("%w: 普通路由不能配置混合评分规则", ErrInvalidConfig)
		}
		var raw any
		if normal.Set() {
			raw = normal.Value()
		} else if currentMode == port.PublicRouteStrategyMode(ModeNormal) && current.NormalRoutingConfig != nil {
			raw = current.NormalRoutingConfig
		}
		config, err := normalizeNormalRoutingConfig(raw)
		if err != nil {
			return routeStrategyConfig{}, err
		}
		return routeStrategyConfig{NormalRoutingConfig: config}, nil
	case ModeHybridSmart:
		if normal.Set() && normal.Value() != nil {
			return routeStrategyConfig{}, fmt.Errorf("%w: 只有普通路由可以配置调度偏好", ErrInvalidConfig)
		}
		var raw any
		if hybrid.Set() {
			raw = hybrid.Value()
		} else if currentMode == port.PublicRouteStrategyMode(ModeHybridSmart) {
			raw = current.HybridRoutingConfig
		}
		config, err := normalizeHybridRoutingConfig(raw)
		if err != nil {
			return routeStrategyConfig{}, err
		}
		return routeStrategyConfig{HybridRoutingConfig: config}, nil
	default:
		if normal.Set() && normal.Value() != nil {
			return routeStrategyConfig{}, fmt.Errorf("%w: 只有普通路由可以配置调度偏好", ErrInvalidConfig)
		}
		if hybrid.Set() && hybrid.Value() != nil {
			return routeStrategyConfig{}, fmt.Errorf("%w: 只有混合智能路由可以配置混合评分规则", ErrInvalidConfig)
		}
		return routeStrategyConfig{}, nil
	}
}

func normalizeNormalRoutingConfig(value any) (*NormalRoutingConfig, error) {
	if value == nil {
		return &NormalRoutingConfig{SchedulingPreference: defaultSchedulingPreference}, nil
	}
	if typed, ok := value.(*NormalRoutingConfig); ok && typed != nil {
		return typed, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: 普通路由调度配置无效", ErrInvalidConfig)
	}
	if err := rejectConfigKeys(record, map[string]bool{
		"schedulingPreference": true,
		"speedFirstConfig":     true,
	}); err != nil {
		return nil, err
	}
	preference := defaultSchedulingPreference
	if raw, ok := record["schedulingPreference"]; ok && raw != nil {
		text, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("%w: 普通路由调度偏好无效", ErrInvalidConfig)
		}
		preference = strings.TrimSpace(text)
	}
	switch preference {
	case defaultSchedulingPreference:
		return &NormalRoutingConfig{SchedulingPreference: preference}, nil
	case schedulingPreferenceSpeedFirst:
		speedConfig, err := normalizeSpeedFirstConfig(record["speedFirstConfig"])
		if err != nil {
			return nil, err
		}
		return &NormalRoutingConfig{SchedulingPreference: preference, SpeedFirstConfig: speedConfig}, nil
	default:
		return nil, fmt.Errorf("%w: 普通路由调度偏好无效", ErrInvalidConfig)
	}
}

func normalizeSpeedFirstConfig(value any) (*SpeedFirstConfig, error) {
	config := &SpeedFirstConfig{
		FirstByteThresholdMs:          defaultFirstByteThresholdMs,
		SlowTriggerCount:              defaultSlowTriggerCount,
		SlowWindowSeconds:             defaultSlowWindowSeconds,
		RecoverySuccessCount:          defaultRecoverySuccessCount,
		ProbeIntervalSeconds:          defaultProbeIntervalSeconds,
		DegradedTTLSeconds:            defaultDegradedTTLSeconds,
		MaxFirstByteRetriesPerRequest: defaultMaxFirstByteRetriesPerRequest,
	}
	if value == nil {
		return config, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: 速度优先配置无效", ErrInvalidConfig)
	}
	if err := rejectConfigKeys(record, map[string]bool{
		"firstByteThresholdMs":          true,
		"slowTriggerCount":              true,
		"slowWindowSeconds":             true,
		"recoverySuccessCount":          true,
		"probeIntervalSeconds":          true,
		"degradedTtlSeconds":            true,
		"maxFirstByteRetriesPerRequest": true,
	}); err != nil {
		return nil, err
	}
	var err error
	if config.FirstByteThresholdMs, err = configInt(record, "firstByteThresholdMs", config.FirstByteThresholdMs, 10000, 60000, "首字观察阈值必须是 10000-60000 毫秒"); err != nil {
		return nil, err
	}
	if config.SlowTriggerCount, err = configInt(record, "slowTriggerCount", config.SlowTriggerCount, 2, 10, "速度优先触发次数必须是 2-10"); err != nil {
		return nil, err
	}
	if config.SlowWindowSeconds, err = configInt(record, "slowWindowSeconds", config.SlowWindowSeconds, 60, 600, "速度优先窗口期必须是 60-600 秒"); err != nil {
		return nil, err
	}
	if config.RecoverySuccessCount, err = configInt(record, "recoverySuccessCount", config.RecoverySuccessCount, 3, 10, "速度优先恢复次数必须是 3-10"); err != nil {
		return nil, err
	}
	if config.ProbeIntervalSeconds, err = configInt(record, "probeIntervalSeconds", config.ProbeIntervalSeconds, 10, 300, "速度优先探针间隔必须是 10-300 秒"); err != nil {
		return nil, err
	}
	if config.DegradedTTLSeconds, err = configInt(record, "degradedTtlSeconds", config.DegradedTTLSeconds, 60, 3600, "速度优先降级保留时间必须是 60-3600 秒"); err != nil {
		return nil, err
	}
	if config.MaxFirstByteRetriesPerRequest, err = configInt(record, "maxFirstByteRetriesPerRequest", config.MaxFirstByteRetriesPerRequest, 1, 3, "速度优先单请求切号次数必须是 1-3"); err != nil {
		return nil, err
	}
	return config, nil
}

func normalizeHybridRoutingConfig(value any) (map[string]any, error) {
	record, ok := value.(map[string]any)
	if !ok || len(record) == 0 {
		return nil, fmt.Errorf("%w: 混合路由配置不能为空", ErrInvalidConfig)
	}
	return record, nil
}

func configInt(record map[string]any, key string, fallback int, minValue int, maxValue int, message string) (int, error) {
	value, ok := record[key]
	if !ok || value == nil {
		return fallback, nil
	}
	var parsed int
	switch typed := value.(type) {
	case json.Number:
		intValue, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("%w: %s", ErrInvalidConfig, message)
		}
		parsed = int(intValue)
	case float64:
		if typed != float64(int(typed)) {
			return 0, fmt.Errorf("%w: %s", ErrInvalidConfig, message)
		}
		parsed = int(typed)
	case int:
		parsed = typed
	default:
		return 0, fmt.Errorf("%w: %s", ErrInvalidConfig, message)
	}
	if parsed < minValue || parsed > maxValue {
		return 0, fmt.Errorf("%w: %s", ErrInvalidConfig, message)
	}
	return parsed, nil
}

func rejectConfigKeys(record map[string]any, allowed map[string]bool) error {
	for key := range record {
		if !allowed[key] {
			return fmt.Errorf("%w: 策略路由配置包含未知字段：%s", ErrInvalidConfig, key)
		}
	}
	return nil
}

func configJSON(config routeStrategyConfig) (*string, error) {
	output := map[string]any{}
	if config.NormalRoutingConfig != nil && config.NormalRoutingConfig.SchedulingPreference != defaultSchedulingPreference {
		output["normalRoutingConfig"] = config.NormalRoutingConfig
	}
	if config.HybridRoutingConfig != nil {
		output["hybridRoutingConfig"] = config.HybridRoutingConfig
	}
	if len(output) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("%w: 策略路由配置无效", ErrInvalidConfig)
	}
	value := string(data)
	return &value, nil
}

func parseConfig(raw *string) routeStrategyConfig {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return routeStrategyConfig{}
	}
	var out routeStrategyConfig
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return routeStrategyConfig{}
	}
	if out.NormalRoutingConfig != nil {
		if normalized, err := normalizeNormalRoutingConfig(mapFromNormalConfig(out.NormalRoutingConfig)); err == nil {
			out.NormalRoutingConfig = normalized
		}
	}
	return out
}

func mapFromNormalConfig(config *NormalRoutingConfig) map[string]any {
	out := map[string]any{"schedulingPreference": config.SchedulingPreference}
	if config.SpeedFirstConfig != nil {
		data, _ := json.Marshal(config.SpeedFirstConfig)
		var speed map[string]any
		_ = json.Unmarshal(data, &speed)
		out["speedFirstConfig"] = speed
	}
	return out
}

func normalizeMode(value string) string {
	switch strings.TrimSpace(value) {
	case "", ModeNormal:
		return ModeNormal
	case ModeHybridSmart:
		return ModeHybridSmart
	case ModeWeighted:
		return ModeWeighted
	case ModeFailover:
		return ModeFailover
	case ModeRoundRobin:
		return ModeRoundRobin
	default:
		return ModeNormal
	}
}

func normalizeStatus(value string, fallback string) string {
	switch strings.TrimSpace(value) {
	case "":
		return fallback
	case StatusActive:
		return StatusActive
	case StatusDisabled:
		return StatusDisabled
	default:
		return fallback
	}
}

func normalizeListMode(value string) string {
	value = strings.TrimSpace(value)
	if value == "all" {
		return ""
	}
	return value
}

func normalizeListStatus(value string) string {
	value = strings.TrimSpace(value)
	if value == "all" {
		return ""
	}
	return value
}

func assertTargetActive(target port.PublicGroupTarget) error {
	if target.Status != "active" {
		return fmt.Errorf("%w: %s", ErrTargetDisabled, target.Username)
	}
	return nil
}

func publicTarget(target port.PublicGroupTarget) Target {
	return Target{
		Username:        target.Username,
		DisplayName:     target.DisplayName,
		SystemAccountID: target.ID,
		Created:         target.Created,
	}
}

func routeStrategyResponse(action string, target port.PublicGroupTarget, routeStrategy port.PublicRouteStrategySummary, generatedAt string) RouteStrategyResponse {
	summary := routeStrategySummary(routeStrategy)
	return RouteStrategyResponse{
		Source:        "stats",
		GeneratedAt:   generatedAt,
		Action:        action,
		Target:        publicTarget(target),
		RouteStrategy: &summary,
	}
}

func routeStrategySummary(routeStrategy port.PublicRouteStrategySummary) RouteStrategySummary {
	config := parseConfig(routeStrategy.ConfigJSON)
	mode := string(routeStrategy.Mode)
	normalConfig := config.NormalRoutingConfig
	if mode == ModeNormal && normalConfig == nil {
		normalConfig = &NormalRoutingConfig{SchedulingPreference: defaultSchedulingPreference}
	}
	bindings := make([]GroupBindingSummary, 0, len(routeStrategy.GroupBindings))
	for _, binding := range routeStrategy.GroupBindings {
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
	return RouteStrategySummary{
		ID:                  routeStrategy.ID,
		Name:                routeStrategy.Name,
		Description:         routeStrategy.Description,
		Mode:                mode,
		Status:              string(routeStrategy.Status),
		IsDefault:           routeStrategy.IsDefault,
		NormalRoutingConfig: normalConfig,
		HybridRoutingConfig: config.HybridRoutingConfig,
		GroupBindings:       bindings,
		APIKeyCount:         routeStrategy.APIKeyCount,
		CreatedAt:           routeStrategy.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:           routeStrategy.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (s *Service) generatedAt() string {
	return s.now().UTC().Format(time.RFC3339Nano)
}

func (s *Service) inTx(ctx context.Context, fn func(context.Context, port.PublicRouteStrategyStore) error) error {
	if s.transactor != nil {
		return s.transactor.PublicRouteStrategyInTx(ctx, fn)
	}
	return fn(ctx, s.store)
}
