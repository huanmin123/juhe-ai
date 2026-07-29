package accountprobe

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/accounthealth"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/store/port"
)

type ExactCandidateLoader interface {
	Load(context.Context, LoadInput) (gatewaycandidatewindow.Candidate, bool, error)
}

type CooldownCandidateReader interface {
	FindDueCooldownAccountRetest(context.Context, string, time.Time) (port.CooldownAccountRetestCandidate, bool, error)
}

type APIKeyExecutionFence struct {
	Loader    ExactCandidateLoader
	Current   CooldownCandidateReader
	LoadInput LoadInput
	Expected  port.CooldownAccountRetestCandidate
	Candidate gatewaycandidatewindow.Candidate
	Prepared  PreparedRequest
	Attempt   APIKeyAttempt
	Now       func() time.Time
}

func (f APIKeyExecutionFence) Recheck(ctx context.Context) error {
	if f.Loader == nil || f.Current == nil {
		return fmt.Errorf("account probe execution fence candidate readers are required")
	}
	now := time.Now()
	if f.Now != nil {
		now = f.Now()
	}
	input := f.LoadInput
	input.Now = now.UTC()
	currentCooldown, found, err := f.Current.FindDueCooldownAccountRetest(ctx, f.Expected.ID, input.Now)
	if err != nil {
		return fmt.Errorf("reload cooldown account probe execution fence: %w", err)
	}
	if !found || !sameCooldownCandidate(f.Expected, currentCooldown) {
		return fmt.Errorf("account probe execution fence cooldown generation changed")
	}
	current, found, err := f.Loader.Load(ctx, input)
	if err != nil {
		return fmt.Errorf("reload account probe execution fence: %w", err)
	}
	if !found {
		return fmt.Errorf("account probe execution fence target is no longer available")
	}
	if err := verifyCooldownCandidateVersion(f.Expected, current); err != nil {
		return err
	}
	if !sameExecutionCandidate(f.Candidate, current) {
		return fmt.Errorf("account probe execution fence candidate changed")
	}
	currentAttempt, err := PrepareAPIKeyAttempt(current, f.Prepared, now)
	if err != nil {
		return fmt.Errorf("rebuild account probe execution fence attempt: %w", err)
	}
	if currentAttempt.Method() != f.Attempt.Method() || currentAttempt.URL() != f.Attempt.URL() ||
		currentAttempt.KeyFingerprint() != f.Attempt.KeyFingerprint() || currentAttempt.KeyIndex() != f.Attempt.KeyIndex() ||
		!sameHeader(currentAttempt.Header(), f.Attempt.Header()) || !bytes.Equal(currentAttempt.Body(), f.Attempt.Body()) {
		return fmt.Errorf("account probe execution fence request or credential changed")
	}
	return nil
}

type OAuthExecutionFence struct {
	Reloader  OAuthCandidateReloader
	Current   CooldownCandidateReader
	LoadInput LoadInput
	Expected  port.CooldownAccountRetestCandidate
	Candidate gatewaycandidatewindow.Candidate
	Prepared  PreparedRequest
	Attempt   OAuthAttempt
	Fallback  bool
	Now       func() time.Time
}

func (f OAuthExecutionFence) Recheck(ctx context.Context) error {
	if f.Reloader == nil || f.Current == nil {
		return fmt.Errorf("OAuth account probe execution fence candidate readers are required")
	}
	now := time.Now()
	if f.Now != nil {
		now = f.Now()
	}
	input := f.LoadInput
	input.Now = now.UTC()
	currentCooldown, found, err := f.Current.FindDueCooldownAccountRetest(ctx, f.Expected.ID, input.Now)
	if err != nil {
		return fmt.Errorf("reload OAuth cooldown account probe execution fence: %w", err)
	}
	if !found || !sameCooldownCandidate(f.Expected, currentCooldown) {
		return fmt.Errorf("OAuth account probe execution fence cooldown generation changed")
	}
	snapshot, found, err := f.Reloader.ReloadOAuthProbeCandidate(ctx, input)
	if err != nil {
		return fmt.Errorf("reload OAuth account probe execution fence: %w", err)
	}
	if !found {
		return fmt.Errorf("OAuth account probe execution fence target is no longer available")
	}
	if ShouldRefreshOAuth(snapshot.Credentials(), now.UTC()) {
		return fmt.Errorf("OAuth account probe execution fence credentials require refresh")
	}
	current := snapshot.Candidate()
	if err := verifyCooldownCandidateVersion(f.Expected, current); err != nil {
		return err
	}
	if !sameExecutionCandidate(f.Candidate, current) {
		return fmt.Errorf("OAuth account probe execution fence candidate changed")
	}
	currentAttempt, err := PrepareOAuthAttempt(current, f.Prepared)
	if err != nil {
		return fmt.Errorf("rebuild OAuth account probe execution fence attempt: %w", err)
	}
	if f.Fallback {
		var ok bool
		currentAttempt, ok = currentAttempt.XAIModelFallback(http.StatusForbidden, []byte("access denied"), false)
		if !ok {
			return fmt.Errorf("rebuild OAuth account probe execution fence fallback: request is no longer eligible")
		}
	}
	if currentAttempt.Method() != f.Attempt.Method() || currentAttempt.URL() != f.Attempt.URL() ||
		currentAttempt.EvidenceMode() != f.Attempt.EvidenceMode() ||
		!sameHeader(currentAttempt.Header(), f.Attempt.Header()) || !bytes.Equal(currentAttempt.Body(), f.Attempt.Body()) {
		return fmt.Errorf("OAuth account probe execution fence request or credential changed")
	}
	return nil
}

func verifyCooldownCandidateVersion(expected port.CooldownAccountRetestCandidate, current gatewaycandidatewindow.Candidate) error {
	projection := current.Projection
	if strings.TrimSpace(projection.AccountID) != strings.TrimSpace(expected.ID) ||
		strings.TrimSpace(projection.GroupID) != strings.TrimSpace(expected.GroupID) ||
		strings.TrimSpace(projection.SystemAccountID) != strings.TrimSpace(expected.SystemAccountID) ||
		projection.ConfigRevision != expected.ConfigRevision || projection.DispatchRevision != int64(expected.DispatchRevision) {
		return fmt.Errorf("account probe execution fence target revision or binding changed")
	}
	hasResource := strings.TrimSpace(projection.ResourceAccountID) != ""
	if expected.SourceConfigRevision == nil {
		if hasResource {
			return fmt.Errorf("account probe execution fence gained an unexpected source account")
		}
		return nil
	}
	identity := gatewaycandidatewindow.EffectiveAccountIdentity(current)
	if !hasResource || identity.ConfigRevision != *expected.SourceConfigRevision {
		return fmt.Errorf("account probe execution fence source revision changed")
	}
	return nil
}

func sameExecutionCandidate(left, right gatewaycandidatewindow.Candidate) bool {
	leftProjection := left.Projection
	rightProjection := right.Projection
	if gatewaycandidatewindow.EffectiveAccountIdentity(left) != gatewaycandidatewindow.EffectiveAccountIdentity(right) ||
		leftProjection.AccountID != rightProjection.AccountID ||
		leftProjection.SystemAccountID != rightProjection.SystemAccountID ||
		leftProjection.GroupID != rightProjection.GroupID ||
		leftProjection.AccountAuthorizationID != rightProjection.AccountAuthorizationID ||
		leftProjection.AuthorizationSourceAccountID != rightProjection.AuthorizationSourceAccountID ||
		leftProjection.AuthorizationID != rightProjection.AuthorizationID ||
		leftProjection.ResourceAccountID != rightProjection.ResourceAccountID ||
		leftProjection.Status != rightProjection.Status ||
		leftProjection.Schedulable != rightProjection.Schedulable ||
		!sameTimePointer(leftProjection.CooldownUntil, rightProjection.CooldownUntil) ||
		!sameTimePointer(leftProjection.AccountExpiresAt, rightProjection.AccountExpiresAt) ||
		leftProjection.ResourceStatus != rightProjection.ResourceStatus ||
		leftProjection.ResourceSchedulable != rightProjection.ResourceSchedulable ||
		!sameTimePointer(leftProjection.ResourceCooldownUntil, rightProjection.ResourceCooldownUntil) ||
		!sameTimePointer(leftProjection.ResourceAccountExpiresAt, rightProjection.ResourceAccountExpiresAt) ||
		leftProjection.ConfigRevision != rightProjection.ConfigRevision ||
		leftProjection.DispatchRevision != rightProjection.DispatchRevision ||
		leftProjection.ClientCompatibility != rightProjection.ClientCompatibility ||
		leftProjection.ResourceClientCompatibility != rightProjection.ResourceClientCompatibility ||
		leftProjection.ProxyProfileID != rightProjection.ProxyProfileID ||
		leftProjection.ResourceProxyProfileID != rightProjection.ResourceProxyProfileID ||
		left.DefaultBaseURL != right.DefaultBaseURL ||
		!slices.Equal(left.SupportedModels, right.SupportedModels) ||
		!slices.Equal(left.ModelMappings, right.ModelMappings) {
		return false
	}
	return sameProxy(left.Proxy, right.Proxy)
}

func sameCooldownCandidate(left, right port.CooldownAccountRetestCandidate) bool {
	leftVersion := accounthealth.RetestTaskVersion{
		ConfigRevision: left.ConfigRevision, DispatchRevision: left.DispatchRevision,
		ObservationStartedAt: left.ObservationStartedAt, Generation: left.Generation, SourceConfigRevision: left.SourceConfigRevision,
	}
	rightVersion := accounthealth.RetestTaskVersion{
		ConfigRevision: right.ConfigRevision, DispatchRevision: right.DispatchRevision,
		ObservationStartedAt: right.ObservationStartedAt, Generation: right.Generation, SourceConfigRevision: right.SourceConfigRevision,
	}
	return accounthealth.CooldownRetestTaskCurrent(leftVersion, rightVersion) &&
		left.ID == right.ID && left.SystemAccountID == right.SystemAccountID && left.GroupID == right.GroupID &&
		left.HealthCheckModel == right.HealthCheckModel && left.HealthCheckEndpointMode == right.HealthCheckEndpointMode
}

func sameTimePointer(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func sameProxy(left, right *gatewaycandidatewindow.ProxyRuntime) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.ID != right.ID || left.Type != right.Type || left.Host != right.Host || left.Port != right.Port ||
		left.Username != right.Username || left.Enabled != right.Enabled || left.Available != right.Available ||
		left.UnavailableReason != right.UnavailableReason {
		return false
	}
	leftPassword, leftHasPassword := left.Credentials.StringValue("password")
	rightPassword, rightHasPassword := right.Credentials.StringValue("password")
	return leftHasPassword == rightHasPassword && leftPassword == rightPassword
}

func sameHeader(left, right http.Header) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValues := range left {
		rightValues, ok := right[key]
		if !ok || !slices.Equal(leftValues, rightValues) {
			return false
		}
	}
	return true
}

var _ ExactCandidateLoader = Loader{}
