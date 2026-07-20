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
	Available bool   `json:"available"`
	Status    string `json:"status"`
	Label     string `json:"label"`
	Color     string `json:"color"`
}

type Item struct {
	ID                                   string                `json:"id"`
	SystemAccountID                      string                `json:"systemAccountId"`
	Name                                 string                `json:"name"`
	Status                               string                `json:"status"`
	Schedulable                          bool                  `json:"schedulable"`
	AccountExpiresAt                     string                `json:"accountExpiresAt,omitempty"`
	CooldownUntil                        string                `json:"cooldownUntil,omitempty"`
	LastErrorCode                        string                `json:"lastErrorCode,omitempty"`
	LastErrorMessage                     string                `json:"lastErrorMessage,omitempty"`
	LastErrorTraceID                     string                `json:"lastErrorTraceId,omitempty"`
	LastHealthCheckAt                    string                `json:"lastHealthCheckAt,omitempty"`
	NextHealthCheckAt                    string                `json:"nextHealthCheckAt,omitempty"`
	LastHealthCheckStatusCode            int                   `json:"lastHealthCheckStatusCode,omitempty"`
	LastHealthCheckErrorCode             string                `json:"lastHealthCheckErrorCode,omitempty"`
	LastHealthCheckErrorMessage          string                `json:"lastHealthCheckErrorMessage,omitempty"`
	LastHealthCheckTraceID               string                `json:"lastHealthCheckTraceId,omitempty"`
	LastUsedAt                           string                `json:"lastUsedAt,omitempty"`
	AuthorizationID                      string                `json:"authorizationId,omitempty"`
	AuthorizationStatus                  string                `json:"authorizationStatus,omitempty"`
	AuthorizationExpiresAt               string                `json:"authorizationExpiresAt,omitempty"`
	AuthorizationInstanceSourceAccountID string                `json:"authorizationInstanceSourceAccountId,omitempty"`
	BoundGroupID                         string                `json:"boundGroupId,omitempty"`
	BoundGroupName                       string                `json:"boundGroupName,omitempty"`
	GroupBindStatus                      string                `json:"groupBindStatus,omitempty"`
	TodayUsage                           map[string]any        `json:"todayUsage"`
	CurrentConcurrency                   int                   `json:"currentConcurrency"`
	RuntimeAvailability                  any                   `json:"runtimeAvailability,omitempty"`
	AvailabilityPresentation             any                   `json:"availabilityPresentation"`
	EffectiveAvailability                EffectiveAvailability `json:"effectiveAvailability"`
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
	currentConcurrency := make(map[string]int)
	concurrencyAvailable := false
	if s.accountConcurrency != nil {
		accountIDs := make([]string, 0, len(rows))
		for _, row := range rows {
			accountIDs = append(accountIDs, row.ID)
		}
		values, readErr := s.accountConcurrency.LoadAccountCurrentConcurrencyByIDs(ctx, accountIDs, s.now())
		if readErr == nil {
			currentConcurrency = values
			concurrencyAvailable = true
		}
	}
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		item := snapshotItem(row)
		item.CurrentConcurrency = currentConcurrency[row.ID]
		items = append(items, item)
	}
	return Result{
		GeneratedAt: s.now().UTC().Format(time.RFC3339Nano),
		RuntimeSnapshot: RuntimeSnapshot{
			AccountConcurrencyAvailable: concurrencyAvailable,
		},
		Items: items,
	}, nil
}

func normalizeIDs(ids []string) ([]string, error) { return ParseAccountIDs(strings.Join(ids, ",")) }
func isAdmin(role string) bool {
	return strings.TrimSpace(role) == "admin" || strings.TrimSpace(role) == "super_admin"
}

func snapshotItem(row port.ManagementAccountStatusProjection) Item {
	item := Item{ID: row.ID, SystemAccountID: row.SystemAccountID, Name: row.Name, Status: row.Status, Schedulable: row.Schedulable,
		AccountExpiresAt: row.AccountExpiresAt, CooldownUntil: row.CooldownUntil, LastErrorCode: row.LastErrorCode, LastErrorMessage: row.LastErrorMessage,
		LastErrorTraceID: row.LastErrorTraceID, LastHealthCheckAt: row.LastHealthCheckAt, NextHealthCheckAt: row.NextHealthCheckAt,
		LastHealthCheckStatusCode: row.LastHealthCheckStatusCode, LastHealthCheckErrorCode: row.LastHealthCheckErrorCode, LastHealthCheckErrorMessage: row.LastHealthCheckErrorMessage,
		LastHealthCheckTraceID: row.LastHealthCheckTraceID, LastUsedAt: row.LastUsedAt, AuthorizationID: row.AuthorizationID, AuthorizationStatus: row.AuthorizationStatus,
		AuthorizationExpiresAt: row.AuthorizationExpiresAt, AuthorizationInstanceSourceAccountID: row.AuthorizationInstanceSourceAccountID, BoundGroupID: row.BoundGroupID,
		BoundGroupName: row.BoundGroupName, GroupBindStatus: row.GroupBindStatus, TodayUsage: map[string]any{}, AvailabilityPresentation: map[string]any{}}
	if row.TodayUsageJSON != "" {
		_ = json.Unmarshal([]byte(row.TodayUsageJSON), &item.TodayUsage)
	}
	item.EffectiveAvailability = effective(row)
	return item
}

func effective(row port.ManagementAccountStatusProjection) EffectiveAvailability {
	if row.AuthorizationID != "" && row.BoundGroupID == "" {
		return EffectiveAvailability{false, "binding_missing", "未绑定分组", "red"}
	}
	if row.Status == "disabled" {
		return EffectiveAvailability{false, "instance_disabled", "账户停用", "default"}
	}
	if row.Status != "active" {
		return EffectiveAvailability{false, "instance_" + row.Status, "账户" + row.Status, "red"}
	}
	if !row.Schedulable {
		return EffectiveAvailability{false, "instance_unschedulable", "账户停调", "orange"}
	}
	return EffectiveAvailability{true, "available", "可调度", "green"}
}
