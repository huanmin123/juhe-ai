package publicaccounts

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	AccountTypeAPIKey = "api_key"

	StatusActive      = "active"
	StatusPendingTest = "pending_test"
	StatusDisabled    = "disabled"

	DefaultConcurrencyLimit                   = 20
	DefaultPriority                           = 0
	DefaultClientCompat                       = "openai_standard"
	defaultGroupType                          = "personal"
	defaultTargetDescription                  = "由公开接口自动创建"
	defaultTargetPassword                     = "go-public-auto-created-target-password-hash"
	defaultCredentialSecret                   = "juhe-ai-go-development-secret"
	invalidSupportedModelsRequiredMessage     = "账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型"
	invalidHealthCheckModelRequiredMessage    = "账户检查模型不能为空"
	invalidHealthCheckModelUnsupportedMessage = "账户检查模型必须属于账户支持模型"
	providerModelsRequiredMessage             = "public account provider models reader is required"
	hybridProviderCode                        = "hybrid"
	accountHealthCheckReasonActivation        = "activation"
	accountHealthCheckReasonConfiguration     = "configuration"
	accountHealthCheckDispatchFailedEvent     = "public_account_health_check_dispatch_failed"
	accountHealthCheckDispatchTimeout         = 2 * time.Second
)

var (
	ErrTargetNotFound                 = errors.New("public account target not found")
	ErrTargetDisabled                 = errors.New("public account target disabled")
	ErrProviderProfileNotFound        = errors.New("public account provider profile not found")
	ErrProviderDisabled               = errors.New("public account provider disabled")
	ErrProviderProfileDisabled        = errors.New("public account provider profile disabled")
	ErrUnsupportedAccountType         = errors.New("public account unsupported type")
	ErrTargetGroupRequired            = errors.New("public account target group required")
	ErrGroupNotFound                  = errors.New("public account group not found")
	ErrGroupProviderMismatch          = errors.New("public account group provider mismatch")
	ErrAccountNotFound                = errors.New("public account not found")
	ErrDuplicateAccountName           = errors.New("public account duplicate name")
	ErrInvalidCredentials             = errors.New("public account invalid credentials")
	ErrInvalidBaseURL                 = errors.New("public account invalid base url")
	ErrInvalidAPIKey                  = errors.New("public account invalid api key")
	ErrInvalidSupportedModels         = errors.New("public account invalid supported models")
	ErrInvalidHealthCheckModel        = errors.New("public account invalid health check model")
	ErrInvalidHealthCheckEndpointMode = errors.New("public account invalid health check endpoint mode")
	ErrInvalidAvailability            = errors.New("public account invalid availability schedule")
	ErrInvalidDispatchField           = errors.New("public account invalid dispatch field")
	ErrInvalidStatusTransition        = errors.New("public account invalid status transition")
	ErrCredentialCodecUnusable        = errors.New("public account credential codec unusable")
)

type Service struct {
	store                      port.PublicAccountStore
	transactor                 port.PublicAccountTransactor
	providerModels             ProviderModelReader
	dispatcher                 AccountHealthCheckDispatcher
	healthCheckDispatchTimeout time.Duration
	logger                     *slog.Logger
	now                        func() time.Time
	newID                      func(prefix string) string
	codec                      CredentialCodec
}

type Options struct {
	Store                      port.PublicAccountStore
	Transactor                 port.PublicAccountTransactor
	ProviderModels             ProviderModelReader
	HealthCheckDispatcher      AccountHealthCheckDispatcher
	HealthCheckDispatchTimeout time.Duration
	Logger                     *slog.Logger
	Now                        func() time.Time
	NewID                      func(prefix string) string
	Codec                      CredentialCodec
	Secret                     string
}

type CredentialCodec interface {
	EncryptJSON(value map[string]any) (string, error)
	DecryptJSON(value string) (map[string]any, error)
}

type ProviderModelReader interface {
	Models(ctx context.Context, input managementprovidermodels.ModelListInput) ([]managementprovidermodels.ModelCatalogItem, error)
}

type AccountHealthCheckDispatcher interface {
	Dispatch(ctx context.Context, accountID string, reason string) error
}

type Target struct {
	Username        string `json:"username"`
	DisplayName     string `json:"displayName"`
	SystemAccountID string `json:"systemAccountId"`
	Created         bool   `json:"created"`
	GroupID         string `json:"groupId,omitempty"`
	GroupName       string `json:"groupName,omitempty"`
	GroupCreated    *bool  `json:"groupCreated,omitempty"`
}

type AccountSummary struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	ProviderCode              string   `json:"providerCode"`
	ProviderProtocolProfileID string   `json:"providerProtocolProfileId,omitempty"`
	ProtocolCode              string   `json:"protocolCode,omitempty"`
	ProtocolVersion           string   `json:"protocolVersion,omitempty"`
	Type                      string   `json:"type"`
	ClientCompatibility       string   `json:"clientCompatibility,omitempty"`
	Status                    string   `json:"status"`
	SupportedModels           []string `json:"supportedModels,omitempty"`
	HealthCheckEndpointMode   string   `json:"-"`
	BoundGroupID              string   `json:"boundGroupId,omitempty"`
	BoundGroupName            string   `json:"boundGroupName,omitempty"`
	Schedulable               bool     `json:"schedulable"`
	AvailabilitySchedule      any      `json:"availabilitySchedule,omitempty"`
	ConcurrencyLimit          *int     `json:"concurrencyLimit,omitempty"`
	Priority                  *int     `json:"priority,omitempty"`
}

type AccountResponse struct {
	Source      string          `json:"source"`
	GeneratedAt string          `json:"generatedAt"`
	Action      string          `json:"action"`
	Target      Target          `json:"target"`
	Account     *AccountSummary `json:"account"`
}

type AccountListResponse struct {
	Source         string           `json:"source"`
	GeneratedAt    string           `json:"generatedAt"`
	Target         Target           `json:"target"`
	Page           int              `json:"page"`
	PageSize       int              `json:"pageSize"`
	PageUpperBound int              `json:"pageUpperBound"`
	HasMore        bool             `json:"hasMore"`
	Items          []AccountSummary `json:"items"`
}

type ListInput struct {
	TargetUsername            string
	TargetGroupName           string
	ProviderCode              string
	ProviderProtocolProfileID string
	GroupID                   string
	Keyword                   string
	Type                      string
	Status                    string
	Schedulable               string
	Page                      int
	PageSize                  int
}

type AddInput struct {
	TargetUsername            string
	TargetDisplayName         string
	TargetGroupName           string
	ProviderCode              string
	ProviderProtocolProfileID string
	Name                      string
	Type                      string
	BaseURL                   string
	APIKey                    string
	SupportedModels           StringListValue
	HealthCheckEndpointMode   string
	Status                    string
	ConcurrencyLimit          *int
	Priority                  *int
	AvailabilitySchedule      JSONValue
	Notes                     *string
}

type UpdateInput struct {
	AccountID                 string
	TargetUsername            *string
	TargetGroupName           *string
	ProviderCode              *string
	ProviderProtocolProfileID *string
	Name                      *string
	Type                      *string
	BaseURL                   *string
	APIKey                    *string
	SupportedModels           StringListValue
	HealthCheckEndpointMode   *string
	Status                    *string
	ConcurrencyLimit          *int
	Priority                  *int
	AvailabilitySchedule      JSONValue
	Notes                     OptionalString
}

type DeleteInput struct {
	AccountID                 string
	TargetUsername            *string
	TargetGroupName           *string
	ProviderCode              *string
	ProviderProtocolProfileID *string
}

type OptionalString struct {
	value *string
	set   bool
}

type JSONValue struct {
	value any
	set   bool
}

type StringListValue struct {
	value []string
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
	codec := opts.Codec
	if codec == nil {
		secret := strings.TrimSpace(opts.Secret)
		if secret == "" {
			secret = defaultCredentialSecret
		}
		codec = newAESGCMCredentialCodec(secret)
	}
	healthCheckDispatchTimeout := opts.HealthCheckDispatchTimeout
	if healthCheckDispatchTimeout <= 0 {
		healthCheckDispatchTimeout = accountHealthCheckDispatchTimeout
	}
	return &Service{
		store:                      opts.Store,
		transactor:                 opts.Transactor,
		providerModels:             opts.ProviderModels,
		dispatcher:                 opts.HealthCheckDispatcher,
		healthCheckDispatchTimeout: healthCheckDispatchTimeout,
		logger:                     opts.Logger,
		now:                        now,
		newID:                      newID,
		codec:                      codec,
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

func NewStringListValue(value []string, set bool) StringListValue {
	return StringListValue{value: value, set: set}
}

func (v StringListValue) Set() bool {
	return v.set
}

func (v StringListValue) Value() []string {
	return append([]string(nil), v.value...)
}

func (s *Service) List(ctx context.Context, input ListInput) (AccountListResponse, error) {
	target, err := s.requireTarget(ctx, input.TargetUsername)
	if err != nil {
		return AccountListResponse{}, err
	}

	profileID := strings.TrimSpace(input.ProviderProtocolProfileID)
	providerCode := strings.TrimSpace(input.ProviderCode)
	if profileID != "" {
		profile, err := s.requireProviderProfile(ctx, s.store, target.ID, providerCode, profileID)
		if err != nil {
			return AccountListResponse{}, err
		}
		profileID = profile.ID
	}

	groupID := strings.TrimSpace(input.GroupID)
	groupName := strings.TrimSpace(input.TargetGroupName)
	if groupName != "" {
		if providerCode == "" {
			return AccountListResponse{}, fmt.Errorf("%w: 按目标分组名称查询账号时必须提供 providerCode", ErrGroupProviderMismatch)
		}
		group, ok, err := s.store.FindExistingPublicAccountGroupByName(ctx, target.ID, providerCode, groupName)
		if err != nil {
			return AccountListResponse{}, err
		}
		if !ok {
			return emptyAccountListResponse(target, input, s.generatedAt()), nil
		}
		groupID = group.ID
	}

	page, err := s.store.ListPublicAccounts(ctx, port.PublicAccountListInput{
		SystemAccountID:           target.ID,
		ProviderCode:              providerCode,
		ProviderProtocolProfileID: profileID,
		GroupID:                   groupID,
		Keyword:                   strings.TrimSpace(input.Keyword),
		Type:                      strings.TrimSpace(input.Type),
		Status:                    normalizeListStatus(input.Status),
		Schedulable:               strings.TrimSpace(input.Schedulable),
		Page:                      input.Page,
		PageSize:                  input.PageSize,
	})
	if err != nil {
		return AccountListResponse{}, err
	}
	items := make([]AccountSummary, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, publicAccountSummary(item, true))
	}
	return AccountListResponse{
		Source:         "stats",
		GeneratedAt:    s.generatedAt(),
		Target:         publicTarget(target, nil),
		Page:           page.Page,
		PageSize:       page.PageSize,
		PageUpperBound: page.PageUpperBound,
		HasMore:        page.HasMore,
		Items:          items,
	}, nil
}

func (s *Service) Add(ctx context.Context, input AddInput) (AccountResponse, error) {
	var response AccountResponse
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		response, err = s.addOnce(ctx, input)
		if publicAccountAddRetryable(err) {
			continue
		}
		if err == nil && response.Account != nil && response.Account.Status == StatusPendingTest {
			s.dispatchAccountHealthCheck(ctx, response.Account.ID, accountHealthCheckReasonActivation)
		}
		return response, err
	}
	return response, err
}

func (s *Service) addOnce(ctx context.Context, input AddInput) (AccountResponse, error) {
	var response AccountResponse
	err := s.inTx(ctx, func(ctx context.Context, store port.PublicAccountStore) error {
		target, targetFound, err := store.FindPublicAccountTargetByUsername(ctx, strings.TrimSpace(input.TargetUsername))
		if err != nil {
			return err
		}
		systemAccountID := ""
		if targetFound {
			systemAccountID = target.ID
		}
		profile, err := s.requireProviderProfile(ctx, store, systemAccountID, input.ProviderCode, input.ProviderProtocolProfileID)
		if err != nil {
			return err
		}
		if !targetFound {
			target, err = s.ensureTarget(ctx, store, input.TargetUsername, input.TargetDisplayName)
			if err != nil {
				return err
			}
		}
		if err := assertTargetActive(target); err != nil {
			return err
		}
		group, err := s.ensureGroup(ctx, store, target.ID, input.ProviderCode, input.TargetGroupName)
		if err != nil {
			return err
		}
		existing, ok, err := store.FindExistingPublicAccountByNameInGroup(ctx, port.PublicAccountNameLookupInput{
			SystemAccountID:           target.ID,
			ProviderCode:              strings.TrimSpace(input.ProviderCode),
			ProviderProtocolProfileID: profile.ID,
			GroupID:                   group.ID,
			Name:                      strings.TrimSpace(input.Name),
		})
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("%w: %s", ErrDuplicateAccountName, existing.Name)
		}

		credential, err := s.encryptedCredentials(input.APIKey, input.BaseURL)
		if err != nil {
			return err
		}
		supportedModels := profile.DefaultSupportedModels
		if input.SupportedModels.Set() {
			supportedModels = input.SupportedModels.Value()
		}
		models, err := normalizeSupportedModels(supportedModels)
		if err != nil {
			return err
		}
		if err := s.validateSupportedModelsInProviderCatalog(ctx, target.ID, input.ProviderCode, models); err != nil {
			return err
		}
		healthCheckModel, err := normalizeAccountHealthCheckModel(profile.DefaultHealthCheckModel, models)
		if err != nil {
			return err
		}
		var requestedHealthCheckEndpointMode *string
		if input.HealthCheckEndpointMode != "" {
			requestedHealthCheckEndpointMode = &input.HealthCheckEndpointMode
		}
		healthCheckEndpointMode, err := resolveHealthCheckEndpointMode(
			requestedHealthCheckEndpointMode,
			input.ProviderCode,
			profile.ID,
			profile.EnabledEndpointModes,
		)
		if err != nil {
			return err
		}
		scheduleJSON, err := normalizeAvailabilityScheduleJSON(input.AvailabilitySchedule)
		if err != nil {
			return err
		}
		status := addAccountStatus(input.Status)
		now := s.now().UTC()
		created, err := store.CreatePublicAccount(ctx, port.PublicAccountCreateInput{
			ID:                        s.newID("acc"),
			SystemAccountID:           target.ID,
			GroupID:                   group.ID,
			ProviderCode:              strings.TrimSpace(input.ProviderCode),
			ProviderProtocolProfileID: profile.ID,
			ProtocolCode:              profile.ProtocolCode,
			ProtocolVersion:           profile.ProtocolVersion,
			Name:                      strings.TrimSpace(input.Name),
			Type:                      AccountTypeAPIKey,
			Status:                    port.PublicAccountStatus(status),
			CredentialsEncrypted:      credential.Encrypted,
			CredentialFingerprint:     credential.Fingerprint,
			CredentialMask:            credential.Mask,
			ClientCompatibility:       DefaultClientCompat,
			SupportedModels:           models,
			HealthCheckModel:          healthCheckModel,
			HealthCheckEndpointMode:   healthCheckEndpointMode,
			Schedulable:               false,
			AvailabilityScheduleJSON:  scheduleJSON,
			ConcurrencyLimit:          intPtrValue(input.ConcurrencyLimit, DefaultConcurrencyLimit),
			Priority:                  intPtrValue(input.Priority, DefaultPriority),
			Notes:                     normalizeOptionalText(input.Notes),
			Now:                       now,
		})
		if errors.Is(err, port.ErrPublicAccountDuplicateName) {
			return fmt.Errorf("%w: %s", ErrDuplicateAccountName, strings.TrimSpace(input.Name))
		}
		if err != nil {
			return err
		}
		response = accountResponse("created", target, &group, created, false, s.generatedAt())
		return nil
	})
	return response, err
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (AccountResponse, error) {
	var response AccountResponse
	var healthCheckAccountID string
	err := s.inTx(ctx, func(ctx context.Context, store port.PublicAccountStore) error {
		current, target, err := s.accountAndTargetForWrite(ctx, store, input.AccountID, input.TargetUsername)
		if err != nil {
			return err
		}
		if current.Type != AccountTypeAPIKey {
			return fmt.Errorf("%w: 公开账号修改仅支持 API Key 账户", ErrUnsupportedAccountType)
		}
		if input.Type != nil && strings.TrimSpace(*input.Type) != AccountTypeAPIKey {
			return fmt.Errorf("%w: 公开账号接口仅支持 API Key 账户", ErrUnsupportedAccountType)
		}
		profile, err := s.requireProviderProfile(
			ctx,
			store,
			current.SystemAccountID,
			current.ProviderCode,
			current.ProviderProtocolProfileID,
		)
		if err != nil {
			return err
		}
		if err := s.assertAccountFilters(ctx, store, current, &profile, input.ProviderCode, input.ProviderProtocolProfileID, input.TargetGroupName); err != nil {
			return err
		}

		next := current
		if input.Name != nil {
			next.Name = strings.TrimSpace(*input.Name)
		}
		if input.Status != nil {
			status, err := updateAccountStatus(current.Status, *input.Status)
			if err != nil {
				return err
			}
			next.Status = port.PublicAccountStatus(status)
			next.Schedulable = status == StatusActive
		}
		if input.ConcurrencyLimit != nil {
			next.ConcurrencyLimit = *input.ConcurrencyLimit
		}
		if input.Priority != nil {
			next.Priority = *input.Priority
		}
		if input.Notes.Set() {
			next.Notes = normalizeOptionalText(input.Notes.Value())
		}
		if input.AvailabilitySchedule.Set() {
			scheduleJSON, err := normalizeAvailabilityScheduleJSON(input.AvailabilitySchedule)
			if err != nil {
				return err
			}
			next.AvailabilityScheduleJSON = scheduleJSON
		}
		connectionConfigurationChanged := false
		if input.APIKey != nil || input.BaseURL != nil {
			credentials, currentAPIKey, currentBaseURL, err := s.currentCredentials(current.CredentialsEncrypted)
			if err != nil {
				return err
			}
			if input.APIKey != nil {
				credentials["api_key"] = *input.APIKey
			}
			if input.BaseURL != nil {
				credentials["base_url"] = *input.BaseURL
			}
			credential, err := s.encryptedCredentialMap(credentials)
			if err != nil {
				return err
			}
			next.CredentialsEncrypted = credential.Encrypted
			next.CredentialFingerprint = credential.Fingerprint
			next.CredentialMask = credential.Mask
			nextAPIKey, _ := credentials["api_key"].(string)
			nextBaseURL, _ := credentials["base_url"].(string)
			connectionConfigurationChanged = nextAPIKey != currentAPIKey || nextBaseURL != currentBaseURL
		}
		if input.SupportedModels.Set() {
			next.SupportedModels = input.SupportedModels.Value()
		}
		models, err := normalizeSupportedModels(next.SupportedModels)
		if err != nil {
			return err
		}
		supportedModelsChanged := input.SupportedModels.Set() && !unorderedStringListsEqual(models, current.SupportedModels)
		if supportedModelsChanged {
			if err := s.validateSupportedModelsInProviderCatalog(ctx, target.ID, current.ProviderCode, models); err != nil {
				return err
			}
		}
		healthCheckModel, err := normalizeAccountHealthCheckModel(current.HealthCheckModel, models)
		if err != nil {
			return err
		}
		next.SupportedModels = models
		next.HealthCheckModel = healthCheckModel
		healthCheckEndpointMode := &current.HealthCheckEndpointMode
		if input.HealthCheckEndpointMode != nil {
			healthCheckEndpointMode = input.HealthCheckEndpointMode
		}
		next.HealthCheckEndpointMode, err = resolveHealthCheckEndpointMode(
			healthCheckEndpointMode,
			current.ProviderCode,
			current.ProviderProtocolProfileID,
			profile.EnabledEndpointModes,
		)
		if err != nil {
			return err
		}
		if connectionConfigurationChanged && next.Status != port.PublicAccountStatusDisabled {
			next.Status = port.PublicAccountStatusPendingTest
			next.Schedulable = false
		}
		resetFailureState := input.Status != nil || connectionConfigurationChanged
		scheduleHealthCheck := connectionConfigurationChanged || input.SupportedModels.Set() || input.HealthCheckEndpointMode != nil
		updated, ok, err := store.UpdatePublicAccount(ctx, port.PublicAccountUpdateInput{
			ID:                       current.ID,
			SystemAccountID:          current.SystemAccountID,
			ProviderCode:             current.ProviderCode,
			Name:                     next.Name,
			Status:                   next.Status,
			CredentialsEncrypted:     next.CredentialsEncrypted,
			CredentialFingerprint:    next.CredentialFingerprint,
			CredentialMask:           next.CredentialMask,
			SupportedModels:          next.SupportedModels,
			SupportedModelsChanged:   supportedModelsChanged,
			HealthCheckModel:         next.HealthCheckModel,
			HealthCheckEndpointMode:  next.HealthCheckEndpointMode,
			ResetFailureState:        resetFailureState,
			ScheduleHealthCheck:      scheduleHealthCheck,
			ResetHealthDiagnostics:   connectionConfigurationChanged,
			Schedulable:              next.Schedulable,
			AvailabilityScheduleJSON: next.AvailabilityScheduleJSON,
			ConcurrencyLimit:         next.ConcurrencyLimit,
			Priority:                 next.Priority,
			Notes:                    next.Notes,
			Now:                      s.now().UTC(),
		})
		if errors.Is(err, port.ErrPublicAccountDuplicateName) {
			return fmt.Errorf("%w: %s", ErrDuplicateAccountName, next.Name)
		}
		if err != nil {
			return err
		}
		if !ok {
			return ErrAccountNotFound
		}
		if scheduleHealthCheck {
			healthCheckAccountID = updated.ID
		}
		response = accountResponse("updated", target, nil, updated, false, s.generatedAt())
		return nil
	})
	if err == nil && healthCheckAccountID != "" {
		s.dispatchAccountHealthCheck(ctx, healthCheckAccountID, accountHealthCheckReasonConfiguration)
	}
	return response, err
}

func (s *Service) Delete(ctx context.Context, input DeleteInput) (AccountResponse, error) {
	var response AccountResponse
	err := s.inTx(ctx, func(ctx context.Context, store port.PublicAccountStore) error {
		current, ok, err := store.FindPublicAccountByID(ctx, strings.TrimSpace(input.AccountID))
		if err != nil {
			return err
		}
		if !ok {
			response = notFoundAccountResponse(input, s.generatedAt())
			return nil
		}
		target, ok, err := targetByIDOrUsername(ctx, store, current.SystemAccountID, input.TargetUsername)
		if err != nil {
			return err
		}
		if !ok {
			response = notFoundAccountResponse(input, s.generatedAt())
			return nil
		}
		if err := assertTargetActive(target); err != nil {
			return err
		}
		if err := s.assertAccountFilters(ctx, store, current, nil, input.ProviderCode, input.ProviderProtocolProfileID, input.TargetGroupName); err != nil {
			return err
		}
		deleted, err := store.DeletePublicAccount(ctx, current.ID, current.SystemAccountID, target.ID, s.now().UTC())
		if err != nil {
			return err
		}
		if !deleted {
			response = notFoundAccountResponse(input, s.generatedAt())
			return nil
		}
		response = accountResponse("deleted", target, nil, current, false, s.generatedAt())
		return nil
	})
	return response, err
}

func (s *Service) requireTarget(ctx context.Context, username string) (port.PublicGroupTarget, error) {
	target, ok, err := s.store.FindPublicAccountTargetByUsername(ctx, strings.TrimSpace(username))
	if err != nil {
		return port.PublicGroupTarget{}, err
	}
	if !ok {
		return port.PublicGroupTarget{}, fmt.Errorf("%w: %s", ErrTargetNotFound, strings.TrimSpace(username))
	}
	if err := assertTargetActive(target); err != nil {
		return port.PublicGroupTarget{}, err
	}
	return target, nil
}

func (s *Service) ensureTarget(ctx context.Context, store port.PublicAccountStore, username string, displayName string) (port.PublicGroupTarget, error) {
	username = strings.TrimSpace(username)
	target, ok, err := store.FindPublicAccountTargetByUsername(ctx, username)
	if err != nil {
		return port.PublicGroupTarget{}, err
	}
	if ok {
		return target, nil
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}
	return store.CreatePublicAccountTarget(ctx, port.PublicGroupTargetCreateInput{
		ID:           s.newID("sys"),
		Username:     username,
		DisplayName:  displayName,
		Description:  defaultTargetDescription,
		PasswordHash: defaultTargetPassword,
		Now:          s.now().UTC(),
	})
}

func (s *Service) ensureGroup(ctx context.Context, store port.PublicAccountStore, systemAccountID string, providerCode string, name string) (port.PublicAccountGroupRef, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return port.PublicAccountGroupRef{}, ErrTargetGroupRequired
	}
	providerCode = strings.TrimSpace(providerCode)
	group, ok, err := store.FindExistingPublicAccountGroupByName(ctx, systemAccountID, providerCode, name)
	if err != nil {
		return port.PublicAccountGroupRef{}, err
	}
	if ok {
		if !group.Enabled {
			return port.PublicAccountGroupRef{}, fmt.Errorf("%w: 目标分组已停用", ErrGroupProviderMismatch)
		}
		return group, nil
	}
	created, err := store.CreatePublicAccountGroup(ctx, port.PublicGroupCreateInput{
		ID:              s.newID("grp"),
		SystemAccountID: systemAccountID,
		Name:            name,
		ProviderCode:    providerCode,
		Description:     stringPtr(defaultTargetDescription),
		Enabled:         true,
		GroupType:       defaultGroupType,
		Now:             s.now().UTC(),
	})
	if err != nil {
		return port.PublicAccountGroupRef{}, err
	}
	created.Created = true
	return created, nil
}

func (s *Service) requireProviderProfile(ctx context.Context, store port.PublicAccountStore, systemAccountID string, providerCode string, profileID string) (port.PublicAccountProviderProfile, error) {
	providerCode = strings.TrimSpace(providerCode)
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return port.PublicAccountProviderProfile{}, fmt.Errorf("%w: providerProtocolProfileId 不能为空", ErrProviderProfileNotFound)
	}
	profile, ok, err := store.FindPublicAccountProviderProfile(ctx, strings.TrimSpace(systemAccountID), providerCode, profileID)
	if err != nil {
		return port.PublicAccountProviderProfile{}, err
	}
	if !ok {
		return port.PublicAccountProviderProfile{}, fmt.Errorf("%w: %s", ErrProviderProfileNotFound, profileID)
	}
	if !profile.ProviderEnabled {
		return port.PublicAccountProviderProfile{}, fmt.Errorf("%w: %s", ErrProviderDisabled, providerCode)
	}
	if !profile.Enabled {
		return port.PublicAccountProviderProfile{}, fmt.Errorf("%w: %s", ErrProviderProfileDisabled, profileID)
	}
	if !providerAccountTypesContain(profile.AccountTypesJSON, AccountTypeAPIKey) {
		return port.PublicAccountProviderProfile{}, fmt.Errorf("%w: %s", ErrUnsupportedAccountType, profileID)
	}
	return profile, nil
}

func (s *Service) accountAndTargetForWrite(ctx context.Context, store port.PublicAccountStore, accountID string, targetUsername *string) (port.PublicAccountSummary, port.PublicGroupTarget, error) {
	account, ok, err := store.FindPublicAccountByID(ctx, strings.TrimSpace(accountID))
	if err != nil {
		return port.PublicAccountSummary{}, port.PublicGroupTarget{}, err
	}
	if !ok {
		return port.PublicAccountSummary{}, port.PublicGroupTarget{}, ErrAccountNotFound
	}
	target, ok, err := targetByIDOrUsername(ctx, store, account.SystemAccountID, targetUsername)
	if err != nil {
		return port.PublicAccountSummary{}, port.PublicGroupTarget{}, err
	}
	if !ok {
		return port.PublicAccountSummary{}, port.PublicGroupTarget{}, ErrAccountNotFound
	}
	if err := assertTargetActive(target); err != nil {
		return port.PublicAccountSummary{}, port.PublicGroupTarget{}, err
	}
	return account, target, nil
}

func targetByIDOrUsername(ctx context.Context, store port.PublicAccountStore, ownerSystemAccountID string, targetUsername *string) (port.PublicGroupTarget, bool, error) {
	if targetUsername != nil {
		target, ok, err := store.FindPublicAccountTargetByUsername(ctx, *targetUsername)
		if err != nil || !ok {
			return port.PublicGroupTarget{}, false, err
		}
		if target.ID != ownerSystemAccountID {
			return port.PublicGroupTarget{}, false, nil
		}
		return target, true, nil
	}
	return store.FindPublicAccountTargetByID(ctx, ownerSystemAccountID)
}

func (s *Service) assertAccountFilters(
	ctx context.Context,
	store port.PublicAccountStore,
	account port.PublicAccountSummary,
	currentProfile *port.PublicAccountProviderProfile,
	providerCode *string,
	profileID *string,
	groupName *string,
) error {
	if providerCode != nil && strings.TrimSpace(*providerCode) != account.ProviderCode {
		return ErrAccountNotFound
	}
	if profileID != nil {
		requestedProfileID := strings.TrimSpace(*profileID)
		if currentProfile == nil || requestedProfileID != currentProfile.ID {
			provider := account.ProviderCode
			if providerCode != nil && strings.TrimSpace(*providerCode) != "" {
				provider = strings.TrimSpace(*providerCode)
			}
			profile, err := s.requireProviderProfile(ctx, store, account.SystemAccountID, provider, requestedProfileID)
			if err != nil {
				return err
			}
			if profile.ID != account.ProviderProtocolProfileID {
				return ErrAccountNotFound
			}
		}
	}
	if groupName != nil {
		group, ok, err := store.FindExistingPublicAccountGroupByName(ctx, account.SystemAccountID, account.ProviderCode, strings.TrimSpace(*groupName))
		if err != nil {
			return err
		}
		if !ok || account.BoundGroupID == nil || group.ID != *account.BoundGroupID {
			return ErrAccountNotFound
		}
	}
	return nil
}

func (s *Service) encryptedCredentials(apiKey string, baseURL string) (encryptedCredential, error) {
	return s.encryptedCredentialMap(map[string]any{
		"api_key":  apiKey,
		"base_url": baseURL,
	})
}

func (s *Service) encryptedCredentialMap(credentials map[string]any) (encryptedCredential, error) {
	apiKey, _ := credentials["api_key"].(string)
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return encryptedCredential{}, ErrInvalidAPIKey
	}
	baseURL, _ := credentials["base_url"].(string)
	baseURL, err := normalizeBaseURL(baseURL)
	if err != nil {
		return encryptedCredential{}, err
	}
	credentials["api_key"] = apiKey
	credentials["base_url"] = baseURL
	encrypted, err := s.codec.EncryptJSON(credentials)
	if err != nil {
		return encryptedCredential{}, fmt.Errorf("%w: %v", ErrCredentialCodecUnusable, err)
	}
	fingerprint := hashSecret(apiKey)
	return encryptedCredential{
		Encrypted:   encrypted,
		Fingerprint: &fingerprint,
		Mask:        maskSecret(apiKey),
	}, nil
}

func (s *Service) currentCredentials(encrypted string) (map[string]any, string, string, error) {
	credentials, err := s.codec.DecryptJSON(encrypted)
	if err != nil {
		return nil, "", "", fmt.Errorf("%w: %v", ErrCredentialCodecUnusable, err)
	}
	apiKey, _ := credentials["api_key"].(string)
	baseURL, _ := credentials["base_url"].(string)
	if strings.TrimSpace(apiKey) == "" || strings.TrimSpace(baseURL) == "" {
		return nil, "", "", fmt.Errorf("%w: 当前账号凭据不完整", ErrInvalidCredentials)
	}
	apiKey = strings.TrimSpace(apiKey)
	baseURL, err = normalizeBaseURL(baseURL)
	if err != nil {
		return nil, "", "", err
	}
	return credentials, apiKey, baseURL, nil
}

func (s *Service) inTx(ctx context.Context, fn func(context.Context, port.PublicAccountStore) error) error {
	if s.transactor != nil {
		return s.transactor.PublicAccountInTx(ctx, fn)
	}
	return fn(ctx, s.store)
}

func (s *Service) dispatchAccountHealthCheck(ctx context.Context, accountID string, reason string) {
	if s.dispatcher == nil {
		return
	}
	dispatchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.healthCheckDispatchTimeout)
	defer cancel()
	if err := s.dispatcher.Dispatch(dispatchCtx, accountID, reason); err != nil && s.logger != nil {
		s.logger.Warn(
			"公开账户健康检查投递失败",
			slog.String("event", accountHealthCheckDispatchFailedEvent),
			slog.String("account_id", accountID),
			slog.String("reason", reason),
			slog.Any("error", err),
		)
	}
}

func publicAccountAddRetryable(err error) bool {
	return errors.Is(err, port.ErrPublicGroupTargetDuplicateUsername) ||
		errors.Is(err, port.ErrPublicGroupDuplicateName) ||
		errors.Is(err, port.ErrPublicAccountDuplicateName)
}

func assertTargetActive(target port.PublicGroupTarget) error {
	if target.Status != "active" {
		return fmt.Errorf("%w: %s", ErrTargetDisabled, target.Username)
	}
	return nil
}

func providerAccountTypesContain(raw string, want string) bool {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return false
	}
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func normalizeBaseURL(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", ErrInvalidBaseURL
	}
	parsed, err := url.Parse(text)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("%w: baseUrl 无效", ErrInvalidBaseURL)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("%w: baseUrl 仅支持 http/https", ErrInvalidBaseURL)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: baseUrl 不能包含用户名或密码", ErrInvalidBaseURL)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", fmt.Errorf("%w: baseUrl 不能指向本机地址", ErrInvalidBaseURL)
	}
	if ip := net.ParseIP(host); ip != nil && !publicIP(ip) {
		return "", fmt.Errorf("%w: baseUrl 不能指向内网或保留地址", ErrInvalidBaseURL)
	}
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func publicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() &&
		!ip.IsPrivate() &&
		!ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsUnspecified()
}

func normalizeSupportedModels(values []string) ([]string, error) {
	if len(values) > 500 {
		return nil, fmt.Errorf("%w: supportedModels 最多 500 项", ErrInvalidSupportedModels)
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			return nil, fmt.Errorf("%w: supportedModels 不能包含空值", ErrInvalidSupportedModels)
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidSupportedModels, invalidSupportedModelsRequiredMessage)
	}
	return out, nil
}

func normalizeAccountHealthCheckModel(value string, supportedModels []string) (string, error) {
	model := strings.TrimSpace(value)
	if model == "" {
		return "", fmt.Errorf("%w: %s", ErrInvalidHealthCheckModel, invalidHealthCheckModelRequiredMessage)
	}
	for _, supportedModel := range supportedModels {
		if supportedModel == model {
			return model, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrInvalidHealthCheckModel, invalidHealthCheckModelUnsupportedMessage)
}

func resolveHealthCheckEndpointMode(value *string, providerCode string, profileID string, enabledEndpointModes []string) (string, error) {
	if value == nil {
		return defaultHealthCheckEndpointMode(providerCode, profileID, enabledEndpointModes)
	}
	mode := strings.TrimSpace(*value)
	if !isHealthCheckEndpointMode(mode) {
		return "", fmt.Errorf("%w: 账户健康检查请求形态无效", ErrInvalidHealthCheckEndpointMode)
	}
	if !stringListContains(enabledEndpointModes, mode) {
		return "", fmt.Errorf(
			"%w: 账户健康检查请求形态 %s 未启用",
			ErrInvalidHealthCheckEndpointMode,
			mode,
		)
	}
	return mode, nil
}

func defaultHealthCheckEndpointMode(providerCode string, profileID string, enabledEndpointModes []string) (string, error) {
	enabledModes := make([]string, 0, len(enabledEndpointModes))
	for _, mode := range enabledEndpointModes {
		mode = strings.TrimSpace(mode)
		if isHealthCheckEndpointMode(mode) {
			enabledModes = append(enabledModes, mode)
		}
	}
	preferred := preferredHealthCheckEndpointMode(providerCode, profileID)
	if stringListContains(enabledModes, preferred) {
		return preferred, nil
	}
	for _, mode := range enabledModes {
		if strings.HasSuffix(mode, "_json") {
			return mode, nil
		}
	}
	if len(enabledModes) > 0 {
		return enabledModes[0], nil
	}
	return "", fmt.Errorf(
		"%w: 账户至少需要启用一个可用于健康检查的请求形态",
		ErrInvalidHealthCheckEndpointMode,
	)
}

func preferredHealthCheckEndpointMode(providerCode string, profileID string) string {
	providerCode = strings.TrimSpace(providerCode)
	profileID = strings.TrimSpace(profileID)
	if profileID == "profile_gemini_native_v1beta" {
		return "generate_content_json"
	}
	if strings.Contains(profileID, "anthropic") || providerCode == "anthropic" {
		return "messages_json"
	}
	if providerCode == "gpt" {
		return "responses_sse"
	}
	return "chat_json"
}

func isHealthCheckEndpointMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "chat_json", "chat_sse", "responses_json", "responses_sse", "messages_json", "messages_sse", "generate_content_json", "generate_content_sse":
		return true
	default:
		return false
	}
}

func stringListContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s *Service) validateSupportedModelsInProviderCatalog(ctx context.Context, systemAccountID string, providerCode string, models []string) error {
	providerCode = strings.TrimSpace(providerCode)
	if strings.EqualFold(providerCode, hybridProviderCode) {
		return nil
	}
	if s.providerModels == nil {
		return errors.New(providerModelsRequiredMessage)
	}
	catalog, err := s.providerModels.Models(ctx, managementprovidermodels.ModelListInput{
		ProviderCode:    providerCode,
		SystemAccountID: strings.TrimSpace(systemAccountID),
		IncludeInactive: false,
		IncludeUnpriced: false,
	})
	if err != nil {
		return err
	}
	available := make(map[string]struct{}, len(catalog))
	for _, item := range catalog {
		model := strings.TrimSpace(item.Model)
		if model != "" {
			available[model] = struct{}{}
		}
	}
	invalid := make([]string, 0)
	for _, model := range models {
		if _, ok := available[model]; !ok {
			invalid = append(invalid, model)
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf(
			"%w: 账户支持模型不在供应商模型目录中：%s",
			ErrInvalidSupportedModels,
			strings.Join(invalid[:min(5, len(invalid))], "、"),
		)
	}
	return nil
}

func unorderedStringListsEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		count, ok := counts[value]
		if !ok || count == 0 {
			return false
		}
		counts[value] = count - 1
	}
	return true
}

func normalizeAvailabilityScheduleJSON(value JSONValue) (*string, error) {
	if !value.Set() || value.Value() == nil {
		return nil, nil
	}
	if _, ok := value.Value().(map[string]any); !ok {
		return nil, fmt.Errorf("%w: availabilitySchedule 必须是对象", ErrInvalidAvailability)
	}
	data, err := json.Marshal(value.Value())
	if err != nil {
		return nil, fmt.Errorf("%w: availabilitySchedule 无法序列化", ErrInvalidAvailability)
	}
	text := string(data)
	return &text, nil
}

func addAccountStatus(value string) string {
	if strings.TrimSpace(value) == StatusDisabled {
		return StatusDisabled
	}
	return StatusPendingTest
}

func updateAccountStatus(current port.PublicAccountStatus, requested string) (string, error) {
	switch strings.TrimSpace(requested) {
	case StatusActive:
		if current == port.PublicAccountStatusPendingTest {
			return "", fmt.Errorf("%w: 待检查账户只能由后台激活检查恢复", ErrInvalidStatusTransition)
		}
		return StatusActive, nil
	case StatusDisabled:
		return StatusDisabled, nil
	default:
		return "", fmt.Errorf("%w: status 取值无效", ErrInvalidStatusTransition)
	}
}

func normalizeListStatus(value string) string {
	value = strings.TrimSpace(value)
	if value == "all" {
		return ""
	}
	return value
}

func intPtrValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
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

func publicTarget(target port.PublicGroupTarget, group *port.PublicAccountGroupRef) Target {
	out := Target{
		Username:        target.Username,
		DisplayName:     target.DisplayName,
		SystemAccountID: target.ID,
		Created:         target.Created,
	}
	if group != nil {
		out.GroupID = group.ID
		out.GroupName = group.Name
		groupCreated := group.Created
		out.GroupCreated = &groupCreated
	}
	return out
}

func accountResponse(action string, target port.PublicGroupTarget, group *port.PublicAccountGroupRef, account port.PublicAccountSummary, listShape bool, generatedAt string) AccountResponse {
	summary := publicAccountSummary(account, listShape)
	return AccountResponse{
		Source:      "stats",
		GeneratedAt: generatedAt,
		Action:      action,
		Target:      publicTarget(target, group),
		Account:     &summary,
	}
}

func notFoundAccountResponse(input DeleteInput, generatedAt string) AccountResponse {
	username := ""
	if input.TargetUsername != nil {
		username = strings.TrimSpace(*input.TargetUsername)
	}
	return AccountResponse{
		Source:      "stats",
		GeneratedAt: generatedAt,
		Action:      "not_found",
		Target: Target{
			Username: username,
		},
		Account: nil,
	}
}

func emptyAccountListResponse(target port.PublicGroupTarget, input ListInput, generatedAt string) AccountListResponse {
	page := input.Page
	if page < 1 {
		page = 1
	}
	pageSize := input.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	return AccountListResponse{
		Source:         "stats",
		GeneratedAt:    generatedAt,
		Target:         publicTarget(target, nil),
		Page:           page,
		PageSize:       pageSize,
		PageUpperBound: 0,
		HasMore:        false,
		Items:          []AccountSummary{},
	}
}

func publicAccountSummary(account port.PublicAccountSummary, listShape bool) AccountSummary {
	summary := AccountSummary{
		ID:                        account.ID,
		Name:                      account.Name,
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
		Type:                      account.Type,
		ClientCompatibility:       account.ClientCompatibility,
		Status:                    string(account.Status),
		SupportedModels:           append([]string(nil), account.SupportedModels...),
		HealthCheckEndpointMode:   account.HealthCheckEndpointMode,
		Schedulable:               account.Schedulable,
		AvailabilitySchedule:      jsonValue(account.AvailabilityScheduleJSON),
	}
	if account.BoundGroupID != nil {
		summary.BoundGroupID = *account.BoundGroupID
	}
	if account.BoundGroupName != nil {
		summary.BoundGroupName = *account.BoundGroupName
	}
	if listShape {
		concurrency := account.ConcurrencyLimit
		priority := account.Priority
		summary.ConcurrencyLimit = &concurrency
		summary.Priority = &priority
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

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 10 {
		return value[:min(2, len(value))] + "***" + value[max(0, len(value)-2):]
	}
	return value[:6] + "***" + value[len(value)-4:]
}

func stringPtr(value string) *string {
	return &value
}

type encryptedCredential struct {
	Encrypted   string
	Fingerprint *string
	Mask        string
}

type aesGCMCredentialCodec struct {
	key [32]byte
}

func newAESGCMCredentialCodec(secret string) aesGCMCredentialCodec {
	return aesGCMCredentialCodec{key: sha256.Sum256([]byte(secret))}
}

func (c aesGCMCredentialCodec) EncryptJSON(value map[string]any) (string, error) {
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, nonce, plain, nil)
	tagSize := aead.Overhead()
	ciphertext := sealed[:len(sealed)-tagSize]
	tag := sealed[len(sealed)-tagSize:]
	encode := base64.RawURLEncoding.EncodeToString
	return "v1:" + encode(nonce) + ":" + encode(tag) + ":" + encode(ciphertext), nil
}

func (c aesGCMCredentialCodec) DecryptJSON(value string) (map[string]any, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 4 || parts[0] != "v1" {
		return nil, fmt.Errorf("unsupported encrypted credential format")
	}
	decode := base64.RawURLEncoding.DecodeString
	nonce, err := decode(parts[1])
	if err != nil {
		return nil, err
	}
	tag, err := decode(parts[2])
	if err != nil {
		return nil, err
	}
	ciphertext, err := decode(parts[3])
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sealed := append(append([]byte{}, ciphertext...), tag...)
	plain, err := aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, err
	}
	return out, nil
}
