package managementaccounts

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceUpdateTagsPublishesScopedAccountStaticChangeAfterCommit(t *testing.T) {
	store := successfulTagUpdateStore()
	grantees := &accountAuthorizationGranteeReaderStub{ids: []string{" grantee-b ", "sys_request", "grantee-a"}}
	publisher := &accountStaticChangePublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{Store: store, GranteeReader: grantees, PageDataPublisher: publisher})

	if _, err := service.UpdateTags(t.Context(), TagUpdateInput{
		AccountID: " account-1 ", SystemAccountID: " sys_request ", Tags: []string{"主力"},
	}); err != nil {
		t.Fatalf("UpdateTags() error = %v", err)
	}
	if got, want := grantees.accountIDs, []string{"account-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("grantee lookup account ids = %#v, want %#v", got, want)
	}
	if got, want := publisher.inputs, []AccountStaticChangeInput{{
		AccountID: "account-1", OwnerSystemAccountIDs: []string{"grantee-a", "grantee-b", "owner-1", "sys_request"},
		FieldMask: []string{"tags"}, FilterChanged: true, PageChanged: true,
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("published inputs = %#v, want %#v", got, want)
	}
}

func TestServiceUpdateTagsFallsBackToAllScopesWhenGranteeLookupFails(t *testing.T) {
	store := successfulTagUpdateStore()
	publisher := &accountStaticChangePublisherStub{err: errors.New("redis unavailable")}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store, GranteeReader: &accountAuthorizationGranteeReaderStub{err: errors.New("postgres unavailable")},
		PageDataPublisher: publisher,
	})

	if _, err := service.UpdateTags(t.Context(), TagUpdateInput{
		AccountID: "account-1", SystemAccountID: "sys_request", Tags: []string{"主力"},
	}); err != nil {
		t.Fatalf("post-commit lookup and publish failures must not fail UpdateTags(): %v", err)
	}
	if got, want := publisher.inputs, []AccountStaticChangeInput{{
		AccountID: "account-1", OwnerSystemAccountIDs: []string{"owner-1", "sys_request"},
		FieldMask: []string{"tags"}, FilterChanged: true, PageChanged: true, AllScopes: true,
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("published fallback inputs = %#v, want %#v", got, want)
	}
}

func TestServiceUpdateTagsDoesNotPublishBeforeSuccessfulWrite(t *testing.T) {
	for _, store := range []*accountOptionStoreStub{
		{updateErr: errors.New("write failed")},
		{updateOK: false},
	} {
		grantees := &accountAuthorizationGranteeReaderStub{}
		publisher := &accountStaticChangePublisherStub{}
		service := NewServiceWithOptions(ServiceOptions{Store: store, GranteeReader: grantees, PageDataPublisher: publisher})

		_, _ = service.UpdateTags(t.Context(), TagUpdateInput{
			AccountID: "account-1", SystemAccountID: "sys_request", Tags: []string{"主力"},
		})
		if len(grantees.accountIDs) != 0 || len(publisher.inputs) != 0 {
			t.Fatalf("failed write invoked post-commit work: lookups=%#v publishes=%#v", grantees.accountIDs, publisher.inputs)
		}
	}
}

func TestServiceUpdateTagsDetachesPageDataWorkFromCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	grantees := &accountAuthorizationGranteeReaderStub{ids: []string{"grantee-1"}}
	publisher := &accountStaticChangePublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store: successfulTagUpdateStore(), GranteeReader: grantees, PageDataPublisher: publisher,
	})

	if _, err := service.UpdateTags(ctx, TagUpdateInput{
		AccountID: "account-1", SystemAccountID: "sys_request", Tags: []string{"主力"},
	}); err != nil {
		t.Fatalf("UpdateTags() error = %v", err)
	}
	if grantees.contextErr != nil || publisher.contextErr != nil {
		t.Fatalf("post-commit contexts were canceled: lookup=%v publish=%v", grantees.contextErr, publisher.contextErr)
	}
	if grantees.deadline.IsZero() || time.Until(grantees.deadline) > 5*time.Second {
		t.Fatalf("grantee lookup deadline = %v, want bounded detached context", grantees.deadline)
	}
}

func successfulTagUpdateStore() *accountOptionStoreStub {
	return &accountOptionStoreStub{
		updateOK: true,
		updateAccount: port.ManagementAccountTagUpdateAccount{
			ID: "account-1", SystemAccountID: "sys_request", OwnerSystemAccountID: "owner-1", Name: "账户",
		},
	}
}

type accountAuthorizationGranteeReaderStub struct {
	ids        []string
	err        error
	accountIDs []string
	contextErr error
	deadline   time.Time
}

func (s *accountAuthorizationGranteeReaderStub) ListAccountAuthorizationGranteeIDs(ctx context.Context, accountID string) ([]string, error) {
	s.accountIDs = append(s.accountIDs, accountID)
	s.contextErr = ctx.Err()
	s.deadline, _ = ctx.Deadline()
	return append([]string(nil), s.ids...), s.err
}

type accountStaticChangePublisherStub struct {
	inputs     []AccountStaticChangeInput
	err        error
	contextErr error
}

func (s *accountStaticChangePublisherStub) PublishAccountStaticChange(ctx context.Context, input AccountStaticChangeInput) error {
	s.contextErr = ctx.Err()
	input.OwnerSystemAccountIDs = append([]string(nil), input.OwnerSystemAccountIDs...)
	input.FieldMask = append([]string(nil), input.FieldMask...)
	s.inputs = append(s.inputs, input)
	return s.err
}
