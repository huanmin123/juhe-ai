package oauthrefresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Keepalive windows mirror the dispatch-preparation lead constants: tokens
// within the window of expiry (or without one) are refreshed ahead of demand.
const (
	AnthropicKeepaliveLead = 60 * time.Second
	GeminiKeepaliveLead    = 60 * time.Second
	GrokKeepaliveLead      = 5 * time.Minute
	// KeepalivePersistAttempts mirrors anthropicOAuthPersistAttempts and its
	// per-provider siblings: the CAS merge writer retries revision conflicts.
	KeepalivePersistAttempts = 3
	// DefaultKeepaliveBatch mirrors the OpenAI refresh batch default scale.
	DefaultKeepaliveBatch = 20
)

// KeepaliveResult mirrors the refresh result shape for the three-vendor job.
type KeepaliveResult struct {
	Provider      string
	Scanned       int
	Due           int
	Refreshed     int
	Failed        int
	SkippedLocked int
	SkippedFresh  int
}

// KeepaliveJob runs the anthropic/gemini/grok OAuth keepalive refresh family.
// The refresh trigger/window semantics come from the dispatch preparation
// (oauth-dispatch-preparation.ts); the failure handling follows the OpenAI
// refresh family (upstream failures only back off via the caller's scheduler,
// local configuration failures surface verbatim).
type KeepaliveJob struct {
	store     *Store
	exchanger TokenExchanger
	locks     *accountLocks
	clock     Clock
	logger    *slog.Logger
	// updateCredentials overrides the CAS merge writer (mirrors the Node
	// setXxxForTest injection seams; nil uses the store).
	updateCredentials func(ctx context.Context, accountID string, credentials map[string]any, expectedConfigRevision int64) (*RotationAccount, error)
}

// NewKeepaliveJob wires the job.
func NewKeepaliveJob(store *Store, exchanger TokenExchanger, opts ...func(*KeepaliveJob)) *KeepaliveJob {
	job := &KeepaliveJob{
		store:     store,
		exchanger: exchanger,
		locks:     newAccountLocks(),
		clock:     SystemClock(),
		logger:    slog.Default(),
	}
	for _, opt := range opts {
		opt(job)
	}
	return job
}

// WithClock overrides the clock (tests).
func WithKeepaliveClock(clock Clock) func(*KeepaliveJob) {
	return func(job *KeepaliveJob) { job.clock = clock }
}

// WithKeepaliveLogger overrides the logger.
func WithKeepaliveLogger(logger *slog.Logger) func(*KeepaliveJob) {
	return func(job *KeepaliveJob) { job.logger = logger }
}

// KeepalivePlan describes one provider's keepalive query parameters.
type KeepalivePlan struct {
	Provider          string
	AccountType       string
	RequiredProfileID string
	Lead              time.Duration
}

// KeepalivePlans returns the three-provider plans.
func KeepalivePlans() []KeepalivePlan {
	return []KeepalivePlan{
		{Provider: ProviderAnthropic, AccountType: AccountTypeOAuth, Lead: AnthropicKeepaliveLead},
		{Provider: ProviderGemini, AccountType: AccountTypeGoogleOAuth, Lead: GeminiKeepaliveLead},
		{Provider: ProviderXAI, AccountType: AccountTypeOAuth, RequiredProfileID: ProfileXAIOpenAIV1, Lead: GrokKeepaliveLead},
	}
}

func (j *KeepaliveJob) now() time.Time { return j.clock.Now() }

// RunOnce refreshes every due account of the provider.
func (j *KeepaliveJob) RunOnce(ctx context.Context, plan KeepalivePlan, limit int) (KeepaliveResult, error) {
	result := KeepaliveResult{Provider: plan.Provider}
	if limit <= 0 {
		limit = DefaultKeepaliveBatch
	}
	now := j.now()
	candidates, err := j.store.ListDueKeepaliveAccounts(ctx, plan.Provider, plan.AccountType, plan.RequiredProfileID, plan.Lead, limit, now)
	if err != nil {
		return result, err
	}
	result.Scanned = len(candidates)
	for _, candidate := range candidates {
		account := candidate.Account
		if account == nil {
			continue
		}
		result.Due++
		refreshed := false
		lockErr := j.locks.TryLock(account.ID, func() error {
			flag, err := j.refreshOne(ctx, plan, account, now)
			refreshed = flag
			return err
		})
		if lockErr != nil {
			var busy *LockBusyError
			if errors.As(lockErr, &busy) {
				result.SkippedLocked++
				continue
			}
			result.Failed++
			j.logger.Warn("供应商 OAuth 保活刷新失败",
				"event", "oauth_keepalive_refresh_account_failed",
				"provider", plan.Provider,
				"accountId", account.ID,
				"accountName", account.Name,
				"error", lockErr)
			continue
		}
		if !refreshed {
			result.SkippedFresh++
			continue
		}
		result.Refreshed++
	}
	return result, nil
}

// refreshOne mirrors refreshAndPersistXxxOAuthAccount: re-read the source,
// re-check the window, refresh, merge (base_url preserved) and CAS-persist
// with at most KeepalivePersistAttempts attempts. It returns false when the
// account turned out to be still fresh (no refresh needed).
func (j *KeepaliveJob) refreshOne(ctx context.Context, plan KeepalivePlan, account *RotationAccount, now time.Time) (bool, error) {
	source, err := j.store.FindRotationAccount(ctx, account.ID)
	if err != nil {
		return false, err
	}
	if source == nil {
		return false, &LocalConfigurationError{Message: providerSourceMissingMessage(plan.Provider), ExpectedConfigRevision: account.ConfigRevision}
	}
	if !isRefreshableKeepaliveAccount(plan, source) {
		return false, &LocalConfigurationError{Message: providerSourceMissingMessage(plan.Provider), ExpectedConfigRevision: account.ConfigRevision}
	}
	if !shouldKeepaliveRefresh(source.Credentials, now, plan.Lead) {
		return false, nil
	}
	refreshToken := stringCredential(source.Credentials, "refresh_token")
	if refreshToken == "" {
		return false, &LocalConfigurationError{Message: providerMissingRefreshTokenMessage(plan.Provider), ExpectedConfigRevision: source.ConfigRevision}
	}

	var tokenCredentials map[string]any
	switch plan.Provider {
	case ProviderAnthropic:
		info, refreshErr := RefreshAnthropicToken(ctx, j.exchanger, refreshToken, stringCredential(source.Credentials, "client_id"), j.now())
		if refreshErr != nil {
			return false, refreshErr
		}
		tokenCredentials = BuildAnthropicOAuthCredentials(info, refreshToken)
	case ProviderGemini:
		fallback := geminiFallbackFromCredentials(source.Credentials)
		info, refreshErr := RefreshGeminiToken(ctx, j.exchanger, refreshToken, fallback, j.now())
		if refreshErr != nil {
			return false, refreshErr
		}
		tokenCredentials = BuildGeminiOAuthCredentials(info, &fallback)
	case ProviderXAI:
		info, refreshErr := RefreshGrokToken(ctx, j.exchanger, refreshToken, stringCredential(source.Credentials, "client_id"), j.now())
		if refreshErr != nil {
			return false, refreshErr
		}
		tokenCredentials = BuildGrokOAuthCredentials(info, refreshToken)
	default:
		return false, fmt.Errorf("不支持的保活供应商：%s", plan.Provider)
	}

	var lastConflict error
	for attempt := 0; attempt < KeepalivePersistAttempts; attempt++ {
		// {...current, ...token} with the stored base_url preserved (the
		// anthropic/gemini/grok merge keeps the operator-managed base URL).
		merged := mergeCredentials(source.Credentials, tokenCredentials)
		if baseURL := stringCredential(source.Credentials, "base_url"); baseURL != "" {
			merged["base_url"] = baseURL
		}
		updated, persistErr := j.persistCredentials(ctx, source.ID, merged, source.ConfigRevision)
		if persistErr == nil {
			if updated == nil {
				return false, errors.New(providerSourceMissingMessage(plan.Provider))
			}
			return true, nil
		}
		var conflict *RevisionConflictError
		if !errors.As(persistErr, &conflict) {
			return false, persistErr
		}
		lastConflict = persistErr
		latest, loadErr := j.store.FindRotationAccount(ctx, source.ID)
		if loadErr != nil {
			return false, loadErr
		}
		if latest == nil || !isRefreshableKeepaliveAccount(plan, latest) {
			return false, persistErr
		}
		latestRefreshToken := stringCredential(latest.Credentials, "refresh_token")
		if latestRefreshToken != refreshToken {
			if !shouldKeepaliveRefresh(latest.Credentials, j.now(), plan.Lead) {
				// Another writer already refreshed the account with a usable
				// token; keep it (Node dispatch-preparation parity).
				return false, nil
			}
		}
		source = latest
	}
	if lastConflict != nil {
		return false, lastConflict
	}
	return false, errors.New("OAuth 凭据并发写回失败")
}

// persistCredentials routes the CAS merge writer through the test seam when
// installed.
func (j *KeepaliveJob) persistCredentials(ctx context.Context, accountID string, credentials map[string]any, expectedConfigRevision int64) (*RotationAccount, error) {
	if j.updateCredentials != nil {
		return j.updateCredentials(ctx, accountID, credentials, expectedConfigRevision)
	}
	return j.store.UpdateAccountCredentials(ctx, accountID, credentials, expectedConfigRevision)
}

// shouldKeepaliveRefresh mirrors shouldRefreshXxxOAuthCredentials: missing
// access token, missing expires_at, unparsable expires_at or inside the lead
// window. Unparsable timestamps read as due so a scheduled sweeper cannot
// wedge on one malformed row; the row surfaces through the failure log.
func shouldKeepaliveRefresh(credentials map[string]any, now time.Time, lead time.Duration) bool {
	if stringCredential(credentials, "access_token") == "" {
		return true
	}
	expiresAtText := credentialExpiresAt(credentials)
	if expiresAtText == "" {
		return true
	}
	expiresAtMs, ok := rfc3339Millis(expiresAtText)
	if !ok {
		return true
	}
	return expiresAtMs-now.UnixMilli() <= lead.Milliseconds()
}

func isRefreshableKeepaliveAccount(plan KeepalivePlan, account *RotationAccount) bool {
	if account.ProviderCode != plan.Provider || account.Type != plan.AccountType {
		return false
	}
	if plan.RequiredProfileID != "" && account.ProviderProtocolProfileID != plan.RequiredProfileID {
		return false
	}
	return true
}

func geminiFallbackFromCredentials(credentials map[string]any) GeminiCredentialFallback {
	return GeminiCredentialFallback{
		RefreshToken:   stringCredential(credentials, "refresh_token"),
		OAuthType:      stringCredential(credentials, "oauth_type"),
		ClientID:       stringCredential(credentials, "client_id"),
		ClientSecret:   stringCredential(credentials, "client_secret"),
		ProjectID:      stringCredential(credentials, "project_id"),
		TierID:         stringCredential(credentials, "tier_id"),
		QuotaProjectID: stringCredential(credentials, "quota_project_id"),
		BaseURL:        stringCredential(credentials, "base_url"),
		Scope:          stringCredential(credentials, "scope"),
	}
}

func providerSourceMissingMessage(provider string) string {
	switch provider {
	case ProviderAnthropic:
		return "Anthropic OAuth 凭据源账户不存在或类型不匹配"
	case ProviderGemini:
		return "Gemini OAuth 凭据源账户不存在或类型不匹配"
	}
	return "Grok OAuth 凭据源账户不存在或类型不匹配"
}

func providerMissingRefreshTokenMessage(provider string) string {
	switch provider {
	case ProviderAnthropic:
		return "Anthropic OAuth 账户缺少 Refresh Token"
	case ProviderGemini:
		return "Gemini OAuth 账户缺少 Refresh Token"
	}
	return "Grok OAuth 账户缺少 Refresh Token"
}
