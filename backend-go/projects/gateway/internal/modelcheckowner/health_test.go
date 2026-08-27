package modelcheckowner

import (
	"testing"
	"time"
)

func TestCompareLatestWinsObservedAtThenRunID(t *testing.T) {
	base := HealthFact{AccountID: "a", StatHour: "hour", RunID: "run-1", ObservedAt: time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC)}
	newer := base
	newer.ObservedAt = base.ObservedAt.Add(time.Second)
	if got, err := CompareLatestWins(newer, base); err != nil || got != 1 {
		t.Fatalf("newer compare=%d err=%v", got, err)
	}
	tie := base
	tie.RunID = "run-2"
	if got, err := CompareLatestWins(tie, base); err != nil || got != 1 {
		t.Fatalf("tie compare=%d err=%v", got, err)
	}
	if got, err := CompareLatestWins(base, tie); err != nil || got != -1 {
		t.Fatalf("older tie compare=%d err=%v", got, err)
	}
}

func TestCompareLatestWinsRejectsScopeMismatch(t *testing.T) {
	base := HealthFact{AccountID: "a", StatHour: "hour", RunID: "run", ObservedAt: time.Now()}
	other := base
	other.AccountID = "b"
	if _, err := CompareLatestWins(other, base); err == nil {
		t.Fatal("scope mismatch must fail closed")
	}
}
