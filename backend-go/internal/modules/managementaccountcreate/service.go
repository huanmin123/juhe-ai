package managementaccountcreate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/accountpagedata"
	"juhe-ai/backend-go/internal/store/port"
)

const GatewayRuntimeReason = "account_created"

var (
	ErrInvalid         = errors.New("账户参数无效")
	ErrProviderInvalid = errors.New("账户供应商或协议档案不可用")
	ErrGroupInvalid    = errors.New("账户分组无效")
)

type CredentialCodec interface {
	EncryptJSON(map[string]any) (string, error)
}
type AccountLookupInvalidator interface {
	InvalidateAccountLookupCache(context.Context, string) error
}
type GroupAccountIDsInvalidator interface{ InvalidateGroupAccountIDsCache(context.Context) error }
type GatewayRuntimeInvalidator interface {
	InvalidateGatewayRuntime(context.Context, string) error
}

type Options struct {
	Store                      port.ManagementAccountCreator
	CredentialCodec            CredentialCodec
	GranteeReader              accountpagedata.GranteeReader
	PageDataPublisher          accountpagedata.Publisher
	AccountLookupInvalidator   AccountLookupInvalidator
	GroupAccountIDsInvalidator GroupAccountIDsInvalidator
	GatewayRuntimeInvalidator  GatewayRuntimeInvalidator
	Logger                     *slog.Logger
	Now                        func() time.Time
}

type Service struct {
	opts   Options
	logger *slog.Logger
	now    func() time.Time
}

type Input struct {
	ActorSystemAccountID                       string
	ActorRole                                  string
	SystemAccountID                            string
	SelfOnly                                   bool
	ProviderCode                               string
	ProviderProtocolProfileID                  string
	Name                                       string
	Type                                       string
	Credentials                                map[string]any
	SupportedModels                            []string
	HealthCheckModel                           string
	HealthCheckEndpointMode                    string
	Status                                     string
	ConcurrencyLimit                           int
	Priority                                   int
	SuperPriorityEnabled                       bool
	FallbackEnabled                            bool
	ProxyProfileID                             string
	Schedulable                                *bool
	GroupID                                    string
	AccountExpiresAt                           *time.Time
	AvailabilitySchedule                       map[string]any
	TemporaryUnavailableContinuousProbeEnabled bool
	Notes                                      string
}

func NewService(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{opts: opts, logger: logger, now: now}
}

func (s *Service) Create(ctx context.Context, input Input) (map[string]any, error) {
	if s.opts.Store == nil || s.opts.CredentialCodec == nil {
		return nil, fmt.Errorf("management account create dependencies are required")
	}
	owner := strings.TrimSpace(input.SystemAccountID)
	actor := strings.TrimSpace(input.ActorSystemAccountID)
	if input.SelfOnly || !isAdmin(input.ActorRole) {
		owner = actor
	}
	if owner == "" || actor == "" || strings.TrimSpace(input.ProviderCode) == "" || strings.TrimSpace(input.ProviderProtocolProfileID) == "" || strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Type) == "" || len(input.Credentials) == 0 {
		return nil, ErrInvalid
	}
	if input.SuperPriorityEnabled && input.FallbackEnabled {
		return nil, ErrInvalid
	}
	status := strings.TrimSpace(input.Status)
	if status == "" || status == "active" || status == "pending_test" {
		status = "pending_test"
	} else if status != "disabled" {
		return nil, ErrInvalid
	}
	if input.ConcurrencyLimit == 0 {
		input.ConcurrencyLimit = 20
	}
	if input.ConcurrencyLimit < 1 || input.ConcurrencyLimit > 100000 || input.Priority < 0 {
		return nil, ErrInvalid
	}
	if input.HealthCheckEndpointMode == "" {
		input.HealthCheckEndpointMode = defaultEndpoint(input.ProviderCode)
	}
	if input.TemporaryUnavailableContinuousProbeEnabled == false {
		input.TemporaryUnavailableContinuousProbeEnabled = true
	}
	if input.Schedulable == nil {
		value := true
		input.Schedulable = &value
	}
	if input.AvailabilitySchedule != nil {
		if _, err := json.Marshal(input.AvailabilitySchedule); err != nil {
			return nil, ErrInvalid
		}
	}
	encrypted, err := s.opts.CredentialCodec.EncryptJSON(input.Credentials)
	if err != nil {
		return nil, fmt.Errorf("encrypt account credentials: %w", err)
	}
	credentialJSON, _ := json.Marshal(input.Credentials)
	fingerprint := sha256.Sum256(credentialJSON)
	accountID := "acct_" + hex.EncodeToString(fingerprint[:])[:24] + "_" + fmt.Sprint(s.now().UnixNano())
	now := s.now().UTC()
	var schedule *string
	if input.AvailabilitySchedule != nil {
		value, _ := json.Marshal(input.AvailabilitySchedule)
		text := string(value)
		schedule = &text
	}
	result, err := s.opts.Store.CreateManagementAccount(ctx, port.ManagementAccountCreateInput{
		ID: accountID, SystemAccountID: owner, ProviderCode: strings.TrimSpace(input.ProviderCode), ProviderProtocolProfileID: strings.TrimSpace(input.ProviderProtocolProfileID), Name: strings.TrimSpace(input.Name), Type: strings.TrimSpace(input.Type), Status: status, CredentialsEncrypted: encrypted, CredentialFingerprint: hex.EncodeToString(fingerprint[:]), HealthCheckModel: strings.TrimSpace(input.HealthCheckModel), HealthCheckEndpointMode: input.HealthCheckEndpointMode, ConcurrencyLimit: input.ConcurrencyLimit, Priority: input.Priority, SuperPriorityEnabled: input.SuperPriorityEnabled, FallbackEnabled: input.FallbackEnabled, Schedulable: *input.Schedulable, SupportedModels: input.SupportedModels, GroupID: strings.TrimSpace(input.GroupID), ProxyProfileID: strings.TrimSpace(input.ProxyProfileID), AccountExpiresAt: input.AccountExpiresAt, AvailabilityScheduleJSON: schedule, TemporaryUnavailableContinuousProbe: 1, Notes: optionalText(input.Notes), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return nil, mapCreateError(err)
	}
	s.afterCommit(ctx, result.Account)
	return result.Account, nil
}

func mapCreateError(err error) error {
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "provider") || strings.Contains(text, "profile") {
		return ErrProviderInvalid
	}
	if strings.Contains(text, "group") {
		return ErrGroupInvalid
	}
	return err
}

func (s *Service) afterCommit(ctx context.Context, account map[string]any) {
	accountID, _ := account["id"].(string)
	owner, _ := account["systemAccountId"].(string)
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if s.opts.AccountLookupInvalidator != nil {
		_ = s.opts.AccountLookupInvalidator.InvalidateAccountLookupCache(postCtx, accountID)
	}
	if s.opts.GroupAccountIDsInvalidator != nil {
		_ = s.opts.GroupAccountIDsInvalidator.InvalidateGroupAccountIDsCache(postCtx)
	}
	if s.opts.GatewayRuntimeInvalidator != nil {
		_ = s.opts.GatewayRuntimeInvalidator.InvalidateGatewayRuntime(postCtx, GatewayRuntimeReason)
	}
	if s.opts.PageDataPublisher == nil {
		return
	}
	owners, allScopes, err := accountpagedata.ResolveOwners(postCtx, s.opts.GranteeReader, accountID, []string{owner})
	if err != nil {
		s.logger.WarnContext(postCtx, "账户创建后页面数据 owner 查询失败", "accountId", accountID, "error", err)
	}
	_ = s.opts.PageDataPublisher.PublishAccountStaticChange(postCtx, accountpagedata.ChangeInput{AccountID: accountID, Operation: accountpagedata.OperationUpsert, OwnerSystemAccountIDs: owners, AllScopes: allScopes, FieldMask: []string{"id", "name", "status", "boundGroupId"}, MembershipChanged: true, OrderChanged: true, FilterChanged: true, PageChanged: true})
}

func optionalText(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
func isAdmin(role string) bool {
	return strings.TrimSpace(role) == "admin" || strings.TrimSpace(role) == "super_admin"
}
func defaultEndpoint(provider string) string {
	if strings.EqualFold(provider, "anthropic") {
		return "messages_json"
	}
	if strings.EqualFold(provider, "gemini") {
		return "generate_content_json"
	}
	return "chat_json"
}
