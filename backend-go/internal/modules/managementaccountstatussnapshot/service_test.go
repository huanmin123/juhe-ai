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

type statusReaderStub struct {
	input port.ManagementAccountStatusSnapshotInput
	rows  []port.ManagementAccountStatusProjection
}

func (s *statusReaderStub) ListManagementAccountStatusProjections(_ context.Context, input port.ManagementAccountStatusSnapshotInput) ([]port.ManagementAccountStatusProjection, error) {
	s.input = input
	return s.rows, nil
}
