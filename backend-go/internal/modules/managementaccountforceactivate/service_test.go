package managementaccountforceactivate

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/accountpagedata"
	"juhe-ai/backend-go/internal/modules/managementaccountdetails"
	"juhe-ai/backend-go/internal/store/port"
)

type detailStub struct {
	before, after map[string]any
	calls         int
	found         bool
}

func (s *detailStub) Get(context.Context, managementaccountdetails.Input, managementaccountdetails.Level) (map[string]any, bool, error) {
	s.calls++
	if !s.found {
		return nil, false, nil
	}
	if s.calls == 1 {
		return s.before, true, nil
	}
	return s.after, true, nil
}

type activatorStub struct {
	input   port.ManagementAccountForceActivateInput
	changed bool
	err     error
}

type runtimeClearerStub struct{ accountIDs []string }

func (s *runtimeClearerStub) ClearAccountRuntimeAvailability(_ context.Context, accountID string) error {
	s.accountIDs = append(s.accountIDs, accountID)
	return nil
}

type gatewayInvalidatorStub struct{ reasons []string }

func (s *gatewayInvalidatorStub) InvalidateGatewayRuntime(_ context.Context, reason string) error {
	s.reasons = append(s.reasons, reason)
	return nil
}

type pageDataPublisherStub struct{ runtimeChanges []accountpagedata.ChangeInput }

func (s *pageDataPublisherStub) PublishAccountStaticChange(context.Context, accountpagedata.ChangeInput) error {
	return nil
}

func (s *pageDataPublisherStub) PublishAccountRuntimeChange(_ context.Context, input accountpagedata.ChangeInput) error {
	s.runtimeChanges = append(s.runtimeChanges, input)
	return nil
}

type granteeReaderStub struct{ ids []string }

func (s *granteeReaderStub) ListAccountAuthorizationGranteeIDs(context.Context, string) ([]string, error) {
	return s.ids, nil
}

func (s *activatorStub) ForceActivatePendingAccount(_ context.Context, input port.ManagementAccountForceActivateInput) (port.ManagementAccountForceActivateResult, bool, error) {
	s.input = input
	return port.ManagementAccountForceActivateResult{AccountID: input.AccountID, OwnerSystemID: input.OwnerSystemID, BeforeStatus: "pending_test", AfterStatus: "active", Status: "active", Schedulable: true}, s.changed, s.err
}

func TestForceActivateRequiresExplicitAcknowledgement(t *testing.T) {
	s := NewService(ServiceOptions{})
	if _, err := s.ForceActivate(context.Background(), Input{AccountID: "a"}); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("err=%v", err)
	}
}

func TestForceActivateRejectsAuthorizedAndNonPendingAccounts(t *testing.T) {
	for _, test := range []struct {
		name   string
		before map[string]any
		want   error
	}{
		{"authorized", map[string]any{"accessType": "authorized", "status": "pending_test"}, ErrAuthorized},
		{"active", map[string]any{"accessType": "owner", "status": "active"}, ErrInvalidStatus},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := &detailStub{before: test.before, found: true}
			s := NewService(ServiceOptions{Details: d, Store: &activatorStub{}})
			if _, err := s.ForceActivate(context.Background(), Input{AccountID: "a", Acknowledged: true}); !errors.Is(err, test.want) {
				t.Fatalf("err=%v want %v", err, test.want)
			}
		})
	}
}

func TestForceActivateUsesOwnerAndConfigRevisionThenReadsUpdatedDetail(t *testing.T) {
	d := &detailStub{found: true, before: map[string]any{"accessType": "owner", "status": "pending_test", "ownerSystemAccountId": "owner-1", "configRevision": 7}, after: map[string]any{"accessType": "owner", "status": "active", "ownerSystemAccountId": "owner-1"}}
	a := &activatorStub{changed: true}
	s := NewService(ServiceOptions{Details: d, Store: a, Now: func() time.Time { return time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC) }})
	result, err := s.ForceActivate(context.Background(), Input{AccountID: "a", SystemAccountID: "", Acknowledged: true})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if a.input.OwnerSystemID != "owner-1" || a.input.ConfigRevision != 7 {
		t.Fatalf("input=%+v", a.input)
	}
	if result.After["status"] != "active" || d.calls != 2 {
		t.Fatalf("result=%+v calls=%d", result, d.calls)
	}
}

func TestForceActivateMapsCASMissToStateChanged(t *testing.T) {
	d := &detailStub{found: true, before: map[string]any{"status": "pending_test", "ownerSystemAccountId": "owner-1"}}
	s := NewService(ServiceOptions{Details: d, Store: &activatorStub{changed: false}})
	if _, err := s.ForceActivate(context.Background(), Input{AccountID: "a", Acknowledged: true}); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("err=%v", err)
	}
}

func TestForceActivateInvalidatesRuntimeAndPublishesOwnerScopes(t *testing.T) {
	d := &detailStub{found: true,
		before: map[string]any{"accessType": "owner", "status": "pending_test", "ownerSystemAccountId": "owner-1", "configRevision": 2},
		after:  map[string]any{"id": "a", "name": "account", "status": "active", "ownerSystemAccountId": "owner-1"},
	}
	clearer := &runtimeClearerStub{}
	invalidator := &gatewayInvalidatorStub{}
	publisher := &pageDataPublisherStub{}
	s := NewService(ServiceOptions{
		Details: d, Store: &activatorStub{changed: true}, RuntimeClearer: clearer,
		GatewayInvalidator: invalidator, PageDataPublisher: publisher,
		GranteeReader: &granteeReaderStub{ids: []string{"grantee-1"}},
	})
	if _, err := s.ForceActivate(context.Background(), Input{AccountID: "a", Acknowledged: true}); err != nil {
		t.Fatalf("ForceActivate() error = %v", err)
	}
	if len(clearer.accountIDs) != 1 || clearer.accountIDs[0] != "a" {
		t.Fatalf("runtime clears = %#v", clearer.accountIDs)
	}
	if len(invalidator.reasons) != 1 || invalidator.reasons[0] != "account_pending_force_activated" {
		t.Fatalf("runtime invalidations = %#v", invalidator.reasons)
	}
	if len(publisher.runtimeChanges) != 1 {
		t.Fatalf("page data changes = %#v", publisher.runtimeChanges)
	}
	change := publisher.runtimeChanges[0]
	if change.AccountID != "a" || change.AllScopes || len(change.OwnerSystemAccountIDs) != 2 ||
		change.OwnerSystemAccountIDs[0] != "grantee-1" || change.OwnerSystemAccountIDs[1] != "owner-1" {
		t.Fatalf("page data change = %+v", change)
	}
}
