package managementaccountstatussnapshot

import (
	"context"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestParseAccountIDsBoundsAndOrder(t *testing.T) {
	got, err := ParseAccountIDs(" account_b, account_a,account_b ")
	if err != nil || len(got) != 2 || got[0] != "account_b" || got[1] != "account_a" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if _, err := ParseAccountIDs(""); err != ErrInvalidAccountIDs {
		t.Fatalf("empty err=%v", err)
	}
	if _, err := ParseAccountIDs("a," + strings.Repeat("x", MaxQueryLength)); err != ErrQueryTooLong {
		t.Fatalf("long err=%v", err)
	}
}

func TestServiceSelfScopeAndEffectiveAvailability(t *testing.T) {
	reader := &statusReaderStub{rows: []port.ManagementAccountStatusProjection{{ID: "a1", SystemAccountID: "u1", Name: "A", Status: "active", Schedulable: true}}}
	s := NewService(reader)
	s.now = func() time.Time { return time.Unix(0, 0) }
	result, err := s.Get(context.Background(), Input{ActorSystemAccountID: "u1", ActorRole: "user", SelfOnly: true, AccountIDs: []string{"a1"}})
	if err != nil || reader.input.SystemAccountID != "u1" || !result.Items[0].EffectiveAvailability.Available {
		t.Fatalf("result=%+v err=%v input=%+v", result, err, reader.input)
	}
}

func TestServiceLoadsCurrentConcurrencyWhenReaderIsAvailable(t *testing.T) {
	reader := &statusReaderStub{rows: []port.ManagementAccountStatusProjection{{ID: "a1", SystemAccountID: "u1", Name: "A", Status: "active", Schedulable: true}}}
	concurrency := &statusConcurrencyReaderStub{values: map[string]int{"a1": 3}}
	s := NewServiceWithOptions(ServiceOptions{Reader: reader, AccountConcurrency: concurrency})

	result, err := s.Get(context.Background(), Input{ActorSystemAccountID: "u1", ActorRole: "user", SelfOnly: true, AccountIDs: []string{"a1"}})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !result.RuntimeSnapshot.AccountConcurrencyAvailable || result.Items[0].CurrentConcurrency != 3 || len(concurrency.ids) != 1 || concurrency.ids[0] != "a1" {
		t.Fatalf("runtime=%+v item=%+v ids=%v", result.RuntimeSnapshot, result.Items[0], concurrency.ids)
	}
}

type statusReaderStub struct {
	input port.ManagementAccountStatusSnapshotInput
	rows  []port.ManagementAccountStatusProjection
}

type statusConcurrencyReaderStub struct {
	ids    []string
	values map[string]int
}

func (s *statusConcurrencyReaderStub) LoadAccountCurrentConcurrencyByIDs(_ context.Context, ids []string, _ time.Time) (map[string]int, error) {
	s.ids = append([]string(nil), ids...)
	return s.values, nil
}

func (s *statusReaderStub) ListManagementAccountStatusProjections(_ context.Context, input port.ManagementAccountStatusSnapshotInput) ([]port.ManagementAccountStatusProjection, error) {
	s.input = input
	return s.rows, nil
}
