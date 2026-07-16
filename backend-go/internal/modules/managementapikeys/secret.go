package managementapikeys

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/apikeysecret"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	apiKeySecretRefreshedReason                = "api_key_secret_refreshed"
	apiKeyRefreshUsageEnrichmentFailedEvent    = "management_api_key_refresh_usage_enrichment_failed"
	defaultAPIKeyRefreshUsageTimeout           = 5 * time.Second
	defaultAPIKeyValidationInvalidationTimeout = 5 * time.Second
)

var (
	ErrAPIKeyNotFound                           = errors.New("management API Key not found")
	ErrAPIKeySecretUnavailable                  = errors.New("management API Key secret unavailable")
	ErrAPIKeySecretInvalid                      = errors.New("management API Key secret input invalid")
	ErrAPIKeyRefreshValidationCacheInvalidation = errors.New("API Key 密钥刷新后校验缓存失效失败")
)

type secretJSONCodec interface {
	EncryptJSON(value map[string]any) (string, error)
	DecryptJSON(value string) (map[string]any, error)
}

type APIKeyGatewayCacheInvalidator interface {
	InvalidateAPIKeyValidationCache(ctx context.Context) error
	InvalidateAPIKeyLookupCache(ctx context.Context, apiKeyID string, reason string) error
	InvalidateGatewayRuntime(ctx context.Context, reason string) error
	InvalidateAPIKeyQuotaChanged(ctx context.Context, apiKeyID string, reason string) error
}

type ServiceOptions struct {
	ListReader                    port.ManagementAPIKeyListReader
	Creator                       port.ManagementAPIKeyCreator
	Updater                       port.ManagementAPIKeyUpdater
	Deleter                       port.ManagementAPIKeyDeleter
	UsageStatsTimezoneReader      port.ManagementUsageStatsTimezoneReader
	SecretStore                   port.ManagementAPIKeySecretStore
	SecretTransactor              port.ManagementAPIKeySecretTransactor
	Invalidator                   APIKeyGatewayCacheInvalidator
	Logger                        *slog.Logger
	ValidationInvalidationTimeout time.Duration
	RefreshUsageTimeout           time.Duration
	Secret                        string
	Now                           func() time.Time
	NewID                         func(prefix string) string
	NewSecret                     func() (string, error)
}

type SecretInput struct {
	ActorSystemAccountID string
	ActorRole            string
	APIKeyID             string
	SystemAccountID      string
	SelfOnly             bool
}

type SecretResult struct {
	Key                  string `json:"key"`
	APIKeyID             string `json:"-"`
	OwnerSystemAccountID string `json:"-"`
	Name                 string `json:"-"`
	KeyMarker            string `json:"-"`
}

type RefreshResult struct {
	ListItem
	Key                  string `json:"key"`
	UsageAvailable       bool   `json:"usageAvailable"`
	OwnerSystemAccountID string `json:"-"`
	PreviousKeyMarker    string `json:"-"`
	KeyMarker            string `json:"-"`
	Committed            bool   `json:"-"`
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
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
		newSecret = apikeysecret.Generate
	}
	validationInvalidationTimeout := opts.ValidationInvalidationTimeout
	if validationInvalidationTimeout <= 0 {
		validationInvalidationTimeout = defaultAPIKeyValidationInvalidationTimeout
	}
	refreshUsageTimeout := opts.RefreshUsageTimeout
	if refreshUsageTimeout <= 0 {
		refreshUsageTimeout = defaultAPIKeyRefreshUsageTimeout
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	codec := secretcrypto.NewJSONCodec(opts.Secret)
	return &Service{
		store:                         opts.ListReader,
		creator:                       opts.Creator,
		updater:                       opts.Updater,
		deleter:                       opts.Deleter,
		usageStatsTimezoneReader:      opts.UsageStatsTimezoneReader,
		secretStore:                   opts.SecretStore,
		secretTransactor:              opts.SecretTransactor,
		invalidator:                   opts.Invalidator,
		logger:                        logger,
		validationInvalidationTimeout: validationInvalidationTimeout,
		refreshUsageTimeout:           refreshUsageTimeout,
		codec:                         codec,
		now:                           now,
		newID:                         newID,
		newSecret:                     newSecret,
	}
}

func (s *Service) Reveal(ctx context.Context, input SecretInput) (SecretResult, error) {
	if s.secretStore == nil {
		return SecretResult{}, fmt.Errorf("management API Key secret store is required")
	}
	scope, _, err := managementAPIKeySecretScope(input)
	if err != nil {
		return SecretResult{}, err
	}
	row, found, err := s.secretStore.FindManagementAPIKeySecret(ctx, scope)
	if err != nil {
		return SecretResult{}, err
	}
	if !found {
		return SecretResult{}, ErrAPIKeyNotFound
	}
	if row.KeySecretEncrypted == nil || strings.TrimSpace(*row.KeySecretEncrypted) == "" {
		return SecretResult{}, ErrAPIKeySecretUnavailable
	}
	payload, err := s.codec.DecryptJSON(*row.KeySecretEncrypted)
	if err != nil {
		return SecretResult{}, fmt.Errorf("%w: decrypt secret", ErrAPIKeySecretUnavailable)
	}
	key, ok := payload["key"].(string)
	if !ok || key == "" {
		return SecretResult{}, fmt.Errorf("%w: missing key", ErrAPIKeySecretUnavailable)
	}
	return SecretResult{
		Key:                  key,
		APIKeyID:             row.ID,
		OwnerSystemAccountID: row.SystemAccountID,
		Name:                 row.Name,
		KeyMarker:            apiKeySecretMarker(row.KeyPrefix, row.KeySuffix),
	}, nil
}

func (s *Service) Refresh(ctx context.Context, input SecretInput) (RefreshResult, error) {
	if s.store == nil {
		return RefreshResult{}, fmt.Errorf("management API Key list reader is required")
	}
	if s.secretStore == nil {
		return RefreshResult{}, fmt.Errorf("management API Key secret store is required")
	}
	if s.secretTransactor == nil {
		return RefreshResult{}, fmt.Errorf("management API Key secret transactor is required")
	}
	scope, includeOwner, err := managementAPIKeySecretScope(input)
	if err != nil {
		return RefreshResult{}, err
	}

	var row port.ManagementAPIKeyListRow
	var key string
	var previousMarker string
	err = s.secretTransactor.ManagementAPIKeySecretInTx(
		ctx,
		func(ctx context.Context, txStore port.ManagementAPIKeySecretStore) error {
			current, found, err := txStore.LockManagementAPIKeySecretRefreshTarget(ctx, scope)
			if err != nil {
				return err
			}
			if !found {
				return ErrAPIKeyNotFound
			}

			key, err = s.newSecret()
			if err != nil {
				return fmt.Errorf("generate management API Key secret: %w", err)
			}
			if key == "" {
				return fmt.Errorf("generate management API Key secret: empty secret")
			}
			encrypted, err := s.codec.EncryptJSON(map[string]any{"key": key})
			if err != nil {
				return fmt.Errorf("encrypt management API Key secret: %w", err)
			}
			previousMarker = apiKeySecretMarker(current.KeyPrefix, current.KeySuffix)
			updated, err := txStore.UpdateManagementAPIKeySecret(ctx, port.ManagementAPIKeySecretUpdateInput{
				APIKeyID:           current.ID,
				SystemAccountID:    current.SystemAccountID,
				KeyHash:            apikeysecret.Hash(key),
				KeyPrefix:          apikeysecret.Prefix(key),
				KeySuffix:          apikeysecret.Suffix(key),
				KeySecretEncrypted: encrypted,
				UpdatedAt:          s.now().UTC(),
			})
			if err != nil {
				return err
			}
			if !updated {
				return ErrAPIKeyNotFound
			}
			current.KeyPrefix = apikeysecret.Prefix(key)
			current.KeySuffix = apikeysecret.Suffix(key)
			row = current
			return nil
		},
	)
	if err != nil {
		return RefreshResult{}, err
	}

	if s.invalidator == nil {
		return RefreshResult{}, fmt.Errorf("management API Key cache invalidator is required")
	}
	invalidationCtx, cancelInvalidation := context.WithTimeout(
		context.WithoutCancel(ctx),
		s.validationInvalidationTimeout,
	)

	item, parseErr := listItem(row, port.ManagementAccountUsageSummary{}, includeOwner)
	result := RefreshResult{
		ListItem:             item,
		Key:                  key,
		OwnerSystemAccountID: row.SystemAccountID,
		PreviousKeyMarker:    previousMarker,
		KeyMarker:            apiKeySecretMarker(row.KeyPrefix, row.KeySuffix),
		Committed:            true,
	}
	if err := s.invalidator.InvalidateAPIKeyValidationCache(invalidationCtx); err != nil {
		cancelInvalidation()
		return result, ErrAPIKeyRefreshValidationCacheInvalidation
	}
	_ = s.invalidator.InvalidateAPIKeyLookupCache(
		invalidationCtx,
		row.ID,
		apiKeySecretRefreshedReason,
	)
	_ = s.invalidator.InvalidateGatewayRuntime(invalidationCtx, apiKeySecretRefreshedReason)
	_ = s.invalidator.InvalidateAPIKeyQuotaChanged(
		invalidationCtx,
		row.ID,
		apiKeySecretRefreshedReason,
	)
	cancelInvalidation()
	if parseErr != nil {
		return result, parseErr
	}

	usageCtx, cancelUsage := context.WithTimeout(
		context.WithoutCancel(ctx),
		s.refreshUsageTimeout,
	)
	usageRows, err := s.store.ListManagementAPIKeyUsageTotals(usageCtx, []port.ManagementAPIKeyUsageScope{{
		SystemAccountID: row.SystemAccountID,
		APIKeyID:        row.ID,
	}})
	cancelUsage()
	if err != nil {
		s.logger.Warn(
			"API Key 密钥刷新后用量摘要读取失败",
			slog.String("event", apiKeyRefreshUsageEnrichmentFailedEvent),
			slog.String("api_key_id", row.ID),
			slog.String("system_account_id", row.SystemAccountID),
			slog.Any("error", err),
		)
		return result, nil
	}
	result.UsageAvailable = true
	var usage port.ManagementAccountUsageSummary
	for _, usageRow := range usageRows {
		if usageRow.SystemAccountID == row.SystemAccountID && usageRow.APIKeyID == row.ID {
			usage = usageRow.Usage
			break
		}
	}
	result.ListItem.Usage = usage
	return result, nil
}

func managementAPIKeySecretScope(
	input SecretInput,
) (port.ManagementAPIKeySecretScope, bool, error) {
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	apiKeyID := strings.TrimSpace(input.APIKeyID)
	if actorSystemAccountID == "" || apiKeyID == "" {
		return port.ManagementAPIKeySecretScope{}, false, ErrAPIKeySecretInvalid
	}
	if input.SelfOnly || !isAdminRole(input.ActorRole) {
		return port.ManagementAPIKeySecretScope{
			APIKeyID:        apiKeyID,
			SystemAccountID: actorSystemAccountID,
		}, false, nil
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "all" {
		systemAccountID = ""
	}
	return port.ManagementAPIKeySecretScope{
		APIKeyID:        apiKeyID,
		SystemAccountID: systemAccountID,
	}, true, nil
}

func apiKeySecretMarker(prefix string, suffix string) string {
	return prefix + "..." + suffix
}
