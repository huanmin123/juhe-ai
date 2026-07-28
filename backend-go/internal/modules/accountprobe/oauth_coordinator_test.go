package accountprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/store/port"
)

func TestOAuthCoordinatorFreshCredentialsBuildAttemptWithoutRefreshDependencies(t *testing.T) {
	now := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	candidate := oauthCoordinatorCandidate(7, "candidate-token", "refresh-secret", now.Add(time.Hour))
	snapshot, err := NewOAuthProbeCandidateSnapshot(candidate, map[string]any{
		"access_token": "access-secret", "refresh_token": "refresh-secret", "expires_at": now.Add(time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := (OAuthCoordinator{}).Coordinate(t.Context(), OAuthCoordinationInput{
		Snapshot: snapshot, Prepared: oauthCoordinatorPrepared(), Now: now,
	})
	if result.Disposition() != OAuthCoordinationReady || result.Err() != nil {
		t.Fatalf("Coordinate() = %s, %v", result.Disposition(), result.Err())
	}
	attempt, ok := result.Attempt()
	if !ok || !strings.HasPrefix(attempt.Header().Get("Authorization"), "Bearer access-secret") {
		t.Fatal("fresh OAuth attempt was not prepared")
	}
}

func TestOAuthCoordinatorStaleRefreshCASSuccessRequiresReschedule(t *testing.T) {
	now := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	snapshot := oauthCoordinatorSnapshot(t, 7, "old-access", "refresh-secret", now.Add(-time.Minute))
	sequence := make([]string, 0, 5)
	reloader := &oauthCoordinatorReloader{snapshots: []OAuthProbeCandidateSnapshot{snapshot}, sequence: &sequence}
	lock := oauthCoordinatorLock{sequence: &sequence}
	refresh := &oauthCoordinatorRefresh{sequence: &sequence, responses: []OAuthRefreshHTTPResponse{
		NewOAuthRefreshHTTPResponse(http.StatusOK, []byte(`{"access_token":"new-access","expires_in":3600}`), false),
	}}
	cas := &oauthCoordinatorCAS{sequence: &sequence, swapped: []bool{true}}
	result := (OAuthCoordinator{Reloader: reloader, Lock: lock, Refresh: refresh, CAS: cas}).Coordinate(t.Context(), OAuthCoordinationInput{
		Snapshot: snapshot, Prepared: oauthCoordinatorPrepared(), Reload: LoadInput{AccountID: "binding"}, Now: now,
	})
	if result.Disposition() != OAuthCoordinationReschedule || result.Err() != nil {
		t.Fatalf("Coordinate() = %s, %v", result.Disposition(), result.Err())
	}
	if _, ok := result.Attempt(); ok {
		t.Fatal("CAS success exposed a model attempt under the stale task revision")
	}
	want := []string{"lock", "reload", "refresh", "prepare_cas", "assert_owned", "cas"}
	if fmt.Sprint(sequence) != fmt.Sprint(want) {
		t.Fatalf("sequence = %v, want %v", sequence, want)
	}
	if got := cas.inputs[0].Patch().Values()["access_token"]; got != "new-access" {
		t.Fatalf("CAS patch access token = %v", got)
	}
	if got := cas.inputs[0].Patch().Values()["request_overrides"]; got == nil {
		t.Fatal("CAS patch dropped a non-refresh credential field")
	}
	if !cas.inputs[0].ConnectionIdentityChanged() {
		t.Fatal("access-token rotation did not change the Node-compatible circuit owner identity")
	}
}

func TestOAuthCoordinatorReloadsAfterLockAndUsesFreshWinnerWithoutExchange(t *testing.T) {
	now := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	stale := oauthCoordinatorSnapshot(t, 7, "old", "refresh", now.Add(-time.Minute))
	fresh := oauthCoordinatorSnapshot(t, 8, "winner", "refresh", now.Add(time.Hour))
	refresh := &oauthCoordinatorRefresh{}
	cas := &oauthCoordinatorCAS{}
	result := (OAuthCoordinator{
		Reloader: &oauthCoordinatorReloader{snapshots: []OAuthProbeCandidateSnapshot{fresh}},
		Lock:     oauthCoordinatorLock{}, Refresh: refresh, CAS: cas,
	}).Coordinate(t.Context(), OAuthCoordinationInput{Snapshot: stale, Prepared: oauthCoordinatorPrepared(), Now: now})
	if result.Disposition() != OAuthCoordinationReschedule || refresh.calls != 0 || len(cas.inputs) != 0 {
		t.Fatalf("result=%s refresh=%d cas=%d", result.Disposition(), refresh.calls, len(cas.inputs))
	}
}

func TestOAuthCoordinatorCASConflictReloadsAndRecognizesRotatedWinner(t *testing.T) {
	now := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	stale := oauthCoordinatorSnapshot(t, 7, "old", "refresh-a", now.Add(-time.Minute))
	winner := oauthCoordinatorSnapshot(t, 8, "winner", "refresh-b", now.Add(-time.Minute))
	reloader := &oauthCoordinatorReloader{snapshots: []OAuthProbeCandidateSnapshot{stale, winner}}
	refresh := &oauthCoordinatorRefresh{responses: []OAuthRefreshHTTPResponse{
		NewOAuthRefreshHTTPResponse(200, []byte(`{"access_token":"loser","expires_in":3600}`), false),
	}}
	cas := &oauthCoordinatorCAS{swapped: []bool{false}}
	result := (OAuthCoordinator{Reloader: reloader, Lock: oauthCoordinatorLock{}, Refresh: refresh, CAS: cas}).Coordinate(
		t.Context(), OAuthCoordinationInput{Snapshot: stale, Prepared: oauthCoordinatorPrepared(), Now: now},
	)
	if result.Disposition() != OAuthCoordinationReschedule || reloader.calls != 2 || refresh.calls != 1 || len(cas.inputs) != 1 {
		t.Fatalf("result=%s reload=%d refresh=%d cas=%d", result.Disposition(), reloader.calls, refresh.calls, len(cas.inputs))
	}
}

func TestOAuthCoordinatorExecutionFenceRejectsCredentialDriftBeforeRefreshWrite(t *testing.T) {
	now := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	stale := oauthCoordinatorSnapshot(t, 7, "old", "refresh-a", now.Add(-time.Minute))
	drifted := oauthCoordinatorSnapshot(t, 7, "old", "refresh-b", now.Add(-time.Minute))
	reloader := &oauthCoordinatorReloader{snapshots: []OAuthProbeCandidateSnapshot{stale, drifted}}
	refresh := &oauthCoordinatorRefresh{
		runFence: true,
		responses: []OAuthRefreshHTTPResponse{NewOAuthRefreshHTTPResponse(
			http.StatusOK, []byte(`{"access_token":"new","expires_in":3600}`), false,
		)},
	}
	result := (OAuthCoordinator{
		Reloader: reloader, Lock: oauthCoordinatorLock{}, Refresh: refresh, CAS: &oauthCoordinatorCAS{},
	}).Coordinate(t.Context(), OAuthCoordinationInput{Snapshot: stale, Prepared: oauthCoordinatorPrepared(), Now: now})
	if result.Disposition() != OAuthCoordinationTaskFailure || result.Err() == nil {
		t.Fatalf("Coordinate() = %s, %v", result.Disposition(), result.Err())
	}
	if refresh.writes != 0 || reloader.calls != 2 {
		t.Fatalf("refresh writes=%d reloads=%d, want 0 writes and 2 reloads", refresh.writes, reloader.calls)
	}
}

func TestOAuthCoordinatorAuthorizedCandidateCASUsesSourceOwnerIdentity(t *testing.T) {
	now := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	snapshot := oauthCoordinatorSnapshot(t, 7, "old", "refresh", now.Add(-time.Minute))
	snapshot.candidate.Projection.ResourceAccountID = "source-account"
	snapshot.candidate.Projection.ResourceProviderCode = "openai"
	snapshot.candidate.Projection.ResourceType = "oauth"
	snapshot.candidate.Projection.ResourceConfigRevision = 11
	snapshot.candidate.Projection.AuthorizationOwnerSystemAccountID = "source-owner"
	refresh := &oauthCoordinatorRefresh{responses: []OAuthRefreshHTTPResponse{
		NewOAuthRefreshHTTPResponse(200, []byte(`{"access_token":"new","expires_in":3600}`), false),
	}}
	cas := &oauthCoordinatorCAS{swapped: []bool{true}}
	result := (OAuthCoordinator{
		Reloader: &oauthCoordinatorReloader{snapshots: []OAuthProbeCandidateSnapshot{snapshot}},
		Lock:     oauthCoordinatorLock{}, Refresh: refresh, CAS: cas,
	}).Coordinate(t.Context(), OAuthCoordinationInput{Snapshot: snapshot, Prepared: oauthCoordinatorPrepared(), Now: now})
	if result.Disposition() != OAuthCoordinationReschedule || len(cas.inputs) != 1 {
		t.Fatalf("result=%s cas=%d", result.Disposition(), len(cas.inputs))
	}
	input := cas.inputs[0]
	if input.AccountID() != "source-account" || input.SystemAccountID() != "source-owner" || input.ExpectedConfigRevision() != 11 {
		t.Fatalf("CAS source identity = account %q owner %q revision %d", input.AccountID(), input.SystemAccountID(), input.ExpectedConfigRevision())
	}
}

func TestOAuthCoordinatorCASRetriesAtMostThreeRounds(t *testing.T) {
	now := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	stale := oauthCoordinatorSnapshot(t, 7, "old", "refresh", now.Add(-time.Minute))
	snapshots := make([]OAuthProbeCandidateSnapshot, 0, 6)
	responses := make([]OAuthRefreshHTTPResponse, 0, 3)
	for index := 0; index < 6; index++ {
		snapshots = append(snapshots, stale)
	}
	for index := 0; index < 3; index++ {
		responses = append(responses, NewOAuthRefreshHTTPResponse(200, []byte(`{"access_token":"new","expires_in":3600}`), false))
	}
	refresh := &oauthCoordinatorRefresh{responses: responses}
	cas := &oauthCoordinatorCAS{swapped: []bool{false, false, false}}
	result := (OAuthCoordinator{
		Reloader: &oauthCoordinatorReloader{snapshots: snapshots}, Lock: oauthCoordinatorLock{}, Refresh: refresh, CAS: cas,
	}).Coordinate(t.Context(), OAuthCoordinationInput{Snapshot: stale, Prepared: oauthCoordinatorPrepared(), Now: now})
	if result.Disposition() != OAuthCoordinationTaskFailure || !strings.Contains(result.Err().Error(), "retry limit") {
		t.Fatalf("Coordinate() = %s, %v", result.Disposition(), result.Err())
	}
	if refresh.calls != 3 || len(cas.inputs) != 3 {
		t.Fatalf("refresh=%d cas=%d, want 3 each", refresh.calls, len(cas.inputs))
	}
}

func TestOAuthCoordinatorRefreshAndLocalFailuresAreTaskFailures(t *testing.T) {
	now := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	stale := oauthCoordinatorSnapshot(t, 7, "old", "refresh", now.Add(-time.Minute))
	for _, test := range []struct {
		name  string
		coord OAuthCoordinator
	}{
		{name: "lock", coord: OAuthCoordinator{Reloader: &oauthCoordinatorReloader{}, Lock: oauthCoordinatorLock{err: errors.New("redis")}, Refresh: &oauthCoordinatorRefresh{}, CAS: &oauthCoordinatorCAS{}}},
		{name: "reload", coord: OAuthCoordinator{Reloader: &oauthCoordinatorReloader{err: errors.New("db")}, Lock: oauthCoordinatorLock{}, Refresh: &oauthCoordinatorRefresh{}, CAS: &oauthCoordinatorCAS{}}},
		{name: "refresh", coord: OAuthCoordinator{Reloader: &oauthCoordinatorReloader{snapshots: []OAuthProbeCandidateSnapshot{stale}}, Lock: oauthCoordinatorLock{}, Refresh: &oauthCoordinatorRefresh{err: errors.New("network")}, CAS: &oauthCoordinatorCAS{}}},
		{name: "ownership", coord: OAuthCoordinator{Reloader: &oauthCoordinatorReloader{snapshots: []OAuthProbeCandidateSnapshot{stale}}, Lock: oauthCoordinatorLock{assertErr: errors.New("lost")}, Refresh: &oauthCoordinatorRefresh{responses: []OAuthRefreshHTTPResponse{NewOAuthRefreshHTTPResponse(200, []byte(`{"access_token":"new","expires_in":3600}`), false)}}, CAS: &oauthCoordinatorCAS{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := test.coord.Coordinate(t.Context(), OAuthCoordinationInput{Snapshot: stale, Prepared: oauthCoordinatorPrepared(), Now: now})
			if result.Disposition() != OAuthCoordinationTaskFailure || result.Err() == nil {
				t.Fatalf("Coordinate() = %s, %v", result.Disposition(), result.Err())
			}
		})
	}
}

func TestOAuthCoordinatorExpiredAnthropicAccessOnlyIsLocalTaskFailure(t *testing.T) {
	now := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	candidate := oauthCoordinatorCandidate(7, "expired-access", "", now.Add(-time.Minute))
	candidate.Projection.ProviderCode = "anthropic"
	snapshot, err := NewOAuthProbeCandidateSnapshot(candidate, map[string]any{
		"access_token": "expired-access", "expires_at": now.Add(-time.Minute).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	refresh := &oauthCoordinatorRefresh{}
	cas := &oauthCoordinatorCAS{}
	result := (OAuthCoordinator{
		Reloader: &oauthCoordinatorReloader{snapshots: []OAuthProbeCandidateSnapshot{snapshot}},
		Lock:     oauthCoordinatorLock{}, Refresh: refresh, CAS: cas, Now: func() time.Time { return now },
	}).Coordinate(t.Context(), OAuthCoordinationInput{Snapshot: snapshot, Prepared: oauthCoordinatorPrepared(), Now: now})
	if result.Disposition() != OAuthCoordinationTaskFailure || refresh.calls != 0 || len(cas.inputs) != 0 {
		t.Fatalf("result=%s refresh=%d cas=%d", result.Disposition(), refresh.calls, len(cas.inputs))
	}
}

func TestOAuthCoordinatorReloadUsesCurrentTimeAfterWaitingForLock(t *testing.T) {
	observed := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	authorizationExpires := observed.Add(time.Minute)
	afterWait := authorizationExpires.Add(time.Second)
	stale := oauthCoordinatorSnapshot(t, 7, "old", "refresh", observed.Add(-time.Minute))
	reloader := &oauthCoordinatorReloader{snapshots: []OAuthProbeCandidateSnapshot{stale}, unavailableAfter: authorizationExpires}
	refresh := &oauthCoordinatorRefresh{}
	cas := &oauthCoordinatorCAS{}
	result := (OAuthCoordinator{
		Reloader: reloader, Lock: oauthCoordinatorLock{}, Refresh: refresh, CAS: cas,
		Now: func() time.Time { return afterWait },
	}).Coordinate(t.Context(), OAuthCoordinationInput{Snapshot: stale, Prepared: oauthCoordinatorPrepared(), Now: observed})
	if result.Disposition() != OAuthCoordinationTaskFailure || refresh.calls != 0 || len(cas.inputs) != 0 {
		t.Fatalf("result=%s refresh=%d cas=%d", result.Disposition(), refresh.calls, len(cas.inputs))
	}
	if len(reloader.inputs) != 1 || !reloader.inputs[0].Now.Equal(afterWait) {
		t.Fatalf("reload Now = %v, want %v", reloader.inputs, afterWait)
	}
}

func TestOAuthCoordinatorGeminiAIStudioRetryExhaustionIsBounded(t *testing.T) {
	credentials, err := ParseOAuthCredentials(OAuthGemini, map[string]any{
		"access_token": "old", "refresh_token": "refresh", "oauth_type": "ai_studio",
		"client_id": "client", "client_secret": "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := BuildOAuthRefreshRequest(credentials)
	if err != nil {
		t.Fatal(err)
	}
	executor := &oauthCoordinatorRefresh{responses: []OAuthRefreshHTTPResponse{
		NewOAuthRefreshHTTPResponse(http.StatusInternalServerError, []byte(`{"error":"temporary"}`), false),
	}}
	coordinator := OAuthCoordinator{Refresh: executor, Sleep: func(context.Context, time.Duration) error { return nil }}
	if _, err := coordinator.exchangeRefresh(t.Context(), gatewaycandidatewindow.Candidate{}, request); err == nil {
		t.Fatal("exchangeRefresh() error = nil")
	}
	if executor.calls != 4 {
		t.Fatalf("refresh calls = %d, want 4", executor.calls)
	}
}

func TestOAuthCoordinatorGeminiLegacyFallbackRetryExhaustionIsBounded(t *testing.T) {
	credentials, err := ParseOAuthCredentials(OAuthGemini, map[string]any{
		"access_token": "old", "refresh_token": "refresh", "oauth_type": "code_assist",
		"client_id": "legacy-client", "client_secret": "legacy-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := BuildOAuthRefreshRequest(credentials)
	if err != nil {
		t.Fatal(err)
	}
	executor := &oauthCoordinatorRefresh{responses: []OAuthRefreshHTTPResponse{
		NewOAuthRefreshHTTPResponse(http.StatusUnauthorized, []byte(`{"error":"unauthorized_client"}`), false),
		NewOAuthRefreshHTTPResponse(http.StatusInternalServerError, []byte(`{"error":"temporary"}`), false),
	}}
	coordinator := OAuthCoordinator{Refresh: executor, Sleep: func(context.Context, time.Duration) error { return nil }}
	if _, err := coordinator.exchangeRefresh(t.Context(), gatewaycandidatewindow.Candidate{}, request); err == nil {
		t.Fatal("exchangeRefresh() error = nil")
	}
	if executor.calls != 5 {
		t.Fatalf("refresh calls = %d, want primary plus 4 fallback attempts", executor.calls)
	}
}

func TestOAuthCoordinatorSecretBearingTypesRedactFormattingAndJSON(t *testing.T) {
	secret := "oauth-super-secret"
	now := time.Now().UTC()
	snapshot := oauthCoordinatorSnapshot(t, 7, secret, "refresh-"+secret, now.Add(time.Hour))
	patch := OAuthCredentialPatch{values: map[string]any{"access_token": secret}}
	values := []any{
		OAuthCoordinationInput{Snapshot: snapshot},
		snapshot,
		OAuthCoordinationResult{disposition: OAuthCoordinationTaskFailure, err: errors.New(secret)},
		NewOAuthRefreshHTTPResponse(200, []byte(secret), false),
		OAuthCredentialCASInput{patch: patch},
	}
	for _, value := range values {
		for _, formatted := range []string{fmt.Sprint(value), fmt.Sprintf("%+v", value), fmt.Sprintf("%#v", value)} {
			if strings.Contains(formatted, secret) {
				t.Fatalf("format leaked secret: %s", formatted)
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil || strings.Contains(string(encoded), secret) {
			t.Fatalf("JSON = %s, %v", encoded, err)
		}
	}
}

type oauthCoordinatorReloader struct {
	snapshots        []OAuthProbeCandidateSnapshot
	err              error
	calls            int
	sequence         *[]string
	inputs           []LoadInput
	unavailableAfter time.Time
}

func (r *oauthCoordinatorReloader) ReloadOAuthProbeCandidate(_ context.Context, input LoadInput) (OAuthProbeCandidateSnapshot, bool, error) {
	r.calls++
	r.inputs = append(r.inputs, input)
	if r.sequence != nil {
		*r.sequence = append(*r.sequence, "reload")
	}
	if r.err != nil {
		return OAuthProbeCandidateSnapshot{}, false, r.err
	}
	if !r.unavailableAfter.IsZero() && !input.Now.Before(r.unavailableAfter) {
		return OAuthProbeCandidateSnapshot{}, false, nil
	}
	if len(r.snapshots) == 0 {
		return OAuthProbeCandidateSnapshot{}, false, nil
	}
	index := min(r.calls-1, len(r.snapshots)-1)
	return r.snapshots[index], true, nil
}

type oauthCoordinatorLock struct {
	err       error
	assertErr error
	sequence  *[]string
}

func (l oauthCoordinatorLock) WithOAuthRefreshLock(ctx context.Context, _, _ string, task OAuthRefreshLockTask) error {
	if l.sequence != nil {
		*l.sequence = append(*l.sequence, "lock")
	}
	if l.err != nil {
		return l.err
	}
	return task(ctx, func(context.Context) error {
		if l.sequence != nil {
			*l.sequence = append(*l.sequence, "assert_owned")
		}
		return l.assertErr
	})
}

type oauthCoordinatorRefresh struct {
	responses []OAuthRefreshHTTPResponse
	err       error
	calls     int
	sequence  *[]string
	runFence  bool
	writes    int
}

func (e *oauthCoordinatorRefresh) ExecuteOAuthRefresh(ctx context.Context, _ gatewaycandidatewindow.Candidate, request OAuthRefreshRequest) (OAuthRefreshHTTPResponse, error) {
	e.calls++
	if e.runFence {
		fence := oauthHTTPExecutionFenceFromContext(ctx)
		if fence == nil {
			return OAuthRefreshHTTPResponse{}, errors.New("missing OAuth HTTP execution fence")
		}
		if err := fence(ctx); err != nil {
			return OAuthRefreshHTTPResponse{}, err
		}
	}
	e.writes++
	if e.sequence != nil {
		*e.sequence = append(*e.sequence, "refresh")
	}
	if e.err != nil {
		return OAuthRefreshHTTPResponse{}, e.err
	}
	if len(e.responses) == 0 {
		return OAuthRefreshHTTPResponse{}, errors.New("missing response")
	}
	return e.responses[min(e.calls-1, len(e.responses)-1)], nil
}

type oauthCoordinatorCAS struct {
	inputs     []OAuthCredentialCASInput
	prepared   []OAuthPreparedCredentialCAS
	swapped    []bool
	prepareErr error
	err        error
	sequence   *[]string
}

func (c *oauthCoordinatorCAS) PrepareOAuthProbeCredentialCAS(_ context.Context, input OAuthCredentialCASInput) (OAuthPreparedCredentialCAS, error) {
	if c.sequence != nil {
		*c.sequence = append(*c.sequence, "prepare_cas")
	}
	c.inputs = append(c.inputs, input)
	if c.prepareErr != nil {
		return OAuthPreparedCredentialCAS{}, c.prepareErr
	}
	prepared := NewOAuthPreparedCredentialCAS(len(c.inputs) - 1)
	c.prepared = append(c.prepared, prepared)
	return prepared, nil
}

func (c *oauthCoordinatorCAS) CompareAndSwapOAuthProbeCredentials(_ context.Context, prepared OAuthPreparedCredentialCAS) (bool, error) {
	if c.sequence != nil {
		*c.sequence = append(*c.sequence, "cas")
	}
	if c.err != nil {
		return false, c.err
	}
	if len(c.swapped) == 0 {
		return false, nil
	}
	index, _ := prepared.Value().(int)
	return c.swapped[min(index, len(c.swapped)-1)], nil
}

func oauthCoordinatorSnapshot(t *testing.T, revision int, accessToken, refreshToken string, expiresAt time.Time) OAuthProbeCandidateSnapshot {
	t.Helper()
	candidate := oauthCoordinatorCandidate(revision, accessToken, refreshToken, expiresAt)
	snapshot, err := NewOAuthProbeCandidateSnapshot(candidate, map[string]any{
		"access_token": accessToken, "refresh_token": refreshToken, "expires_at": expiresAt.Format(time.RFC3339),
		"request_overrides": map[string]any{"preserved": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func oauthCoordinatorCandidate(revision int, accessToken, refreshToken string, expiresAt time.Time) gatewaycandidatewindow.Candidate {
	return gatewaycandidatewindow.Candidate{
		Projection: port.GatewayAccountCandidate{
			AccountID: "binding", SystemAccountID: "system", GroupID: "group", ProviderCode: "openai", Type: "oauth",
			ConfigRevision: revision, DispatchRevision: 10,
		},
		Credentials: gatewaycandidatewindow.NewCredentialSet(map[string]any{
			"access_token": accessToken, "refresh_token": refreshToken, "expires_at": expiresAt.Format(time.RFC3339),
		}),
		DefaultBaseURL: "https://api.openai.com/v1",
	}
}

func oauthCoordinatorPrepared() PreparedRequest {
	return PreparedRequest{Request: RequestSpec{
		Mode: ModeResponsesJSON, Method: http.MethodPost, PathAndQuery: "/v1/responses", Model: "gpt-5",
		Header: make(http.Header), Body: []byte(`{"model":"gpt-5","input":"probe","stream":false}`),
	}}
}
