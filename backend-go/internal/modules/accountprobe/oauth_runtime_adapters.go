package accountprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

type OAuthCredentialCodec interface {
	DecryptJSON(string) (map[string]any, error)
	EncryptJSON(map[string]any) (string, error)
}

type OAuthSnapshotLoader struct {
	Loader ExactCandidateLoader
	Codec  OAuthCredentialCodec
}

func (l OAuthSnapshotLoader) Snapshot(candidate gatewaycandidatewindow.Candidate) (OAuthProbeCandidateSnapshot, error) {
	if l.Codec == nil {
		return OAuthProbeCandidateSnapshot{}, fmt.Errorf("OAuth probe credential codec is required")
	}
	ciphertext := candidate.Projection.CredentialsEncrypted
	if strings.TrimSpace(candidate.Projection.ResourceAccountID) != "" {
		ciphertext = candidate.Projection.ResourceCredentialsEncrypted
	}
	if strings.TrimSpace(ciphertext) == "" {
		return OAuthProbeCandidateSnapshot{}, fmt.Errorf("OAuth probe source credentials are unavailable")
	}
	values, err := l.Codec.DecryptJSON(ciphertext)
	if err != nil {
		return OAuthProbeCandidateSnapshot{}, fmt.Errorf("decrypt OAuth probe source credentials: %w", err)
	}
	return NewOAuthProbeCandidateSnapshot(candidate, values)
}

func (l OAuthSnapshotLoader) ReloadOAuthProbeCandidate(ctx context.Context, input LoadInput) (OAuthProbeCandidateSnapshot, bool, error) {
	if l.Loader == nil {
		return OAuthProbeCandidateSnapshot{}, false, fmt.Errorf("OAuth probe exact candidate loader is required")
	}
	candidate, found, err := l.Loader.Load(ctx, input)
	if err != nil || !found {
		return OAuthProbeCandidateSnapshot{}, found, err
	}
	snapshot, err := l.Snapshot(candidate)
	return snapshot, err == nil, err
}

type RedisOAuthRefreshLockRunner struct {
	lock *redisplatform.OAuthRefreshLock
}

func NewRedisOAuthRefreshLockRunner(lock *redisplatform.OAuthRefreshLock) RedisOAuthRefreshLockRunner {
	return RedisOAuthRefreshLockRunner{lock: lock}
}

func (r RedisOAuthRefreshLockRunner) WithOAuthRefreshLock(
	ctx context.Context,
	providerCode, sourceAccountID string,
	task OAuthRefreshLockTask,
) error {
	if r.lock == nil {
		return fmt.Errorf("OAuth refresh Redis lock is required")
	}
	return r.lock.WithLock(ctx, providerCode, sourceAccountID, func(lockCtx context.Context, assertOwned func(context.Context) error) error {
		return task(lockCtx, assertOwned)
	})
}

type OAuthRefreshTransportExecutor struct{ Factory CandidateTransportFactory }

func (e OAuthRefreshTransportExecutor) ExecuteOAuthRefresh(
	ctx context.Context,
	candidate gatewaycandidatewindow.Candidate,
	request OAuthRefreshRequest,
) (OAuthRefreshHTTPResponse, error) {
	if e.Factory == nil {
		return OAuthRefreshHTTPResponse{}, fmt.Errorf("OAuth refresh transport factory is required")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, request.URL(), bytes.NewReader(request.Body()))
	if err != nil {
		return OAuthRefreshHTTPResponse{}, fmt.Errorf("build OAuth refresh request: %w", err)
	}
	httpRequest.Header = request.Header()
	transport, err := e.Factory.New(candidate)
	if err != nil {
		return OAuthRefreshHTTPResponse{}, err
	}
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		defer closer.CloseIdleConnections()
	}
	result, executeErr := transport.ExecuteWithFence(ctx, httpRequest, nil)
	if !result.FramingComplete {
		if executeErr == nil {
			executeErr = fmt.Errorf("OAuth refresh response framing is incomplete")
		}
		return OAuthRefreshHTTPResponse{}, executeErr
	}
	return NewOAuthRefreshHTTPResponse(result.StatusCode, result.Body, result.BodyTruncated), nil
}

type OAuthCredentialCASAdapter struct {
	Codec OAuthCredentialCodec
	Store port.OAuthCredentialRefreshStore
	Now   func() time.Time
}

func (a OAuthCredentialCASAdapter) PrepareOAuthProbeCredentialCAS(
	ctx context.Context,
	input OAuthCredentialCASInput,
) (OAuthPreparedCredentialCAS, error) {
	if err := ctx.Err(); err != nil {
		return OAuthPreparedCredentialCAS{}, err
	}
	if a.Codec == nil || a.Store == nil {
		return OAuthPreparedCredentialCAS{}, fmt.Errorf("OAuth credential CAS codec and store are required")
	}
	values := input.Patch().Values()
	encrypted, err := a.Codec.EncryptJSON(values)
	if err != nil {
		return OAuthPreparedCredentialCAS{}, fmt.Errorf("encrypt OAuth refresh credentials: %w", err)
	}
	credentialSource := firstOAuthCredentialText(values, "refresh_token", "access_token")
	if credentialSource == "" {
		return OAuthPreparedCredentialCAS{}, fmt.Errorf("OAuth refresh credential source is required")
	}
	fingerprintBytes := sha256.Sum256([]byte(credentialSource))
	var expiresAt *time.Time
	if raw := firstOAuthCredentialText(values, "expires_at"); raw != "" {
		parsed, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return OAuthPreparedCredentialCAS{}, fmt.Errorf("parse OAuth refresh expiry: %w", parseErr)
		}
		parsed = parsed.UTC()
		expiresAt = &parsed
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	prepared := port.OAuthCredentialRefreshCASInput{
		AccountID: input.AccountID(), SystemAccountID: input.SystemAccountID(),
		ExpectedAccountType: input.ExpectedAccountType(), ExpectedConfigRevision: input.ExpectedConfigRevision(),
		Secrets: port.NewOAuthCredentialRefreshSecrets(
			encrypted, hex.EncodeToString(fingerprintBytes[:]), maskOAuthCredential(credentialSource),
		),
		AccessTokenExpiresAt: expiresAt, RefreshTokenPresent: firstOAuthCredentialText(values, "refresh_token") != "",
		CircuitOwnerConfigurationChanged: input.ConnectionIdentityChanged(), UpdatedAt: now.UTC(),
	}
	return NewOAuthPreparedCredentialCAS(prepared), nil
}

func (a OAuthCredentialCASAdapter) CompareAndSwapOAuthProbeCredentials(
	ctx context.Context,
	prepared OAuthPreparedCredentialCAS,
) (bool, error) {
	if a.Store == nil {
		return false, fmt.Errorf("OAuth credential CAS store is required")
	}
	input, ok := prepared.Value().(port.OAuthCredentialRefreshCASInput)
	if !ok {
		return false, fmt.Errorf("OAuth prepared credential CAS has an invalid payload")
	}
	_, applied, err := a.Store.CompareAndSwapOAuthCredentials(ctx, input)
	return applied, err
}

type OAuthGeminiRefreshEnricher struct{ Enricher *GeminiOAuthEnricher }

func (e OAuthGeminiRefreshEnricher) EnrichOAuthRefresh(
	ctx context.Context,
	candidate gatewaycandidatewindow.Candidate,
	result OAuthRefreshResult,
) (OAuthRefreshResult, error) {
	if e.Enricher == nil || result.provider != OAuthGemini {
		return OAuthRefreshResult{}, fmt.Errorf("Gemini OAuth refresh enricher is required")
	}
	proxyURL, err := candidateProxyURL(candidate.Proxy)
	if err != nil {
		return OAuthRefreshResult{}, err
	}
	output, err := e.Enricher.EnrichGeminiOAuth(ctx, GeminiOAuthEnrichmentInput{
		OAuthType: oauthString(result.values, "oauth_type"),
		Secrets:   NewGeminiOAuthEnrichmentSecrets(oauthString(result.values, "access_token")),
		ProjectID: oauthString(result.values, "project_id"), TierID: oauthString(result.values, "tier_id"),
		Scope: oauthString(result.values, "scope"), ProxyURL: proxyURL,
	})
	if err != nil {
		return OAuthRefreshResult{}, err
	}
	values := cloneOAuthMap(result.values)
	if output.ProjectID != "" {
		values["project_id"] = output.ProjectID
	}
	if output.TierID != "" {
		values["tier_id"] = output.TierID
	}
	if output.DriveStorageLimit != nil && output.DriveStorageUsage != nil {
		values["drive_storage_limit"] = *output.DriveStorageLimit
		values["drive_storage_usage"] = *output.DriveStorageUsage
		if !output.DriveTierUpdatedAt.IsZero() {
			values["drive_tier_updated_at"] = output.DriveTierUpdatedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	return OAuthRefreshResult{provider: result.provider, values: values}, nil
}

func firstOAuthCredentialText(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := oauthString(values, key); value != "" {
			return value
		}
	}
	return ""
}

func maskOAuthCredential(value string) string {
	if len(value) <= 10 {
		return value[:min(2, len(value))] + "***" + value[max(0, len(value)-2):]
	}
	return value[:6] + "***" + value[len(value)-4:]
}

var _ OAuthCandidateReloader = OAuthSnapshotLoader{}
var _ OAuthRefreshLockRunner = RedisOAuthRefreshLockRunner{}
var _ OAuthRefreshHTTPExecutor = OAuthRefreshTransportExecutor{}
var _ OAuthCredentialCAS = OAuthCredentialCASAdapter{}
var _ OAuthRefreshEnricher = OAuthGeminiRefreshEnricher{}
