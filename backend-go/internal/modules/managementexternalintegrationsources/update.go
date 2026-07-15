package managementexternalintegrationsources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"time"
	"unicode/utf16"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrUpdateInvalid            = errors.New("来源系统参数无效")
	ErrNotFound                 = errors.New("来源系统不存在")
	ErrBuiltInUpdateRestricted  = errors.New("内置测试 Token 只支持启用或停用，不支持编辑名称、授权范围、限频、到期时间或备注")
	ErrNameExists               = errors.New("来源系统名称已存在")
	updateServerDateTimePattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{3}))?Z$`)
)

type updateValidationError struct {
	cause error
}

func (e updateValidationError) Error() string { return e.cause.Error() }
func (e updateValidationError) Unwrap() error { return e.cause }

func IsUpdateValidationError(err error) bool {
	if errors.Is(err, ErrUpdateInvalid) {
		return true
	}
	var target updateValidationError
	return errors.As(err, &target)
}

type UpdateInput struct {
	SourceID      string
	HasName       bool
	Name          string
	HasStatus     bool
	Status        string
	HasScopes     bool
	Scopes        any
	HasRateLimits bool
	RateLimits    any
	HasExpiresAt  bool
	ExpiresAt     any
	HasNotes      bool
	Notes         any
}

type UpdateResult struct {
	Before    Detail
	After     Detail
	Committed bool
}

type UpdateService struct {
	store port.ManagementExternalIntegrationSourceUpdater
	now   func() time.Time
}

func NewUpdateService(store port.ManagementExternalIntegrationSourceUpdater) *UpdateService {
	return &UpdateService{store: store, now: time.Now}
}

func (s *UpdateService) Update(ctx context.Context, input UpdateInput) (UpdateResult, error) {
	if s == nil || s.store == nil {
		return UpdateResult{}, fmt.Errorf("management external integration source updater is required")
	}
	normalized, err := normalizeUpdateInput(input)
	if err != nil {
		return UpdateResult{}, err
	}
	normalized.UpdatedAt = s.now().UTC()
	var result UpdateResult
	_, err = s.store.UpdateManagementExternalIntegrationSource(ctx, normalized, func(stored port.ManagementExternalIntegrationSourceUpdateResult) error {
		var mapErr error
		result.Before, mapErr = updateDetailFromStore(stored.BeforeSource, stored.BeforeTokens)
		if mapErr != nil {
			return mapErr
		}
		result.After, mapErr = updateDetailFromStore(stored.AfterSource, stored.AfterTokens)
		return mapErr
	})
	if err != nil {
		switch {
		case errors.Is(err, port.ErrManagementExternalIntegrationSourceNotFound):
			return UpdateResult{}, ErrNotFound
		case errors.Is(err, port.ErrManagementExternalIntegrationSourceBuiltInUpdateRestricted):
			return UpdateResult{}, ErrBuiltInUpdateRestricted
		case errors.Is(err, port.ErrManagementExternalIntegrationSourceNameExists):
			return UpdateResult{}, ErrNameExists
		default:
			return UpdateResult{}, err
		}
	}

	result.Committed = true
	return result, nil
}

func normalizeUpdateInput(input UpdateInput) (port.ManagementExternalIntegrationSourceUpdateInput, error) {
	output := port.ManagementExternalIntegrationSourceUpdateInput{
		SourceID:      trimECMAScriptWhitespace(input.SourceID),
		HasName:       input.HasName,
		HasStatus:     input.HasStatus,
		HasScopes:     input.HasScopes,
		HasRateLimits: input.HasRateLimits,
		HasExpiresAt:  input.HasExpiresAt,
		HasNotes:      input.HasNotes,
	}
	if output.SourceID == "" {
		return output, newUpdateValidationError(ErrUpdateInvalid)
	}
	var err error
	if input.HasName {
		output.Name = trimECMAScriptWhitespace(input.Name)
		if output.Name == "" {
			return output, newUpdateValidationError(errors.New("来源系统名称不能为空"))
		}
		if utf16LengthOf(output.Name) > 80 {
			return output, newUpdateValidationError(errors.New("来源系统名称不能超过 80 个字符"))
		}
	}
	if input.HasStatus {
		if input.Status != publicapi.SourceStatusActive && input.Status != publicapi.SourceStatusDisabled {
			return output, newUpdateValidationError(errors.New("来源系统状态无效"))
		}
		output.Status = input.Status
	}
	if input.HasScopes {
		output.ScopesJSON, err = normalizeUpdateScopes(input.Scopes)
		if err != nil {
			return output, newUpdateValidationError(err)
		}
	}
	if input.HasRateLimits {
		output.RateLimitsJSON, err = normalizeUpdateRateLimits(input.RateLimits)
		if err != nil {
			return output, newUpdateValidationError(err)
		}
	}
	if input.HasExpiresAt {
		output.ExpiresAt, err = normalizeUpdateExpiresAt(input.ExpiresAt)
		if err != nil {
			return output, newUpdateValidationError(err)
		}
	}
	if input.HasNotes {
		output.Notes, err = normalizeUpdateNotes(input.Notes)
		if err != nil {
			return output, newUpdateValidationError(err)
		}
	}
	return output, nil
}

func normalizeUpdateScopes(value any) (string, error) {
	items, ok := value.([]any)
	if !ok {
		return "", errors.New("来源系统 scopes 必须是字符串数组")
	}
	seen := make(map[string]struct{}, len(items))
	scopes := make([]string, 0, len(items))
	for _, item := range items {
		scope, ok := item.(string)
		if !ok {
			return "", errors.New("来源系统 scopes 必须是字符串数组")
		}
		scope = trimECMAScriptWhitespace(scope)
		if scope == "" {
			return "", errors.New("来源系统 scopes 不能为空")
		}
		if _, ok := supportedScopes[scope]; !ok {
			return "", fmt.Errorf("来源系统 scope 不受支持：%s", scope)
		}
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	encoded, err := json.Marshal(scopes)
	return string(encoded), err
}

func normalizeUpdateRateLimits(value any) (string, error) {
	items, ok := value.([]any)
	if !ok {
		return "", errors.New("来源系统限频规则必须是数组")
	}
	if len(items) > 8 {
		return "", errors.New("来源系统限频规则最多 8 条")
	}
	rules := make([]RateLimitRule, 0, len(items))
	seen := make(map[int]struct{}, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return "", errors.New("来源系统限频规则必须是对象")
		}
		if len(record) != 2 {
			return "", errors.New("来源系统限频规则只能包含 windowSeconds 和 maxRequests")
		}
		windowRaw, hasWindow := record["windowSeconds"]
		requestsRaw, hasRequests := record["maxRequests"]
		if !hasWindow || !hasRequests {
			return "", errors.New("来源系统限频规则只能包含 windowSeconds 和 maxRequests")
		}
		window, err := updateInteger(windowRaw, 1, 86_400, "来源系统限频窗口")
		if err != nil {
			return "", err
		}
		requests, err := updateInteger(requestsRaw, 1, 100_000, "来源系统限频次数")
		if err != nil {
			return "", err
		}
		if _, exists := seen[window]; exists {
			return "", errors.New("来源系统限频窗口不能重复")
		}
		seen[window] = struct{}{}
		rules = append(rules, RateLimitRule{WindowSeconds: window, MaxRequests: requests})
	}
	sort.Slice(rules, func(i int, j int) bool { return rules[i].WindowSeconds < rules[j].WindowSeconds })
	encoded, err := json.Marshal(rules)
	return string(encoded), err
}

func updateInteger(value any, minimum int, maximum int, label string) (int, error) {
	var numeric float64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(typed.String(), 64)
		if err != nil {
			return 0, fmt.Errorf("%s必须是整数", label)
		}
		numeric = parsed
	case float64:
		numeric = typed
	case int:
		numeric = float64(typed)
	case int64:
		numeric = float64(typed)
	default:
		return 0, fmt.Errorf("%s必须是整数", label)
	}
	if math.IsInf(numeric, 0) || math.IsNaN(numeric) || math.Trunc(numeric) != numeric {
		return 0, fmt.Errorf("%s必须是整数", label)
	}
	if numeric < float64(minimum) || numeric > float64(maximum) {
		return 0, fmt.Errorf("%s必须在 %d 到 %d 之间", label, minimum, maximum)
	}
	return int(numeric), nil
}

func normalizeUpdateExpiresAt(value any) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, errors.New("过期时间无效")
	}
	text = trimECMAScriptWhitespace(text)
	match := updateServerDateTimePattern.FindStringSubmatch(text)
	if match == nil {
		return nil, errors.New("过期时间无效")
	}
	layout := "2006-01-02T15:04:05Z"
	if match[7] != "" {
		layout = javaScriptISOStringLayout
	}
	parsed, err := time.Parse(layout, text)
	if err != nil {
		return nil, errors.New("过期时间无效")
	}
	parsed = parsed.UTC().Truncate(time.Millisecond)
	return &parsed, nil
}

func normalizeUpdateNotes(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, errors.New("备注必须是字符串")
	}
	text = trimECMAScriptWhitespace(text)
	if utf16LengthOf(text) > 500 {
		return nil, errors.New("备注不能超过 500 个字符")
	}
	if text == "" {
		return nil, nil
	}
	return &text, nil
}

func updateDetailFromStore(
	sourceRow port.ManagementExternalIntegrationSourceListRow,
	tokenRows []port.ManagementExternalIntegrationSourcePrimaryTokenRow,
) (Detail, error) {
	source, err := sourceFromStore(sourceRow)
	if err != nil {
		return Detail{}, err
	}
	return detailFromSourceAndTokenRows(source, tokenRows)
}

func utf16LengthOf(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func newUpdateValidationError(err error) error {
	if err == nil || IsUpdateValidationError(err) {
		return err
	}
	return updateValidationError{cause: err}
}
