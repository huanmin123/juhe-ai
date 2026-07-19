package publicaccounts

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/accountpagedata"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceAddPublishesStaticPageDataAfterCommit(t *testing.T) {
	store := newPublicAccountStoreFake()
	events := &publicAccountEventRecorder{}
	publisher := &publicAccountPageDataPublisherStub{events: events}
	reader := &publicAccountGranteeReaderStub{ids: []string{"grantee-1"}}
	service := newPublicAccountPageDataService(store, &publicAccountTransactorFake{store: store, events: events}, reader, publisher)
	input := validPublicAccountAddInput("页面数据新增账号", "gpt-5.4-mini")
	input.Status = StatusDisabled

	response, err := service.Add(t.Context(), input)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if got, want := events.snapshot(), []string{"transaction_committed", "static"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if response.Account == nil {
		t.Fatal("Add() account = nil")
	}
	assertPublicAccountPageDataInput(t, publisher.staticInputs[0], accountpagedata.ChangeInput{
		AccountID: response.Account.ID, Operation: accountpagedata.OperationUpsert,
		OwnerSystemAccountIDs: []string{"grantee-1", response.Target.SystemAccountID},
		FieldMask:             []string{"id", "name", "status", "boundGroupId"},
		MembershipChanged:     true, OrderChanged: true, FilterChanged: true, PageChanged: true,
	})
}

func TestServiceUpdatePublishesStaticAndRuntimePageDataAfterCommit(t *testing.T) {
	store := newPublicAccountStoreFake()
	created, err := newPublicAccountServiceForTest(store, nil).Add(t.Context(), validPublicAccountAddInput("待更新页面数据账号", "gpt-5.4-mini"))
	if err != nil {
		t.Fatalf("setup Add() error = %v", err)
	}
	account := store.accounts[created.Account.ID]
	account.Status = port.PublicAccountStatusActive
	account.Schedulable = true
	store.accounts[account.ID] = account
	events := &publicAccountEventRecorder{}
	publisher := &publicAccountPageDataPublisherStub{events: events}
	service := newPublicAccountPageDataService(store, &publicAccountTransactorFake{store: store, events: events}, &publicAccountGranteeReaderStub{}, publisher)
	status := StatusDisabled

	if _, err := service.Update(t.Context(), UpdateInput{AccountID: account.ID, Status: &status}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got, want := events.snapshot(), []string{"transaction_committed", "static", "runtime"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	assertPublicAccountPageDataInput(t, publisher.staticInputs[0], accountpagedata.ChangeInput{
		AccountID: account.ID, Operation: accountpagedata.OperationUpsert,
		OwnerSystemAccountIDs: []string{account.SystemAccountID}, FieldMask: []string{"status"}, FilterChanged: true,
	})
	assertPublicAccountPageDataInput(t, publisher.runtimeInputs[0], accountpagedata.ChangeInput{
		AccountID: account.ID, Operation: accountpagedata.OperationUpsert,
		OwnerSystemAccountIDs: []string{account.SystemAccountID},
		FieldMask:             []string{"status", "schedulable", "cooldownUntil", "lastErrorCode", "lastErrorMessage"},
	})
}

func TestServiceUpdateEquivalentCredentialsDoesNotPublishRuntimePageData(t *testing.T) {
	store := newPublicAccountStoreFake()
	created, err := newPublicAccountServiceForTest(store, nil).Add(t.Context(), validPublicAccountAddInput("等价凭据页面数据账号", "gpt-5.4-mini"))
	if err != nil {
		t.Fatalf("setup Add() error = %v", err)
	}
	publisher := &publicAccountPageDataPublisherStub{}
	service := newPublicAccountPageDataService(store, nil, &publicAccountGranteeReaderStub{}, publisher)
	apiKey := " sk-public-account-secret-0123456789abcdef "
	baseURL := "https://api.openai.com/v1/"

	if _, err := service.Update(t.Context(), UpdateInput{AccountID: created.Account.ID, APIKey: &apiKey, BaseURL: &baseURL}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if len(publisher.staticInputs) != 1 || len(publisher.runtimeInputs) != 0 {
		t.Fatalf("publishes static=%d runtime=%d, want 1/0", len(publisher.staticInputs), len(publisher.runtimeInputs))
	}
}

func TestServiceUpdateChangedCredentialsPublishesRuntimeAfterStaticFailure(t *testing.T) {
	store := newPublicAccountStoreFake()
	created, err := newPublicAccountServiceForTest(store, nil).Add(t.Context(), validPublicAccountAddInput("变更凭据页面数据账号", "gpt-5.4-mini"))
	if err != nil {
		t.Fatalf("setup Add() error = %v", err)
	}
	account := store.accounts[created.Account.ID]
	account.Status = port.PublicAccountStatusActive
	account.Schedulable = true
	store.accounts[account.ID] = account
	publisher := &publicAccountPageDataPublisherStub{err: errors.New("redis unavailable")}
	service := newPublicAccountPageDataService(store, nil, &publicAccountGranteeReaderStub{}, publisher)
	apiKey := "sk-page-data-changed-abcdef0123456789"

	if _, err := service.Update(t.Context(), UpdateInput{AccountID: account.ID, APIKey: &apiKey}); err != nil {
		t.Fatalf("post-commit publish failures must not fail Update(): %v", err)
	}
	if len(publisher.staticInputs) != 1 || len(publisher.runtimeInputs) != 1 {
		t.Fatalf("publishes static=%d runtime=%d, want 1/1", len(publisher.staticInputs), len(publisher.runtimeInputs))
	}
}

func TestServiceDeletePublishesStaticDeleteAfterCommit(t *testing.T) {
	store := newPublicAccountStoreFake()
	created, err := newPublicAccountServiceForTest(store, nil).Add(t.Context(), validPublicAccountAddInput("待删除页面数据账号", "gpt-5.4-mini"))
	if err != nil {
		t.Fatalf("setup Add() error = %v", err)
	}
	events := &publicAccountEventRecorder{}
	publisher := &publicAccountPageDataPublisherStub{events: events}
	service := newPublicAccountPageDataService(store, &publicAccountTransactorFake{store: store, events: events}, &publicAccountGranteeReaderStub{}, publisher)

	if _, err := service.Delete(t.Context(), DeleteInput{AccountID: created.Account.ID}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got, want := events.snapshot(), []string{"transaction_committed", "static"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	input := publisher.staticInputs[0]
	if input.Operation != accountpagedata.OperationDelete || !input.MembershipChanged || !input.OrderChanged || !input.FilterChanged || !input.PageChanged {
		t.Fatalf("delete input = %#v", input)
	}
}

func TestServicePageDataFallsBackAndDoesNotPublishOnCommitFailure(t *testing.T) {
	store := newPublicAccountStoreFake()
	publisher := &publicAccountPageDataPublisherStub{err: errors.New("redis unavailable")}
	service := newPublicAccountPageDataService(store, nil, &publicAccountGranteeReaderStub{err: errors.New("lookup unavailable")}, publisher)
	input := validPublicAccountAddInput("全作用域页面数据账号", "gpt-5.4-mini")
	input.Status = StatusDisabled
	if _, err := service.Add(t.Context(), input); err != nil {
		t.Fatalf("post-commit page data failures must not fail Add(): %v", err)
	}
	if len(publisher.staticInputs) != 1 || !publisher.staticInputs[0].AllScopes {
		t.Fatalf("fallback publishes = %#v", publisher.staticInputs)
	}

	publisher.staticInputs = nil
	commitErr := errors.New("commit failed")
	service = newPublicAccountPageDataService(store, &publicAccountTransactorFake{store: store, commitError: commitErr}, &publicAccountGranteeReaderStub{}, publisher)
	input.Name = "提交失败页面数据账号"
	if _, err := service.Add(t.Context(), input); !errors.Is(err, commitErr) {
		t.Fatalf("Add() error = %v, want commit failure", err)
	}
	if len(publisher.staticInputs) != 0 {
		t.Fatalf("commit failure published %#v", publisher.staticInputs)
	}
}

func TestServicePageDataDetachesFromCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := newPublicAccountStoreFake()
	reader := &publicAccountGranteeReaderStub{}
	publisher := &publicAccountPageDataPublisherStub{}
	service := newPublicAccountPageDataService(store, nil, reader, publisher)
	input := validPublicAccountAddInput("取消上下文页面数据账号", "gpt-5.4-mini")
	input.Status = StatusDisabled

	if _, err := service.Add(ctx, input); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if reader.contextErr != nil || publisher.contextErr != nil || reader.deadline.IsZero() || time.Until(reader.deadline) > 5*time.Second {
		t.Fatalf("post-commit contexts reader=%v publisher=%v deadline=%v", reader.contextErr, publisher.contextErr, reader.deadline)
	}
}

func TestServicePageDataUsesOnePostCommitTimeoutBudget(t *testing.T) {
	store := newPublicAccountStoreFake()
	created, err := newPublicAccountServiceForTest(store, nil).Add(t.Context(), validPublicAccountAddInput("总预算页面数据账号", "gpt-5.4-mini"))
	if err != nil {
		t.Fatalf("setup Add() error = %v", err)
	}
	publisher := &publicAccountPageDataPublisherStub{blockStaticUntilCanceled: true}
	service := NewService(Options{
		Store: store, ProviderModels: defaultProviderModelReaderStub(), GranteeReader: &publicAccountGranteeReaderStub{},
		PageDataPublisher: publisher, PageDataPostCommitTimeout: 50 * time.Millisecond, PageDataOwnerLookupTimeout: 10 * time.Millisecond,
		Now: fixedPublicAccountNow, NewID: sequentialPublicAccountID(), Secret: "public-account-test-secret",
	})
	status := StatusDisabled

	if _, err := service.Update(t.Context(), UpdateInput{AccountID: created.Account.ID, Status: &status}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if len(publisher.staticInputs) != 1 || len(publisher.runtimeInputs) != 1 {
		t.Fatalf("publishes static=%d runtime=%d, want both attempted", len(publisher.staticInputs), len(publisher.runtimeInputs))
	}
	if publisher.staticDeadline.IsZero() || !publisher.staticDeadline.Equal(publisher.runtimeDeadline) {
		t.Fatalf("page data deadlines static=%v runtime=%v, want one shared budget", publisher.staticDeadline, publisher.runtimeDeadline)
	}
}

func newPublicAccountPageDataService(store *publicAccountStoreFake, transactor port.PublicAccountTransactor, reader accountpagedata.GranteeReader, publisher accountpagedata.Publisher) *Service {
	return NewService(Options{
		Store: store, Transactor: transactor, ProviderModels: defaultProviderModelReaderStub(),
		GranteeReader: reader, PageDataPublisher: publisher,
		Now: fixedPublicAccountNow, NewID: sequentialPublicAccountID(), Secret: "public-account-test-secret",
	})
}

func assertPublicAccountPageDataInput(t *testing.T, got accountpagedata.ChangeInput, want accountpagedata.ChangeInput) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("page data input = %#v, want %#v", got, want)
	}
}

type publicAccountGranteeReaderStub struct {
	ids        []string
	err        error
	contextErr error
	deadline   time.Time
}

func (s *publicAccountGranteeReaderStub) ListAccountAuthorizationGranteeIDs(ctx context.Context, _ string) ([]string, error) {
	s.contextErr = ctx.Err()
	s.deadline, _ = ctx.Deadline()
	return append([]string(nil), s.ids...), s.err
}

type publicAccountPageDataPublisherStub struct {
	staticInputs             []accountpagedata.ChangeInput
	runtimeInputs            []accountpagedata.ChangeInput
	events                   *publicAccountEventRecorder
	err                      error
	contextErr               error
	blockStaticUntilCanceled bool
	staticDeadline           time.Time
	runtimeDeadline          time.Time
}

func (s *publicAccountPageDataPublisherStub) PublishAccountStaticChange(ctx context.Context, input accountpagedata.ChangeInput) error {
	s.contextErr = ctx.Err()
	s.staticDeadline, _ = ctx.Deadline()
	s.staticInputs = append(s.staticInputs, clonePublicAccountPageDataInput(input))
	if s.events != nil {
		s.events.record("static")
	}
	if s.blockStaticUntilCanceled {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.err
}

func (s *publicAccountPageDataPublisherStub) PublishAccountRuntimeChange(ctx context.Context, input accountpagedata.ChangeInput) error {
	s.contextErr = ctx.Err()
	s.runtimeDeadline, _ = ctx.Deadline()
	s.runtimeInputs = append(s.runtimeInputs, clonePublicAccountPageDataInput(input))
	if s.events != nil {
		s.events.record("runtime")
	}
	return s.err
}

func clonePublicAccountPageDataInput(input accountpagedata.ChangeInput) accountpagedata.ChangeInput {
	input.OwnerSystemAccountIDs = append([]string(nil), input.OwnerSystemAccountIDs...)
	input.FieldMask = append([]string(nil), input.FieldMask...)
	return input
}
