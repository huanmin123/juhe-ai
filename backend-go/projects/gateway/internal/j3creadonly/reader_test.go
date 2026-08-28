package j3creadonly

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckowner"
)

type fakeSource struct {
	fact  modelcheckowner.HealthFact
	found bool
	err   error
	reads int
	force bool
}

func (f *fakeSource) ReadHealthFact(_ context.Context, accountID, statHour string) (modelcheckowner.HealthFact, bool, error) {
	f.reads++
	if !f.force && (accountID != f.fact.AccountID || statHour != f.fact.StatHour) {
		return modelcheckowner.HealthFact{}, false, nil
	}
	return f.fact, f.found, f.err
}

var _ HealthSource = (*fakeSource)(nil)

func TestReaderProjectsOnlyPublishedHealth(t *testing.T) {
	source := &fakeSource{found: true, fact: modelcheckowner.HealthFact{
		AccountID: "acct-1", SystemAccountID: "sys-1", StatHour: "2026-08-28T10:00:00Z", RunID: "run-1",
		ProviderCode: "openai", Model: "gpt-5.6", Profile: "full", Level: "success", ObservedAt: time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC), Score: 92, Threshold: 70,
		PenaltyAction: "disable", RecoveryIntervalMinutes: 30,
	}}
	reader, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := reader.Read(context.Background(), source.fact.AccountID, source.fact.StatHour)
	if err != nil || !found {
		t.Fatalf("health found=%v err=%v", found, err)
	}
	if got.AccountID != "acct-1" || got.Score != 92 || got.Threshold != 70 || got.Level != "success" || got.RunID != "run-1" || got.ObservedAt.IsZero() {
		t.Fatalf("projected health=%+v", got)
	}
	if source.reads != 1 {
		t.Fatalf("expected one scoped read, got %d", source.reads)
	}
}

func TestReaderFailsClosedForMissingAndInvalidFacts(t *testing.T) {
	source := &fakeSource{found: false, fact: modelcheckowner.HealthFact{AccountID: "acct-1", StatHour: "2026-08-28T10:00:00Z"}}
	reader, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := reader.Read(context.Background(), "acct-1", source.fact.StatHour); err != nil || found {
		t.Fatalf("missing health found=%v err=%v", found, err)
	}
	source.found = true
	if _, found, err := reader.Read(context.Background(), "acct-1", source.fact.StatHour); err == nil || found {
		t.Fatalf("invalid health found=%v err=%v", found, err)
	}
}

func TestReaderRejectsUnscopedInputAndNilSource(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("nil source must fail")
	}
	reader, err := New(&fakeSource{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := reader.Read(context.Background(), "", "2026-08-28T10:00:00Z"); err == nil || found {
		t.Fatalf("empty account scope found=%v err=%v", found, err)
	}
}

func TestReaderRejectsMismatchedPublishedScope(t *testing.T) {
	source := &fakeSource{found: true, force: true, fact: modelcheckowner.HealthFact{
		AccountID: "acct-other", SystemAccountID: "sys-1", StatHour: "2026-08-28T09:00:00Z", RunID: "run-1",
		ProviderCode: "openai", Model: "gpt-5.6", Profile: "quick", Level: "success", ObservedAt: time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC), Score: 90, Threshold: 70,
	}}
	reader, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := reader.Read(context.Background(), "acct-1", "2026-08-28T10:00:00Z"); err == nil || found {
		t.Fatalf("scope mismatch found=%v err=%v", found, err)
	}
}
