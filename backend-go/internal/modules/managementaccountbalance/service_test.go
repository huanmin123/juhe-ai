package managementaccountbalance

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceGetUsesScopeAndDecodesSnapshot(t *testing.T) {
	reader := &balanceReaderStub{snapshot: port.ManagementAccountBalanceSnapshot{
		AccountID: "account-1", SystemAccountID: "system-1", Status: "fresh",
		SnapshotJSON: `{"status":"fresh","balance":"12.50","currency":"USD"}`,
	}}
	service := NewService(ServiceOptions{Reader: reader})

	got, found, err := service.Get(context.Background(), Input{AccountID: " account-1 ", SystemAccountID: " system-1 "})
	if err != nil || !found || got.Balance != "12.50" || reader.input != (port.ManagementAccountBalanceInput{AccountID: "account-1", SystemAccountID: "system-1"}) {
		t.Fatalf("Get() = %#v, found=%v, err=%v, input=%#v", got, found, err, reader.input)
	}
}

func TestServiceRefreshWritesTimestampedSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	writer := &balanceWriterStub{}
	service := NewService(ServiceOptions{
		Reader: &balanceReaderStub{candidate: port.ManagementAccountBalanceCandidate{AccountID: "account-1", SystemAccountID: "system-1", Type: "api_key"}},
		Writer: writer,
		Now:    func() time.Time { return now },
		Query: func(_ context.Context, candidate port.ManagementAccountBalanceCandidate) (Snapshot, error) {
			if candidate.AccountID != "account-1" {
				t.Fatalf("candidate = %#v", candidate)
			}
			return Snapshot{Status: "fresh", Balance: "9.99"}, nil
		},
	})

	got, found, err := service.Refresh(context.Background(), Input{AccountID: "account-1"})
	if err != nil || !found || got.LastAttemptAt != now.Format(time.RFC3339Nano) || got.LastSuccessAt != got.LastAttemptAt {
		t.Fatalf("Refresh() = %#v, found=%v, err=%v", got, found, err)
	}
	if writer.snapshot.Status != "fresh" || writer.snapshot.SnapshotJSON == "" {
		t.Fatalf("written snapshot = %#v", writer.snapshot)
	}
}

func TestServiceRefreshDoesNotWriteWithoutQuery(t *testing.T) {
	writer := &balanceWriterStub{}
	service := NewService(ServiceOptions{
		Reader: &balanceReaderStub{candidate: port.ManagementAccountBalanceCandidate{AccountID: "account-1"}},
		Writer: writer,
	})
	_, found, err := service.Refresh(context.Background(), Input{AccountID: "account-1"})
	if found || !errors.Is(err, ErrBalanceQueryMissing) || writer.snapshot.SnapshotJSON != "" {
		t.Fatalf("Refresh() found=%v err=%v writer=%#v", found, err, writer.snapshot)
	}
}

type balanceReaderStub struct {
	input     port.ManagementAccountBalanceInput
	snapshot  port.ManagementAccountBalanceSnapshot
	candidate port.ManagementAccountBalanceCandidate
}

func (s *balanceReaderStub) GetManagementAccountBalanceSnapshot(_ context.Context, input port.ManagementAccountBalanceInput) (port.ManagementAccountBalanceSnapshot, bool, error) {
	s.input = input
	return s.snapshot, s.snapshot.AccountID != "", nil
}

func (s *balanceReaderStub) GetManagementAccountBalanceCandidate(_ context.Context, _ port.ManagementAccountBalanceInput) (port.ManagementAccountBalanceCandidate, bool, error) {
	return s.candidate, s.candidate.AccountID != "", nil
}

type balanceWriterStub struct {
	snapshot port.ManagementAccountBalanceSnapshot
}

func (s *balanceWriterStub) UpsertManagementAccountBalanceSnapshot(_ context.Context, snapshot port.ManagementAccountBalanceSnapshot) error {
	s.snapshot = snapshot
	return nil
}
