package publicapikeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	StatusActive   = "active"
	StatusDisabled = "disabled"

	maxSafeInteger      = 9007199254740991
	defaultScheduleTZ   = "UTC"
	scheduleModeWindows = "allow_windows"
)

var (
	ErrTargetNotFound                   = errors.New("public api key target not found")
	ErrTargetDisabled                   = errors.New("public api key target disabled")
	ErrAPIKeyNotFound                   = errors.New("public api key not found")
	ErrDuplicateAPIKeyName              = errors.New("public api key duplicate name")
	ErrRouteStrategyNotFound            = errors.New("public api key route strategy not found")
	ErrRouteStrategyDisabled            = errors.New("public api key route strategy disabled")
	ErrDefaultAPIKeyDelete              = errors.New("public api key default delete")
	ErrDefaultAPIKeyRouteStrategyChange = errors.New("public api key default route strategy change")
	ErrInvalidExpiresAt                 = errors.New("public api key invalid expires_at")
	ErrInvalidQuotaLimits               = errors.New("public api key invalid quota limits")
	ErrInvalidAvailabilitySchedule      = errors.New("public api key invalid availability schedule")
)

type Service struct {
	store      port.PublicAPIKeyStore
	transactor port.PublicAPIKeyTransactor
	now        func() time.Time
	newID      func(prefix string) string
	newSecret  func() (string, error)
}

type Options struct {
	Store      port.PublicAPIKeyStore
	Transactor port.PublicAPIKeyTransactor
	Now        func() time.Time
	NewID      func(prefix string) string
	NewSecret  func() (string, error)
}

type Target struct {
	Username        string `json:"username"`
	DisplayName     string `json:"displayName"`
	SystemAccountID string `json:"systemAccountId"`
	Created         bool   `json:"created"`
}

type APIKeySummary struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	KeyPrefix            string `json:"keyPrefix"`
	KeySuffix            string `json:"keySuffix,omitempty"`
	Key                  string `json:"key,omitempty"`
	Status               string `json:"status"`
	RouteStrategyID      string `json:"routeStrategyId"`
	RouteStrategyName    string `json:"routeStrategyName,omitempty"`
	RouteStrategyMode    string `json:"routeStrategyMode,omitempty"`
	RouteStrategyStatus  string `json:"routeStrategyStatus,omitempty"`
	ExpiresAt            string `json:"expiresAt,omitempty"`
	QuotaLimits          any    `json:"quotaLimits,omitempty"`
	AvailabilitySchedule any    `json:"availabilitySchedule,omitempty"`
}

type APIKeyResponse struct {
	Source      string         `json:"source"`
	GeneratedAt string         `json:"generatedAt"`
	Action      string         `json:"action"`
	Target      Target         `json:"target"`
	APIKey      *APIKeySummary `json:"apiKey"`
}

type APIKeyListResponse struct {
	Source         string          `json:"source"`
	GeneratedAt    string          `json:"generatedAt"`
	Target         Target          `json:"target"`
	Page           int             `json:"page"`
	PageSize       int             `json:"pageSize"`
	PageUpperBound int             `json:"pageUpperBound"`
	HasMore        bool            `json:"hasMore"`
	Items          []APIKeySummary `json:"items"`
}

type ListInput struct {
	TargetUsername  string
	RouteStrategyID string
	Keyword         string
	Status          string
	Page            int
	PageSize        int
}

type AddInput struct {
	TargetUsername       string
	Name                 string
	Description          *string
	RouteStrategyID      string
	Status               string
	ExpiresAt            *string
	QuotaLimits          JSONValue
	AvailabilitySchedule JSONValue
}

type UpdateInput struct {
	TargetUsername       *string
	APIKeyID             string
	Name                 *string
	Description          OptionalString
	RouteStrategyID      *string
	Status               *string
	ExpiresAt            OptionalString
	QuotaLimits          JSONValue
	AvailabilitySchedule JSONValue
}

type DeleteInput struct {
	TargetUsername *string
	APIKeyID       string
}

type OptionalString struct {
	value *string
	set   bool
}

type JSONValue struct {
	value any
	set   bool
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
	newSecret := opts.NewSecret
	if newSecret == nil {
		newSecret = createAPIKeySecret
	}
	return &Service{
		store:      opts.Store,
		transactor: opts.Transactor,
		now:        now,
		newID:      newID,
		newSecret:  newSecret,
	}
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

func NewJSONValue(value any, set bool) JSONValue {
	return JSONValue{value: value, set: set}
}

func (v JSONValue) Set() bool {
	return v.set
}

func (v JSONValue) Value() any {
	return v.value
}

func (s *Service) List(ctx context.Context, input ListInput) (APIKeyListResponse, error) {
	target, err := s.requireTarget(ctx, input.TargetUsername)
	if err != nil {
		return APIKeyListResponse{}, err
	}
	page, err := s.store.ListPublicAPIKeys(ctx, port.PublicAPIKeyListInput{
		SystemAccountID: target.ID,
		RouteStrategyID: strings.TrimSpace(input.RouteStrategyID),
		Keyword:         strings.TrimSpace(input.Keyword),
		Status:          normalizeListStatus(input.Status),
		Page:            input.Page,
		PageSize:        input.PageSize,
	})
	if err != nil {
		return APIKeyListResponse{}, err
	}
	items := make([]APIKeySummary, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, apiKeySummary(item, ""))
	}
	return APIKeyListResponse{
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

func (s *Service) Add(ctx context.Context, input AddInput) (APIKeyResponse, error) {
	var response APIKeyResponse
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		response, err = s.addOnce(ctx, input)
		if errors.Is(err, port.ErrPublicAPIKeyDuplicateHash) {
			continue
		}
		return response, err
	}
	return response, err
}

func (s *Service) addOnce(ctx context.Context, input AddInput) (APIKeyResponse, error) {
	var response APIKeyResponse
	err := s.inTx(ctx, func(ctx context.Context, store port.PublicAPIKeyStore) error {
		target, err := s.requireTargetWithStore(ctx, store, input.TargetUsername)
		if err != nil {
			return err
		}
		if _, err := requireActiveRouteStrategy(ctx, store, target.ID, input.RouteStrategyID); err != nil {
			return err
		}

		now := s.now().UTC()
		expiresAt, err := parseOptionalTime(input.ExpiresAt)
		if err != nil {
			return err
		}
		quotaJSON, err := normalizeQuotaLimitsJSON(input.QuotaLimits)
		if err != nil {
			return err
		}
		scheduleJSON, nextCheckAt, scheduleAllowed, scheduleSet, err := normalizeAvailabilityScheduleJSON(input.AvailabilitySchedule, now)
		if err != nil {
			return err
		}
		status := normalizeStatus(input.Status, StatusActive)
		if scheduleSet {
			status = StatusDisabled
			if scheduleAllowed {
				status = StatusActive
			}
		}
		secret, err := s.newSecret()
		if err != nil {
			return err
		}
		created, err := store.CreatePublicAPIKey(ctx, port.PublicAPIKeyCreateInput{
			ID:                              s.newID("key"),
			SystemAccountID:                 target.ID,
			RouteStrategyID:                 strings.TrimSpace(input.RouteStrategyID),
			Name:                            strings.TrimSpace(input.Name),
			Description:                     normalizeOptionalText(input.Description),
			KeyHash:                         hashSecret(secret),
			KeyPrefix:                       secretPrefix(secret),
			KeySuffix:                       secretSuffix(secret),
			Status:                          port.PublicAPIKeyStatus(status),
			ExpiresAt:                       expiresAt,
			QuotaLimitsJSON:                 quotaJSON,
			AvailabilityScheduleJSON:        scheduleJSON,
			AvailabilityScheduleNextCheckAt: nextCheckAt,
			Now:                             now,
		})
		if errors.Is(err, port.ErrPublicAPIKeyDuplicateName) {
			return fmt.Errorf("%w: %s", ErrDuplicateAPIKeyName, strings.TrimSpace(input.Name))
		}
		if err != nil {
			return err
		}
		response = apiKeyResponse("created", target, created, secret, s.generatedAt())
		return nil
	})
	return response, err
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (APIKeyResponse, error) {
	var response APIKeyResponse
	err := s.inTx(ctx, func(ctx context.Context, store port.PublicAPIKeyStore) error {
		current, target, err := s.apiKeyAndTargetForWrite(ctx, store, input.APIKeyID, input.TargetUsername)
		if err != nil {
			return err
		}

		next := current
		if input.Name != nil {
			next.Name = strings.TrimSpace(*input.Name)
		}
		if input.Description.Set() {
			next.Description = normalizeOptionalText(input.Description.Value())
		}
		if input.RouteStrategyID != nil {
			nextRouteID := strings.TrimSpace(*input.RouteStrategyID)
			if current.IsDefault && nextRouteID != current.RouteStrategyID {
				return ErrDefaultAPIKeyRouteStrategyChange
			}
			if _, err := requireActiveRouteStrategy(ctx, store, current.SystemAccountID, nextRouteID); err != nil {
				return err
			}
			next.RouteStrategyID = nextRouteID
		}
		if input.Status != nil {
			next.Status = port.PublicAPIKeyStatus(normalizeStatus(*input.Status, string(current.Status)))
		}
		if input.ExpiresAt.Set() {
			expiresAt, err := parseOptionalTime(input.ExpiresAt.Value())
			if err != nil {
				return err
			}
			next.ExpiresAt = expiresAt
		}
		if input.QuotaLimits.Set() {
			quotaJSON, err := normalizeQuotaLimitsJSON(input.QuotaLimits)
			if err != nil {
				return err
			}
			next.QuotaLimitsJSON = quotaJSON
		}
		if input.AvailabilitySchedule.Set() {
			scheduleJSON, nextCheckAt, scheduleAllowed, scheduleSet, err := normalizeAvailabilityScheduleJSON(input.AvailabilitySchedule, s.now().UTC())
			if err != nil {
				return err
			}
			next.AvailabilityScheduleJSON = scheduleJSON
			next.AvailabilityScheduleNextCheckAt = nextCheckAt
			if scheduleSet {
				next.Status = port.PublicAPIKeyStatus(StatusDisabled)
				if scheduleAllowed {
					next.Status = port.PublicAPIKeyStatus(StatusActive)
				}
			}
		}
		updated, ok, err := store.UpdatePublicAPIKey(ctx, port.PublicAPIKeyUpdateInput{
			ID:                              current.ID,
			SystemAccountID:                 current.SystemAccountID,
			RouteStrategyID:                 next.RouteStrategyID,
			Name:                            next.Name,
			Description:                     next.Description,
			Status:                          next.Status,
			ExpiresAt:                       next.ExpiresAt,
			QuotaLimitsJSON:                 next.QuotaLimitsJSON,
			AvailabilityScheduleJSON:        next.AvailabilityScheduleJSON,
			AvailabilityScheduleNextCheckAt: next.AvailabilityScheduleNextCheckAt,
			Now:                             s.now().UTC(),
		})
		if errors.Is(err, port.ErrPublicAPIKeyDuplicateName) {
			return fmt.Errorf("%w: %s", ErrDuplicateAPIKeyName, next.Name)
		}
		if err != nil {
			return err
		}
		if !ok {
			return ErrAPIKeyNotFound
		}
		response = apiKeyResponse("updated", target, updated, "", s.generatedAt())
		return nil
	})
	return response, err
}

func (s *Service) Delete(ctx context.Context, input DeleteInput) (APIKeyResponse, error) {
	var response APIKeyResponse
	err := s.inTx(ctx, func(ctx context.Context, store port.PublicAPIKeyStore) error {
		current, target, err := s.apiKeyAndTargetForWrite(ctx, store, input.APIKeyID, input.TargetUsername)
		if err != nil {
			return err
		}
		if current.IsDefault {
			return ErrDefaultAPIKeyDelete
		}
		deleted, err := store.DeletePublicAPIKey(ctx, current.ID, current.SystemAccountID)
		if err != nil {
			return err
		}
		if !deleted {
			return ErrAPIKeyNotFound
		}
		response = apiKeyResponse("deleted", target, current, "", s.generatedAt())
		return nil
	})
	return response, err
}

func (s *Service) requireTarget(ctx context.Context, username string) (port.PublicGroupTarget, error) {
	return s.requireTargetWithStore(ctx, s.store, username)
}

func (s *Service) requireTargetWithStore(ctx context.Context, store port.PublicAPIKeyStore, username string) (port.PublicGroupTarget, error) {
	username = strings.TrimSpace(username)
	target, ok, err := store.FindPublicAPIKeyTargetByUsername(ctx, username)
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

func (s *Service) apiKeyAndTargetForWrite(ctx context.Context, store port.PublicAPIKeyStore, apiKeyID string, targetUsername *string) (port.PublicAPIKeySummary, port.PublicGroupTarget, error) {
	apiKey, ok, err := store.FindPublicAPIKeyByID(ctx, strings.TrimSpace(apiKeyID))
	if err != nil {
		return port.PublicAPIKeySummary{}, port.PublicGroupTarget{}, err
	}
	if !ok {
		return port.PublicAPIKeySummary{}, port.PublicGroupTarget{}, ErrAPIKeyNotFound
	}

	target, ok, err := targetByIDOrUsername(ctx, store, apiKey.SystemAccountID, targetUsername)
	if err != nil {
		return port.PublicAPIKeySummary{}, port.PublicGroupTarget{}, err
	}
	if !ok {
		return port.PublicAPIKeySummary{}, port.PublicGroupTarget{}, ErrAPIKeyNotFound
	}
	if err := assertTargetActive(target); err != nil {
		return port.PublicAPIKeySummary{}, port.PublicGroupTarget{}, err
	}
	return apiKey, target, nil
}

func targetByIDOrUsername(ctx context.Context, store port.PublicAPIKeyStore, ownerSystemAccountID string, targetUsername *string) (port.PublicGroupTarget, bool, error) {
	if targetUsername != nil {
		target, ok, err := store.FindPublicAPIKeyTargetByUsername(ctx, *targetUsername)
		if err != nil || !ok {
			return port.PublicGroupTarget{}, false, err
		}
		if target.ID != ownerSystemAccountID {
			return port.PublicGroupTarget{}, false, nil
		}
		return target, true, nil
	}
	return store.FindPublicAPIKeyTargetByID(ctx, ownerSystemAccountID)
}

func requireActiveRouteStrategy(ctx context.Context, store port.PublicAPIKeyStore, systemAccountID string, routeStrategyID string) (port.PublicAPIKeyRouteStrategyRef, error) {
	routeStrategy, ok, err := store.FindPublicAPIKeyRouteStrategy(ctx, systemAccountID, strings.TrimSpace(routeStrategyID))
	if err != nil {
		return port.PublicAPIKeyRouteStrategyRef{}, err
	}
	if !ok {
		return port.PublicAPIKeyRouteStrategyRef{}, fmt.Errorf("%w: %s", ErrRouteStrategyNotFound, strings.TrimSpace(routeStrategyID))
	}
	if routeStrategy.Status != port.PublicRouteStrategyStatusActive {
		return port.PublicAPIKeyRouteStrategyRef{}, fmt.Errorf("%w: %s", ErrRouteStrategyDisabled, routeStrategy.ID)
	}
	return routeStrategy, nil
}

func (s *Service) inTx(ctx context.Context, fn func(context.Context, port.PublicAPIKeyStore) error) error {
	if s.transactor != nil {
		return s.transactor.PublicAPIKeyInTx(ctx, fn)
	}
	return fn(ctx, s.store)
}

func normalizeListStatus(value string) string {
	value = strings.TrimSpace(value)
	if value == "all" {
		return ""
	}
	return value
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

func assertTargetActive(target port.PublicGroupTarget) error {
	if target.Status != "active" {
		return fmt.Errorf("%w: %s", ErrTargetDisabled, target.Username)
	}
	return nil
}

func parseOptionalTime(raw *string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	text := strings.TrimSpace(*raw)
	if text == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return nil, fmt.Errorf("%w: expiresAt 必须是 RFC3339 时间", ErrInvalidExpiresAt)
	}
	value := parsed.UTC()
	return &value, nil
}

func normalizeOptionalText(value *string) *string {
	if value == nil {
		return nil
	}
	text := strings.TrimSpace(*value)
	if text == "" {
		return nil
	}
	return &text
}

func normalizeQuotaLimitsJSON(value JSONValue) (*string, error) {
	if !value.Set() || value.Value() == nil {
		return nil, nil
	}
	record, ok := value.Value().(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: quotaLimits 必须是对象", ErrInvalidQuotaLimits)
	}
	allowed := map[string]bool{"hourly": true, "daily": true, "weekly": true, "monthly": true, "total": true}
	out := make(map[string]any, len(record))
	for key, raw := range record {
		if !allowed[key] {
			return nil, fmt.Errorf("%w: quotaLimits 包含未知字段：%s", ErrInvalidQuotaLimits, key)
		}
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: %s 配额必须是对象", ErrInvalidQuotaLimits, key)
		}
		normalized, err := normalizeQuotaLimitItem(key, item)
		if err != nil {
			return nil, err
		}
		out[key] = normalized
	}
	if len(out) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("%w: quotaLimits 无法序列化", ErrInvalidQuotaLimits)
	}
	text := string(data)
	return &text, nil
}

func normalizeQuotaLimitItem(key string, item map[string]any) (map[string]any, error) {
	allowed := map[string]bool{"enabled": true, "limit": true}
	if key == "hourly" {
		allowed["hours"] = true
	}
	for field := range item {
		if !allowed[field] {
			return nil, fmt.Errorf("%w: %s 配额包含未知字段：%s", ErrInvalidQuotaLimits, key, field)
		}
	}
	enabled, ok := item["enabled"].(bool)
	if !ok || !enabled {
		return nil, fmt.Errorf("%w: %s 配额 enabled 必须为 true", ErrInvalidQuotaLimits, key)
	}
	limit, err := normalizedPositiveNumber(item["limit"])
	if err != nil {
		return nil, fmt.Errorf("%w: %s 配额 limit 无效", ErrInvalidQuotaLimits, key)
	}
	out := map[string]any{"enabled": true, "limit": limit}
	if key == "hourly" {
		hours, err := normalizedInteger(item["hours"], 1, 720)
		if err != nil {
			return nil, fmt.Errorf("%w: hourly.hours 必须是 1-720 的整数", ErrInvalidQuotaLimits)
		}
		out["hours"] = hours
	}
	return out, nil
}

func normalizeAvailabilityScheduleJSON(value JSONValue, now time.Time) (*string, *time.Time, bool, bool, error) {
	if !value.Set() || value.Value() == nil {
		return nil, nil, false, false, nil
	}
	record, ok := value.Value().(map[string]any)
	if !ok {
		return nil, nil, false, false, fmt.Errorf("%w: availabilitySchedule 必须是对象", ErrInvalidAvailabilitySchedule)
	}
	schedule, allowed, err := normalizeSchedule(record, now)
	if err != nil {
		return nil, nil, false, false, err
	}
	data, err := json.Marshal(schedule)
	if err != nil {
		return nil, nil, false, false, fmt.Errorf("%w: availabilitySchedule 无法序列化", ErrInvalidAvailabilitySchedule)
	}
	text := string(data)
	nextCheck := now.UTC().Add(time.Minute)
	return &text, &nextCheck, allowed, true, nil
}

type scheduleWindow struct {
	DaysOfWeek []int
	Start      string
	End        string
	startMin   int
	endMin     int
}

type exceptionWindow struct {
	Start    string
	End      string
	startMin int
	endMin   int
}

func normalizeSchedule(record map[string]any, now time.Time) (map[string]any, bool, error) {
	allowedKeys := map[string]bool{
		"enabled":    true,
		"timezone":   true,
		"mode":       true,
		"windows":    true,
		"dateRange":  true,
		"exceptions": true,
	}
	for key := range record {
		if !allowedKeys[key] {
			return nil, false, fmt.Errorf("%w: availabilitySchedule 包含未知字段：%s", ErrInvalidAvailabilitySchedule, key)
		}
	}
	enabled, ok := record["enabled"].(bool)
	if !ok || !enabled {
		return nil, false, fmt.Errorf("%w: availabilitySchedule.enabled 必须为 true", ErrInvalidAvailabilitySchedule)
	}
	mode, _ := record["mode"].(string)
	if strings.TrimSpace(mode) != scheduleModeWindows {
		return nil, false, fmt.Errorf("%w: availabilitySchedule.mode 必须为 allow_windows", ErrInvalidAvailabilitySchedule)
	}
	timezone := defaultScheduleTZ
	if raw, ok := record["timezone"]; ok && raw != nil {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, false, fmt.Errorf("%w: availabilitySchedule.timezone 无效", ErrInvalidAvailabilitySchedule)
		}
		timezone = strings.TrimSpace(text)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, false, fmt.Errorf("%w: availabilitySchedule.timezone 无效", ErrInvalidAvailabilitySchedule)
	}
	windows, err := normalizeScheduleWindows(record["windows"])
	if err != nil {
		return nil, false, err
	}
	dateRange, dateAllowed, err := normalizeScheduleDateRange(record["dateRange"], now.In(location))
	if err != nil {
		return nil, false, err
	}
	exceptions, exceptionSet, exceptionAllowed, err := normalizeScheduleExceptions(record["exceptions"], now.In(location))
	if err != nil {
		return nil, false, err
	}
	allowed := false
	if dateAllowed {
		allowed = scheduleWindowsAllow(windows, now.In(location))
	}
	if exceptionSet {
		allowed = exceptionAllowed
	}

	out := map[string]any{
		"enabled":  true,
		"timezone": timezone,
		"mode":     scheduleModeWindows,
		"windows":  scheduleWindowsOutput(windows),
	}
	if dateRange != nil {
		out["dateRange"] = dateRange
	}
	if exceptions != nil {
		out["exceptions"] = exceptions
	}
	return out, allowed, nil
}

func normalizeScheduleWindows(raw any) ([]scheduleWindow, error) {
	items, ok := raw.([]any)
	if !ok || len(items) < 1 || len(items) > 32 {
		return nil, fmt.Errorf("%w: availabilitySchedule.windows 数量无效", ErrInvalidAvailabilitySchedule)
	}
	out := make([]scheduleWindow, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: availabilitySchedule.windows 项必须是对象", ErrInvalidAvailabilitySchedule)
		}
		for key := range record {
			if key != "daysOfWeek" && key != "start" && key != "end" {
				return nil, fmt.Errorf("%w: availabilitySchedule.windows 包含未知字段：%s", ErrInvalidAvailabilitySchedule, key)
			}
		}
		days, err := normalizeDaysOfWeek(record["daysOfWeek"])
		if err != nil {
			return nil, err
		}
		start, startMin, err := normalizeHHMM(record["start"])
		if err != nil {
			return nil, fmt.Errorf("%w: availabilitySchedule.windows.start 无效", ErrInvalidAvailabilitySchedule)
		}
		end, endMin, err := normalizeHHMM(record["end"])
		if err != nil || startMin == endMin {
			return nil, fmt.Errorf("%w: availabilitySchedule.windows.end 无效", ErrInvalidAvailabilitySchedule)
		}
		out = append(out, scheduleWindow{DaysOfWeek: days, Start: start, End: end, startMin: startMin, endMin: endMin})
	}
	return out, nil
}

func normalizeDaysOfWeek(raw any) ([]int, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("%w: daysOfWeek 必须是非空数组", ErrInvalidAvailabilitySchedule)
	}
	seen := map[int]struct{}{}
	out := make([]int, 0, len(items))
	for _, item := range items {
		value, err := normalizedInteger(item, 1, 7)
		if err != nil {
			return nil, fmt.Errorf("%w: daysOfWeek 必须是 1-7 的整数", ErrInvalidAvailabilitySchedule)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: daysOfWeek 必须是非空数组", ErrInvalidAvailabilitySchedule)
	}
	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

func normalizeScheduleDateRange(raw any, now time.Time) (map[string]any, bool, error) {
	if raw == nil {
		return nil, true, nil
	}
	record, ok := raw.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%w: availabilitySchedule.dateRange 必须是对象", ErrInvalidAvailabilitySchedule)
	}
	for key := range record {
		if key != "startDate" && key != "endDate" {
			return nil, false, fmt.Errorf("%w: dateRange 包含未知字段：%s", ErrInvalidAvailabilitySchedule, key)
		}
	}
	startText, startDate, err := optionalDate(record["startDate"])
	if err != nil {
		return nil, false, err
	}
	endText, endDate, err := optionalDate(record["endDate"])
	if err != nil {
		return nil, false, err
	}
	if startDate != nil && endDate != nil && startDate.After(*endDate) {
		return nil, false, fmt.Errorf("%w: dateRange.startDate 不能晚于 endDate", ErrInvalidAvailabilitySchedule)
	}
	today := dateOnly(now)
	allowed := (startDate == nil || !today.Before(*startDate)) && (endDate == nil || !today.After(*endDate))
	out := map[string]any{}
	if startText != "" {
		out["startDate"] = startText
	}
	if endText != "" {
		out["endDate"] = endText
	}
	if len(out) == 0 {
		return nil, allowed, nil
	}
	return out, allowed, nil
}

func normalizeScheduleExceptions(raw any, now time.Time) ([]map[string]any, bool, bool, error) {
	if raw == nil {
		return nil, false, false, nil
	}
	items, ok := raw.([]any)
	if !ok || len(items) > 128 {
		return nil, false, false, fmt.Errorf("%w: availabilitySchedule.exceptions 数量无效", ErrInvalidAvailabilitySchedule)
	}
	out := make([]map[string]any, 0, len(items))
	today := now.Format("2006-01-02")
	activeSet := false
	activeAllowed := false
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, false, false, fmt.Errorf("%w: exceptions 项必须是对象", ErrInvalidAvailabilitySchedule)
		}
		for key := range record {
			if key != "date" && key != "action" && key != "windows" {
				return nil, false, false, fmt.Errorf("%w: exceptions 包含未知字段：%s", ErrInvalidAvailabilitySchedule, key)
			}
		}
		dateText, date, err := requiredDate(record["date"])
		if err != nil {
			return nil, false, false, err
		}
		_ = date
		action, ok := record["action"].(string)
		if !ok || (action != "allow" && action != "deny") {
			return nil, false, false, fmt.Errorf("%w: exceptions.action 无效", ErrInvalidAvailabilitySchedule)
		}
		itemOut := map[string]any{"date": dateText, "action": action}
		if action == "deny" {
			if _, exists := record["windows"]; exists {
				return nil, false, false, fmt.Errorf("%w: deny exception 不能包含 windows", ErrInvalidAvailabilitySchedule)
			}
			if dateText == today {
				activeSet = true
				activeAllowed = false
			}
			out = append(out, itemOut)
			continue
		}
		windows, err := normalizeExceptionWindows(record["windows"])
		if err != nil {
			return nil, false, false, err
		}
		itemOut["windows"] = exceptionWindowsOutput(windows)
		if dateText == today {
			activeSet = true
			activeAllowed = exceptionWindowsAllow(windows, now)
		}
		out = append(out, itemOut)
	}
	return out, activeSet, activeAllowed, nil
}

func normalizeExceptionWindows(raw any) ([]exceptionWindow, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 || len(items) > 32 {
		return nil, fmt.Errorf("%w: allow exception windows 数量无效", ErrInvalidAvailabilitySchedule)
	}
	out := make([]exceptionWindow, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: exception windows 项必须是对象", ErrInvalidAvailabilitySchedule)
		}
		for key := range record {
			if key != "start" && key != "end" {
				return nil, fmt.Errorf("%w: exception windows 包含未知字段：%s", ErrInvalidAvailabilitySchedule, key)
			}
		}
		start, startMin, err := normalizeHHMM(record["start"])
		if err != nil {
			return nil, fmt.Errorf("%w: exception windows.start 无效", ErrInvalidAvailabilitySchedule)
		}
		end, endMin, err := normalizeHHMM(record["end"])
		if err != nil || startMin == endMin {
			return nil, fmt.Errorf("%w: exception windows.end 无效", ErrInvalidAvailabilitySchedule)
		}
		out = append(out, exceptionWindow{Start: start, End: end, startMin: startMin, endMin: endMin})
	}
	return out, nil
}

func optionalDate(raw any) (string, *time.Time, error) {
	if raw == nil {
		return "", nil, nil
	}
	return requiredDate(raw)
}

func requiredDate(raw any) (string, *time.Time, error) {
	text, ok := raw.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", nil, fmt.Errorf("%w: 日期必须是 YYYY-MM-DD", ErrInvalidAvailabilitySchedule)
	}
	text = strings.TrimSpace(text)
	parsed, err := time.ParseInLocation("2006-01-02", text, time.UTC)
	if err != nil {
		return "", nil, fmt.Errorf("%w: 日期必须是 YYYY-MM-DD", ErrInvalidAvailabilitySchedule)
	}
	return text, &parsed, nil
}

func normalizeHHMM(raw any) (string, int, error) {
	text, ok := raw.(string)
	if !ok {
		return "", 0, fmt.Errorf("time must be string")
	}
	text = strings.TrimSpace(text)
	if len(text) != 5 || text[2] != ':' {
		return "", 0, fmt.Errorf("time must be HH:mm")
	}
	hour, err := strconv.Atoi(text[:2])
	if err != nil || hour < 0 || hour > 23 {
		return "", 0, fmt.Errorf("hour invalid")
	}
	minute, err := strconv.Atoi(text[3:])
	if err != nil || minute < 0 || minute > 59 {
		return "", 0, fmt.Errorf("minute invalid")
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), hour*60 + minute, nil
}

func scheduleWindowsAllow(windows []scheduleWindow, now time.Time) bool {
	today := scheduleDay(now)
	yesterday := today - 1
	if yesterday == 0 {
		yesterday = 7
	}
	minute := now.Hour()*60 + now.Minute()
	for _, window := range windows {
		if window.startMin < window.endMin {
			if containsInt(window.DaysOfWeek, today) && minute >= window.startMin && minute < window.endMin {
				return true
			}
			continue
		}
		if containsInt(window.DaysOfWeek, today) && minute >= window.startMin {
			return true
		}
		if containsInt(window.DaysOfWeek, yesterday) && minute < window.endMin {
			return true
		}
	}
	return false
}

func exceptionWindowsAllow(windows []exceptionWindow, now time.Time) bool {
	minute := now.Hour()*60 + now.Minute()
	for _, window := range windows {
		if window.startMin < window.endMin {
			if minute >= window.startMin && minute < window.endMin {
				return true
			}
			continue
		}
		if minute >= window.startMin || minute < window.endMin {
			return true
		}
	}
	return false
}

func scheduleDay(now time.Time) int {
	switch now.Weekday() {
	case time.Monday:
		return 1
	case time.Tuesday:
		return 2
	case time.Wednesday:
		return 3
	case time.Thursday:
		return 4
	case time.Friday:
		return 5
	case time.Saturday:
		return 6
	default:
		return 7
	}
}

func dateOnly(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func scheduleWindowsOutput(windows []scheduleWindow) []map[string]any {
	out := make([]map[string]any, 0, len(windows))
	for _, window := range windows {
		out = append(out, map[string]any{
			"daysOfWeek": window.DaysOfWeek,
			"start":      window.Start,
			"end":        window.End,
		})
	}
	return out
}

func exceptionWindowsOutput(windows []exceptionWindow) []map[string]any {
	out := make([]map[string]any, 0, len(windows))
	for _, window := range windows {
		out = append(out, map[string]any{
			"start": window.Start,
			"end":   window.End,
		})
	}
	return out
}

func normalizedPositiveNumber(raw any) (any, error) {
	text, err := numberText(raw)
	if err != nil {
		return nil, err
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > maxSafeInteger {
		return nil, fmt.Errorf("invalid positive number")
	}
	if decimalPlaces(text) > 6 {
		return nil, fmt.Errorf("too many decimals")
	}
	if strings.ContainsAny(text, ".eE") {
		return json.Number(text), nil
	}
	return json.Number(text), nil
}

func normalizedInteger(raw any, minValue int, maxValue int) (int, error) {
	text, err := numberText(raw)
	if err != nil || strings.ContainsAny(text, ".eE") {
		return 0, fmt.Errorf("invalid integer")
	}
	value, err := strconv.Atoi(text)
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("invalid integer")
	}
	return value, nil
}

func numberText(raw any) (string, error) {
	switch typed := raw.(type) {
	case json.Number:
		return typed.String(), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", fmt.Errorf("invalid number")
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case int32:
		return strconv.FormatInt(int64(typed), 10), nil
	default:
		return "", fmt.Errorf("invalid number")
	}
}

func decimalPlaces(text string) int {
	if index := strings.IndexByte(text, '.'); index >= 0 {
		frac := text[index+1:]
		if exp := strings.IndexAny(frac, "eE"); exp >= 0 {
			frac = frac[:exp]
		}
		return len(strings.TrimRight(frac, "0"))
	}
	return 0
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func publicTarget(target port.PublicGroupTarget) Target {
	return Target{
		Username:        target.Username,
		DisplayName:     target.DisplayName,
		SystemAccountID: target.ID,
		Created:         target.Created,
	}
}

func apiKeyResponse(action string, target port.PublicGroupTarget, apiKey port.PublicAPIKeySummary, secret string, generatedAt string) APIKeyResponse {
	summary := apiKeySummary(apiKey, secret)
	return APIKeyResponse{
		Source:      "stats",
		GeneratedAt: generatedAt,
		Action:      action,
		Target:      publicTarget(target),
		APIKey:      &summary,
	}
}

func apiKeySummary(apiKey port.PublicAPIKeySummary, secret string) APIKeySummary {
	summary := APIKeySummary{
		ID:                   apiKey.ID,
		Name:                 apiKey.Name,
		KeyPrefix:            apiKey.KeyPrefix,
		KeySuffix:            apiKey.KeySuffix,
		Key:                  secret,
		Status:               string(apiKey.Status),
		RouteStrategyID:      apiKey.RouteStrategyID,
		RouteStrategyName:    apiKey.RouteStrategyName,
		RouteStrategyMode:    string(apiKey.RouteStrategyMode),
		RouteStrategyStatus:  string(apiKey.RouteStrategyStatus),
		QuotaLimits:          jsonValue(apiKey.QuotaLimitsJSON),
		AvailabilitySchedule: jsonValue(apiKey.AvailabilityScheduleJSON),
	}
	if apiKey.ExpiresAt != nil {
		summary.ExpiresAt = apiKey.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return summary
}

func jsonValue(raw *string) any {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(*raw), &value); err != nil {
		return nil
	}
	return value
}

func (s *Service) generatedAt() string {
	return s.now().UTC().Format(time.RFC3339Nano)
}

func createAPIKeySecret() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("create api key secret: %w", err)
	}
	return "sk-" + hex.EncodeToString(bytes[:]), nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func secretPrefix(secret string) string {
	if len(secret) <= 8 {
		return secret
	}
	return secret[:8]
}

func secretSuffix(secret string) string {
	if len(secret) <= 8 {
		return secret
	}
	return secret[len(secret)-8:]
}
