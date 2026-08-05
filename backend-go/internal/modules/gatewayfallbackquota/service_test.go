package gatewayfallbackquota

import (
	"context"
	"errors"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayfallbackpolicy"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceUsesNodeSnapshotSemanticsForGroupAndAccountAuthorizations(t *testing.T) {
	service := newService(t, port.GatewayPreflightQuotaSnapshot{
		GeneratedAt: "2026-08-04T08:00:00.000Z", AuthorizationEntriesComplete: true,
		AuthorizationEntries: []port.GatewayAuthorizationQuotaSnapshotEntry{
			{ScopeType: groupAuthorizationScope, AuthorizationID: "group-auth", Allowed: true},
			{ScopeType: accountAuthorizationScope, AuthorizationID: "account-denied", Allowed: false},
		},
	})
	result, err := service.CheckFallbackAuthorizationQuota(t.Context(), quotaInput(true, []port.GatewayAccountCandidate{
		{AccountID: "allowed", AccountAuthorizationID: "account-allowed", AuthorizationLimitsJSON: enabledQuotaJSON},
		{AccountID: "denied", AccountAuthorizationID: "account-denied", AuthorizationLimitsJSON: enabledQuotaJSON},
		{AccountID: "none"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || !result.AllowedByAccountID["allowed"] || result.AllowedByAccountID["denied"] || !result.AllowedByAccountID["none"] {
		t.Fatalf("result = %+v", result)
	}
}

func TestServiceRejectsOnlyLimitedMissingScopeFromIncompleteSnapshot(t *testing.T) {
	service := newService(t, port.GatewayPreflightQuotaSnapshot{
		GeneratedAt: "2026-08-04T08:00:00.000Z", AuthorizationEntriesComplete: false,
	})
	result, err := service.CheckFallbackAuthorizationQuota(t.Context(), quotaInput(false, []port.GatewayAccountCandidate{
		{AccountID: "limited", AccountAuthorizationID: "limited-auth", AuthorizationLimitsJSON: enabledQuotaJSON},
		{AccountID: "unlimited", AccountAuthorizationID: "unlimited-auth"},
		{AccountID: "none"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.AllowedByAccountID["limited"] || !result.AllowedByAccountID["unlimited"] || !result.AllowedByAccountID["none"] {
		t.Fatalf("result = %+v", result)
	}
}

func TestServiceUsesPresentDecisionsFromIncompleteSnapshot(t *testing.T) {
	service := newService(t, port.GatewayPreflightQuotaSnapshot{
		GeneratedAt: "2026-08-04T08:00:00.000Z", AuthorizationEntriesComplete: false,
		AuthorizationEntries: []port.GatewayAuthorizationQuotaSnapshotEntry{
			{ScopeType: groupAuthorizationScope, AuthorizationID: "group-auth", Allowed: true},
			{ScopeType: accountAuthorizationScope, AuthorizationID: "account-allowed", Allowed: true},
			{ScopeType: accountAuthorizationScope, AuthorizationID: "account-denied", Allowed: false},
		},
	})
	result, err := service.CheckFallbackAuthorizationQuota(t.Context(), quotaInput(true, []port.GatewayAccountCandidate{
		{AccountID: "allowed", AccountAuthorizationID: "account-allowed", AuthorizationLimitsJSON: enabledQuotaJSON},
		{AccountID: "denied", AccountAuthorizationID: "account-denied", AuthorizationLimitsJSON: enabledQuotaJSON},
	}))
	if err != nil || !result.AllowedByAccountID["allowed"] || result.AllowedByAccountID["denied"] {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestServiceCombinesGroupAndAccountDecisions(t *testing.T) {
	service := newService(t, port.GatewayPreflightQuotaSnapshot{
		GeneratedAt: "2026-08-04T08:00:00.000Z", AuthorizationEntriesComplete: true,
		AuthorizationEntries: []port.GatewayAuthorizationQuotaSnapshotEntry{
			{ScopeType: groupAuthorizationScope, AuthorizationID: "group-auth", Allowed: false},
			{ScopeType: accountAuthorizationScope, AuthorizationID: "account-auth", Allowed: true},
		},
	})
	result, err := service.CheckFallbackAuthorizationQuota(t.Context(), quotaInput(true, []port.GatewayAccountCandidate{
		{AccountID: "candidate", AccountAuthorizationID: "account-auth", AuthorizationLimitsJSON: enabledQuotaJSON},
	}))
	if err != nil || result.AllowedByAccountID["candidate"] {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestServiceRejectsMissingSnapshotAndMalformedOrDuplicateFacts(t *testing.T) {
	t.Run("missing snapshot", func(t *testing.T) {
		service, err := NewService(&snapshotReaderStub{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.CheckFallbackAuthorizationQuota(t.Context(), quotaInput(false, []port.GatewayAccountCandidate{{AccountID: "limited", AccountAuthorizationID: "auth", AuthorizationLimitsJSON: enabledQuotaJSON}})); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("reader error", func(t *testing.T) {
		service, err := NewService(&snapshotReaderStub{err: errors.New("snapshot unavailable")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.CheckFallbackAuthorizationQuota(t.Context(), quotaInput(false, []port.GatewayAccountCandidate{{AccountID: "limited", AccountAuthorizationID: "auth", AuthorizationLimitsJSON: enabledQuotaJSON}})); err == nil || !strings.Contains(err.Error(), "snapshot unavailable") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unlimited does not require snapshot", func(t *testing.T) {
		service, err := NewService(&snapshotReaderStub{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.CheckFallbackAuthorizationQuota(t.Context(), quotaInput(false, []port.GatewayAccountCandidate{{AccountID: "unlimited"}}))
		if err != nil || !result.Complete || !result.AllowedByAccountID["unlimited"] {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("Node-invalid quota limits fail closed", func(t *testing.T) {
		service := newService(t, port.GatewayPreflightQuotaSnapshot{GeneratedAt: "2026-08-04T08:00:00.000Z", AuthorizationEntriesComplete: true})
		for _, value := range []string{
			`{"daily":{"enabled":false,"limit":1}}`,
			`{"daily":{"Enabled":true,"limit":1}}`,
			`{"daily":{"enabled":true,"LIMIT":1}}`,
			`{"hourly":{"enabled":true,"limit":1,"Hours":1}}`,
			`{"daily":null}`,
			`{"daily":{"enabled":true,"limit":0}}`,
			`{"daily":{"enabled":true,"limit":"1"}}`,
			`{"daily":{"enabled":true,"limit":1.0000001}}`,
			`{"hourly":{"enabled":true,"limit":1}}`,
			`{"hourly":{"enabled":true,"limit":1,"hours":721}}`,
			`{"hourly":{"enabled":true,"limit":1,"hours":1.5}}`,
			`{"daily":{"enabled":true,"limit":1,"hours":1}}`,
			`{"unknown":{"enabled":true,"limit":1}}`,
		} {
			input := quotaInput(false, []port.GatewayAccountCandidate{{AccountID: "account", AuthorizationLimitsJSON: value}})
			if _, err := service.CheckFallbackAuthorizationQuota(t.Context(), input); err == nil {
				t.Fatalf("limits %s succeeded", value)
			}
		}
	})
	t.Run("malformed limits", func(t *testing.T) {
		service := newService(t, port.GatewayPreflightQuotaSnapshot{GeneratedAt: "2026-08-04T08:00:00.000Z", AuthorizationEntriesComplete: true})
		input := quotaInput(false, []port.GatewayAccountCandidate{{AccountID: "account", AccountAuthorizationID: "auth", AuthorizationLimitsJSON: `{"unexpected":true}`}})
		if _, err := service.CheckFallbackAuthorizationQuota(t.Context(), input); err == nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("account enabled quota without authorization id", func(t *testing.T) {
		service := newService(t, port.GatewayPreflightQuotaSnapshot{GeneratedAt: "2026-08-04T08:00:00.000Z", AuthorizationEntriesComplete: true})
		input := quotaInput(false, []port.GatewayAccountCandidate{{AccountID: "account", AuthorizationLimitsJSON: enabledQuotaJSON}})
		if _, err := service.CheckFallbackAuthorizationQuota(t.Context(), input); err == nil || !strings.Contains(err.Error(), "without an authorization id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("group enabled quota without authorization id", func(t *testing.T) {
		service := newService(t, port.GatewayPreflightQuotaSnapshot{GeneratedAt: "2026-08-04T08:00:00.000Z", AuthorizationEntriesComplete: true})
		input := quotaInput(true, nil)
		input.Window.Access.GroupAuthorizationID = ""
		if _, err := service.CheckFallbackAuthorizationQuota(t.Context(), input); err == nil || !strings.Contains(err.Error(), "without an authorization id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("duplicate decision", func(t *testing.T) {
		service := newService(t, port.GatewayPreflightQuotaSnapshot{GeneratedAt: "2026-08-04T08:00:00.000Z", AuthorizationEntries: []port.GatewayAuthorizationQuotaSnapshotEntry{
			{ScopeType: groupAuthorizationScope, AuthorizationID: "auth", Allowed: true},
			{ScopeType: groupAuthorizationScope, AuthorizationID: "auth", Allowed: false},
		}})
		if _, err := service.CheckFallbackAuthorizationQuota(t.Context(), quotaInput(true, nil)); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("err = %v", err)
		}
	})
	for _, entry := range []port.GatewayAuthorizationQuotaSnapshotEntry{
		{ScopeType: "team_authorization", AuthorizationID: "auth", Allowed: true},
		{ScopeType: groupAuthorizationScope, AuthorizationID: "", Allowed: true},
	} {
		entry := entry
		t.Run("invalid snapshot decision", func(t *testing.T) {
			service := newService(t, port.GatewayPreflightQuotaSnapshot{
				GeneratedAt: "2026-08-04T08:00:00.000Z", AuthorizationEntriesComplete: true,
				AuthorizationEntries: []port.GatewayAuthorizationQuotaSnapshotEntry{entry},
			})
			if _, err := service.CheckFallbackAuthorizationQuota(t.Context(), quotaInput(true, nil)); err == nil {
				t.Fatalf("entry %+v succeeded", entry)
			}
		})
	}
}

const enabledQuotaJSON = `{"daily":{"enabled":true,"limit":1}}`

func newService(t *testing.T, snapshot port.GatewayPreflightQuotaSnapshot) *Service {
	t.Helper()
	service, err := NewService(&snapshotReaderStub{snapshot: snapshot, found: true})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func quotaInput(groupLimited bool, candidates []port.GatewayAccountCandidate) gatewayfallbackpolicy.AuthorizationQuotaInput {
	groupLimits := ""
	if groupLimited {
		groupLimits = enabledQuotaJSON
	}
	items := make([]gatewaycandidatewindow.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, gatewaycandidatewindow.Candidate{Projection: candidate})
	}
	return gatewayfallbackpolicy.AuthorizationQuotaInput{Window: gatewaycandidatewindow.Window{Access: port.GatewayGroupAccess{
		GroupAuthorizationID: "group-auth", GroupAuthorizationLimitsJSON: groupLimits,
	}, Candidates: items}, Candidates: items}
}

type snapshotReaderStub struct {
	snapshot port.GatewayPreflightQuotaSnapshot
	found    bool
	err      error
}

func (s *snapshotReaderStub) LoadGatewayPreflightQuotaSnapshotCurrent(context.Context) (port.GatewayPreflightQuotaSnapshot, bool, error) {
	return s.snapshot, s.found, s.err
}
