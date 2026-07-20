package managementaccountstatussnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	MaxAccountIDs  = 100
	MaxQueryLength = 8192
)

var (
	ErrInvalidAccountIDs = errors.New("账户状态快照至少选择 1 个账户")
	ErrAccountIDsTooMany = errors.New("账户状态快照最多查询 100 个账户")
	ErrQueryTooLong      = errors.New("账户状态快照查询参数过长")
	ErrReaderMissing     = errors.New("账户状态快照读取器未配置")
)

type RuntimeSnapshot struct {
	AccountConcurrencyAvailable         bool `json:"accountConcurrencyAvailable"`
	AccountRuntimeAvailabilityAvailable bool `json:"accountRuntimeAvailabilityAvailable"`
}

type AccountConcurrencyReader interface {
	LoadAccountCurrentConcurrencyByIDs(context.Context, []string, time.Time) (map[string]int, error)
}

type EffectiveAvailability struct {
	Available    bool   `json:"available"`
	Status       string `json:"status"`
	Label        string `json:"label"`
	Color        string `json:"color"`
	BlockerScope string `json:"blockerScope,omitempty"`
	Reason       string `json:"reason,omitempty"`
	RetryAt      string `json:"retryAt,omitempty"`
}

type StatusBoundary struct {
	At   string `json:"at"`
	Kind string `json:"kind"`
}

type AvailabilityPresentation struct {
	Status         string          `json:"status"`
	Label          string          `json:"label"`
	Reason         string          `json:"reason,omitempty"`
	Action         string          `json:"action"`
	StatusBoundary *StatusBoundary `json:"statusBoundary,omitempty"`
}

type Item struct {
	ID                                                 string                   `json:"id"`
	SystemAccountID                                    string                   `json:"systemAccountId"`
	Name                                               string                   `json:"name"`
	Status                                             string                   `json:"status"`
	Schedulable                                        bool                     `json:"schedulable"`
	AccountExpiresAt                                   string                   `json:"accountExpiresAt,omitempty"`
	CooldownUntil                                      string                   `json:"cooldownUntil,omitempty"`
	LastErrorCode                                      string                   `json:"lastErrorCode,omitempty"`
	LastErrorMessage                                   string                   `json:"lastErrorMessage,omitempty"`
	LastErrorTraceID                                   string                   `json:"lastErrorTraceId,omitempty"`
	LastHealthCheckAt                                  string                   `json:"lastHealthCheckAt,omitempty"`
	NextHealthCheckAt                                  string                   `json:"nextHealthCheckAt,omitempty"`
	LastHealthCheckStatusCode                          int                      `json:"lastHealthCheckStatusCode,omitempty"`
	LastHealthCheckErrorCode                           string                   `json:"lastHealthCheckErrorCode,omitempty"`
	LastHealthCheckErrorMessage                        string                   `json:"lastHealthCheckErrorMessage,omitempty"`
	LastHealthCheckTraceID                             string                   `json:"lastHealthCheckTraceId,omitempty"`
	LastUsedAt                                         string                   `json:"lastUsedAt,omitempty"`
	AuthorizationID                                    string                   `json:"authorizationId,omitempty"`
	AuthorizationStatus                                string                   `json:"authorizationStatus,omitempty"`
	AuthorizationExpiresAt                             string                   `json:"authorizationExpiresAt,omitempty"`
	AuthorizationInstanceSourceAccountID               string                   `json:"authorizationInstanceSourceAccountId,omitempty"`
	AuthorizationInstanceSourceAccountStatus           string                   `json:"authorizationInstanceSourceAccountStatus,omitempty"`
	AuthorizationInstanceSourceAccountSchedulable      *bool                    `json:"authorizationInstanceSourceAccountSchedulable,omitempty"`
	AuthorizationInstanceSourceAccountExpiresAt        string                   `json:"authorizationInstanceSourceAccountExpiresAt,omitempty"`
	AuthorizationInstanceSourceAccountCooldownUntil    string                   `json:"authorizationInstanceSourceAccountCooldownUntil,omitempty"`
	AuthorizationInstanceSourceAccountLastErrorCode    string                   `json:"authorizationInstanceSourceAccountLastErrorCode,omitempty"`
	AuthorizationInstanceSourceAccountLastErrorMessage string                   `json:"authorizationInstanceSourceAccountLastErrorMessage,omitempty"`
	AuthorizationInstanceSourceAccountLastErrorTraceID string                   `json:"authorizationInstanceSourceAccountLastErrorTraceId,omitempty"`
	BoundGroupID                                       string                   `json:"boundGroupId,omitempty"`
	BoundGroupName                                     string                   `json:"boundGroupName,omitempty"`
	GroupBindStatus                                    string                   `json:"groupBindStatus,omitempty"`
	TodayUsage                                         map[string]any           `json:"todayUsage"`
	CurrentConcurrency                                 int                      `json:"currentConcurrency"`
	RuntimeAvailability                                any                      `json:"runtimeAvailability,omitempty"`
	AvailabilityPresentation                           AvailabilityPresentation `json:"availabilityPresentation"`
	EffectiveAvailability                              EffectiveAvailability    `json:"effectiveAvailability"`
}

type Result struct {
	GeneratedAt     string          `json:"generatedAt"`
	RuntimeSnapshot RuntimeSnapshot `json:"runtimeSnapshot"`
	Items           []Item          `json:"items"`
}

type Input struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	AccountIDs           []string
}

type Service struct {
	reader             port.ManagementAccountStatusSnapshotReader
	accountConcurrency AccountConcurrencyReader
	now                func() time.Time
}

type ServiceOptions struct {
	Reader             port.ManagementAccountStatusSnapshotReader
	AccountConcurrency AccountConcurrencyReader
	Now                func() time.Time
}

func NewService(reader port.ManagementAccountStatusSnapshotReader) *Service {
	return NewServiceWithOptions(ServiceOptions{Reader: reader})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{reader: opts.Reader, accountConcurrency: opts.AccountConcurrency, now: now}
}

func ParseAccountIDs(raw string) ([]string, error) {
	if len(raw) > MaxQueryLength {
		return nil, ErrQueryTooLong
	}
	seen := make(map[string]struct{})
	ids := make([]string, 0, MaxAccountIDs)
	for _, value := range strings.Split(raw, ",") {
		id := strings.TrimSpace(value)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, ErrInvalidAccountIDs
	}
	if len(ids) > MaxAccountIDs {
		return nil, ErrAccountIDsTooMany
	}
	return ids, nil
}

func (s *Service) Get(ctx context.Context, input Input) (Result, error) {
	if s == nil || s.reader == nil {
		return Result{}, ErrReaderMissing
	}
	if strings.TrimSpace(input.ActorSystemAccountID) == "" {
		return Result{}, errors.New("未登录")
	}
	if !input.SelfOnly && !isAdmin(input.ActorRole) {
		return Result{}, errors.New("需要管理员权限")
	}
	ids, err := normalizeIDs(input.AccountIDs)
	if err != nil {
		return Result{}, err
	}
	scope := strings.TrimSpace(input.SystemAccountID)
	if input.SelfOnly {
		scope = strings.TrimSpace(input.ActorSystemAccountID)
	}
	rows, err := s.reader.ListManagementAccountStatusProjections(ctx, port.ManagementAccountStatusSnapshotInput{AccountIDs: ids, SystemAccountID: scope})
	if err != nil {
		return Result{}, err
	}
	now := s.now()
	currentConcurrency := make(map[string]int)
	concurrencyAvailable := false
	if s.accountConcurrency != nil {
		accountIDs := make([]string, 0, len(rows))
		for _, row := range rows {
			accountIDs = append(accountIDs, concurrencyAccountID(row))
		}
		values, readErr := s.accountConcurrency.LoadAccountCurrentConcurrencyByIDs(ctx, accountIDs, now)
		if readErr == nil {
			currentConcurrency = values
			concurrencyAvailable = true
		}
	}
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		item := snapshotItem(row, now)
		item.CurrentConcurrency = currentConcurrency[concurrencyAccountID(row)]
		items = append(items, item)
	}
	return Result{
		GeneratedAt: now.UTC().Format(time.RFC3339Nano),
		RuntimeSnapshot: RuntimeSnapshot{
			AccountConcurrencyAvailable: concurrencyAvailable,
		},
		Items: items,
	}, nil
}

func concurrencyAccountID(row port.ManagementAccountStatusProjection) string {
	if sourceID := strings.TrimSpace(row.AuthorizationInstanceSourceAccountID); sourceID != "" {
		return sourceID
	}
	return row.ID
}

func normalizeIDs(ids []string) ([]string, error) { return ParseAccountIDs(strings.Join(ids, ",")) }
func isAdmin(role string) bool {
	return strings.TrimSpace(role) == "admin" || strings.TrimSpace(role) == "super_admin"
}

func snapshotItem(row port.ManagementAccountStatusProjection, now time.Time) Item {
	item := Item{ID: row.ID, SystemAccountID: row.SystemAccountID, Name: row.Name, Status: row.Status, Schedulable: row.Schedulable,
		AccountExpiresAt: row.AccountExpiresAt, CooldownUntil: row.CooldownUntil, LastErrorCode: row.LastErrorCode, LastErrorMessage: row.LastErrorMessage,
		LastErrorTraceID: row.LastErrorTraceID, LastHealthCheckAt: row.LastHealthCheckAt, NextHealthCheckAt: row.NextHealthCheckAt,
		LastHealthCheckStatusCode: row.LastHealthCheckStatusCode, LastHealthCheckErrorCode: row.LastHealthCheckErrorCode, LastHealthCheckErrorMessage: row.LastHealthCheckErrorMessage,
		LastHealthCheckTraceID: row.LastHealthCheckTraceID, LastUsedAt: row.LastUsedAt, AuthorizationID: row.AuthorizationID, AuthorizationStatus: row.AuthorizationStatus,
		AuthorizationExpiresAt: row.AuthorizationExpiresAt, AuthorizationInstanceSourceAccountID: row.AuthorizationInstanceSourceAccountID,
		AuthorizationInstanceSourceAccountStatus:           row.AuthorizationInstanceSourceAccountStatus,
		AuthorizationInstanceSourceAccountExpiresAt:        row.AuthorizationInstanceSourceExpiresAt,
		AuthorizationInstanceSourceAccountCooldownUntil:    row.AuthorizationInstanceSourceCooldownUntil,
		AuthorizationInstanceSourceAccountLastErrorCode:    row.AuthorizationInstanceSourceLastErrorCode,
		AuthorizationInstanceSourceAccountLastErrorMessage: row.AuthorizationInstanceSourceLastErrorMessage,
		AuthorizationInstanceSourceAccountLastErrorTraceID: row.AuthorizationInstanceSourceLastErrorTraceID,
		BoundGroupID: row.BoundGroupID, BoundGroupName: row.BoundGroupName, GroupBindStatus: row.GroupBindStatus, TodayUsage: map[string]any{}}
	if row.AuthorizationInstanceSourceAccountID != "" && row.AuthorizationInstanceSourceAccountStatus != "" {
		schedulable := row.AuthorizationInstanceSourceSchedulable
		item.AuthorizationInstanceSourceAccountSchedulable = &schedulable
	}
	if row.TodayUsageJSON != "" {
		_ = json.Unmarshal([]byte(row.TodayUsageJSON), &item.TodayUsage)
	}
	item.EffectiveAvailability = effective(row, now)
	item.AvailabilityPresentation = availabilityPresentation(item.EffectiveAvailability, row)
	return item
}

func effective(row port.ManagementAccountStatusProjection, now time.Time) EffectiveAvailability {
	if row.AuthorizationID != "" && row.BoundGroupID == "" {
		return blocked("binding_missing", "未绑定分组", "red", "binding", "授权账户需要先绑定到你的分组", "")
	}
	if row.AuthorizationID != "" && row.GroupBindStatus == "authorization_unavailable" {
		return blocked("authorization_unavailable", "授权已失效", "red", "binding", "当前分组绑定的授权已失效，请重新绑定分组或联系授权人", "")
	}
	if row.AuthorizationID != "" {
		if row.AuthorizationStatus == "expired" || expiredAt(row.AuthorizationExpiresAt, now) {
			return blocked("authorization_expired", "授权到期", "red", "authorization", "授权已到期，当前账户不能调用", "")
		}
		if row.AuthorizationStatus == "paused" {
			return blocked("authorization_paused", "授权暂停", "orange", "authorization", "授权已暂停，当前账户不能调用", "")
		}
		if row.AuthorizationStatus == "revoked" || row.AuthorizationStatus == "returned" {
			return blocked("authorization_unavailable", "授权已失效", "red", "authorization", "授权关系已失效，当前账户不能调用", "")
		}
		if row.AuthorizationInstanceSourceAccountID == "" || row.AuthorizationInstanceSourceAccountStatus == "" {
			return blocked("source_deleted", "来源缺失", "red", "source_account", "授权方原账户不存在或已删除，当前账户不能调用", "")
		}
		if row.AuthorizationInstanceSourceLastErrorCode == "account_expired" || expiredAt(row.AuthorizationInstanceSourceExpiresAt, now) {
			return blocked("source_expired", "来源到期", "red", "source_account", "授权方原账户已到期，当前账户不能调用", "")
		}
		sourceStatus := row.AuthorizationInstanceSourceAccountStatus
		switch sourceStatus {
		case "disabled":
			return blocked("source_disabled", "来源停用", "red", "source_account", "授权方原账户已停用，当前账户不能调用", "")
		case "pending_test":
			return blocked("source_pending_test", "来源待检查", "blue", "source_account", "授权方原账户尚未通过后台健康检查，当前账户不能调用", "")
		case "error":
			return blocked("source_error", "来源异常", "red", "source_account", fallback(row.AuthorizationInstanceSourceLastErrorMessage, "授权方原账户处于异常状态，当前账户不能调用"), "")
		case "rate_limited":
			return blocked("source_rate_limited", "来源限流中", "orange", "source_account", fallback(row.AuthorizationInstanceSourceLastErrorMessage, "授权方原账户限流中，当前账户不能调用"), "")
		case "temporary_unavailable":
			return blocked("source_temporary_unavailable", "来源临时不可调用", "gold", "source_account", fallback(row.AuthorizationInstanceSourceLastErrorMessage, "授权方原账户临时不可调用，当前账户不能调用"), "")
		}
		if futureAt(row.AuthorizationInstanceSourceCooldownUntil, now) {
			return blocked("source_cooldown", "来源冷却", "gold", "source_account", "授权方原账户正在冷却，恢复前当前账户不能调用", row.AuthorizationInstanceSourceCooldownUntil)
		}
		if !row.AuthorizationInstanceSourceSchedulable {
			return blocked("source_unschedulable", "来源停调", "orange", "source_account", "授权方原账户已关闭调度，当前账户不能调用", "")
		}
	}
	instanceLabel, reasonPrefix, blockerScope := "账户", "账户", "account"
	if row.AuthorizationID != "" {
		instanceLabel, reasonPrefix, blockerScope = "授权实例", "授权账户", "authorized_instance"
	}
	if row.LastErrorCode == "account_expired" || expiredAt(row.AccountExpiresAt, now) {
		return blocked("instance_expired", instanceLabel+"到期", "red", blockerScope, reasonPrefix+"已到期，当前不可用", "")
	}
	switch row.Status {
	case "disabled":
		return blocked("instance_disabled", instanceLabel+"停用", "default", blockerScope, reasonPrefix+"已停用，当前不可用", "")
	case "pending_test":
		reason := reasonPrefix + "正在等待后台健康检查，检查通过前不会参与调度"
		color := "blue"
		label := instanceLabel + "待检查"
		if row.LastHealthCheckAt != "" && (row.LastHealthCheckErrorCode != "" || row.LastHealthCheckErrorMessage != "") {
			reason, color, label = reasonPrefix+"后台健康检查未通过，系统将自动重试", "red", instanceLabel+"检查失败"
		}
		return blocked("instance_pending_test", label, color, blockerScope, reason, "")
	case "error":
		return blocked("instance_error", instanceLabel+"异常", "red", blockerScope, fallback(row.LastErrorMessage, reasonPrefix+"处于异常状态，当前不可用"), "")
	case "rate_limited":
		return blocked("instance_rate_limited", instanceLabel+"限流中", "orange", blockerScope, fallback(row.LastErrorMessage, reasonPrefix+"限流中，恢复前不会参与调度"), "")
	case "temporary_unavailable":
		return blocked("instance_temporary_unavailable", instanceLabel+"临时不可调用", "gold", blockerScope, fallback(row.LastErrorMessage, reasonPrefix+"临时不可调用，恢复前不会参与调度"), "")
	}
	if futureAt(row.CooldownUntil, now) {
		return blocked("instance_cooldown", instanceLabel+"冷却", "gold", blockerScope, reasonPrefix+"正在冷却，恢复前不会参与调度", row.CooldownUntil)
	}
	if !row.Schedulable {
		return blocked("instance_unschedulable", instanceLabel+"停调", "orange", blockerScope, reasonPrefix+"暂时不可调用，恢复前不会参与调度", "")
	}
	return EffectiveAvailability{Available: true, Status: "available", Label: "可调度", Color: "green"}
}

func blocked(status, label, color, scope, reason, retryAt string) EffectiveAvailability {
	return EffectiveAvailability{Available: false, Status: status, Label: label, Color: color, BlockerScope: scope, Reason: reason, RetryAt: retryAt}
}

func availabilityPresentation(effective EffectiveAvailability, row port.ManagementAccountStatusProjection) AvailabilityPresentation {
	status, action := presentationStatusAction(effective.Status)
	result := AvailabilityPresentation{Status: status, Label: effective.Label, Reason: effective.Reason, Action: action}
	switch effective.Status {
	case "authorization_expired":
		result.StatusBoundary = boundary(row.AuthorizationExpiresAt, "authorization_expired")
	case "source_expired":
		result.StatusBoundary = boundary(row.AuthorizationInstanceSourceExpiresAt, "source_expired")
	case "instance_expired":
		result.StatusBoundary = boundary(row.AccountExpiresAt, "account_expired")
	case "source_cooldown":
		result.StatusBoundary = boundary(row.AuthorizationInstanceSourceCooldownUntil, "cooldown_expiry")
	case "instance_cooldown":
		result.StatusBoundary = boundary(row.CooldownUntil, "cooldown_expiry")
	}
	return result
}

func presentationStatusAction(status string) (string, string) {
	switch status {
	case "available":
		return "available", "none"
	case "binding_missing":
		return "binding_missing", "bind_group"
	case "authorization_expired", "instance_expired":
		return "expired", "renew_authorization"
	case "source_expired":
		return "expired", "contact_authorizer"
	case "authorization_paused", "authorization_unavailable":
		return "authorization_blocked", "contact_authorizer"
	case "source_disabled", "source_unschedulable", "source_deleted":
		return "source_blocked", "contact_authorizer"
	case "source_pending_test":
		return "pending_check", "contact_authorizer"
	case "instance_pending_test":
		return "pending_check", "retry_check"
	case "source_rate_limited":
		return "rate_limited", "contact_authorizer"
	case "instance_rate_limited":
		return "rate_limited", "restore_account"
	case "source_temporary_unavailable", "source_cooldown":
		return "temporarily_unavailable", "contact_authorizer"
	case "instance_temporary_unavailable", "instance_cooldown":
		return "temporarily_unavailable", "restore_account"
	case "instance_disabled", "instance_unschedulable":
		return "disabled", "enable_account"
	case "source_error":
		return "error", "contact_authorizer"
	default:
		return "error", "fix_configuration"
	}
}

func boundary(at, kind string) *StatusBoundary {
	if strings.TrimSpace(at) == "" {
		return nil
	}
	return &StatusBoundary{At: at, Kind: kind}
}

func expiredAt(value string, now time.Time) bool {
	parsed, ok := parseStoredTime(value)
	return ok && !parsed.After(now)
}
func futureAt(value string, now time.Time) bool {
	parsed, ok := parseStoredTime(value)
	return ok && parsed.After(now)
}
func parseStoredTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}
func fallback(value, fallbackValue string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallbackValue
}
