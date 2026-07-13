package managementclientippolicies

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceBlacklistLocksReplacesAndReturnsNodeMillisecondExpiry(t *testing.T) {
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
	durationMinutes := 90
	reason := " 可疑来源 "
	wantIPHash := strings.Repeat("a", 64)
	wantExpiresAt := time.Date(2026, time.July, 12, 10, 0, 0, 123000000, time.UTC)
	store := &callbackStore{
		events: &events,
		lock: func(_ context.Context, gotIPHash string) (port.ManagementClientIPRegistryRow, bool, error) {
			if gotIPHash != wantIPHash {
				t.Fatalf("lock ipHash = %q, want %q", gotIPHash, wantIPHash)
			}
			return port.ManagementClientIPRegistryRow{
				IPHash:   gotIPHash,
				ClientIP: "203.0.113.8",
			}, true, nil
		},
		disableAll: func(_ context.Context, input port.ManagementClientIPPolicyDisableInput) (int64, error) {
			want := port.ManagementClientIPPolicyDisableInput{
				IPHash:               wantIPHash,
				ActorSystemAccountID: "sys_admin",
				Reason:               "被新的封禁策略替换",
				Now:                  now.UTC().Truncate(time.Millisecond),
			}
			if !reflect.DeepEqual(input, want) {
				t.Fatalf("disable input = %+v, want %+v", input, want)
			}
			return 2, nil
		},
		insertBlacklist: func(_ context.Context, input port.ManagementClientIPBlacklistCreateInput) (port.ManagementClientIPPolicySummary, error) {
			if input.ID != "ip_policy_blacklist" ||
				input.IPHash != wantIPHash ||
				input.ActorSystemAccountID != "sys_admin" ||
				!input.Now.Equal(now.UTC().Truncate(time.Millisecond)) ||
				input.Now.Location() != time.UTC ||
				input.Reason == nil ||
				*input.Reason != "可疑来源" ||
				input.ExpiresAt == nil ||
				!input.ExpiresAt.Equal(wantExpiresAt) ||
				input.ExpiresAt.Location() != time.UTC {
				t.Fatalf("insert blacklist input = %+v", input)
			}
			return port.ManagementClientIPPolicySummary{
				ID:                       input.ID,
				IPHash:                   input.IPHash,
				PolicyType:               port.ManagementClientIPPolicyTypeBlacklist,
				Status:                   port.ManagementClientIPPolicyStatusActive,
				Reason:                   input.Reason,
				ExpiresAt:                input.ExpiresAt,
				CreatedBySystemAccountID: input.ActorSystemAccountID,
				CreatedAt:                input.Now,
				UpdatedAt:                input.Now,
			}, nil
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Transactor:  &callbackTransactor{events: &events, store: store},
		Invalidator: &callbackInvalidator{events: &events},
		Now:         func() time.Time { return now },
		NewID:       func(string) string { return "ip_policy_blacklist" },
	})

	result, err := service.Blacklist(context.Background(), BlacklistInput{
		IPHash:               " " + strings.Repeat("A", 64) + " ",
		ActorSystemAccountID: " sys_admin ",
		Reason:               &reason,
		DurationMinutes:      &durationMinutes,
	})
	if err != nil {
		t.Fatalf("Blacklist() error = %v", err)
	}
	if result.ID != "ip_policy_blacklist" ||
		result.PolicyType != "blacklist" ||
		result.Status != "active" ||
		result.ExpiresAt == nil ||
		*result.ExpiresAt != "2026-07-12T10:00:00.123Z" ||
		result.CreatedAt != "2026-07-12T08:30:00.123Z" ||
		result.UpdatedAt != "2026-07-12T08:30:00.123Z" {
		t.Fatalf("Blacklist() result = %+v", result)
	}
	assertEvents(
		t,
		events,
		"tx",
		"lock",
		"disable_all",
		"insert_blacklist",
		"commit",
		"invalidate",
	)
}

func TestBlacklistDurationValidBoundariesUseFixed24HourDays(t *testing.T) {
	tests := []struct {
		name            string
		durationMinutes *int
		durationDays    *int
		want            *time.Duration
	}{
		{name: "permanent"},
		{
			name:            "minimum minutes",
			durationMinutes: blacklistIntPointer(1),
			want:            blacklistDurationPointer(time.Minute),
		},
		{
			name:            "maximum minutes",
			durationMinutes: blacklistIntPointer(525600),
			want:            blacklistDurationPointer(525600 * time.Minute),
		},
		{
			name:         "minimum days",
			durationDays: blacklistIntPointer(1),
			want:         blacklistDurationPointer(24 * time.Hour),
		},
		{
			name:         "maximum days",
			durationDays: blacklistIntPointer(3650),
			want:         blacklistDurationPointer(3650 * 24 * time.Hour),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := blacklistDuration(test.durationMinutes, test.durationDays)
			if err != nil {
				t.Fatalf("blacklistDuration() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("blacklistDuration() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestServiceBlacklistRejectsInvalidDurationWithoutOpeningTransaction(t *testing.T) {
	tests := []struct {
		name            string
		durationMinutes *int
		durationDays    *int
		wantMessage     string
	}{
		{
			name:            "minutes below minimum",
			durationMinutes: blacklistIntPointer(0),
			wantMessage:     "封禁分钟数不能小于 1",
		},
		{
			name:            "minutes above maximum",
			durationMinutes: blacklistIntPointer(525601),
			wantMessage:     "封禁分钟数不能超过 525600",
		},
		{
			name:         "days below minimum",
			durationDays: blacklistIntPointer(0),
			wantMessage:  "封禁天数不能小于 1",
		},
		{
			name:         "days above maximum",
			durationDays: blacklistIntPointer(3651),
			wantMessage:  "封禁天数不能超过 3650",
		},
		{
			name:            "mutually exclusive",
			durationMinutes: blacklistIntPointer(1),
			durationDays:    blacklistIntPointer(1),
			wantMessage:     "封禁时长只能选择一种",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transactor := &callbackTransactor{}
			service := NewService(transactor)

			_, err := service.Blacklist(context.Background(), BlacklistInput{
				IPHash:               strings.Repeat("a", 64),
				ActorSystemAccountID: "sys_admin",
				DurationMinutes:      test.durationMinutes,
				DurationDays:         test.durationDays,
			})

			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || validationErr.Error() != test.wantMessage {
				t.Fatalf("Blacklist() error = %T %v, want %q", err, err, test.wantMessage)
			}
			if transactor.calls != 0 {
				t.Fatalf("transaction calls = %d, want 0", transactor.calls)
			}
		})
	}
}

func TestServiceBlacklistMissingRegistryRollsBackWithoutInvalidation(t *testing.T) {
	events := []string{}
	invalidator := &callbackInvalidator{events: &events}
	service := NewServiceWithOptions(ServiceOptions{
		Transactor: &callbackTransactor{
			events: &events,
			store: &callbackStore{
				events: &events,
				lock: func(context.Context, string) (port.ManagementClientIPRegistryRow, bool, error) {
					return port.ManagementClientIPRegistryRow{}, false, nil
				},
			},
		},
		Invalidator: invalidator,
		NewID:       func(string) string { return "ip_policy_unused" },
	})

	_, err := service.Blacklist(context.Background(), BlacklistInput{
		IPHash:               strings.Repeat("b", 64),
		ActorSystemAccountID: "sys_admin",
	})

	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || validationErr.Error() != "IP 不存在" {
		t.Fatalf("Blacklist() error = %T %v, want typed IP 不存在", err, err)
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidation calls = %d, want 0", invalidator.calls)
	}
	assertEvents(t, events, "tx", "lock", "rollback")
}

func TestServiceUnblockAcceptsMissingRegistryAndZeroRowsThenInvalidates(t *testing.T) {
	events := []string{}
	now := time.Date(2026, time.July, 12, 1, 2, 3, 4, time.FixedZone("UTC-7", -7*60*60))
	wantIPHash := strings.Repeat("c", 64)
	store := &callbackStore{
		events: &events,
		lock: func(_ context.Context, gotIPHash string) (port.ManagementClientIPRegistryRow, bool, error) {
			if gotIPHash != wantIPHash {
				t.Fatalf("lock ipHash = %q, want %q", gotIPHash, wantIPHash)
			}
			return port.ManagementClientIPRegistryRow{}, false, nil
		},
		disableBlacklist: func(_ context.Context, input port.ManagementClientIPPolicyDisableInput) (int64, error) {
			want := port.ManagementClientIPPolicyDisableInput{
				IPHash:               wantIPHash,
				ActorSystemAccountID: "sys_admin",
				Reason:               "管理员解除策略",
				Now:                  now.UTC().Truncate(time.Millisecond),
			}
			if !reflect.DeepEqual(input, want) {
				t.Fatalf("disable blacklist input = %+v, want %+v", input, want)
			}
			return 0, nil
		},
	}
	invalidator := &callbackInvalidator{events: &events}
	service := NewServiceWithOptions(ServiceOptions{
		Transactor:  &callbackTransactor{events: &events, store: store},
		Invalidator: invalidator,
		Now:         func() time.Time { return now },
	})

	result, err := service.Unblock(context.Background(), UnblockInput{
		IPHash:               " " + strings.Repeat("C", 64) + " ",
		ActorSystemAccountID: " sys_admin ",
		Reason:               stringPointer(" \t "),
	})
	if err != nil {
		t.Fatalf("Unblock() error = %v", err)
	}
	if result.DisabledCount != 0 {
		t.Fatalf("disabled count = %d, want 0", result.DisabledCount)
	}
	if invalidator.calls != 1 {
		t.Fatalf("invalidation calls = %d, want 1", invalidator.calls)
	}
	assertEvents(t, events, "tx", "lock", "disable_blacklist", "commit", "invalidate")
}

func TestServiceBlacklistAndUnblockDoNotInvalidateFailedTransactions(t *testing.T) {
	mutationErr := errors.New("policy mutation failed")
	tests := []struct {
		name      string
		store     *callbackStore
		commitErr error
		run       func(*Service) error
	}{
		{
			name: "blacklist insert failure",
			store: &callbackStore{
				lock: func(context.Context, string) (port.ManagementClientIPRegistryRow, bool, error) {
					return port.ManagementClientIPRegistryRow{}, true, nil
				},
				disableAll: func(context.Context, port.ManagementClientIPPolicyDisableInput) (int64, error) {
					return 1, nil
				},
				insertBlacklist: func(context.Context, port.ManagementClientIPBlacklistCreateInput) (port.ManagementClientIPPolicySummary, error) {
					return port.ManagementClientIPPolicySummary{}, mutationErr
				},
			},
			run: func(service *Service) error {
				_, err := service.Blacklist(context.Background(), BlacklistInput{
					IPHash:               strings.Repeat("d", 64),
					ActorSystemAccountID: "sys_admin",
				})
				return err
			},
		},
		{
			name: "unblock update failure",
			store: &callbackStore{
				lock: func(context.Context, string) (port.ManagementClientIPRegistryRow, bool, error) {
					return port.ManagementClientIPRegistryRow{}, false, nil
				},
				disableBlacklist: func(context.Context, port.ManagementClientIPPolicyDisableInput) (int64, error) {
					return 0, mutationErr
				},
			},
			run: func(service *Service) error {
				_, err := service.Unblock(context.Background(), UnblockInput{
					IPHash:               strings.Repeat("e", 64),
					ActorSystemAccountID: "sys_admin",
				})
				return err
			},
		},
		{
			name: "unblock commit failure",
			store: &callbackStore{
				lock: func(context.Context, string) (port.ManagementClientIPRegistryRow, bool, error) {
					return port.ManagementClientIPRegistryRow{}, false, nil
				},
				disableBlacklist: func(context.Context, port.ManagementClientIPPolicyDisableInput) (int64, error) {
					return 0, nil
				},
			},
			commitErr: mutationErr,
			run: func(service *Service) error {
				_, err := service.Unblock(context.Background(), UnblockInput{
					IPHash:               strings.Repeat("f", 64),
					ActorSystemAccountID: "sys_admin",
				})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalidator := &callbackInvalidator{}
			service := NewServiceWithOptions(ServiceOptions{
				Transactor: &callbackTransactor{
					store:     test.store,
					commitErr: test.commitErr,
				},
				Invalidator: invalidator,
				NewID:       func(string) string { return "ip_policy_failure" },
			})

			err := test.run(service)

			if !errors.Is(err, mutationErr) {
				t.Fatalf("error = %v, want %v", err, mutationErr)
			}
			if invalidator.calls != 0 {
				t.Fatalf("invalidation calls = %d, want 0", invalidator.calls)
			}
		})
	}
}

func blacklistIntPointer(value int) *int {
	return &value
}

func blacklistDurationPointer(value time.Duration) *time.Duration {
	return &value
}
