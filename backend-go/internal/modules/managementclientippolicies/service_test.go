package managementclientippolicies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceAllowlistNormalizesInputAndCommitsBeforeInvalidation(t *testing.T) {
	events := []string{}
	now := time.Date(
		2026,
		time.July,
		12,
		16,
		30,
		0,
		123456789,
		time.FixedZone("UTC+8", 8*60*60),
	)
	ipHash := strings.Repeat("A", 64)
	reason := " 可信来源 "
	store := &callbackStore{
		events: &events,
		lock: func(_ context.Context, gotIPHash string) (port.ManagementClientIPRegistryRow, bool, error) {
			if gotIPHash != strings.Repeat("a", 64) {
				t.Fatalf("lock ipHash = %q", gotIPHash)
			}
			return port.ManagementClientIPRegistryRow{
				IPHash:   gotIPHash,
				ClientIP: "203.0.113.8",
			}, true, nil
		},
		disableAll: func(_ context.Context, input port.ManagementClientIPPolicyDisableInput) (int64, error) {
			want := port.ManagementClientIPPolicyDisableInput{
				IPHash:               strings.Repeat("a", 64),
				ActorSystemAccountID: "sys_admin",
				Reason:               "被新的白名单策略替换",
				Now:                  now.UTC().Truncate(time.Millisecond),
			}
			if !reflect.DeepEqual(input, want) {
				t.Fatalf("disable input = %+v, want %+v", input, want)
			}
			return 2, nil
		},
		insertAllowlist: func(_ context.Context, input port.ManagementClientIPAllowlistCreateInput) (port.ManagementClientIPPolicySummary, error) {
			if input.ID != "ip_policy_fixed" ||
				input.IPHash != strings.Repeat("a", 64) ||
				input.ActorSystemAccountID != "sys_admin" ||
				!input.Now.Equal(now.UTC().Truncate(time.Millisecond)) ||
				input.Now.Location() != time.UTC ||
				input.Reason == nil ||
				*input.Reason != "可信来源" {
				t.Fatalf("insert input = %+v", input)
			}
			return port.ManagementClientIPPolicySummary{
				ID:                       input.ID,
				IPHash:                   input.IPHash,
				PolicyType:               port.ManagementClientIPPolicyTypeAllowlist,
				Status:                   port.ManagementClientIPPolicyStatusActive,
				Reason:                   input.Reason,
				CreatedBySystemAccountID: input.ActorSystemAccountID,
				CreatedAt:                input.Now.In(time.FixedZone("UTC-5", -5*60*60)),
				UpdatedAt:                input.Now.In(time.FixedZone("UTC-5", -5*60*60)),
			}, nil
		},
	}
	transactor := &callbackTransactor{events: &events, store: store}
	invalidator := &callbackInvalidator{
		events: &events,
		call: func(context.Context) error {
			return nil
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Transactor:  transactor,
		Invalidator: invalidator,
		Now:         func() time.Time { return now },
		NewID: func(prefix string) string {
			if prefix != "ip_policy" {
				t.Fatalf("NewID prefix = %q", prefix)
			}
			return "ip_policy_fixed"
		},
	})

	result, err := service.Allowlist(context.Background(), AllowlistInput{
		IPHash:               " " + ipHash + "\t",
		ActorSystemAccountID: " sys_admin ",
		Reason:               &reason,
	})
	if err != nil {
		t.Fatalf("Allowlist() error = %v", err)
	}
	if result.ID != "ip_policy_fixed" ||
		result.IPHash != strings.Repeat("a", 64) ||
		result.PolicyType != "allowlist" ||
		result.Status != "active" ||
		result.Reason == nil ||
		*result.Reason != "可信来源" ||
		result.CreatedBySystemAccountID != "sys_admin" ||
		result.CreatedAt != "2026-07-12T08:30:00.123Z" ||
		result.UpdatedAt != "2026-07-12T08:30:00.123Z" ||
		result.ExpiresAt != nil ||
		result.DisabledAt != nil ||
		result.DisabledBySystemAccountID != nil ||
		result.DisabledReason != nil {
		t.Fatalf("Allowlist() result = %+v", result)
	}
	assertEvents(t, events, "tx", "lock", "disable_all", "insert", "commit", "invalidate")

	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, key := range []string{
		"id",
		"ipHash",
		"policyType",
		"status",
		"reason",
		"createdBySystemAccountId",
		"createdAt",
		"updatedAt",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("JSON payload missing %q: %s", key, payload)
		}
	}
	for _, key := range []string{
		"expiresAt",
		"disabledAt",
		"disabledBySystemAccountId",
		"disabledReason",
	} {
		if _, ok := decoded[key]; ok {
			t.Fatalf("JSON payload unexpectedly contains %q: %s", key, payload)
		}
	}
}

func TestServiceAllowlistReturnsTypedValidationErrorWhenRegistryMissing(t *testing.T) {
	events := []string{}
	store := &callbackStore{
		events: &events,
		lock: func(context.Context, string) (port.ManagementClientIPRegistryRow, bool, error) {
			return port.ManagementClientIPRegistryRow{}, false, nil
		},
	}
	invalidator := &callbackInvalidator{events: &events}
	service := NewServiceWithOptions(ServiceOptions{
		Transactor:  &callbackTransactor{events: &events, store: store},
		Invalidator: invalidator,
		NewID:       func(string) string { return "ip_policy_unused" },
	})

	_, err := service.Allowlist(context.Background(), AllowlistInput{
		IPHash:               strings.Repeat("b", 64),
		ActorSystemAccountID: "sys_admin",
	})

	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || validationErr.Error() != "IP 不存在" {
		t.Fatalf("Allowlist() error = %T %v, want typed IP 不存在", err, err)
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidation calls = %d, want 0", invalidator.calls)
	}
	assertEvents(t, events, "tx", "lock", "rollback")
}

func TestServiceUnallowlistLocksBestEffortAndAcceptsMissingRegistryAndZeroRows(t *testing.T) {
	events := []string{}
	now := time.Date(
		2026,
		time.July,
		12,
		1,
		2,
		3,
		4,
		time.FixedZone("UTC-7", -7*60*60),
	)
	store := &callbackStore{
		events: &events,
		lock: func(_ context.Context, gotIPHash string) (port.ManagementClientIPRegistryRow, bool, error) {
			if gotIPHash != strings.Repeat("c", 64) {
				t.Fatalf("lock ipHash = %q", gotIPHash)
			}
			return port.ManagementClientIPRegistryRow{}, false, nil
		},
		disableAllowlist: func(_ context.Context, input port.ManagementClientIPPolicyDisableInput) (int64, error) {
			want := port.ManagementClientIPPolicyDisableInput{
				IPHash:               strings.Repeat("c", 64),
				ActorSystemAccountID: "sys_admin",
				Reason:               "管理员解除策略",
				Now:                  now.UTC().Truncate(time.Millisecond),
			}
			if !reflect.DeepEqual(input, want) {
				t.Fatalf("disable allowlist input = %+v, want %+v", input, want)
			}
			return 0, nil
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Transactor:  &callbackTransactor{events: &events, store: store},
		Invalidator: &callbackInvalidator{events: &events},
		Now:         func() time.Time { return now },
	})

	result, err := service.Unallowlist(context.Background(), UnallowlistInput{
		IPHash:               " " + strings.Repeat("C", 64) + " ",
		ActorSystemAccountID: " sys_admin ",
		Reason:               stringPointer(" \t "),
	})
	if err != nil {
		t.Fatalf("Unallowlist() error = %v", err)
	}
	if result.DisabledCount != 0 {
		t.Fatalf("disabled count = %d, want 0", result.DisabledCount)
	}
	assertEvents(t, events, "tx", "lock", "disable_allowlist", "commit", "invalidate")

	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if string(payload) != `{"disabledCount":0}` {
		t.Fatalf("JSON payload = %s", payload)
	}
}

func TestServiceRejectsInvalidInputWithoutOpeningTransaction(t *testing.T) {
	longReason := strings.Repeat("界", 501)
	tests := []struct {
		name string
		run  func(*Service) error
	}{
		{
			name: "allowlist invalid hash",
			run: func(service *Service) error {
				_, err := service.Allowlist(context.Background(), AllowlistInput{
					IPHash:               strings.Repeat("g", 64),
					ActorSystemAccountID: "sys_admin",
				})
				return err
			},
		},
		{
			name: "allowlist blank actor",
			run: func(service *Service) error {
				_, err := service.Allowlist(context.Background(), AllowlistInput{
					IPHash:               strings.Repeat("a", 64),
					ActorSystemAccountID: " \t ",
				})
				return err
			},
		},
		{
			name: "allowlist reason too long",
			run: func(service *Service) error {
				_, err := service.Allowlist(context.Background(), AllowlistInput{
					IPHash:               strings.Repeat("a", 64),
					ActorSystemAccountID: "sys_admin",
					Reason:               &longReason,
				})
				return err
			},
		},
		{
			name: "unallowlist invalid hash",
			run: func(service *Service) error {
				_, err := service.Unallowlist(context.Background(), UnallowlistInput{
					IPHash:               strings.Repeat("a", 63),
					ActorSystemAccountID: "sys_admin",
				})
				return err
			},
		},
		{
			name: "unallowlist blank actor",
			run: func(service *Service) error {
				_, err := service.Unallowlist(context.Background(), UnallowlistInput{
					IPHash:               strings.Repeat("a", 64),
					ActorSystemAccountID: "",
				})
				return err
			},
		},
		{
			name: "unallowlist reason too long",
			run: func(service *Service) error {
				_, err := service.Unallowlist(context.Background(), UnallowlistInput{
					IPHash:               strings.Repeat("a", 64),
					ActorSystemAccountID: "sys_admin",
					Reason:               &longReason,
				})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transactor := &callbackTransactor{}
			service := NewService(transactor)

			err := test.run(service)

			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %T %v, want ValidationError", err, err)
			}
			if transactor.calls != 0 {
				t.Fatalf("transaction calls = %d, want 0", transactor.calls)
			}
		})
	}
}

func TestServiceUsesECMAScriptTrimSemantics(t *testing.T) {
	const nonECMAScriptWhitespace = "\u0085"
	normalized, err := normalizeMutationInput(
		"\uFEFF"+strings.Repeat("A", 64)+"\uFEFF",
		"\uFEFFsys_admin\uFEFF",
		stringPointer("\uFEFFreason\uFEFF"),
	)
	if err != nil {
		t.Fatalf("normalizeMutationInput() error = %v", err)
	}
	if normalized.ipHash != strings.Repeat("a", 64) ||
		normalized.actorSystemAccountID != "sys_admin" ||
		normalized.reason == nil ||
		*normalized.reason != "reason" {
		t.Fatalf("normalized FEFF input = %+v", normalized)
	}

	normalized, err = normalizeMutationInput(
		strings.Repeat("a", 64),
		nonECMAScriptWhitespace,
		stringPointer(nonECMAScriptWhitespace),
	)
	if err != nil {
		t.Fatalf("normalizeMutationInput() non-ECMAScript error = %v", err)
	}
	if normalized.actorSystemAccountID != nonECMAScriptWhitespace ||
		normalized.reason == nil ||
		*normalized.reason != nonECMAScriptWhitespace {
		t.Fatalf("non-ECMAScript whitespace should remain significant: %+v", normalized)
	}
}

func TestServiceReasonLimitUsesJavaScriptUTF16Length(t *testing.T) {
	bmpReason := strings.Repeat("界", 500)
	normalized, err := normalizeMutationInput(
		strings.Repeat("a", 64),
		"sys_admin",
		&bmpReason,
	)
	if err != nil {
		t.Fatalf("normalizeMutationInput() BMP reason error = %v", err)
	}
	if normalized.reason == nil || *normalized.reason != bmpReason {
		t.Fatalf("BMP reason = %+v", normalized.reason)
	}

	emojiReason := strings.Repeat("🙂", 251)
	_, err = normalizeMutationInput(
		strings.Repeat("a", 64),
		"sys_admin",
		&emojiReason,
	)
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || validationErr.Error() != "原因不能超过 500 个字符" {
		t.Fatalf("emoji reason error = %T %v, want ValidationError", err, err)
	}
}

func TestServiceDoesNotInvalidateWhenCallbackOrCommitFails(t *testing.T) {
	callbackErr := errors.New("callback failed")
	commitErr := errors.New("commit failed")
	tests := []struct {
		name       string
		transactor *callbackTransactor
		run        func(*Service) error
		wantErr    error
	}{
		{
			name: "callback failure",
			transactor: &callbackTransactor{
				store: &callbackStore{
					lock: func(context.Context, string) (port.ManagementClientIPRegistryRow, bool, error) {
						return port.ManagementClientIPRegistryRow{}, false, callbackErr
					},
				},
			},
			run: func(service *Service) error {
				_, err := service.Allowlist(context.Background(), AllowlistInput{
					IPHash:               strings.Repeat("d", 64),
					ActorSystemAccountID: "sys_admin",
				})
				return err
			},
			wantErr: callbackErr,
		},
		{
			name: "commit failure",
			transactor: &callbackTransactor{
				store: &callbackStore{
					lock: func(context.Context, string) (port.ManagementClientIPRegistryRow, bool, error) {
						return port.ManagementClientIPRegistryRow{}, false, nil
					},
					disableAllowlist: func(context.Context, port.ManagementClientIPPolicyDisableInput) (int64, error) {
						return 0, nil
					},
				},
				commitErr: commitErr,
			},
			run: func(service *Service) error {
				_, err := service.Unallowlist(context.Background(), UnallowlistInput{
					IPHash:               strings.Repeat("e", 64),
					ActorSystemAccountID: "sys_admin",
				})
				return err
			},
			wantErr: commitErr,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalidator := &callbackInvalidator{}
			service := NewServiceWithOptions(ServiceOptions{
				Transactor:  test.transactor,
				Invalidator: invalidator,
				NewID:       func(string) string { return "ip_policy_failure" },
			})

			err := test.run(service)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if invalidator.calls != 0 {
				t.Fatalf("invalidation calls = %d, want 0", invalidator.calls)
			}
		})
	}
}

func TestServiceInvalidationIsDetachedBoundedAndBestEffort(t *testing.T) {
	type contextKey string
	const requestKey contextKey = "request"

	var logOutput bytes.Buffer
	events := []string{}
	invalidationErr := errors.New("cache unavailable")
	invalidator := &callbackInvalidator{
		events: &events,
		call: func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Fatalf("invalidation context error = %v", ctx.Err())
			}
			if ctx.Value(requestKey) != "request-1" {
				t.Fatalf("invalidation context value = %v", ctx.Value(requestKey))
			}
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("invalidation context has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining <= 4*time.Second || remaining > 5*time.Second {
				t.Fatalf("invalidation deadline remaining = %v", remaining)
			}
			return invalidationErr
		},
	}
	store := &callbackStore{
		events: &events,
		lock: func(context.Context, string) (port.ManagementClientIPRegistryRow, bool, error) {
			return port.ManagementClientIPRegistryRow{}, false, nil
		},
		disableAllowlist: func(context.Context, port.ManagementClientIPPolicyDisableInput) (int64, error) {
			return 0, nil
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Transactor:  &callbackTransactor{events: &events, store: store},
		Invalidator: invalidator,
		Logger: slog.New(slog.NewJSONHandler(&logOutput, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})),
	})
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), requestKey, "request-1"))
	cancel()

	result, err := service.Unallowlist(parent, UnallowlistInput{
		IPHash:               strings.Repeat("f", 64),
		ActorSystemAccountID: "sys_admin",
	})

	if err != nil {
		t.Fatalf("Unallowlist() error = %v", err)
	}
	if result.DisabledCount != 0 {
		t.Fatalf("disabled count = %d", result.DisabledCount)
	}
	assertEvents(t, events, "tx", "lock", "disable_allowlist", "commit", "invalidate")
	if invalidator.calls != 1 {
		t.Fatalf("invalidation calls = %d, want 1", invalidator.calls)
	}
	logText := logOutput.String()
	if !strings.Contains(logText, "management_client_ip_policy_cache_invalidation_failed") ||
		!strings.Contains(logText, invalidationErr.Error()) {
		t.Fatalf("invalidation failure log = %q", logText)
	}
}

type callbackTransactor struct {
	events    *[]string
	store     port.ManagementClientIPPolicyStore
	commitErr error
	calls     int
}

func (t *callbackTransactor) ManagementClientIPPolicyInTx(
	ctx context.Context,
	fn func(context.Context, port.ManagementClientIPPolicyStore) error,
) error {
	t.calls++
	appendEvent(t.events, "tx")
	if err := fn(ctx, t.store); err != nil {
		appendEvent(t.events, "rollback")
		return err
	}
	appendEvent(t.events, "commit")
	return t.commitErr
}

type callbackStore struct {
	events           *[]string
	lock             func(context.Context, string) (port.ManagementClientIPRegistryRow, bool, error)
	disableAll       func(context.Context, port.ManagementClientIPPolicyDisableInput) (int64, error)
	insertAllowlist  func(context.Context, port.ManagementClientIPAllowlistCreateInput) (port.ManagementClientIPPolicySummary, error)
	insertBlacklist  func(context.Context, port.ManagementClientIPBlacklistCreateInput) (port.ManagementClientIPPolicySummary, error)
	disableAllowlist func(context.Context, port.ManagementClientIPPolicyDisableInput) (int64, error)
	disableBlacklist func(context.Context, port.ManagementClientIPPolicyDisableInput) (int64, error)
}

func (s *callbackStore) LockManagementClientIPRegistry(
	ctx context.Context,
	ipHash string,
) (port.ManagementClientIPRegistryRow, bool, error) {
	appendEvent(s.events, "lock")
	if s.lock == nil {
		panic("unexpected LockManagementClientIPRegistry call")
	}
	return s.lock(ctx, ipHash)
}

func (s *callbackStore) DisableActiveManagementClientIPPolicies(
	ctx context.Context,
	input port.ManagementClientIPPolicyDisableInput,
) (int64, error) {
	appendEvent(s.events, "disable_all")
	if s.disableAll == nil {
		panic("unexpected DisableActiveManagementClientIPPolicies call")
	}
	return s.disableAll(ctx, input)
}

func (s *callbackStore) InsertManagementClientIPAllowlistPolicy(
	ctx context.Context,
	input port.ManagementClientIPAllowlistCreateInput,
) (port.ManagementClientIPPolicySummary, error) {
	appendEvent(s.events, "insert")
	if s.insertAllowlist == nil {
		panic("unexpected InsertManagementClientIPAllowlistPolicy call")
	}
	return s.insertAllowlist(ctx, input)
}

func (s *callbackStore) InsertManagementClientIPBlacklistPolicy(
	ctx context.Context,
	input port.ManagementClientIPBlacklistCreateInput,
) (port.ManagementClientIPPolicySummary, error) {
	appendEvent(s.events, "insert_blacklist")
	if s.insertBlacklist == nil {
		panic("unexpected InsertManagementClientIPBlacklistPolicy call")
	}
	return s.insertBlacklist(ctx, input)
}

func (s *callbackStore) DisableActiveManagementClientIPAllowlistPolicies(
	ctx context.Context,
	input port.ManagementClientIPPolicyDisableInput,
) (int64, error) {
	appendEvent(s.events, "disable_allowlist")
	if s.disableAllowlist == nil {
		panic("unexpected DisableActiveManagementClientIPAllowlistPolicies call")
	}
	return s.disableAllowlist(ctx, input)
}

func (s *callbackStore) DisableActiveManagementClientIPBlacklistPolicies(
	ctx context.Context,
	input port.ManagementClientIPPolicyDisableInput,
) (int64, error) {
	appendEvent(s.events, "disable_blacklist")
	if s.disableBlacklist == nil {
		panic("unexpected DisableActiveManagementClientIPBlacklistPolicies call")
	}
	return s.disableBlacklist(ctx, input)
}

type callbackInvalidator struct {
	events *[]string
	call   func(context.Context) error
	calls  int
}

func (i *callbackInvalidator) InvalidateClientIPPolicyCache(ctx context.Context) error {
	i.calls++
	appendEvent(i.events, "invalidate")
	if i.call == nil {
		return nil
	}
	return i.call(ctx)
}

func assertEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func appendEvent(events *[]string, event string) {
	if events != nil {
		*events = append(*events, event)
	}
}

func stringPointer(value string) *string {
	return &value
}
