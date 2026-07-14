package managementapikeys

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceDeleteScopesOwnerAndReturnsCommittedAuditFields(t *testing.T) {
	deletedAt := time.Date(2026, 7, 12, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name      string
		input     DeleteInput
		wantOwner string
	}{
		{
			name: "admin global when owner omitted",
			input: DeleteInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
			},
		},
		{
			name: "admin global when owner all",
			input: DeleteInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "super_admin",
				SystemAccountID:      " all ",
			},
		},
		{
			name: "admin explicit owner narrows",
			input: DeleteInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				SystemAccountID:      " sys_target ",
			},
			wantOwner: "sys_target",
		},
		{
			name: "self only forces actor",
			input: DeleteInput{
				ActorSystemAccountID: " sys_self ",
				ActorRole:            "admin",
				SystemAccountID:      "sys_forged",
				SelfOnly:             true,
			},
			wantOwner: "sys_self",
		},
		{
			name: "non admin forces actor",
			input: DeleteInput{
				ActorSystemAccountID: "sys_user",
				ActorRole:            "user",
				SystemAccountID:      "sys_forged",
			},
			wantOwner: "sys_user",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			store := &managementAPIKeyDeleteStoreStub{
				events: &events,
				result: port.ManagementAPIKeyDeleteResult{
					APIKeyID:             "key_1",
					Name:                 "生产 Key",
					OwnerSystemAccountID: "sys_owner",
				},
			}
			invalidator := &managementAPIKeyDeleteInvalidatorStub{events: &events}
			service := NewServiceWithOptions(ServiceOptions{
				Deleter:     store,
				Invalidator: invalidator,
				Now:         func() time.Time { return deletedAt },
			})
			input := test.input
			input.APIKeyID = " key_1 "

			result, err := service.Delete(context.Background(), input)
			if err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
			if store.calls != 1 {
				t.Fatalf("delete calls = %d, want 1", store.calls)
			}
			if store.input.APIKeyID != "key_1" ||
				store.input.OwnerSystemAccountID != test.wantOwner ||
				!store.input.DeletedAt.Equal(deletedAt) {
				t.Fatalf("delete input = %+v", store.input)
			}
			if !result.Committed ||
				result.APIKeyID != "key_1" ||
				result.Name != "生产 Key" ||
				result.OwnerSystemAccountID != "sys_owner" {
				t.Fatalf("delete result = %+v", result)
			}
			if got, want := events, []string{"delete", "validation", "lookup", "runtime", "quota"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("events = %v, want %v", got, want)
			}
			if invalidator.lookupAPIKeyID != "key_1" ||
				invalidator.lookupReason != apiKeyDeletedReason ||
				invalidator.runtimeReason != apiKeyDeletedReason ||
				invalidator.quotaAPIKeyID != "key_1" ||
				invalidator.quotaReason != apiKeyDeletedReason {
				t.Fatalf("invalidator = %+v", invalidator)
			}
		})
	}
}

func TestServiceDeleteRejectsTrimmedEmptyInputAsTypedInvalid(t *testing.T) {
	tests := []DeleteInput{
		{APIKeyID: "key_1"},
		{ActorSystemAccountID: "   ", APIKeyID: "key_1"},
		{ActorSystemAccountID: "sys_actor"},
		{ActorSystemAccountID: "sys_actor", APIKeyID: "   "},
	}
	for _, input := range tests {
		store := &managementAPIKeyDeleteStoreStub{}
		service := NewServiceWithOptions(ServiceOptions{
			Deleter:     store,
			Invalidator: &managementAPIKeyDeleteInvalidatorStub{},
		})

		_, err := service.Delete(context.Background(), input)
		if !errors.Is(err, ErrAPIKeyDeleteInvalid) {
			t.Fatalf("Delete(%+v) error = %v, want %v", input, err, ErrAPIKeyDeleteInvalid)
		}
		if store.calls != 0 {
			t.Fatalf("Delete(%+v) calls = %d, want 0", input, store.calls)
		}
	}
}

func TestServiceDeleteMapsStoreErrors(t *testing.T) {
	tests := []struct {
		name     string
		storeErr error
		wantErr  error
		wantText string
	}{
		{
			name:     "not found",
			storeErr: port.ErrManagementAPIKeyNotFound,
			wantErr:  ErrAPIKeyNotFound,
		},
		{
			name:     "default",
			storeErr: port.ErrManagementAPIKeyDefaultDelete,
			wantErr:  ErrAPIKeyDefaultDelete,
			wantText: "默认 API Key 不允许删除",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementAPIKeyDeleteStoreStub{err: test.storeErr}
			service := NewServiceWithOptions(ServiceOptions{
				Deleter:     store,
				Invalidator: &managementAPIKeyDeleteInvalidatorStub{},
			})

			result, err := service.Delete(context.Background(), DeleteInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				APIKeyID:             "key_1",
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Delete() error = %v, want %v", err, test.wantErr)
			}
			if test.wantText != "" && err.Error() != test.wantText {
				t.Fatalf("Delete() error text = %q, want %q", err.Error(), test.wantText)
			}
			if result.Committed {
				t.Fatalf("Delete() result = %+v, want uncommitted", result)
			}
		})
	}
}

func TestServiceDeleteRequiresOnlyDeleterAndInvalidator(t *testing.T) {
	t.Run("missing deleter", func(t *testing.T) {
		service := NewServiceWithOptions(ServiceOptions{
			Invalidator: &managementAPIKeyDeleteInvalidatorStub{},
		})
		_, err := service.Delete(context.Background(), DeleteInput{
			ActorSystemAccountID: "sys_actor",
			APIKeyID:             "key_1",
		})
		if err == nil || !strings.Contains(err.Error(), "deleter is required") {
			t.Fatalf("Delete() error = %v, want internal missing deleter error", err)
		}
	})

	t.Run("missing invalidator", func(t *testing.T) {
		store := &managementAPIKeyDeleteStoreStub{}
		service := NewServiceWithOptions(ServiceOptions{Deleter: store})
		_, err := service.Delete(context.Background(), DeleteInput{
			ActorSystemAccountID: "sys_actor",
			APIKeyID:             "key_1",
		})
		if err == nil || !strings.Contains(err.Error(), "cache invalidator is required") {
			t.Fatalf("Delete() error = %v, want internal missing invalidator error", err)
		}
		if store.calls != 0 {
			t.Fatalf("delete calls = %d, want 0 when dependency is missing", store.calls)
		}
	})

	t.Run("no list or usage reader", func(t *testing.T) {
		store := &managementAPIKeyDeleteStoreStub{
			result: port.ManagementAPIKeyDeleteResult{
				APIKeyID:             "key_1",
				Name:                 "生产 Key",
				OwnerSystemAccountID: "sys_owner",
			},
		}
		service := NewServiceWithOptions(ServiceOptions{
			Deleter:     store,
			Invalidator: &managementAPIKeyDeleteInvalidatorStub{},
		})
		result, err := service.Delete(context.Background(), DeleteInput{
			ActorSystemAccountID: "sys_actor",
			APIKeyID:             "key_1",
		})
		if err != nil || !result.Committed {
			t.Fatalf("Delete() result=%+v error=%v", result, err)
		}
	})
}

func TestNewServiceWiresManagementAPIKeyDeleter(t *testing.T) {
	store := &managementAPIKeyDeleteCombinedStoreStub{
		managementAPIKeyDeleteStoreStub: managementAPIKeyDeleteStoreStub{
			result: port.ManagementAPIKeyDeleteResult{
				APIKeyID:             "key_1",
				Name:                 "生产 Key",
				OwnerSystemAccountID: "sys_owner",
			},
		},
	}
	service := NewService(store)
	service.invalidator = &managementAPIKeyDeleteInvalidatorStub{}

	result, err := service.Delete(context.Background(), DeleteInput{
		ActorSystemAccountID: "sys_actor",
		APIKeyID:             "key_1",
	})
	if err != nil || !result.Committed || store.calls != 1 {
		t.Fatalf("Delete() result=%+v error=%v calls=%d", result, err, store.calls)
	}
}

func TestServiceDeleteUsesDetachedBoundedInvalidationContextInOrder(t *testing.T) {
	events := []string{}
	ctx, cancel := context.WithCancel(context.Background())
	store := &managementAPIKeyDeleteStoreStub{
		events: &events,
		result: port.ManagementAPIKeyDeleteResult{
			APIKeyID:             "key_1",
			Name:                 "生产 Key",
			OwnerSystemAccountID: "sys_owner",
		},
		afterDelete: cancel,
	}
	invalidator := &managementAPIKeyDeleteInvalidatorStub{events: &events}
	service := NewServiceWithOptions(ServiceOptions{
		Deleter:     store,
		Invalidator: invalidator,
	})

	startedAt := time.Now()
	result, err := service.Delete(ctx, DeleteInput{
		ActorSystemAccountID: "sys_actor",
		APIKeyID:             "key_1",
	})
	if err != nil || !result.Committed {
		t.Fatalf("Delete() result=%+v error=%v", result, err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("request context error = %v, want canceled", ctx.Err())
	}
	if got, want := events, []string{"delete", "validation", "lookup", "runtime", "quota"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for name, snapshot := range map[string]managementAPIKeyDeleteContextSnapshot{
		"validation": invalidator.validationContext,
		"lookup":     invalidator.lookupContext,
		"runtime":    invalidator.runtimeContext,
		"quota":      invalidator.quotaContext,
	} {
		if snapshot.err != nil ||
			!snapshot.hasDeadline ||
			!snapshot.deadline.After(startedAt) ||
			snapshot.deadline.After(startedAt.Add(5*time.Second+250*time.Millisecond)) {
			t.Fatalf("%s context = %+v", name, snapshot)
		}
	}
}

func TestServiceDeleteValidationFailureReturnsCommittedResultAndStops(t *testing.T) {
	events := []string{}
	validationCause := errors.New("redis unavailable")
	store := &managementAPIKeyDeleteStoreStub{
		events: &events,
		result: port.ManagementAPIKeyDeleteResult{
			APIKeyID:             "key_1",
			Name:                 "生产 Key",
			OwnerSystemAccountID: "sys_owner",
		},
	}
	invalidator := &managementAPIKeyDeleteInvalidatorStub{
		events:        &events,
		validationErr: validationCause,
	}
	service := NewServiceWithOptions(ServiceOptions{
		Deleter:     store,
		Invalidator: invalidator,
	})

	result, err := service.Delete(context.Background(), DeleteInput{
		ActorSystemAccountID: "sys_actor",
		APIKeyID:             "key_1",
	})
	if !errors.Is(err, ErrAPIKeyDeleteValidationCacheInvalidation) {
		t.Fatalf("Delete() error = %v, want %v", err, ErrAPIKeyDeleteValidationCacheInvalidation)
	}
	if errors.Is(err, validationCause) ||
		err.Error() != ErrAPIKeyDeleteValidationCacheInvalidation.Error() {
		t.Fatalf("Delete() leaked validation cause: %v", err)
	}
	if !result.Committed ||
		result.APIKeyID != "key_1" ||
		result.Name != "生产 Key" ||
		result.OwnerSystemAccountID != "sys_owner" {
		t.Fatalf("Delete() result = %+v", result)
	}
	if got, want := events, []string{"delete", "validation"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if invalidator.calls != 1 {
		t.Fatalf("invalidation calls = %d, want 1", invalidator.calls)
	}
}

func TestServiceDeleteLookupRuntimeAndQuotaInvalidationAreBestEffort(t *testing.T) {
	events := []string{}
	store := &managementAPIKeyDeleteStoreStub{
		events: &events,
		result: port.ManagementAPIKeyDeleteResult{
			APIKeyID:             "key_1",
			Name:                 "生产 Key",
			OwnerSystemAccountID: "sys_owner",
		},
	}
	invalidator := &managementAPIKeyDeleteInvalidatorStub{
		events:     &events,
		lookupErr:  errors.New("lookup unavailable"),
		runtimeErr: errors.New("runtime unavailable"),
		quotaErr:   errors.New("quota unavailable"),
	}
	service := NewServiceWithOptions(ServiceOptions{
		Deleter:     store,
		Invalidator: invalidator,
	})

	result, err := service.Delete(context.Background(), DeleteInput{
		ActorSystemAccountID: "sys_actor",
		APIKeyID:             "key_1",
	})
	if err != nil || !result.Committed || invalidator.calls != 4 {
		t.Fatalf("Delete() result=%+v error=%v invalidator=%+v", result, err, invalidator)
	}
	if got, want := events, []string{"delete", "validation", "lookup", "runtime", "quota"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

type managementAPIKeyDeleteStoreStub struct {
	input       port.ManagementAPIKeyDeleteInput
	result      port.ManagementAPIKeyDeleteResult
	err         error
	events      *[]string
	afterDelete func()
	calls       int
}

func (s *managementAPIKeyDeleteStoreStub) DeleteManagementAPIKey(
	_ context.Context,
	input port.ManagementAPIKeyDeleteInput,
) (port.ManagementAPIKeyDeleteResult, error) {
	s.calls++
	s.input = input
	if s.events != nil {
		*s.events = append(*s.events, "delete")
	}
	if s.afterDelete != nil {
		s.afterDelete()
	}
	return s.result, s.err
}

type managementAPIKeyDeleteCombinedStoreStub struct {
	managementAPIKeyDeleteStoreStub
}

func (s *managementAPIKeyDeleteCombinedStoreStub) ListManagementAPIKeys(
	context.Context,
	port.ManagementAPIKeyListInput,
) (port.ManagementAPIKeyListPage, error) {
	return port.ManagementAPIKeyListPage{}, nil
}

func (s *managementAPIKeyDeleteCombinedStoreStub) ListManagementAPIKeyUsageTotals(
	context.Context,
	[]port.ManagementAPIKeyUsageScope,
) ([]port.ManagementAPIKeyUsageRow, error) {
	t := "Delete must not read usage"
	return nil, errors.New(t)
}

type managementAPIKeyDeleteContextSnapshot struct {
	err         error
	hasDeadline bool
	deadline    time.Time
}

type managementAPIKeyDeleteInvalidatorStub struct {
	events            *[]string
	validationErr     error
	lookupErr         error
	runtimeErr        error
	quotaErr          error
	calls             int
	lookupAPIKeyID    string
	lookupReason      string
	runtimeReason     string
	quotaAPIKeyID     string
	quotaReason       string
	validationContext managementAPIKeyDeleteContextSnapshot
	lookupContext     managementAPIKeyDeleteContextSnapshot
	runtimeContext    managementAPIKeyDeleteContextSnapshot
	quotaContext      managementAPIKeyDeleteContextSnapshot
}

func (s *managementAPIKeyDeleteInvalidatorStub) InvalidateAPIKeyValidationCache(
	ctx context.Context,
) error {
	s.calls++
	s.validationContext = managementAPIKeyDeleteSnapshot(ctx)
	s.record("validation")
	return s.validationErr
}

func (s *managementAPIKeyDeleteInvalidatorStub) InvalidateAPIKeyLookupCache(
	ctx context.Context,
	apiKeyID string,
	reason string,
) error {
	s.calls++
	s.lookupAPIKeyID = apiKeyID
	s.lookupReason = reason
	s.lookupContext = managementAPIKeyDeleteSnapshot(ctx)
	s.record("lookup")
	return s.lookupErr
}

func (s *managementAPIKeyDeleteInvalidatorStub) InvalidateGatewayRuntime(
	ctx context.Context,
	reason string,
) error {
	s.calls++
	s.runtimeReason = reason
	s.runtimeContext = managementAPIKeyDeleteSnapshot(ctx)
	s.record("runtime")
	return s.runtimeErr
}

func (s *managementAPIKeyDeleteInvalidatorStub) InvalidateAPIKeyQuotaChanged(
	ctx context.Context,
	apiKeyID string,
	reason string,
) error {
	s.calls++
	s.quotaAPIKeyID = apiKeyID
	s.quotaReason = reason
	s.quotaContext = managementAPIKeyDeleteSnapshot(ctx)
	s.record("quota")
	return s.quotaErr
}

func (s *managementAPIKeyDeleteInvalidatorStub) record(event string) {
	if s.events != nil {
		*s.events = append(*s.events, event)
	}
}

func managementAPIKeyDeleteSnapshot(ctx context.Context) managementAPIKeyDeleteContextSnapshot {
	deadline, hasDeadline := ctx.Deadline()
	return managementAPIKeyDeleteContextSnapshot{
		err:         ctx.Err(),
		hasDeadline: hasDeadline,
		deadline:    deadline,
	}
}

var _ port.ManagementAPIKeyListReader = (*managementAPIKeyDeleteCombinedStoreStub)(nil)
