package publicaccounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceAddDispatchesActivationAsynchronouslyAfterCommit(t *testing.T) {
	store := newPublicAccountStoreFake()
	events := &publicAccountEventRecorder{}
	transactor := &publicAccountTransactorFake{store: store, events: events}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseDispatch := func() {
		releaseOnce.Do(func() { close(release) })
	}
	defer releaseDispatch()
	finished := make(chan struct{}, 1)
	dispatcher := &publicAccountHealthCheckDispatcherFake{
		events:   events,
		started:  started,
		release:  release,
		finished: finished,
	}
	service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, transactor, dispatcher, nil)

	type addResult struct {
		response AccountResponse
		err      error
	}
	resultCh := make(chan addResult, 1)
	go func() {
		response, err := service.Add(context.Background(), validPublicAccountAddInput(
			"提交后激活检查账号",
			"gpt-5.4-mini",
		))
		resultCh <- addResult{response: response, err: err}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("activation dispatch did not start")
	}
	var result addResult
	select {
	case result = <-resultCh:
	case <-time.After(250 * time.Millisecond):
		releaseDispatch()
		<-resultCh
		t.Fatal("Add waited for the blocking activation dispatcher")
	}
	if result.err != nil {
		t.Fatalf("add public account: %v", result.err)
	}
	response := result.response
	if response.Account == nil || response.Account.Status != StatusPendingTest {
		t.Fatalf("response account = %+v, want pending_test", response.Account)
	}
	if got, want := events.snapshot(), []string{"transaction_committed", "dispatch"}; !slices.Equal(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	releaseDispatch()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("activation dispatch did not finish after release")
	}
	assertPublicAccountHealthDispatchCalls(t, dispatcher,
		publicAccountHealthCheckDispatchCall{
			accountID: response.Account.ID,
			reason:    "activation",
		},
	)
}

func TestServiceAddDispatchesActivationOnceAfterRetry(t *testing.T) {
	store := newPublicAccountStoreFake()
	transactor := &publicAccountTransactorFake{
		store:        store,
		beforeErrors: []error{port.ErrPublicGroupTargetDuplicateUsername},
	}
	dispatcher := &publicAccountHealthCheckDispatcherFake{}
	service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, transactor, dispatcher, nil)

	response, err := service.Add(context.Background(), validPublicAccountAddInput(
		"重试后激活检查账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account after retry: %v", err)
	}
	if transactor.calls != 2 {
		t.Fatalf("transaction calls = %d, want 2", transactor.calls)
	}
	assertPublicAccountHealthDispatchCalls(t, dispatcher,
		publicAccountHealthCheckDispatchCall{
			accountID: response.Account.ID,
			reason:    "activation",
		},
	)
}

func TestServiceAddSkipsActivationDispatchWhenDisabledOrTransactionFails(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		store := newPublicAccountStoreFake()
		dispatcher := &publicAccountHealthCheckDispatcherFake{}
		service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, nil, dispatcher, nil)
		input := validPublicAccountAddInput("停用账号", "gpt-5.4-mini")
		input.Status = StatusDisabled

		response, err := service.Add(context.Background(), input)
		if err != nil {
			t.Fatalf("add disabled public account: %v", err)
		}
		if response.Account == nil || response.Account.Status != StatusDisabled {
			t.Fatalf("response account = %+v, want disabled", response.Account)
		}
		if calls := dispatcher.callsSnapshot(); len(calls) != 0 {
			t.Fatalf("dispatch calls = %#v, want none", calls)
		}
	})

	t.Run("transaction failure", func(t *testing.T) {
		store := newPublicAccountStoreFake()
		commitErr := errors.New("commit failed")
		transactor := &publicAccountTransactorFake{store: store, commitError: commitErr}
		dispatcher := &publicAccountHealthCheckDispatcherFake{}
		service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, transactor, dispatcher, nil)

		_, err := service.Add(context.Background(), validPublicAccountAddInput(
			"提交失败账号",
			"gpt-5.4-mini",
		))
		if !errors.Is(err, commitErr) {
			t.Fatalf("add error = %v, want commit failure", err)
		}
		if calls := dispatcher.callsSnapshot(); len(calls) != 0 {
			t.Fatalf("dispatch calls = %#v, want none", calls)
		}
	})

	t.Run("all retries fail", func(t *testing.T) {
		store := newPublicAccountStoreFake()
		transactor := &publicAccountTransactorFake{
			store: store,
			beforeErrors: []error{
				port.ErrPublicGroupTargetDuplicateUsername,
				port.ErrPublicGroupTargetDuplicateUsername,
				port.ErrPublicGroupTargetDuplicateUsername,
			},
		}
		dispatcher := &publicAccountHealthCheckDispatcherFake{}
		service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, transactor, dispatcher, nil)

		_, err := service.Add(context.Background(), validPublicAccountAddInput(
			"持续重试失败账号",
			"gpt-5.4-mini",
		))
		if !errors.Is(err, port.ErrPublicGroupTargetDuplicateUsername) {
			t.Fatalf("add error = %v, want retryable duplicate", err)
		}
		if transactor.calls != 3 {
			t.Fatalf("transaction calls = %d, want 3", transactor.calls)
		}
		if calls := dispatcher.callsSnapshot(); len(calls) != 0 {
			t.Fatalf("dispatch calls = %#v, want none", calls)
		}
	})
}

func TestServiceAddDispatchFailureIsBestEffortAndLoggerIsOptional(t *testing.T) {
	dispatchErr := errors.New("health dispatch failed")

	t.Run("warns when logger configured", func(t *testing.T) {
		store := newPublicAccountStoreFake()
		dispatcher := &publicAccountHealthCheckDispatcherFake{err: dispatchErr}
		var logs synchronizedBuffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, nil, dispatcher, logger)

		response, err := service.Add(context.Background(), validPublicAccountAddInput(
			"投递失败仍创建账号",
			"gpt-5.4-mini",
		))
		if err != nil {
			t.Fatalf("add public account: %v", err)
		}
		if response.Action != "created" || response.Account == nil {
			t.Fatalf("response = %+v, want committed create", response)
		}
		assertPublicAccountHealthDispatchCalls(t, dispatcher,
			publicAccountHealthCheckDispatchCall{
				accountID: response.Account.ID,
				reason:    "activation",
			},
		)
		wants := []string{
			`"level":"WARN"`,
			`"event":"public_account_health_check_dispatch_failed"`,
			`"account_id":"` + response.Account.ID + `"`,
			`"reason":"activation"`,
			dispatchErr.Error(),
		}
		waitForPublicAccountCondition(t, time.Second, "dispatch failure warning", func() bool {
			logText := logs.String()
			for _, want := range wants {
				if !strings.Contains(logText, want) {
					return false
				}
			}
			return true
		})
	})

	t.Run("does not require logger", func(t *testing.T) {
		store := newPublicAccountStoreFake()
		dispatcher := &publicAccountHealthCheckDispatcherFake{err: dispatchErr}
		service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, nil, dispatcher, nil)

		response, err := service.Add(context.Background(), validPublicAccountAddInput(
			"无日志器投递失败账号",
			"gpt-5.4-mini",
		))
		if err != nil {
			t.Fatalf("add public account: %v", err)
		}
		if response.Action != "created" || response.Account == nil {
			t.Fatalf("response = %+v, want committed create", response)
		}
		assertPublicAccountHealthDispatchCalls(t, dispatcher,
			publicAccountHealthCheckDispatchCall{
				accountID: response.Account.ID,
				reason:    "activation",
			},
		)
	})
}

func TestServiceHealthDispatchDetachesCallerCancellationAndSetsDeadline(t *testing.T) {
	store := newPublicAccountStoreFake()
	dispatcher := &publicAccountHealthCheckDispatcherFake{}
	service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, nil, dispatcher, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service.Add(ctx, validPublicAccountAddInput(
		"取消上下文激活检查账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	calls := waitForPublicAccountHealthDispatchCalls(t, dispatcher, 1)
	call := calls[0]
	if call.contextErr != nil {
		t.Fatalf("dispatch context error = %v, want nil", call.contextErr)
	}
	if !call.hasDeadline {
		t.Fatal("dispatch context has no deadline")
	}
	timeout := call.deadline.Sub(call.observedAt)
	if timeout < 1500*time.Millisecond || timeout > 2*time.Second+250*time.Millisecond {
		t.Fatalf("dispatch deadline timeout = %v, want near 2s", timeout)
	}
}

func TestServiceHealthDispatchUsesConfiguredTimeout(t *testing.T) {
	store := newPublicAccountStoreFake()
	dispatcher := &publicAccountHealthCheckDispatcherFake{}
	service := newPublicAccountServiceWithHealthDispatchTimeoutForTest(
		store,
		nil,
		nil,
		dispatcher,
		nil,
		5*time.Second,
	)
	_, err := service.Add(context.Background(), validPublicAccountAddInput(
		"自定义超时激活检查账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	calls := waitForPublicAccountHealthDispatchCalls(t, dispatcher, 1)
	call := calls[0]
	if !call.hasDeadline {
		t.Fatal("dispatch context has no deadline")
	}
	timeout := call.deadline.Sub(call.observedAt)
	if timeout < 4500*time.Millisecond || timeout > 5*time.Second+250*time.Millisecond {
		t.Fatalf("dispatch deadline timeout = %v, want near 5s", timeout)
	}
}

func TestServiceHealthDispatchTimeoutStopsBlockingDispatcher(t *testing.T) {
	store := newPublicAccountStoreFake()
	finished := make(chan struct{}, 1)
	dispatcher := &publicAccountHealthCheckDispatcherFake{
		blockUntilContextDone: true,
		finished:              finished,
	}
	service := newPublicAccountServiceWithHealthDispatchTimeoutForTest(
		store,
		nil,
		nil,
		dispatcher,
		nil,
		40*time.Millisecond,
	)

	response, err := service.Add(context.Background(), validPublicAccountAddInput(
		"超时退出激活检查账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("blocking activation dispatcher did not stop at its context deadline")
	}
	assertPublicAccountHealthDispatchCalls(t, dispatcher,
		publicAccountHealthCheckDispatchCall{
			accountID: response.Account.ID,
			reason:    "activation",
		},
	)
}

func TestServiceAddCreatesTargetGroupPendingTestAndDoesNotExposeCredentials(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := newPublicAccountServiceForTest(store, nil)

	response, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetDisplayName:         "管理员",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "主账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
		SupportedModels:           NewStringListValue([]string{" gpt-5.5 ", "gpt-5.5", "gpt-5.4-mini"}, true),
		Status:                    StatusActive,
	})
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	if response.Action != "created" || !response.Target.Created || response.Target.GroupID == "" || response.Target.GroupCreated == nil || !*response.Target.GroupCreated {
		t.Fatalf("response target/action = %+v", response)
	}
	if response.Account == nil || response.Account.Status != StatusPendingTest || response.Account.Schedulable {
		t.Fatalf("response account = %+v, want pending_test and unschedulable", response.Account)
	}
	if response.Account.HealthCheckEndpointMode != "responses_sse" {
		t.Fatalf("response health check endpoint mode = %q, want responses_sse", response.Account.HealthCheckEndpointMode)
	}
	if got := response.Account.SupportedModels; len(got) != 2 || got[0] != "gpt-5.5" || got[1] != "gpt-5.4-mini" {
		t.Fatalf("supported models = %#v", got)
	}
	created := store.accounts[response.Account.ID]
	if created.Status != port.PublicAccountStatusPendingTest || created.Schedulable {
		t.Fatalf("stored account status/schedulable = %s/%v", created.Status, created.Schedulable)
	}
	if created.HealthCheckModel != defaultGPTHealthCheckModel {
		t.Fatalf("stored health check model = %q, want %q", created.HealthCheckModel, defaultGPTHealthCheckModel)
	}
	if created.HealthCheckEndpointMode != "responses_sse" {
		t.Fatalf("stored health check endpoint mode = %q, want responses_sse", created.HealthCheckEndpointMode)
	}
	if created.CredentialsEncrypted == "" ||
		strings.Contains(created.CredentialsEncrypted, "sk-public-account-secret") ||
		strings.Contains(created.CredentialsEncrypted, "api.openai.com") {
		t.Fatalf("credentials_encrypted is not encrypted enough for public account test: %q", created.CredentialsEncrypted)
	}
	credentials, err := service.codec.DecryptJSON(created.CredentialsEncrypted)
	if err != nil {
		t.Fatalf("decrypt created credentials: %v", err)
	}
	wantEndpointModes := []any{"chat_json", "chat_sse", "responses_json", "responses_sse"}
	if got := credentials["supported_endpoint_modes"]; !reflect.DeepEqual(got, wantEndpointModes) {
		t.Fatalf("created supported_endpoint_modes = %#v, want %#v", got, wantEndpointModes)
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	lower := strings.ToLower(string(data))
	for _, forbidden := range []string{"sk-public-account-secret", "api.openai.com", "credentials", "baseurl", "apikey", "healthcheckmodel", "healthcheckendpointmode"} {
		if strings.Contains(lower, strings.ToLower(forbidden)) {
			t.Fatalf("response leaked %q in %s", forbidden, string(data))
		}
	}
}

func TestServiceAddRejectsUnsupportedHealthCheckEndpointMode(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := newPublicAccountServiceForTest(store, nil)
	input := validPublicAccountAddInput("非法健康检查协议", "gpt-5.4-mini")
	input.HealthCheckEndpointMode = "messages_json"

	_, err := service.Add(context.Background(), input)
	if !errors.Is(err, ErrInvalidHealthCheckEndpointMode) {
		t.Fatalf("add error = %v, want ErrInvalidHealthCheckEndpointMode", err)
	}
	if len(store.accounts) != 0 {
		t.Fatalf("invalid health check endpoint mode wrote accounts: %#v", store.accounts)
	}
}

func TestServiceAddUsesCredentialDefaultsInsteadOfProfileCapabilities(t *testing.T) {
	store := newPublicAccountStoreFake()
	profileKey := "gpt|profile_gpt_openai_v1"
	profile := store.profiles[profileKey]
	profile.EnabledEndpointModes = []string{"generate_content_json"}
	store.profiles[profileKey] = profile
	service := newPublicAccountServiceForTest(store, nil)

	response, err := service.Add(context.Background(), validPublicAccountAddInput(
		"首个 JSON 能力回退账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	if response.Account == nil || response.Account.HealthCheckEndpointMode != "responses_sse" {
		t.Fatalf("response account = %+v, want responses_sse mode from GPT credential defaults", response.Account)
	}
}

func TestServiceAddDoesNotRequireProfileEndpointCapabilities(t *testing.T) {
	store := newPublicAccountStoreFake()
	profileKey := "gpt|profile_gpt_openai_v1"
	profile := store.profiles[profileKey]
	profile.EnabledEndpointModes = nil
	store.profiles[profileKey] = profile
	service := newPublicAccountServiceForTest(store, nil)

	response, err := service.Add(context.Background(), validPublicAccountAddInput(
		"无 JSON 能力账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	if response.Account == nil || response.Account.HealthCheckEndpointMode != "responses_sse" {
		t.Fatalf("response account = %+v, want responses_sse mode from credentials", response.Account)
	}
}

func TestResolveHealthCheckEndpointModeAllowsNonGPTStreamingAndRejectsToolModes(t *testing.T) {
	value := "messages_sse"
	mode, err := resolveHealthCheckEndpointMode(
		&value,
		"anthropic",
		"profile_anthropic_anthropic_v1",
		[]string{"messages_json", "messages_sse", "message_token_counting"},
	)
	if err != nil || mode != "messages_sse" {
		t.Fatalf("resolve Anthropic streaming mode = %q, %v; want messages_sse", mode, err)
	}

	toolMode := "message_token_counting"
	if _, err := resolveHealthCheckEndpointMode(
		&toolMode,
		"anthropic",
		"profile_anthropic_anthropic_v1",
		[]string{"message_token_counting"},
	); !errors.Is(err, ErrInvalidHealthCheckEndpointMode) {
		t.Fatalf("resolve tool mode error = %v, want ErrInvalidHealthCheckEndpointMode", err)
	}
}

func TestServiceHybridAnthropicMessagesProfileCanAddAndUpdate(t *testing.T) {
	store := newPublicAccountStoreFake()
	store.profiles["hybrid|profile_hybrid_anthropic_messages_v1"] = port.PublicAccountProviderProfile{
		ID:                      "profile_hybrid_anthropic_messages_v1",
		ProviderCode:            hybridProviderCode,
		Name:                    "Hybrid / Anthropic Messages",
		Enabled:                 true,
		ProviderEnabled:         true,
		ProtocolCode:            "anthropic",
		ProtocolVersion:         "v1",
		AccountTypesJSON:        `["api_key"]`,
		EnabledEndpointModes:    []string{"messages_json"},
		DefaultSupportedModels:  []string{"hybrid-direct-model"},
		DefaultHealthCheckModel: "hybrid-direct-model",
	}
	service := newPublicAccountServiceForTest(store, nil)
	input := validPublicAccountAddInput("混合 Anthropic 账号", "hybrid-direct-model")
	input.ProviderCode = hybridProviderCode
	input.ProviderProtocolProfileID = "profile_hybrid_anthropic_messages_v1"

	created, err := service.Add(context.Background(), input)
	if err != nil {
		t.Fatalf("add hybrid Anthropic account: %v", err)
	}
	if created.Account == nil || created.Account.HealthCheckEndpointMode != "messages_json" {
		t.Fatalf("created account = %+v, want messages_json mode", created.Account)
	}
	account := store.accounts[created.Account.ID]
	credentials, err := service.codec.DecryptJSON(account.CredentialsEncrypted)
	if err != nil {
		t.Fatalf("decrypt hybrid credentials: %v", err)
	}
	wantEndpointModes := []any{
		"chat_json",
		"chat_sse",
		"responses_json",
		"responses_sse",
		"messages_json",
		"messages_sse",
		"message_token_counting",
		"generate_content_json",
		"generate_content_sse",
		"count_tokens",
		"embed_content",
	}
	if got := credentials["supported_endpoint_modes"]; !reflect.DeepEqual(got, wantEndpointModes) {
		t.Fatalf("hybrid supported_endpoint_modes = %#v, want %#v", got, wantEndpointModes)
	}
	account.HealthCheckEndpointMode = "responses_sse"
	store.accounts[account.ID] = account

	name := "混合 Anthropic 更新账号"
	updated, err := service.Update(context.Background(), UpdateInput{
		AccountID: created.Account.ID,
		Name:      &name,
	})
	if err != nil {
		t.Fatalf("update hybrid Anthropic account: %v", err)
	}
	if updated.Account == nil || updated.Account.Name != name || updated.Account.HealthCheckEndpointMode != "responses_sse" {
		t.Fatalf("updated account = %+v, want exact mode allowed by account credentials", updated.Account)
	}
}

func TestServiceUpdateRejectsCurrentModeWhenItIsNoLongerEnabled(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := newPublicAccountServiceForTest(store, nil)
	created, err := service.Add(context.Background(), validPublicAccountAddInput(
		"当前请求形态无效账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	account := store.accounts[created.Account.ID]
	credentials, err := service.codec.DecryptJSON(account.CredentialsEncrypted)
	if err != nil {
		t.Fatalf("decrypt current credentials: %v", err)
	}
	credentials["supported_endpoint_modes"] = []any{"chat_json", "chat_sse"}
	account.CredentialsEncrypted, err = service.codec.EncryptJSON(credentials)
	if err != nil {
		t.Fatalf("encrypt current credentials: %v", err)
	}
	account.HealthCheckEndpointMode = "responses_sse"
	store.accounts[account.ID] = account
	name := "不应写入的新名称"

	_, err = service.Update(context.Background(), UpdateInput{
		AccountID: account.ID,
		Name:      &name,
	})
	if !errors.Is(err, ErrInvalidHealthCheckEndpointMode) || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("update error = %v, want disabled current mode error", err)
	}
	if store.updateCalls != 0 {
		t.Fatalf("update calls = %d, want 0", store.updateCalls)
	}
}

func TestServiceUpdateBackfillsMissingCredentialEndpointModes(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := newPublicAccountServiceForTest(store, nil)
	created, err := service.Add(context.Background(), validPublicAccountAddInput(
		"缺失能力回退账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	account := store.accounts[created.Account.ID]
	credentials, err := service.codec.DecryptJSON(account.CredentialsEncrypted)
	if err != nil {
		t.Fatalf("decrypt current credentials: %v", err)
	}
	delete(credentials, "supported_endpoint_modes")
	account.CredentialsEncrypted, err = service.codec.EncryptJSON(credentials)
	if err != nil {
		t.Fatalf("encrypt current credentials: %v", err)
	}
	store.accounts[account.ID] = account
	name := "缺失能力已回退账号"

	updated, err := service.Update(context.Background(), UpdateInput{
		AccountID: account.ID,
		Name:      &name,
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if updated.Account == nil || updated.Account.HealthCheckEndpointMode != "responses_sse" {
		t.Fatalf("updated account = %+v, want responses_sse mode", updated.Account)
	}
	storedCredentials, err := service.codec.DecryptJSON(store.accounts[account.ID].CredentialsEncrypted)
	if err != nil {
		t.Fatalf("decrypt updated credentials: %v", err)
	}
	wantEndpointModes := []any{"chat_json", "chat_sse", "responses_json", "responses_sse"}
	if got := storedCredentials["supported_endpoint_modes"]; !reflect.DeepEqual(got, wantEndpointModes) {
		t.Fatalf("backfilled supported_endpoint_modes = %#v, want %#v", got, wantEndpointModes)
	}
}

func TestServiceUpdateRejectsMalformedCredentialEndpointModes(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "null", value: nil},
		{name: "scalar", value: "chat_json"},
		{name: "empty", value: []any{}},
		{name: "unknown", value: []any{"unknown_json"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newPublicAccountStoreFake()
			service := newPublicAccountServiceForTest(store, nil)
			created, err := service.Add(context.Background(), validPublicAccountAddInput(
				"非法能力账号",
				"gpt-5.4-mini",
			))
			if err != nil {
				t.Fatalf("add public account: %v", err)
			}
			account := store.accounts[created.Account.ID]
			credentials, err := service.codec.DecryptJSON(account.CredentialsEncrypted)
			if err != nil {
				t.Fatalf("decrypt current credentials: %v", err)
			}
			credentials["supported_endpoint_modes"] = tt.value
			account.CredentialsEncrypted, err = service.codec.EncryptJSON(credentials)
			if err != nil {
				t.Fatalf("encrypt current credentials: %v", err)
			}
			store.accounts[account.ID] = account
			name := "不应写入的非法能力账号"

			_, err = service.Update(context.Background(), UpdateInput{
				AccountID: account.ID,
				Name:      &name,
			})
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("update error = %v, want ErrInvalidCredentials", err)
			}
			if store.updateCalls != 0 {
				t.Fatalf("update calls = %d, want 0", store.updateCalls)
			}
		})
	}
}

func TestPublicAccountSummaryDoesNotExposeHealthCheckModel(t *testing.T) {
	summaryType := reflect.TypeOf(AccountSummary{})
	for index := 0; index < summaryType.NumField(); index++ {
		field := summaryType.Field(index)
		jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
		if field.Name == "HealthCheckModel" || jsonName == "healthCheckModel" {
			t.Fatalf("public account summary exposes health check model through field %s", field.Name)
		}
	}
}

func TestPublicAccountSummaryKeepsHealthCheckEndpointModeInternal(t *testing.T) {
	account := port.PublicAccountSummary{
		ID:                        "acct_public_summary_mode",
		Name:                      "公开请求形态账号",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Type:                      AccountTypeAPIKey,
		Status:                    port.PublicAccountStatusActive,
		HealthCheckEndpointMode:   "responses_sse",
	}
	for _, listShape := range []bool{false, true} {
		summary := publicAccountSummary(account, listShape)
		if summary.HealthCheckEndpointMode != "responses_sse" {
			t.Fatalf("summary listShape=%t health check endpoint mode = %q, want responses_sse", listShape, summary.HealthCheckEndpointMode)
		}
		data, err := json.Marshal(summary)
		if err != nil {
			t.Fatalf("marshal summary listShape=%t: %v", listShape, err)
		}
		if strings.Contains(string(data), "healthCheckEndpointMode") {
			t.Fatalf("summary listShape=%t JSON exposed healthCheckEndpointMode: %s", listShape, data)
		}
	}
}

func TestServiceAddUsesTargetProviderHealthCheckPreferenceBeforeProfileDefault(t *testing.T) {
	store := newPublicAccountStoreFake()
	target := port.PublicGroupTarget{
		ID:          "sys_existing_admin",
		Username:    "admin",
		DisplayName: "管理员",
		Status:      "active",
	}
	store.targetsByUsername["admin"] = target
	store.targetsByID[target.ID] = target
	store.healthCheckPreferences[target.ID+"|gpt"] = "gpt-5.5"
	service := newPublicAccountServiceForTest(store, nil)

	response, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "偏好检查模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
		SupportedModels:           NewStringListValue([]string{"gpt-5.5"}, true),
	})
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	if response.Target.Created {
		t.Fatalf("existing target was reported as created: %+v", response.Target)
	}
	created := store.accounts[response.Account.ID]
	if created.HealthCheckModel != "gpt-5.5" {
		t.Fatalf("stored health check model = %q, want preference gpt-5.5", created.HealthCheckModel)
	}
	if len(store.profileLookupSystemAccountIDs) != 1 || store.profileLookupSystemAccountIDs[0] != target.ID {
		t.Fatalf("profile lookup system account IDs = %#v, want [%q]", store.profileLookupSystemAccountIDs, target.ID)
	}
}

func TestServiceAddUsesProviderDefaultSupportedModelsWhenOmitted(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := newPublicAccountServiceForTest(store, nil)

	response, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "默认模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	if response.Account == nil {
		t.Fatal("response account is nil")
	}
	if got, want := response.Account.SupportedModels, defaultGPTSupportedModels; !slices.Equal(got, want) {
		t.Fatalf("supported models = %#v, want %#v", got, want)
	}
}

func TestServiceAddExplicitEmptySupportedModelsReturnsErrorWithoutCreatingAccount(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := newPublicAccountServiceForTest(store, nil)

	_, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "空模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
		SupportedModels:           NewStringListValue([]string{}, true),
	})
	assertInvalidSupportedModelsRequired(t, err)
	if len(store.accounts) != 0 {
		t.Fatalf("created accounts = %d, want 0", len(store.accounts))
	}
}

func TestServiceAddEmptyProviderDefaultSupportedModelsReturnsError(t *testing.T) {
	store := newPublicAccountStoreFake()
	profileKey := "gpt|profile_gpt_openai_v1"
	profile := store.profiles[profileKey]
	profile.DefaultSupportedModels = nil
	store.profiles[profileKey] = profile
	service := newPublicAccountServiceForTest(store, nil)

	_, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "默认空模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
	})
	assertInvalidSupportedModelsRequired(t, err)
	if len(store.accounts) != 0 {
		t.Fatalf("created accounts = %d, want 0", len(store.accounts))
	}
}

func TestServiceAddRejectsEmptyEffectiveHealthCheckModel(t *testing.T) {
	store := newPublicAccountStoreFake()
	profileKey := "gpt|profile_gpt_openai_v1"
	profile := store.profiles[profileKey]
	profile.DefaultHealthCheckModel = ""
	store.profiles[profileKey] = profile
	service := newPublicAccountServiceForTest(store, nil)

	_, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "空检查模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
	})
	assertInvalidHealthCheckModel(t, err, invalidHealthCheckModelRequiredMessage)
	if store.createCalls != 0 || len(store.accounts) != 0 {
		t.Fatalf("store create calls/accounts = %d/%d, want 0/0", store.createCalls, len(store.accounts))
	}
}

func TestServiceAddRejectsEffectiveHealthCheckModelOutsideSupportedModels(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := newPublicAccountServiceForTest(store, nil)

	_, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "排除检查模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
		SupportedModels:           NewStringListValue([]string{"gpt-5.5"}, true),
	})
	assertInvalidHealthCheckModel(t, err, invalidHealthCheckModelUnsupportedMessage)
	if store.createCalls != 0 || len(store.accounts) != 0 {
		t.Fatalf("store create calls/accounts = %d/%d, want 0/0", store.createCalls, len(store.accounts))
	}
}

func TestServiceAddDuplicateNamePrecedesEmptySupportedModelsValidation(t *testing.T) {
	store := newPublicAccountStoreFake()
	reader := defaultProviderModelReaderStub()
	service := newPublicAccountServiceForTest(store, reader)
	input := AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "重复账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
	}
	if _, err := service.Add(context.Background(), input); err != nil {
		t.Fatalf("seed public account: %v", err)
	}

	reader.resetCalls()
	input.SupportedModels = NewStringListValue([]string{}, true)
	_, err := service.Add(context.Background(), input)
	if !errors.Is(err, ErrDuplicateAccountName) {
		t.Fatalf("duplicate add error = %v, want ErrDuplicateAccountName", err)
	}
	if reader.calls != 0 {
		t.Fatalf("provider model reader calls = %d, want 0", reader.calls)
	}
}

func TestServiceAddPassesTargetOwnerAndProviderToModelCatalog(t *testing.T) {
	store := newPublicAccountStoreFake()
	reader := providerModelReaderWithItems(
		managementprovidermodels.ModelCatalogItem{ProviderCode: "gpt", Model: "gpt-5.4-mini", Scope: "built_in"},
	)
	service := newPublicAccountServiceForTest(store, reader)

	response, err := service.Add(context.Background(), validPublicAccountAddInput("目录目标账号", "gpt-5.4-mini"))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	if reader.calls != 1 || len(reader.inputs) != 1 {
		t.Fatalf("provider model reader calls/inputs = %d/%d, want 1/1", reader.calls, len(reader.inputs))
	}
	input := reader.inputs[0]
	if input.SystemAccountID != response.Target.SystemAccountID ||
		input.ProviderCode != "gpt" ||
		input.IncludeInactive ||
		input.IncludeUnpriced {
		t.Fatalf("provider model input = %+v, target = %+v", input, response.Target)
	}
}

func TestServiceAddAcceptsBuiltInGlobalAndPersonalModels(t *testing.T) {
	store := newPublicAccountStoreFake()
	profileKey := "gpt|profile_gpt_openai_v1"
	profile := store.profiles[profileKey]
	profile.DefaultHealthCheckModel = "built-in-model"
	store.profiles[profileKey] = profile
	reader := providerModelReaderWithItems(
		managementprovidermodels.ModelCatalogItem{ProviderCode: "gpt", Model: "built-in-model", Scope: "built_in"},
		managementprovidermodels.ModelCatalogItem{ProviderCode: "gpt", Model: "global-model", Scope: "global"},
		managementprovidermodels.ModelCatalogItem{ProviderCode: "gpt", Model: "personal-model", Scope: "personal", SystemAccountID: "sys_test_1"},
	)
	service := newPublicAccountServiceForTest(store, reader)
	wantModels := []string{"built-in-model", "global-model", "personal-model"}

	response, err := service.Add(context.Background(), validPublicAccountAddInput("多范围目录账号", wantModels...))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	if response.Account == nil || !slices.Equal(response.Account.SupportedModels, wantModels) {
		t.Fatalf("response account = %+v, want supported models %#v", response.Account, wantModels)
	}
	if store.createCalls != 1 {
		t.Fatalf("store create calls = %d, want 1", store.createCalls)
	}
}

func TestServiceAddRejectsModelsMissingFromVisibleUsableCatalogWithoutWriting(t *testing.T) {
	tests := []struct {
		name  string
		model string
	}{
		{name: "unknown", model: "unknown-model"},
		{name: "other owner personal", model: "other-owner-model"},
		{name: "disabled", model: "disabled-model"},
		{name: "unpriced", model: "unpriced-model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newPublicAccountStoreFake()
			reader := providerModelReaderWithItems(
				managementprovidermodels.ModelCatalogItem{ProviderCode: "gpt", Model: "visible-model", Scope: "built_in"},
			)
			service := newPublicAccountServiceForTest(store, reader)

			_, err := service.Add(context.Background(), validPublicAccountAddInput("不可用目录账号", tt.model))
			assertInvalidSupportedModelsCatalog(t, err, tt.model)
			if store.createCalls != 0 || len(store.accounts) != 0 {
				t.Fatalf("store create calls/accounts = %d/%d, want 0/0", store.createCalls, len(store.accounts))
			}
		})
	}
}

func TestServiceAddMixedSupportedModelsReportsFirstFiveInvalidModels(t *testing.T) {
	store := newPublicAccountStoreFake()
	reader := providerModelReaderWithItems(
		managementprovidermodels.ModelCatalogItem{ProviderCode: "gpt", Model: "valid-model", Scope: "built_in"},
	)
	service := newPublicAccountServiceForTest(store, reader)
	input := validPublicAccountAddInput(
		"混合目录账号",
		"valid-model",
		"invalid-1",
		"invalid-2",
		"invalid-3",
		"invalid-4",
		"invalid-5",
		"invalid-6",
	)

	_, err := service.Add(context.Background(), input)
	assertInvalidSupportedModelsCatalog(t, err, "invalid-1", "invalid-2", "invalid-3", "invalid-4", "invalid-5")
	if store.createCalls != 0 || len(store.accounts) != 0 {
		t.Fatalf("store create calls/accounts = %d/%d, want 0/0", store.createCalls, len(store.accounts))
	}
}

func TestServiceAddHybridBypassesCatalogButStillRequiresModels(t *testing.T) {
	t.Run("non-empty bypasses catalog", func(t *testing.T) {
		store := newPublicAccountStoreFake()
		readerErr := errors.New("catalog should not be read")
		reader := &providerModelReaderStub{err: readerErr}
		service := newPublicAccountServiceForTest(store, reader)
		input := validPublicAccountAddInput("混合供应商账号", "hybrid-direct-model")
		input.ProviderCode = hybridProviderCode
		input.ProviderProtocolProfileID = "profile_hybrid_openai_v1"

		response, err := service.Add(context.Background(), input)
		if err != nil {
			t.Fatalf("add hybrid public account: %v", err)
		}
		if response.Account == nil || !slices.Equal(response.Account.SupportedModels, []string{"hybrid-direct-model"}) {
			t.Fatalf("response account = %+v", response.Account)
		}
		if reader.calls != 0 {
			t.Fatalf("provider model reader calls = %d, want 0", reader.calls)
		}
	})

	t.Run("empty still fails", func(t *testing.T) {
		store := newPublicAccountStoreFake()
		reader := &providerModelReaderStub{err: errors.New("catalog should not be read")}
		service := newPublicAccountServiceForTest(store, reader)
		input := validPublicAccountAddInput("空混合供应商账号")
		input.ProviderCode = hybridProviderCode
		input.ProviderProtocolProfileID = "profile_hybrid_openai_v1"
		input.SupportedModels = NewStringListValue([]string{}, true)

		_, err := service.Add(context.Background(), input)
		assertInvalidSupportedModelsRequired(t, err)
		if reader.calls != 0 {
			t.Fatalf("provider model reader calls = %d, want 0", reader.calls)
		}
		if store.createCalls != 0 {
			t.Fatalf("store create calls = %d, want 0", store.createCalls)
		}
	})
}

func TestServiceAddRequiresProviderModelReaderForNonHybridProvider(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := NewService(Options{
		Store:  store,
		Now:    fixedPublicAccountNow,
		NewID:  sequentialPublicAccountID(),
		Secret: "public-account-test-secret",
	})

	_, err := service.Add(context.Background(), validPublicAccountAddInput("缺少目录读取器账号", "gpt-5.4-mini"))
	if err == nil || err.Error() != providerModelsRequiredMessage {
		t.Fatalf("add error = %v, want %q", err, providerModelsRequiredMessage)
	}
	if store.createCalls != 0 || len(store.accounts) != 0 {
		t.Fatalf("store create calls/accounts = %d/%d, want 0/0", store.createCalls, len(store.accounts))
	}
}

func TestServiceAddPropagatesProviderModelReaderErrorWithoutWriting(t *testing.T) {
	store := newPublicAccountStoreFake()
	readerErr := errors.New("provider model reader failed")
	reader := &providerModelReaderStub{err: readerErr}
	service := newPublicAccountServiceForTest(store, reader)

	_, err := service.Add(context.Background(), validPublicAccountAddInput("目录读取失败账号", "gpt-5.4-mini"))
	if !errors.Is(err, readerErr) {
		t.Fatalf("add error = %v, want reader error", err)
	}
	if store.createCalls != 0 || len(store.accounts) != 0 {
		t.Fatalf("store create calls/accounts = %d/%d, want 0/0", store.createCalls, len(store.accounts))
	}
}

func TestServiceUpdateRejectsPendingTestToActive(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := newPublicAccountServiceForTest(store, nil)
	created, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "主账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}

	status := StatusActive
	_, err = service.Update(context.Background(), UpdateInput{
		AccountID: created.Account.ID,
		Status:    &status,
	})
	if !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("update status error = %v, want ErrInvalidStatusTransition", err)
	}
}

func TestServiceUpdateValidatesCurrentProfileBeforeFilters(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := newPublicAccountServiceForTest(store, nil)
	created, err := service.Add(context.Background(), validPublicAccountAddInput(
		"停用协议档案账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	profileKey := "gpt|profile_gpt_openai_v1"
	profile := store.profiles[profileKey]
	profile.Enabled = false
	store.profiles[profileKey] = profile
	store.profileLookupSystemAccountIDs = nil
	wrongProvider := "openai"
	name := "不应写入的新名称"

	_, err = service.Update(context.Background(), UpdateInput{
		AccountID:    created.Account.ID,
		ProviderCode: &wrongProvider,
		Name:         &name,
	})
	if !errors.Is(err, ErrProviderProfileDisabled) {
		t.Fatalf("update error = %v, want ErrProviderProfileDisabled", err)
	}
	if len(store.profileLookupSystemAccountIDs) != 1 {
		t.Fatalf("profile lookups = %d, want current profile lookup only", len(store.profileLookupSystemAccountIDs))
	}
	if store.updateCalls != 0 {
		t.Fatalf("update calls = %d, want 0", store.updateCalls)
	}
}

func TestServiceUpdateReusesValidatedCurrentProfileForMatchingFilter(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := newPublicAccountServiceForTest(store, nil)
	created, err := service.Add(context.Background(), validPublicAccountAddInput(
		"匹配协议档案账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	store.profileLookupSystemAccountIDs = nil
	profileID := "profile_gpt_openai_v1"
	name := "匹配协议档案更新账号"

	updated, err := service.Update(context.Background(), UpdateInput{
		AccountID:                 created.Account.ID,
		ProviderProtocolProfileID: &profileID,
		Name:                      &name,
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if updated.Account == nil || updated.Account.Name != name {
		t.Fatalf("updated account = %+v", updated.Account)
	}
	if len(store.profileLookupSystemAccountIDs) != 1 {
		t.Fatalf("profile lookups = %d, want 1", len(store.profileLookupSystemAccountIDs))
	}
}

func TestServiceUpdateCredentialPartialPreservesExtensionFields(t *testing.T) {
	tests := []struct {
		name            string
		update          func() UpdateInput
		wantAPIKey      string
		wantBaseURL     string
		wantFingerprint string
		wantMask        string
	}{
		{
			name: "api key",
			update: func() UpdateInput {
				apiKey := "sk-updated-public-account-secret-abcdef0123456789"
				return UpdateInput{APIKey: &apiKey}
			},
			wantAPIKey:      "sk-updated-public-account-secret-abcdef0123456789",
			wantBaseURL:     "https://api.openai.com/v1",
			wantFingerprint: hashSecret("sk-updated-public-account-secret-abcdef0123456789"),
			wantMask:        maskSecret("sk-updated-public-account-secret-abcdef0123456789"),
		},
		{
			name: "base url",
			update: func() UpdateInput {
				baseURL := "https://gateway.example.com/openai/v1/"
				return UpdateInput{BaseURL: &baseURL}
			},
			wantAPIKey:      "sk-public-account-secret-0123456789abcdef",
			wantBaseURL:     "https://gateway.example.com/openai/v1",
			wantFingerprint: hashSecret("sk-public-account-secret-0123456789abcdef"),
			wantMask:        maskSecret("sk-public-account-secret-0123456789abcdef"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newPublicAccountStoreFake()
			service := newPublicAccountServiceForTest(store, nil)
			created, err := service.Add(context.Background(), validPublicAccountAddInput(
				"扩展凭据保留账号",
				"gpt-5.4-mini",
			))
			if err != nil {
				t.Fatalf("add public account: %v", err)
			}

			account := store.accounts[created.Account.ID]
			credentials, err := service.codec.DecryptJSON(account.CredentialsEncrypted)
			if err != nil {
				t.Fatalf("decrypt current credentials: %v", err)
			}
			extensions := map[string]any{
				"service_tier_override":     "priority",
				"reasoning_effort_override": "high",
				"supported_endpoint_modes":  []any{"chat_json", "responses_json", "responses_sse"},
				"endpoint": map[string]any{
					"chat": "/v1/chat/completions",
				},
				"error": map[string]any{
					"mode": "fallback",
				},
				"response": map[string]any{
					"passthrough": true,
				},
			}
			for key, value := range extensions {
				credentials[key] = value
			}
			account.CredentialsEncrypted, err = service.codec.EncryptJSON(credentials)
			if err != nil {
				t.Fatalf("encrypt extended credentials: %v", err)
			}
			account.Status = port.PublicAccountStatusActive
			account.Schedulable = true
			store.accounts[account.ID] = account

			input := tt.update()
			input.AccountID = account.ID
			response, err := service.Update(context.Background(), input)
			if err != nil {
				t.Fatalf("update public account: %v", err)
			}

			stored := store.accounts[account.ID]
			updatedCredentials, err := service.codec.DecryptJSON(stored.CredentialsEncrypted)
			if err != nil {
				t.Fatalf("decrypt updated credentials: %v", err)
			}
			for key, want := range extensions {
				if got := updatedCredentials[key]; !reflect.DeepEqual(got, want) {
					t.Fatalf("credential extension %q = %#v, want %#v", key, got, want)
				}
			}
			if updatedCredentials["api_key"] != tt.wantAPIKey || updatedCredentials["base_url"] != tt.wantBaseURL {
				t.Fatalf("updated credentials = %#v, want api_key/base_url %q/%q", updatedCredentials, tt.wantAPIKey, tt.wantBaseURL)
			}
			if stored.CredentialFingerprint == nil || *stored.CredentialFingerprint != tt.wantFingerprint {
				t.Fatalf("credential fingerprint = %v, want %q", stored.CredentialFingerprint, tt.wantFingerprint)
			}
			if stored.CredentialMask != tt.wantMask {
				t.Fatalf("credential mask = %q, want %q", stored.CredentialMask, tt.wantMask)
			}
			if response.Account == nil || response.Account.Status != StatusPendingTest || response.Account.Schedulable {
				t.Fatalf("response account = %+v, want pending_test and unschedulable", response.Account)
			}
			if response.Account.HealthCheckEndpointMode != "responses_sse" {
				t.Fatalf("updated response health check endpoint mode = %q, want responses_sse", response.Account.HealthCheckEndpointMode)
			}
			if !store.lastUpdateInput.ResetFailureState {
				t.Fatal("credential submission must reset failure state")
			}
			if !store.lastUpdateInput.ScheduleHealthCheck || !store.lastUpdateInput.ResetHealthDiagnostics {
				t.Fatal("changed credentials must schedule a health check and reset health diagnostics")
			}
		})
	}
}

func TestServiceUpdatePreservesEffectiveMultiAPIKeyPool(t *testing.T) {
	const (
		primaryAPIKey   = "sk-public-account-secret-0123456789abcdef"
		secondaryAPIKey = "sk-public-account-secondary-abcdef0123456789"
		requestedAPIKey = "sk-public-account-requested-abcdef0123456789"
	)
	tests := []struct {
		name   string
		update func() UpdateInput
	}{
		{
			name: "single api key input leaves existing pool authoritative",
			update: func() UpdateInput {
				apiKey := requestedAPIKey
				return UpdateInput{APIKey: &apiKey}
			},
		},
		{
			name: "non credential update preserves existing pool",
			update: func() UpdateInput {
				name := "多 Key 账号改名"
				return UpdateInput{Name: &name}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newPublicAccountStoreFake()
			service := newPublicAccountServiceForTest(store, nil)
			created, err := service.Add(context.Background(), validPublicAccountAddInput(
				"多 Key 公开账号",
				"gpt-5.4-mini",
			))
			if err != nil {
				t.Fatalf("add public account: %v", err)
			}

			account := store.accounts[created.Account.ID]
			credentials, err := service.codec.DecryptJSON(account.CredentialsEncrypted)
			if err != nil {
				t.Fatalf("decrypt current credentials: %v", err)
			}
			credentials["api_key"] = primaryAPIKey
			credentials["api_keys"] = []any{primaryAPIKey, secondaryAPIKey}
			credentials["api_key_strategy"] = "weighted_round_robin"
			credentials["api_key_weights"] = []any{float64(2), float64(3)}
			account.CredentialsEncrypted, err = service.codec.EncryptJSON(credentials)
			if err != nil {
				t.Fatalf("encrypt multi-key credentials: %v", err)
			}
			fingerprint := hashSecret(primaryAPIKey)
			account.CredentialFingerprint = &fingerprint
			account.CredentialMask = maskSecret(primaryAPIKey)
			account.Status = port.PublicAccountStatusActive
			account.Schedulable = true
			store.accounts[account.ID] = account

			input := tt.update()
			input.AccountID = account.ID
			response, err := service.Update(context.Background(), input)
			if err != nil {
				t.Fatalf("update public account: %v", err)
			}

			stored := store.accounts[account.ID]
			updatedCredentials, err := service.codec.DecryptJSON(stored.CredentialsEncrypted)
			if err != nil {
				t.Fatalf("decrypt updated credentials: %v", err)
			}
			if got := updatedCredentials["api_key"]; got != primaryAPIKey {
				t.Fatalf("canonical api_key = %#v, want pool primary %q", got, primaryAPIKey)
			}
			wantAPIKeys := []any{primaryAPIKey, secondaryAPIKey}
			if got := updatedCredentials["api_keys"]; !reflect.DeepEqual(got, wantAPIKeys) {
				t.Fatalf("api_keys = %#v, want %#v", got, wantAPIKeys)
			}
			if got := updatedCredentials["api_key_strategy"]; got != "weighted_round_robin" {
				t.Fatalf("api_key_strategy = %#v, want weighted_round_robin", got)
			}
			wantWeights := []any{float64(2), float64(3)}
			if got := updatedCredentials["api_key_weights"]; !reflect.DeepEqual(got, wantWeights) {
				t.Fatalf("api_key_weights = %#v, want %#v", got, wantWeights)
			}
			if stored.CredentialFingerprint == nil || *stored.CredentialFingerprint != hashSecret(primaryAPIKey) {
				t.Fatalf("credential fingerprint = %v, want effective pool primary", stored.CredentialFingerprint)
			}
			if stored.CredentialMask != maskSecret(primaryAPIKey) {
				t.Fatalf("credential mask = %q, want effective pool primary mask", stored.CredentialMask)
			}
			if response.Account == nil || response.Account.Status != StatusActive || !response.Account.Schedulable {
				t.Fatalf("response account = %+v, want active and schedulable", response.Account)
			}
			if store.lastUpdateInput.ResetFailureState ||
				store.lastUpdateInput.ScheduleHealthCheck ||
				store.lastUpdateInput.ResetHealthDiagnostics {
				t.Fatalf("unchanged effective key pool produced update flags %+v", store.lastUpdateInput)
			}
		})
	}
}

func TestServiceUpdateSchedulesHealthCheckWithoutImmediateDispatch(t *testing.T) {
	store := newPublicAccountStoreFake()
	created, err := newPublicAccountServiceForTest(store, nil).Add(
		context.Background(),
		validPublicAccountAddInput("提交后配置检查账号", "gpt-5.4-mini"),
	)
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	account := store.accounts[created.Account.ID]
	account.Status = port.PublicAccountStatusActive
	account.Schedulable = true
	store.accounts[account.ID] = account

	events := &publicAccountEventRecorder{}
	transactor := &publicAccountTransactorFake{store: store, events: events}
	dispatchStarted := make(chan struct{}, 1)
	dispatcher := &publicAccountHealthCheckDispatcherFake{started: dispatchStarted}
	service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, transactor, dispatcher, nil)
	apiKey := "sk-updated-public-account-secret-abcdef0123456789"

	response, err := service.Update(context.Background(), UpdateInput{
		AccountID: account.ID,
		APIKey:    &apiKey,
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if response.Account == nil || response.Account.Status != StatusPendingTest {
		t.Fatalf("response account = %+v, want pending_test", response.Account)
	}
	if got, want := events.snapshot(), []string{"transaction_committed"}; !slices.Equal(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if !store.lastUpdateInput.ScheduleHealthCheck {
		t.Fatal("configuration update must persist health check scheduling")
	}
	select {
	case <-dispatchStarted:
		t.Fatalf("Update dispatched an immediate health check: %#v", dispatcher.callsSnapshot())
	case <-time.After(100 * time.Millisecond):
	}
}

func TestServiceUpdateEquivalentCredentialsPreservesActiveScheduling(t *testing.T) {
	store := newPublicAccountStoreFake()
	created, err := newPublicAccountServiceForTest(store, nil).Add(context.Background(), validPublicAccountAddInput(
		"相同凭据账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	account := store.accounts[created.Account.ID]
	account.Status = port.PublicAccountStatusActive
	account.Schedulable = true
	store.accounts[account.ID] = account
	dispatcher := &publicAccountHealthCheckDispatcherFake{}
	service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, nil, dispatcher, nil)

	apiKey := " sk-public-account-secret-0123456789abcdef "
	baseURL := "https://api.openai.com/v1/"
	response, err := service.Update(context.Background(), UpdateInput{
		AccountID: account.ID,
		APIKey:    &apiKey,
		BaseURL:   &baseURL,
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if response.Account == nil || response.Account.Status != StatusActive || !response.Account.Schedulable {
		t.Fatalf("response account = %+v, want active and schedulable", response.Account)
	}
	if store.lastUpdateInput.ResetFailureState ||
		store.lastUpdateInput.ScheduleHealthCheck ||
		store.lastUpdateInput.ResetHealthDiagnostics {
		t.Fatalf("equivalent credentials produced update flags %+v", store.lastUpdateInput)
	}
	if calls := dispatcher.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("dispatch calls = %#v, want none", calls)
	}
}

func TestServiceUpdateConfigurationChangeKeepsExplicitDisabled(t *testing.T) {
	store := newPublicAccountStoreFake()
	created, err := newPublicAccountServiceForTest(store, nil).Add(context.Background(), validPublicAccountAddInput(
		"配置修改并停用账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	account := store.accounts[created.Account.ID]
	account.Status = port.PublicAccountStatusActive
	account.Schedulable = true
	store.accounts[account.ID] = account
	dispatcher := &publicAccountHealthCheckDispatcherFake{}
	service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, nil, dispatcher, nil)

	status := StatusDisabled
	apiKey := "sk-disabled-updated-abcdef0123456789"
	response, err := service.Update(context.Background(), UpdateInput{
		AccountID: account.ID,
		APIKey:    &apiKey,
		Status:    &status,
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if response.Account == nil || response.Account.Status != StatusDisabled || response.Account.Schedulable {
		t.Fatalf("response account = %+v, want disabled and unschedulable", response.Account)
	}
	if !store.lastUpdateInput.ResetFailureState {
		t.Fatal("failure state reset flag = false, want true")
	}
	if !store.lastUpdateInput.ScheduleHealthCheck || !store.lastUpdateInput.ResetHealthDiagnostics {
		t.Fatal("changed disabled credentials must schedule a health check and reset health diagnostics")
	}
	if calls := dispatcher.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("dispatch calls = %#v, want none", calls)
	}
}

func TestServiceUpdateNonConfigurationFieldsDoNotForcePendingTest(t *testing.T) {
	store := newPublicAccountStoreFake()
	created, err := newPublicAccountServiceForTest(store, nil).Add(context.Background(), validPublicAccountAddInput(
		"普通字段修改账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	account := store.accounts[created.Account.ID]
	account.Status = port.PublicAccountStatusActive
	account.Schedulable = true
	store.accounts[account.ID] = account
	dispatcher := &publicAccountHealthCheckDispatcherFake{}
	service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, nil, dispatcher, nil)

	name := "普通字段修改后账号"
	concurrency := 32
	priority := 7
	notes := "仅修改普通字段"
	response, err := service.Update(context.Background(), UpdateInput{
		AccountID:            account.ID,
		Name:                 &name,
		ConcurrencyLimit:     &concurrency,
		Priority:             &priority,
		Notes:                NewOptionalString(&notes, true),
		AvailabilitySchedule: NewJSONValue(map[string]any{"enabled": false}, true),
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if response.Account == nil || response.Account.Status != StatusActive || !response.Account.Schedulable {
		t.Fatalf("response account = %+v, want active and schedulable", response.Account)
	}
	if store.lastUpdateInput.ResetFailureState {
		t.Fatal("non-configuration fields must not reset failure state")
	}
	if store.lastUpdateInput.ScheduleHealthCheck || store.lastUpdateInput.ResetHealthDiagnostics {
		t.Fatal("non-configuration fields must not alter health check scheduling")
	}
	if !store.lastUpdateInput.GroupDispatchChanged {
		t.Fatal("explicit priority must update the group binding dispatch")
	}
	if calls := dispatcher.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("dispatch calls = %#v, want none", calls)
	}
}

func TestServiceUpdateWithoutPriorityLeavesGroupBindingDispatchUntouched(t *testing.T) {
	store := newPublicAccountStoreFake()
	created, err := newPublicAccountServiceForTest(store, nil).Add(context.Background(), validPublicAccountAddInput(
		"不修改分组优先级账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}

	name := "仅修改账号名称"
	_, err = newPublicAccountServiceForTest(store, nil).Update(context.Background(), UpdateInput{
		AccountID: created.Account.ID,
		Name:      &name,
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if store.lastUpdateInput.GroupDispatchChanged {
		t.Fatal("omitted priority must not update the group binding dispatch")
	}
}

func TestServiceUpdateOmittedSupportedModelsRejectsExistingEmptyModelsWithoutUpdatingStore(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := newPublicAccountServiceForTest(store, nil)
	created, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "旧空模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}

	account := store.accounts[created.Account.ID]
	account.SupportedModels = nil
	store.accounts[account.ID] = account

	name := "仅修改名称"
	_, err = service.Update(context.Background(), UpdateInput{
		AccountID: account.ID,
		Name:      &name,
	})
	assertInvalidSupportedModelsRequired(t, err)
	if store.updateCalls != 0 {
		t.Fatalf("store update calls = %d, want 0", store.updateCalls)
	}
	stored := store.accounts[account.ID]
	if stored.Name != account.Name || len(stored.SupportedModels) != 0 {
		t.Fatalf("stored account = %+v, want unchanged empty-model account", stored)
	}
}

func TestServiceUpdateOmittedSupportedModelsPreservesExistingNonEmptyModels(t *testing.T) {
	store := newPublicAccountStoreFake()
	reader := defaultProviderModelReaderStub()
	service := newPublicAccountServiceForTest(store, reader)
	wantModels := []string{"gpt-5.5", "gpt-5.4-mini"}
	created, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "正常模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
		SupportedModels:           NewStringListValue(wantModels, true),
	})
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}

	reader.resetCalls()
	name := "正常改名"
	response, err := service.Update(context.Background(), UpdateInput{
		AccountID: created.Account.ID,
		Name:      &name,
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if store.updateCalls != 1 {
		t.Fatalf("store update calls = %d, want 1", store.updateCalls)
	}
	if store.lastUpdateInput.SupportedModelsChanged {
		t.Fatal("omitted supportedModels must not mark the model set as changed")
	}
	if response.Account == nil || !slices.Equal(response.Account.SupportedModels, wantModels) {
		t.Fatalf("response account = %+v, want supported models %#v", response.Account, wantModels)
	}
	if got := store.accounts[created.Account.ID].SupportedModels; !slices.Equal(got, wantModels) {
		t.Fatalf("stored supported models = %#v, want %#v", got, wantModels)
	}
	if reader.calls != 0 {
		t.Fatalf("provider model reader calls = %d, want 0", reader.calls)
	}
}

func TestServiceUpdateUnorderedEquivalentSupportedModelsSkipsCatalog(t *testing.T) {
	store := newPublicAccountStoreFake()
	reader := defaultProviderModelReaderStub()
	created, err := newPublicAccountServiceForTest(store, reader).Add(context.Background(), validPublicAccountAddInput(
		"无序相同模型账号",
		"gpt-5.5",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	account := store.accounts[created.Account.ID]
	account.Status = port.PublicAccountStatusActive
	account.Schedulable = true
	store.accounts[account.ID] = account
	dispatcher := &publicAccountHealthCheckDispatcherFake{}
	service := newPublicAccountServiceWithHealthDispatchForTest(store, reader, nil, dispatcher, nil)

	reader.resetCalls()
	store.updateCalls = 0
	response, err := service.Update(context.Background(), UpdateInput{
		AccountID: created.Account.ID,
		SupportedModels: NewStringListValue([]string{
			" gpt-5.4-mini ",
			"gpt-5.5",
			"gpt-5.5",
		}, true),
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("provider model reader calls = %d, want 0", reader.calls)
	}
	if store.updateCalls != 1 {
		t.Fatalf("store update calls = %d, want 1", store.updateCalls)
	}
	if store.lastUpdateInput.SupportedModelsChanged {
		t.Fatal("unordered equivalent supportedModels must not mark the model set as changed")
	}
	if store.lastUpdateInput.ResetFailureState {
		t.Fatal("submitted supportedModels must preserve failure state when credentials are unchanged")
	}
	if !store.lastUpdateInput.ScheduleHealthCheck || store.lastUpdateInput.ResetHealthDiagnostics {
		t.Fatal("submitted supportedModels must schedule a health check without resetting diagnostics")
	}
	wantModels := []string{"gpt-5.5", "gpt-5.4-mini"}
	if response.Account == nil ||
		response.Account.Status != StatusActive ||
		!response.Account.Schedulable ||
		!slices.Equal(response.Account.SupportedModels, wantModels) {
		t.Fatalf("response account = %+v, want supported models %#v", response.Account, wantModels)
	}
	if calls := dispatcher.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("dispatch calls = %#v, want none", calls)
	}
}

func TestServiceUpdateTransactionFailureSkipsConfigurationDispatch(t *testing.T) {
	store := newPublicAccountStoreFake()
	created, err := newPublicAccountServiceForTest(store, nil).Add(
		context.Background(),
		validPublicAccountAddInput("更新提交失败账号", "gpt-5.4-mini"),
	)
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	commitErr := errors.New("update commit failed")
	transactor := &publicAccountTransactorFake{store: store, commitError: commitErr}
	dispatcher := &publicAccountHealthCheckDispatcherFake{}
	service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, transactor, dispatcher, nil)
	apiKey := "sk-update-commit-failed-abcdef0123456789"

	_, err = service.Update(context.Background(), UpdateInput{
		AccountID: created.Account.ID,
		APIKey:    &apiKey,
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("update error = %v, want commit failure", err)
	}
	if calls := dispatcher.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("dispatch calls = %#v, want none", calls)
	}
}

func TestServiceUpdateDoesNotUseImmediateDispatcher(t *testing.T) {
	store := newPublicAccountStoreFake()
	created, err := newPublicAccountServiceForTest(store, nil).Add(
		context.Background(),
		validPublicAccountAddInput("更新投递失败账号", "gpt-5.4-mini"),
	)
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	dispatcher := &publicAccountHealthCheckDispatcherFake{err: errors.New("dispatch failed")}
	service := newPublicAccountServiceWithHealthDispatchForTest(store, nil, nil, dispatcher, nil)
	baseURL := "https://gateway.example.com/openai/v1"

	response, err := service.Update(context.Background(), UpdateInput{
		AccountID: created.Account.ID,
		BaseURL:   &baseURL,
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if response.Action != "updated" || response.Account == nil {
		t.Fatalf("response = %+v, want committed update", response)
	}
	if !store.lastUpdateInput.ScheduleHealthCheck {
		t.Fatal("configuration update must persist health check scheduling")
	}
	if calls := dispatcher.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("dispatch calls = %#v, want none", calls)
	}
}

func TestServiceUpdateChangedSupportedModelsMarksStoreUpdate(t *testing.T) {
	store := newPublicAccountStoreFake()
	reader := defaultProviderModelReaderStub()
	service := newPublicAccountServiceForTest(store, reader)
	created, err := service.Add(context.Background(), validPublicAccountAddInput(
		"真实更新模型账号",
		"gpt-5.5",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}

	reader.resetCalls()
	store.updateCalls = 0
	response, err := service.Update(context.Background(), UpdateInput{
		AccountID:       created.Account.ID,
		SupportedModels: NewStringListValue([]string{"gpt-5.4-mini"}, true),
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("provider model reader calls = %d, want 1", reader.calls)
	}
	if store.updateCalls != 1 {
		t.Fatalf("store update calls = %d, want 1", store.updateCalls)
	}
	if !store.lastUpdateInput.SupportedModelsChanged {
		t.Fatal("changed supportedModels must mark the model set as changed")
	}
	if store.lastUpdateInput.ResetFailureState {
		t.Fatal("changed supportedModels must preserve failure state")
	}
	if !store.lastUpdateInput.ScheduleHealthCheck || store.lastUpdateInput.ResetHealthDiagnostics {
		t.Fatal("changed supportedModels must schedule a health check without resetting diagnostics")
	}
	wantModels := []string{"gpt-5.4-mini"}
	if response.Account == nil || !slices.Equal(response.Account.SupportedModels, wantModels) {
		t.Fatalf("response account = %+v, want supported models %#v", response.Account, wantModels)
	}
}

func TestServiceUpdateRejectsRemovingCurrentHealthCheckModelWithoutWriting(t *testing.T) {
	store := newPublicAccountStoreFake()
	reader := defaultProviderModelReaderStub()
	service := newPublicAccountServiceForTest(store, reader)
	created, err := service.Add(context.Background(), validPublicAccountAddInput(
		"不能移除检查模型账号",
		defaultGPTHealthCheckModel,
		"gpt-5.5",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	before := store.accounts[created.Account.ID]
	reader.resetCalls()
	store.updateCalls = 0

	_, err = service.Update(context.Background(), UpdateInput{
		AccountID:       created.Account.ID,
		SupportedModels: NewStringListValue([]string{"gpt-5.5"}, true),
	})
	assertInvalidHealthCheckModel(t, err, invalidHealthCheckModelUnsupportedMessage)
	if reader.calls != 1 {
		t.Fatalf("provider model reader calls = %d, want 1", reader.calls)
	}
	if store.updateCalls != 0 {
		t.Fatalf("store update calls = %d, want 0", store.updateCalls)
	}
	if after := store.accounts[created.Account.ID]; !reflect.DeepEqual(after, before) {
		t.Fatalf("stored account changed after rejected health model removal:\nbefore = %+v\nafter  = %+v", before, after)
	}
}

func TestServiceUpdateRejectsExistingInvalidHealthCheckModelOnUnrelatedWrite(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := newPublicAccountServiceForTest(store, nil)
	created, err := service.Add(context.Background(), validPublicAccountAddInput(
		"现有检查模型异常账号",
		defaultGPTHealthCheckModel,
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	account := store.accounts[created.Account.ID]
	account.HealthCheckModel = "gpt-missing"
	store.accounts[account.ID] = account
	name := "不应保存的新名称"

	_, err = service.Update(context.Background(), UpdateInput{
		AccountID: account.ID,
		Name:      &name,
	})
	assertInvalidHealthCheckModel(t, err, invalidHealthCheckModelUnsupportedMessage)
	if store.updateCalls != 0 {
		t.Fatalf("store update calls = %d, want 0", store.updateCalls)
	}
	if stored := store.accounts[account.ID]; stored.Name != account.Name || stored.HealthCheckModel != "gpt-missing" {
		t.Fatalf("stored account = %+v, want unchanged invalid health model account", stored)
	}
}

func TestServiceUpdateCatalogFailureLeavesStateUnchanged(t *testing.T) {
	store := newPublicAccountStoreFake()
	reader := defaultProviderModelReaderStub()
	service := newPublicAccountServiceForTest(store, reader)
	created, err := service.Add(context.Background(), validPublicAccountAddInput(
		"更新目录失败账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	before := store.accounts[created.Account.ID]
	reader.resetCalls()
	store.updateCalls = 0
	updatedName := "不应保存的新名称"

	_, err = service.Update(context.Background(), UpdateInput{
		AccountID:       created.Account.ID,
		Name:            &updatedName,
		SupportedModels: NewStringListValue([]string{"unknown-update-model"}, true),
	})
	assertInvalidSupportedModelsCatalog(t, err, "unknown-update-model")
	if reader.calls != 1 {
		t.Fatalf("provider model reader calls = %d, want 1", reader.calls)
	}
	if store.updateCalls != 0 {
		t.Fatalf("store update calls = %d, want 0", store.updateCalls)
	}
	after := store.accounts[created.Account.ID]
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("stored account changed after failed update:\nbefore = %+v\nafter  = %+v", before, after)
	}
}

func TestServiceUpdateInvalidCredentialsPrecedeCatalogValidation(t *testing.T) {
	store := newPublicAccountStoreFake()
	reader := defaultProviderModelReaderStub()
	service := newPublicAccountServiceForTest(store, reader)
	created, err := service.Add(context.Background(), validPublicAccountAddInput(
		"凭据优先账号",
		"gpt-5.4-mini",
	))
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	before := store.accounts[created.Account.ID]
	reader.resetCalls()
	store.updateCalls = 0
	emptyAPIKey := ""

	_, err = service.Update(context.Background(), UpdateInput{
		AccountID:       created.Account.ID,
		APIKey:          &emptyAPIKey,
		SupportedModels: NewStringListValue([]string{"unknown-update-model"}, true),
	})
	if !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("update error = %v, want ErrInvalidAPIKey", err)
	}
	if reader.calls != 0 {
		t.Fatalf("provider model reader calls = %d, want 0", reader.calls)
	}
	if store.updateCalls != 0 {
		t.Fatalf("store update calls = %d, want 0", store.updateCalls)
	}
	after := store.accounts[created.Account.ID]
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("stored account changed after failed update:\nbefore = %+v\nafter  = %+v", before, after)
	}
}

func assertInvalidSupportedModelsRequired(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalidSupportedModels) {
		t.Fatalf("supported models error = %v, want ErrInvalidSupportedModels", err)
	}
	want := ErrInvalidSupportedModels.Error() + ": " + invalidSupportedModelsRequiredMessage
	if err.Error() != want {
		t.Fatalf("supported models error = %q, want %q", err.Error(), want)
	}
}

func assertInvalidSupportedModelsCatalog(t *testing.T, err error, invalidModels ...string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidSupportedModels) {
		t.Fatalf("supported models error = %v, want ErrInvalidSupportedModels", err)
	}
	want := ErrInvalidSupportedModels.Error() +
		": 账户支持模型不在供应商模型目录中：" +
		strings.Join(invalidModels, "、")
	if err.Error() != want {
		t.Fatalf("supported models error = %q, want %q", err.Error(), want)
	}
}

func assertInvalidHealthCheckModel(t *testing.T, err error, message string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidHealthCheckModel) {
		t.Fatalf("health check model error = %v, want ErrInvalidHealthCheckModel", err)
	}
	want := ErrInvalidHealthCheckModel.Error() + ": " + message
	if err.Error() != want {
		t.Fatalf("health check model error = %q, want %q", err.Error(), want)
	}
}

func TestServiceDeleteMissingIsNotFoundAction(t *testing.T) {
	service := newPublicAccountServiceForTest(newPublicAccountStoreFake(), nil)
	username := "admin"
	response, err := service.Delete(context.Background(), DeleteInput{
		AccountID:      "acc_missing",
		TargetUsername: &username,
	})
	if err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if response.Action != "not_found" || response.Account != nil || response.Target.Username != "admin" {
		t.Fatalf("delete missing response = %+v", response)
	}
}

func newPublicAccountServiceForTest(store *publicAccountStoreFake, providerModels ProviderModelReader) *Service {
	if providerModels == nil {
		providerModels = defaultProviderModelReaderStub()
	}
	return NewService(Options{
		Store:          store,
		ProviderModels: providerModels,
		Now:            fixedPublicAccountNow,
		NewID:          sequentialPublicAccountID(),
		Secret:         "public-account-test-secret",
	})
}

func newPublicAccountServiceWithHealthDispatchForTest(
	store *publicAccountStoreFake,
	providerModels ProviderModelReader,
	transactor port.PublicAccountTransactor,
	dispatcher AccountHealthCheckDispatcher,
	logger *slog.Logger,
) *Service {
	return newPublicAccountServiceWithHealthDispatchTimeoutForTest(
		store,
		providerModels,
		transactor,
		dispatcher,
		logger,
		0,
	)
}

func newPublicAccountServiceWithHealthDispatchTimeoutForTest(
	store *publicAccountStoreFake,
	providerModels ProviderModelReader,
	transactor port.PublicAccountTransactor,
	dispatcher AccountHealthCheckDispatcher,
	logger *slog.Logger,
	timeout time.Duration,
) *Service {
	if providerModels == nil {
		providerModels = defaultProviderModelReaderStub()
	}
	return NewService(Options{
		Store:                      store,
		Transactor:                 transactor,
		ProviderModels:             providerModels,
		HealthCheckDispatcher:      dispatcher,
		HealthCheckDispatchTimeout: timeout,
		Logger:                     logger,
		Now:                        fixedPublicAccountNow,
		NewID:                      sequentialPublicAccountID(),
		Secret:                     "public-account-test-secret",
	})
}

func validPublicAccountAddInput(name string, models ...string) AddInput {
	return AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      name,
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
		SupportedModels:           NewStringListValue(models, true),
	}
}

func fixedPublicAccountNow() time.Time {
	return time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
}

func sequentialPublicAccountID() func(string) string {
	seq := map[string]int{}
	return func(prefix string) string {
		seq[prefix]++
		return prefix + "_test_" + string(rune('0'+seq[prefix]))
	}
}

var defaultGPTSupportedModels = []string{
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-image-2",
}

const defaultGPTHealthCheckModel = "gpt-5.4-mini"

type providerModelReaderStub struct {
	items  []managementprovidermodels.ModelCatalogItem
	err    error
	calls  int
	inputs []managementprovidermodels.ModelListInput
}

func defaultProviderModelReaderStub() *providerModelReaderStub {
	items := make([]managementprovidermodels.ModelCatalogItem, 0, len(defaultGPTSupportedModels))
	for _, model := range defaultGPTSupportedModels {
		items = append(items, managementprovidermodels.ModelCatalogItem{
			ProviderCode: "gpt",
			Model:        model,
			Scope:        "built_in",
			Status:       "active",
		})
	}
	return providerModelReaderWithItems(items...)
}

func providerModelReaderWithItems(items ...managementprovidermodels.ModelCatalogItem) *providerModelReaderStub {
	return &providerModelReaderStub{
		items: append([]managementprovidermodels.ModelCatalogItem(nil), items...),
	}
}

func (s *providerModelReaderStub) Models(_ context.Context, input managementprovidermodels.ModelListInput) ([]managementprovidermodels.ModelCatalogItem, error) {
	s.calls++
	s.inputs = append(s.inputs, input)
	if s.err != nil {
		return nil, s.err
	}
	return append([]managementprovidermodels.ModelCatalogItem(nil), s.items...), nil
}

func (s *providerModelReaderStub) resetCalls() {
	s.calls = 0
	s.inputs = nil
}

type publicAccountHealthCheckDispatchCall struct {
	accountID   string
	reason      string
	contextErr  error
	observedAt  time.Time
	deadline    time.Time
	hasDeadline bool
}

type publicAccountHealthCheckDispatcherFake struct {
	mu                    sync.Mutex
	calls                 []publicAccountHealthCheckDispatchCall
	err                   error
	events                *publicAccountEventRecorder
	started               chan struct{}
	release               <-chan struct{}
	finished              chan struct{}
	blockUntilContextDone bool
}

func (d *publicAccountHealthCheckDispatcherFake) Dispatch(ctx context.Context, accountID string, reason string) error {
	if d.events != nil {
		d.events.record("dispatch")
	}
	deadline, hasDeadline := ctx.Deadline()
	d.mu.Lock()
	d.calls = append(d.calls, publicAccountHealthCheckDispatchCall{
		accountID:   accountID,
		reason:      reason,
		contextErr:  ctx.Err(),
		observedAt:  time.Now(),
		deadline:    deadline,
		hasDeadline: hasDeadline,
	})
	d.mu.Unlock()
	if d.started != nil {
		select {
		case d.started <- struct{}{}:
		default:
		}
	}
	if d.finished != nil {
		defer func() {
			select {
			case d.finished <- struct{}{}:
			default:
			}
		}()
	}
	if d.release != nil {
		select {
		case <-d.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if d.blockUntilContextDone {
		<-ctx.Done()
		return ctx.Err()
	}
	return d.err
}

func (d *publicAccountHealthCheckDispatcherFake) callsSnapshot() []publicAccountHealthCheckDispatchCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]publicAccountHealthCheckDispatchCall(nil), d.calls...)
}

func assertPublicAccountHealthDispatchCalls(
	t *testing.T,
	dispatcher *publicAccountHealthCheckDispatcherFake,
	want ...publicAccountHealthCheckDispatchCall,
) {
	t.Helper()
	got := dispatcher.callsSnapshot()
	if len(want) > 0 {
		got = waitForPublicAccountHealthDispatchCalls(t, dispatcher, len(want))
		time.Sleep(20 * time.Millisecond)
		got = dispatcher.callsSnapshot()
	}
	if len(got) != len(want) {
		t.Fatalf("dispatch calls = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index].accountID != want[index].accountID || got[index].reason != want[index].reason {
			t.Fatalf("dispatch calls = %#v, want %#v", got, want)
		}
	}
}

func waitForPublicAccountHealthDispatchCalls(
	t *testing.T,
	dispatcher *publicAccountHealthCheckDispatcherFake,
	count int,
) []publicAccountHealthCheckDispatchCall {
	t.Helper()
	waitForPublicAccountCondition(t, time.Second, "health check dispatch", func() bool {
		return len(dispatcher.callsSnapshot()) >= count
	})
	return dispatcher.callsSnapshot()
}

func waitForPublicAccountCondition(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(time.Millisecond)
	}
}

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *synchronizedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(value)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

type publicAccountEventRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *publicAccountEventRecorder) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *publicAccountEventRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type publicAccountTransactorFake struct {
	store        *publicAccountStoreFake
	beforeErrors []error
	commitError  error
	events       *publicAccountEventRecorder
	calls        int
}

func (t *publicAccountTransactorFake) PublicAccountInTx(
	ctx context.Context,
	fn func(context.Context, port.PublicAccountStore) error,
) error {
	t.calls++
	if index := t.calls - 1; index < len(t.beforeErrors) && t.beforeErrors[index] != nil {
		return t.beforeErrors[index]
	}
	if err := fn(ctx, t.store); err != nil {
		return err
	}
	if t.commitError != nil {
		return t.commitError
	}
	if t.events != nil {
		t.events.record("transaction_committed")
	}
	return nil
}

type publicAccountStoreFake struct {
	targetsByUsername             map[string]port.PublicGroupTarget
	targetsByID                   map[string]port.PublicGroupTarget
	profiles                      map[string]port.PublicAccountProviderProfile
	healthCheckPreferences        map[string]string
	profileLookupSystemAccountIDs []string
	groups                        map[string]port.PublicAccountGroupRef
	accounts                      map[string]port.PublicAccountSummary
	createCalls                   int
	updateCalls                   int
	lastUpdateInput               port.PublicAccountUpdateInput
}

func newPublicAccountStoreFake() *publicAccountStoreFake {
	return &publicAccountStoreFake{
		targetsByUsername: map[string]port.PublicGroupTarget{},
		targetsByID:       map[string]port.PublicGroupTarget{},
		profiles: map[string]port.PublicAccountProviderProfile{
			"gpt|profile_gpt_openai_v1": {
				ID:                      "profile_gpt_openai_v1",
				ProviderCode:            "gpt",
				Name:                    "GPT / OpenAI v1",
				Enabled:                 true,
				ProviderEnabled:         true,
				ProtocolCode:            "openai",
				ProtocolVersion:         "v1",
				AccountTypesJSON:        `["oauth","api_key"]`,
				EnabledEndpointModes:    []string{"responses_json", "responses_sse", "chat_json", "chat_sse"},
				DefaultSupportedModels:  append([]string(nil), defaultGPTSupportedModels...),
				DefaultHealthCheckModel: defaultGPTHealthCheckModel,
			},
			"hybrid|profile_hybrid_openai_v1": {
				ID:                      "profile_hybrid_openai_v1",
				ProviderCode:            hybridProviderCode,
				Name:                    "Hybrid / OpenAI v1",
				Enabled:                 true,
				ProviderEnabled:         true,
				ProtocolCode:            "openai",
				ProtocolVersion:         "v1",
				AccountTypesJSON:        `["api_key"]`,
				EnabledEndpointModes:    []string{"chat_json", "chat_sse", "responses_json", "responses_sse", "messages_json", "messages_sse", "generate_content_json", "generate_content_sse"},
				DefaultSupportedModels:  []string{"hybrid-direct-model"},
				DefaultHealthCheckModel: "hybrid-direct-model",
			},
		},
		healthCheckPreferences: map[string]string{},
		groups:                 map[string]port.PublicAccountGroupRef{},
		accounts:               map[string]port.PublicAccountSummary{},
	}
}

func (s *publicAccountStoreFake) FindPublicAccountTargetByUsername(_ context.Context, username string) (port.PublicGroupTarget, bool, error) {
	target, ok := s.targetsByUsername[strings.ToLower(strings.TrimSpace(username))]
	return target, ok, nil
}

func (s *publicAccountStoreFake) FindPublicAccountTargetByID(_ context.Context, id string) (port.PublicGroupTarget, bool, error) {
	target, ok := s.targetsByID[id]
	return target, ok, nil
}

func (s *publicAccountStoreFake) CreatePublicAccountTarget(_ context.Context, input port.PublicGroupTargetCreateInput) (port.PublicGroupTarget, error) {
	target := port.PublicGroupTarget{
		ID:          input.ID,
		Username:    input.Username,
		DisplayName: input.DisplayName,
		Status:      "active",
		Created:     true,
	}
	s.targetsByUsername[strings.ToLower(input.Username)] = target
	s.targetsByID[input.ID] = target
	return target, nil
}

func (s *publicAccountStoreFake) FindPublicAccountProviderProfile(_ context.Context, systemAccountID string, providerCode string, profileID string) (port.PublicAccountProviderProfile, bool, error) {
	s.profileLookupSystemAccountIDs = append(s.profileLookupSystemAccountIDs, strings.TrimSpace(systemAccountID))
	profile, ok := s.profiles[strings.TrimSpace(providerCode)+"|"+strings.TrimSpace(profileID)]
	if preference := s.healthCheckPreferences[strings.TrimSpace(systemAccountID)+"|"+strings.TrimSpace(providerCode)]; preference != "" {
		profile.DefaultHealthCheckModel = preference
	}
	return profile, ok, nil
}

func (s *publicAccountStoreFake) FindExistingPublicAccountGroupByName(_ context.Context, systemAccountID string, providerCode string, name string) (port.PublicAccountGroupRef, bool, error) {
	group, ok := s.groups[systemAccountID+"|"+providerCode+"|"+strings.ToLower(strings.TrimSpace(name))]
	return group, ok, nil
}

func (s *publicAccountStoreFake) CreatePublicAccountGroup(_ context.Context, input port.PublicGroupCreateInput) (port.PublicAccountGroupRef, error) {
	group := port.PublicAccountGroupRef{
		ID:              input.ID,
		SystemAccountID: input.SystemAccountID,
		Name:            input.Name,
		ProviderCode:    input.ProviderCode,
		Enabled:         input.Enabled,
		GroupType:       input.GroupType,
		Created:         true,
	}
	s.groups[input.SystemAccountID+"|"+input.ProviderCode+"|"+strings.ToLower(input.Name)] = group
	return group, nil
}

func (s *publicAccountStoreFake) FindPublicAccountGroupByID(_ context.Context, groupID string) (port.PublicAccountGroupRef, bool, error) {
	for _, group := range s.groups {
		if group.ID == groupID {
			return group, true, nil
		}
	}
	return port.PublicAccountGroupRef{}, false, nil
}

func (s *publicAccountStoreFake) ListPublicAccounts(_ context.Context, input port.PublicAccountListInput) (port.PublicAccountListPage, error) {
	items := []port.PublicAccountSummary{}
	for _, account := range s.accounts {
		if account.SystemAccountID == input.SystemAccountID {
			items = append(items, account)
		}
	}
	return port.PublicAccountListPage{Items: items, Page: 1, PageSize: 50, PageUpperBound: len(items), HasMore: false}, nil
}

func (s *publicAccountStoreFake) FindPublicAccountByID(_ context.Context, accountID string) (port.PublicAccountSummary, bool, error) {
	account, ok := s.accounts[accountID]
	return account, ok, nil
}

func (s *publicAccountStoreFake) FindExistingPublicAccountByNameInGroup(_ context.Context, input port.PublicAccountNameLookupInput) (port.PublicAccountSummary, bool, error) {
	for _, account := range s.accounts {
		if account.SystemAccountID == input.SystemAccountID &&
			account.ProviderCode == input.ProviderCode &&
			account.ProviderProtocolProfileID == input.ProviderProtocolProfileID &&
			account.BoundGroupID != nil && *account.BoundGroupID == input.GroupID &&
			strings.EqualFold(account.Name, input.Name) {
			return account, true, nil
		}
	}
	return port.PublicAccountSummary{}, false, nil
}

func (s *publicAccountStoreFake) CreatePublicAccount(_ context.Context, input port.PublicAccountCreateInput) (port.PublicAccountSummary, error) {
	s.createCalls++
	for _, account := range s.accounts {
		if account.SystemAccountID == input.SystemAccountID && strings.EqualFold(account.Name, input.Name) {
			return port.PublicAccountSummary{}, port.ErrPublicAccountDuplicateName
		}
	}
	group, _, _ := s.FindPublicAccountGroupByID(context.Background(), input.GroupID)
	account := port.PublicAccountSummary{
		ID:                        input.ID,
		SystemAccountID:           input.SystemAccountID,
		Name:                      input.Name,
		ProviderCode:              input.ProviderCode,
		ProviderProtocolProfileID: input.ProviderProtocolProfileID,
		ProtocolCode:              input.ProtocolCode,
		ProtocolVersion:           input.ProtocolVersion,
		Type:                      input.Type,
		Status:                    input.Status,
		CredentialsEncrypted:      input.CredentialsEncrypted,
		CredentialFingerprint:     input.CredentialFingerprint,
		CredentialMask:            input.CredentialMask,
		ClientCompatibility:       input.ClientCompatibility,
		SupportedModels:           input.SupportedModels,
		HealthCheckModel:          input.HealthCheckModel,
		HealthCheckEndpointMode:   input.HealthCheckEndpointMode,
		BoundGroupID:              &group.ID,
		BoundGroupName:            &group.Name,
		Schedulable:               input.Schedulable,
		AvailabilityScheduleJSON:  input.AvailabilityScheduleJSON,
		ConcurrencyLimit:          input.ConcurrencyLimit,
		Priority:                  input.Priority,
		Notes:                     input.Notes,
		CreatedAt:                 input.Now,
		UpdatedAt:                 input.Now,
	}
	s.accounts[input.ID] = account
	return account, nil
}

func (s *publicAccountStoreFake) UpdatePublicAccount(_ context.Context, input port.PublicAccountUpdateInput) (port.PublicAccountSummary, bool, error) {
	s.updateCalls++
	s.lastUpdateInput = input
	account, ok := s.accounts[input.ID]
	if !ok {
		return port.PublicAccountSummary{}, false, nil
	}
	account.Name = input.Name
	account.Status = input.Status
	account.CredentialsEncrypted = input.CredentialsEncrypted
	account.CredentialFingerprint = input.CredentialFingerprint
	account.CredentialMask = input.CredentialMask
	if input.SupportedModelsChanged {
		account.SupportedModels = input.SupportedModels
	}
	account.HealthCheckModel = input.HealthCheckModel
	account.HealthCheckEndpointMode = input.HealthCheckEndpointMode
	account.Schedulable = input.Schedulable
	account.AvailabilityScheduleJSON = input.AvailabilityScheduleJSON
	account.ConcurrencyLimit = input.ConcurrencyLimit
	account.Priority = input.Priority
	account.Notes = input.Notes
	account.UpdatedAt = input.Now
	s.accounts[input.ID] = account
	return account, true, nil
}

func (s *publicAccountStoreFake) DeletePublicAccount(_ context.Context, accountID string, systemAccountID string, _ string, _ time.Time) (bool, error) {
	account, ok := s.accounts[accountID]
	if !ok || account.SystemAccountID != systemAccountID {
		return false, nil
	}
	delete(s.accounts, accountID)
	return true, nil
}
