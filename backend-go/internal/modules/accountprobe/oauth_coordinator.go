package accountprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
)

const oauthRefreshCASMaxRounds = 3

type OAuthCoordinationDisposition string

const (
	OAuthCoordinationReady       OAuthCoordinationDisposition = "ready"
	OAuthCoordinationReschedule  OAuthCoordinationDisposition = "reschedule"
	OAuthCoordinationTaskFailure OAuthCoordinationDisposition = "task_failure"
)

type OAuthCoordinationInput struct {
	Snapshot OAuthProbeCandidateSnapshot
	Prepared PreparedRequest
	Reload   LoadInput
	Now      time.Time
}

func (OAuthCoordinationInput) String() string               { return "OAuthCoordinationInput{redacted}" }
func (OAuthCoordinationInput) GoString() string             { return "OAuthCoordinationInput{redacted}" }
func (OAuthCoordinationInput) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

type OAuthProbeCandidateSnapshot struct {
	candidate   gatewaycandidatewindow.Candidate
	credentials OAuthCredentials
}

func NewOAuthProbeCandidateSnapshot(candidate gatewaycandidatewindow.Candidate, credentialValues map[string]any) (OAuthProbeCandidateSnapshot, error) {
	identity := gatewaycandidatewindow.EffectiveAccountIdentity(candidate)
	provider, ok := oauthProviderForIdentity(identity.ProviderCode, identity.Type)
	if !ok {
		return OAuthProbeCandidateSnapshot{}, fmt.Errorf("%w: unsupported OAuth identity", ErrInvalidOAuthSpec)
	}
	credentials, err := ParseOAuthCredentials(provider, credentialValues)
	if err != nil {
		return OAuthProbeCandidateSnapshot{}, err
	}
	// Hydrated Candidate intentionally exposes only a redacted credential view.
	// Keep the full snapshot map on the candidate copy used to build a fresh
	// attempt, otherwise refresh freshness and the actual Authorization header
	// could be evaluated against different access tokens.
	candidate.Credentials = gatewaycandidatewindow.NewCredentialSet(cloneOAuthMap(credentialValues))
	return OAuthProbeCandidateSnapshot{candidate: candidate, credentials: credentials}, nil
}

func (s OAuthProbeCandidateSnapshot) Candidate() gatewaycandidatewindow.Candidate { return s.candidate }
func (s OAuthProbeCandidateSnapshot) Credentials() OAuthCredentials               { return s.credentials }
func (OAuthProbeCandidateSnapshot) String() string                                { return "OAuthProbeCandidateSnapshot{redacted}" }
func (OAuthProbeCandidateSnapshot) GoString() string                              { return "OAuthProbeCandidateSnapshot{redacted}" }
func (OAuthProbeCandidateSnapshot) MarshalJSON() ([]byte, error)                  { return []byte("{}"), nil }

type OAuthCoordinationResult struct {
	disposition OAuthCoordinationDisposition
	attempt     OAuthAttempt
	err         error
}

func (r OAuthCoordinationResult) Disposition() OAuthCoordinationDisposition { return r.disposition }
func (r OAuthCoordinationResult) Attempt() (OAuthAttempt, bool) {
	return r.attempt, r.disposition == OAuthCoordinationReady
}
func (r OAuthCoordinationResult) Err() error { return r.err }
func (r OAuthCoordinationResult) String() string {
	return fmt.Sprintf("OAuthCoordinationResult{Disposition:%q redacted}", r.disposition)
}
func (r OAuthCoordinationResult) GoString() string           { return r.String() }
func (OAuthCoordinationResult) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

type OAuthCandidateReloader interface {
	ReloadOAuthProbeCandidate(context.Context, LoadInput) (OAuthProbeCandidateSnapshot, bool, error)
}

type OAuthRefreshLockTask func(context.Context, func(context.Context) error) error

type OAuthRefreshLockRunner interface {
	WithOAuthRefreshLock(context.Context, string, string, OAuthRefreshLockTask) error
}

type OAuthRefreshHTTPResponse struct {
	statusCode int
	body       []byte
	truncated  bool
}

func NewOAuthRefreshHTTPResponse(statusCode int, body []byte, truncated bool) OAuthRefreshHTTPResponse {
	return OAuthRefreshHTTPResponse{statusCode: statusCode, body: append([]byte(nil), body...), truncated: truncated}
}
func (r OAuthRefreshHTTPResponse) StatusCode() int { return r.statusCode }
func (r OAuthRefreshHTTPResponse) Body() []byte    { return append([]byte(nil), r.body...) }
func (r OAuthRefreshHTTPResponse) Truncated() bool { return r.truncated }
func (OAuthRefreshHTTPResponse) String() string    { return "OAuthRefreshHTTPResponse{redacted}" }
func (OAuthRefreshHTTPResponse) GoString() string  { return "OAuthRefreshHTTPResponse{redacted}" }
func (OAuthRefreshHTTPResponse) MarshalJSON() ([]byte, error) {
	return []byte("{}"), nil
}

type OAuthRefreshHTTPExecutor interface {
	ExecuteOAuthRefresh(context.Context, gatewaycandidatewindow.Candidate, OAuthRefreshRequest) (OAuthRefreshHTTPResponse, error)
}

// OAuthHTTPExecutionFence is evaluated inside the PostgreSQL revocation gate
// immediately before a refresh or enrichment request is written upstream.
type OAuthHTTPExecutionFence func(context.Context) error

type oauthHTTPExecutionFenceContextKey struct{}

func withOAuthHTTPExecutionFence(ctx context.Context, fence OAuthHTTPExecutionFence) context.Context {
	return context.WithValue(ctx, oauthHTTPExecutionFenceContextKey{}, fence)
}

func oauthHTTPExecutionFenceFromContext(ctx context.Context) OAuthHTTPExecutionFence {
	fence, _ := ctx.Value(oauthHTTPExecutionFenceContextKey{}).(OAuthHTTPExecutionFence)
	return fence
}

type OAuthCredentialCASInput struct {
	accountID                 string
	systemAccountID           string
	expectedAccountType       string
	expectedConfigRevision    int
	connectionIdentityChanged bool
	patch                     OAuthCredentialPatch
}

func (i OAuthCredentialCASInput) AccountID() string               { return i.accountID }
func (i OAuthCredentialCASInput) SystemAccountID() string         { return i.systemAccountID }
func (i OAuthCredentialCASInput) ExpectedAccountType() string     { return i.expectedAccountType }
func (i OAuthCredentialCASInput) ExpectedConfigRevision() int     { return i.expectedConfigRevision }
func (i OAuthCredentialCASInput) ConnectionIdentityChanged() bool { return i.connectionIdentityChanged }
func (i OAuthCredentialCASInput) Patch() OAuthCredentialPatch     { return i.patch }
func (OAuthCredentialCASInput) String() string                    { return "OAuthCredentialCASInput{redacted}" }
func (OAuthCredentialCASInput) GoString() string                  { return "OAuthCredentialCASInput{redacted}" }
func (OAuthCredentialCASInput) MarshalJSON() ([]byte, error)      { return []byte("{}"), nil }

type OAuthCredentialCAS interface {
	PrepareOAuthProbeCredentialCAS(context.Context, OAuthCredentialCASInput) (OAuthPreparedCredentialCAS, error)
	CompareAndSwapOAuthProbeCredentials(context.Context, OAuthPreparedCredentialCAS) (bool, error)
}

type OAuthPreparedCredentialCAS struct {
	value any
}

func NewOAuthPreparedCredentialCAS(value any) OAuthPreparedCredentialCAS {
	return OAuthPreparedCredentialCAS{value: value}
}
func (p OAuthPreparedCredentialCAS) Value() any                 { return p.value }
func (OAuthPreparedCredentialCAS) String() string               { return "OAuthPreparedCredentialCAS{redacted}" }
func (OAuthPreparedCredentialCAS) GoString() string             { return "OAuthPreparedCredentialCAS{redacted}" }
func (OAuthPreparedCredentialCAS) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

type OAuthRefreshEnricher interface {
	EnrichOAuthRefresh(context.Context, gatewaycandidatewindow.Candidate, OAuthRefreshResult) (OAuthRefreshResult, error)
}

type OAuthCoordinator struct {
	Reloader OAuthCandidateReloader
	Lock     OAuthRefreshLockRunner
	CAS      OAuthCredentialCAS
	Refresh  OAuthRefreshHTTPExecutor
	Enricher OAuthRefreshEnricher
	Sleep    func(context.Context, time.Duration) error
	Now      func() time.Time
}

func (c OAuthCoordinator) Coordinate(ctx context.Context, input OAuthCoordinationInput) OAuthCoordinationResult {
	now := input.Now
	if now.IsZero() {
		now = c.currentTime()
	}
	now = now.UTC()
	candidate := input.Snapshot.Candidate()
	credentials := input.Snapshot.Credentials()
	provider := credentials.provider
	identity := gatewaycandidatewindow.EffectiveAccountIdentity(candidate)
	expectedProvider, ok := oauthProviderForIdentity(identity.ProviderCode, identity.Type)
	if !ok || !validOAuthProvider(provider) || expectedProvider != provider {
		return oauthTaskFailure(fmt.Errorf("OAuth probe snapshot identity is invalid"))
	}
	if !ShouldRefreshOAuth(credentials, now) {
		attempt, prepareErr := PrepareOAuthAttempt(candidate, input.Prepared)
		if prepareErr != nil {
			return oauthTaskFailure(prepareErr)
		}
		return OAuthCoordinationResult{disposition: OAuthCoordinationReady, attempt: attempt}
	}
	if c.Reloader == nil || c.Lock == nil || c.CAS == nil || c.Refresh == nil {
		return oauthTaskFailure(fmt.Errorf("OAuth refresh coordinator dependencies are required"))
	}
	originalIdentity := identity
	var locked OAuthCoordinationResult
	lockErr := c.Lock.WithOAuthRefreshLock(ctx, string(provider), originalIdentity.AccountID, func(lockCtx context.Context, assertOwned func(context.Context) error) error {
		locked = c.coordinateLocked(lockCtx, assertOwned, input, provider, originalIdentity, now)
		return nil
	})
	if lockErr != nil {
		return oauthTaskFailure(fmt.Errorf("OAuth refresh lock failed"))
	}
	if locked.disposition == "" {
		return oauthTaskFailure(fmt.Errorf("OAuth refresh lock task did not return a result"))
	}
	return locked
}

func (c OAuthCoordinator) coordinateLocked(
	ctx context.Context,
	assertOwned func(context.Context) error,
	input OAuthCoordinationInput,
	provider OAuthProvider,
	originalIdentity gatewaycandidatewindow.AccountIdentity,
	now time.Time,
) OAuthCoordinationResult {
	if assertOwned == nil {
		return oauthTaskFailure(fmt.Errorf("OAuth refresh lock ownership assertion is required"))
	}
	var previous OAuthCredentials
	for round := 0; round < oauthRefreshCASMaxRounds; round++ {
		candidate, credentials, result := c.reloadForRefresh(ctx, input.Reload, provider, originalIdentity)
		if result.disposition != "" {
			return result
		}
		if round > 0 && oauthCredentialRotated(previous, credentials) {
			return OAuthCoordinationResult{disposition: OAuthCoordinationReschedule}
		}
		if !ShouldRefreshOAuth(credentials, now) {
			return OAuthCoordinationResult{disposition: OAuthCoordinationReschedule}
		}
		refreshRequest, err := BuildOAuthRefreshRequest(credentials)
		if err != nil {
			return oauthTaskFailure(err)
		}
		executionFence := c.oauthHTTPExecutionFence(input.Reload, candidate, credentials, assertOwned)
		fencedCtx := withOAuthHTTPExecutionFence(ctx, executionFence)
		refreshResult, err := c.exchangeRefresh(fencedCtx, candidate, refreshRequest)
		if err != nil {
			return oauthTaskFailure(err)
		}
		if refreshResult.RequiresGeminiEnrichment() {
			if c.Enricher == nil {
				return oauthTaskFailure(fmt.Errorf("Gemini OAuth refresh enrichment is required"))
			}
			refreshResult, err = c.Enricher.EnrichOAuthRefresh(fencedCtx, candidate, refreshResult)
			if err != nil {
				return oauthTaskFailure(fmt.Errorf("Gemini OAuth refresh enrichment failed"))
			}
		}
		patch, err := MergeOAuthRefreshCredentials(credentials, refreshResult)
		if err != nil {
			return oauthTaskFailure(err)
		}
		identity := gatewaycandidatewindow.EffectiveAccountIdentity(candidate)
		casInput := OAuthCredentialCASInput{
			accountID: identity.AccountID, systemAccountID: oauthSourceSystemAccountID(candidate),
			expectedAccountType: identity.Type, expectedConfigRevision: identity.ConfigRevision,
			connectionIdentityChanged: oauthConnectionIdentityChanged(credentials, patch), patch: patch,
		}
		preparedCAS, err := c.CAS.PrepareOAuthProbeCredentialCAS(ctx, casInput)
		if err != nil {
			return oauthTaskFailure(fmt.Errorf("prepare OAuth credential CAS failed"))
		}
		if err := assertOwned(ctx); err != nil {
			return oauthTaskFailure(fmt.Errorf("OAuth refresh lock ownership was lost"))
		}
		swapped, err := c.CAS.CompareAndSwapOAuthProbeCredentials(ctx, preparedCAS)
		if err != nil {
			return oauthTaskFailure(fmt.Errorf("OAuth credential CAS failed"))
		}
		if swapped {
			// CAS advances the source configuration revision. The current cooldown
			// task must never send a model request under its now-stale revision.
			return OAuthCoordinationResult{disposition: OAuthCoordinationReschedule}
		}

		_, winnerCredentials, conflict := c.reloadForRefresh(ctx, input.Reload, provider, originalIdentity)
		if conflict.disposition != "" {
			return conflict
		}
		if !ShouldRefreshOAuth(winnerCredentials, now) || oauthCredentialRotated(credentials, winnerCredentials) {
			return OAuthCoordinationResult{disposition: OAuthCoordinationReschedule}
		}
		previous = winnerCredentials
	}
	return oauthTaskFailure(fmt.Errorf("OAuth credential CAS retry limit reached"))
}

func (c OAuthCoordinator) oauthHTTPExecutionFence(
	input LoadInput,
	expectedCandidate gatewaycandidatewindow.Candidate,
	expectedCredentials OAuthCredentials,
	assertOwned func(context.Context) error,
) OAuthHTTPExecutionFence {
	return func(ctx context.Context) error {
		reloadInput := input
		reloadInput.Now = c.currentTime().UTC()
		current, found, err := c.Reloader.ReloadOAuthProbeCandidate(ctx, reloadInput)
		if err != nil {
			return fmt.Errorf("reload OAuth HTTP execution fence: %w", err)
		}
		if !found {
			return fmt.Errorf("OAuth HTTP execution fence target is no longer available")
		}
		if !sameExecutionCandidate(expectedCandidate, current.Candidate()) ||
			!reflect.DeepEqual(expectedCredentials.values, current.Credentials().values) {
			return fmt.Errorf("OAuth HTTP execution fence candidate or credentials changed")
		}
		if assertOwned == nil || assertOwned(ctx) != nil {
			return fmt.Errorf("OAuth HTTP execution fence refresh lock ownership was lost")
		}
		return nil
	}
}

func (c OAuthCoordinator) reloadForRefresh(
	ctx context.Context,
	input LoadInput,
	provider OAuthProvider,
	original gatewaycandidatewindow.AccountIdentity,
) (gatewaycandidatewindow.Candidate, OAuthCredentials, OAuthCoordinationResult) {
	input.Now = c.currentTime().UTC()
	snapshot, found, err := c.Reloader.ReloadOAuthProbeCandidate(ctx, input)
	if err != nil {
		return gatewaycandidatewindow.Candidate{}, OAuthCredentials{}, oauthTaskFailure(fmt.Errorf("reload OAuth probe candidate failed"))
	}
	if !found {
		return gatewaycandidatewindow.Candidate{}, OAuthCredentials{}, oauthTaskFailure(fmt.Errorf("OAuth probe candidate is no longer available"))
	}
	candidate := snapshot.Candidate()
	identity := gatewaycandidatewindow.EffectiveAccountIdentity(candidate)
	if identity.AccountID != original.AccountID || !strings.EqualFold(identity.ProviderCode, original.ProviderCode) || !strings.EqualFold(identity.Type, original.Type) {
		return gatewaycandidatewindow.Candidate{}, OAuthCredentials{}, oauthTaskFailure(fmt.Errorf("OAuth probe source identity changed"))
	}
	credentials := snapshot.Credentials()
	if credentials.provider != provider {
		return gatewaycandidatewindow.Candidate{}, OAuthCredentials{}, oauthTaskFailure(fmt.Errorf("OAuth probe credentials changed provider"))
	}
	return candidate, credentials, OAuthCoordinationResult{}
}

func (c OAuthCoordinator) currentTime() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c OAuthCoordinator) exchangeRefresh(ctx context.Context, candidate gatewaycandidatewindow.Candidate, request OAuthRefreshRequest) (OAuthRefreshResult, error) {
	current := request
	for variant := 0; variant < 2; variant++ {
		maxAttempts := current.MaxAttempts()
		selectedFallback := false
		for completed := 1; completed <= maxAttempts; completed++ {
			callCtx := ctx
			cancel := func() {}
			if timeout := current.Timeout(); timeout > 0 {
				callCtx, cancel = context.WithTimeout(ctx, timeout)
			}
			response, err := c.Refresh.ExecuteOAuthRefresh(callCtx, candidate, current)
			cancel()
			if err != nil {
				if completed < maxAttempts {
					if sleepErr := c.sleep(ctx, current.RetryBackoff(completed)); sleepErr != nil {
						return OAuthRefreshResult{}, fmt.Errorf("OAuth token refresh retry interrupted")
					}
					continue
				}
				return OAuthRefreshResult{}, fmt.Errorf("OAuth token refresh request failed")
			}
			body := response.Body()
			if response.Truncated() || len(body) > current.MaxResponseBytes() {
				return OAuthRefreshResult{}, fmt.Errorf("OAuth token refresh response exceeded the bounded limit")
			}
			if fallback, ok := current.FallbackForResponse(response.StatusCode(), body, response.Truncated()); ok {
				current = fallback
				selectedFallback = true
				break
			}
			if response.StatusCode() >= 200 && response.StatusCode() < 300 {
				return ParseOAuthRefreshResponse(current, response.StatusCode(), body, c.currentTime().UTC())
			}
			if current.RetryableResponse(response.StatusCode(), completed, body) {
				if sleepErr := c.sleep(ctx, current.RetryBackoff(completed)); sleepErr != nil {
					return OAuthRefreshResult{}, fmt.Errorf("OAuth token refresh retry interrupted")
				}
				continue
			}
			return OAuthRefreshResult{}, fmt.Errorf("OAuth token refresh returned HTTP %d", response.StatusCode())
		}
		if !selectedFallback {
			return OAuthRefreshResult{}, fmt.Errorf("OAuth token refresh attempt limit reached")
		}
	}
	return OAuthRefreshResult{}, fmt.Errorf("OAuth token refresh fallback limit reached")
}

func (c OAuthCoordinator) sleep(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	if c.Sleep != nil {
		return c.Sleep(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func oauthCredentialRotated(left, right OAuthCredentials) bool {
	return left.AccessToken() != right.AccessToken() || oauthString(left.values, "refresh_token") != oauthString(right.values, "refresh_token")
}

func oauthSourceSystemAccountID(candidate gatewaycandidatewindow.Candidate) string {
	projection := candidate.Projection
	if strings.TrimSpace(projection.ResourceAccountID) != "" {
		return strings.TrimSpace(projection.AuthorizationOwnerSystemAccountID)
	}
	return strings.TrimSpace(projection.SystemAccountID)
}

func oauthConnectionIdentityChanged(current OAuthCredentials, patch OAuthCredentialPatch) bool {
	for _, key := range []string{
		"api_key", "api_keys", "access_token", "refresh_token", "client_id", "client_secret", "id_token",
		"account_id", "chatgpt_user_id", "quota_project_id", "base_url", "supported_endpoint_modes",
	} {
		if !reflect.DeepEqual(current.values[key], patch.values[key]) {
			return true
		}
	}
	return false
}

func oauthTaskFailure(err error) OAuthCoordinationResult {
	if err == nil {
		err = fmt.Errorf("OAuth probe task failed")
	}
	return OAuthCoordinationResult{disposition: OAuthCoordinationTaskFailure, err: err}
}

var _ json.Marshaler = OAuthCoordinationInput{}
var _ json.Marshaler = OAuthProbeCandidateSnapshot{}
var _ json.Marshaler = OAuthCoordinationResult{}
var _ json.Marshaler = OAuthRefreshHTTPResponse{}
var _ json.Marshaler = OAuthCredentialCASInput{}
var _ json.Marshaler = OAuthPreparedCredentialCAS{}
