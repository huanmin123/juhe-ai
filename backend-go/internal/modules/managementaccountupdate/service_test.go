package managementaccountupdate

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/accountpagedata"
	"juhe-ai/backend-go/internal/store/port"
)

func TestUpdateMergesCredentialsEncryptsAndPropagatesInvalidation(t *testing.T) {
	store := &updateStoreStub{
		target: port.ManagementAccountUpdateTarget{
			ID: "account-1", SystemAccountID: "owner-1", OwnerSystemAccountID: "owner-1",
			AccessType: "owner", ProviderCode: "openai", Type: "api_key", ConfigRevision: 4,
			CredentialsEncrypted: "old-cipher",
		},
		result: port.ManagementAccountUpdateResult{
			AccountID: "account-1", OwnerSystemAccountID: "owner-1", SystemAccountID: "owner-1",
			Before: map[string]any{"name": "old"}, After: map[string]any{"id": "account-1", "name": "new"},
		},
	}
	codec := &codecStub{decrypted: map[string]any{"api_key": "keep", "base_url": "https://old"}}
	page := &publisherStub{}
	runtime := &invalidationStub{}
	service := NewService(Options{
		Store: store, CredentialCodec: codec, PageDataPublisher: page, GranteeReader: &granteeStub{ids: []string{"viewer-1"}},
		GatewayRuntimeInvalidator: runtime, AccountLookupInvalidator: runtime,
		Now: func() time.Time { return time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC) },
	})

	result, err := service.Update(context.Background(), UpdateInput{
		ActorSystemAccountID: "owner-1", ActorRole: "user", SelfOnly: true, AccountID: "account-1",
		ExpectedConfigRevision: 4,
		Fields:                 map[string]any{"name": "new", "credentials": map[string]any{"api_key": "replacement"}},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.After["id"] != "account-1" || store.input.ExpectedConfigRevision != 4 {
		t.Fatalf("result/input = %#v / %#v", result, store.input)
	}
	if codec.encrypted == nil || codec.encrypted["api_key"] != "replacement" || codec.encrypted["base_url"] != "https://old" {
		t.Fatalf("encrypted credentials = %#v", codec.encrypted)
	}
	if store.input.CredentialsEncrypted != "encrypted" || page.staticCalls != 1 || runtime.lookupCalls != 1 || runtime.runtimeCalls != 1 {
		t.Fatalf("side effects store=%#v page=%d lookup=%d runtime=%d", store.input, page.staticCalls, runtime.lookupCalls, runtime.runtimeCalls)
	}
}

func TestUpdateRejectsAuthorizedAndVersionConflict(t *testing.T) {
	for _, test := range []struct {
		name   string
		access string
		err    error
	}{
		{name: "authorized", access: "authorized", err: ErrAuthorized},
		{name: "revision", access: "owner", err: ErrVersionConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &updateStoreStub{target: port.ManagementAccountUpdateTarget{ID: "a", SystemAccountID: "owner", OwnerSystemAccountID: "owner", AccessType: test.access, ConfigRevision: 2}}
			if test.name == "revision" {
				store.updateOK = false
			}
			service := NewService(Options{Store: store, CredentialCodec: &codecStub{decrypted: map[string]any{}}})
			_, err := service.Update(context.Background(), UpdateInput{ActorSystemAccountID: "owner", SelfOnly: true, AccountID: "a", ExpectedConfigRevision: 2, Fields: map[string]any{"name": "x"}})
			if !errors.Is(err, test.err) {
				t.Fatalf("Update() error = %v, want %v", err, test.err)
			}
		})
	}
}

type updateStoreStub struct {
	target   port.ManagementAccountUpdateTarget
	result   port.ManagementAccountUpdateResult
	input    port.ManagementAccountUpdateInput
	updateOK bool
}

func (s *updateStoreStub) LoadManagementAccountUpdateTarget(context.Context, port.ManagementAccountUpdateTargetInput) (port.ManagementAccountUpdateTarget, bool, error) {
	return s.target, s.target.ID != "", nil
}
func (s *updateStoreStub) UpdateManagementAccount(_ context.Context, input port.ManagementAccountUpdateInput) (port.ManagementAccountUpdateResult, bool, error) {
	s.input = input
	if !s.updateOK && s.result.AccountID == "" {
		return port.ManagementAccountUpdateResult{}, false, nil
	}
	return s.result, true, nil
}

type codecStub struct{ decrypted, encrypted map[string]any }

func (s *codecStub) DecryptJSON(string) (map[string]any, error) { return s.decrypted, nil }
func (s *codecStub) EncryptJSON(value map[string]any) (string, error) {
	s.encrypted = value
	return "encrypted", nil
}

type publisherStub struct{ staticCalls int }

func (s *publisherStub) PublishAccountStaticChange(context.Context, accountpagedata.ChangeInput) error {
	s.staticCalls++
	return nil
}
func (s *publisherStub) PublishAccountRuntimeChange(context.Context, accountpagedata.ChangeInput) error {
	return nil
}

type granteeStub struct{ ids []string }

func (s *granteeStub) ListAccountAuthorizationGranteeIDs(context.Context, string) ([]string, error) {
	return s.ids, nil
}

type invalidationStub struct{ lookupCalls, runtimeCalls int }

func (s *invalidationStub) InvalidateAccountLookupCache(context.Context, string) error {
	s.lookupCalls++
	return nil
}
func (s *invalidationStub) InvalidateGatewayRuntime(context.Context, string) error {
	s.runtimeCalls++
	return nil
}
