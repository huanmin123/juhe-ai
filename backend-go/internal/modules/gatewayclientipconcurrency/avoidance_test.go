package gatewayclientipconcurrency

import (
	"sync"
	"testing"
)

func TestAvoidanceInvalidScopeIsObservableNoOp(t *testing.T) {
	tracker := NewTracker(Scope{SystemAccountID: "system", APIKeyID: "key", ClientIP: " "})
	result, err := tracker.Remember(testFailure("account-a", ErrorPhaseUpstreamResponse))
	if err != nil {
		t.Fatalf("invalid scope remember error = %v", err)
	}
	if !result.NoOp || !result.InvalidScope || result.Accepted || tracker.Len() != 0 {
		t.Fatalf("invalid scope result = %+v len=%d", result, tracker.Len())
	}
	transfer := Transfer(tracker, NewTracker(validScope("target")))
	if !transfer.NoOp || !transfer.InvalidSource || transfer.SourceCleared {
		t.Fatalf("invalid source transfer = %+v", transfer)
	}
}

func TestAvoidanceMissingAPIKeyUsesNodeInternalScope(t *testing.T) {
	tracker := NewTracker(Scope{SystemAccountID: "system", ClientIP: " 203.0.113.1 "})
	if !tracker.Valid() {
		t.Fatal("scope with omitted API key should use internal identity")
	}
	if got := tracker.Scope(); got.APIKeyID != InternalAPIKeyID || got.ClientIP != "203.0.113.1" {
		t.Fatalf("normalized scope = %+v", got)
	}
	result, err := tracker.Remember(testFailure("account-a", ErrorPhaseUpstreamResponse))
	if err != nil || !result.Accepted {
		t.Fatalf("remember with internal scope result=%+v err=%v", result, err)
	}
}

func TestAvoidanceEmptySystemAccountStillUsesNodeScope(t *testing.T) {
	tracker := NewTracker(Scope{SystemAccountID: " \t", APIKeyID: "key", ClientIP: "203.0.113.1"})
	if !tracker.Valid() {
		t.Fatal("Node-compatible scope should only require client IP")
	}
	if got := tracker.Scope().SystemAccountID; got != " \t" {
		t.Fatalf("system account ID was normalized to %q", got)
	}
	result, err := tracker.Remember(testFailure("account-a", ErrorPhaseUpstreamResponse))
	if err != nil || !result.Accepted {
		t.Fatalf("remember with empty system account result=%+v err=%v", result, err)
	}
}

func TestAvoidanceRejectsUnknownFailurePhase(t *testing.T) {
	tracker := NewTracker(validScope("scope"))
	_, err := tracker.Remember(testFailure("account-a", ErrorPhase("transport")))
	if err == nil {
		t.Fatal("unknown phase was accepted")
	}
	if tracker.Len() != 0 {
		t.Fatalf("invalid failure changed tracker: %+v", tracker.Snapshot())
	}
}

func TestAvoidanceCapAndLatestReplacementPreserveOrder(t *testing.T) {
	tracker := NewTracker(validScope("scope"))
	for i := 0; i < MaxPendingFailures; i++ {
		result, err := tracker.Remember(testFailure(accountID(i), ErrorPhaseUpstreamRequest))
		if err != nil || !result.Accepted {
			t.Fatalf("remember %d result=%+v err=%v", i, result, err)
		}
	}
	dropped, err := tracker.Remember(testFailure("overflow", ErrorPhaseStream))
	if err != nil || !dropped.CapacityDropped || dropped.Outcome != RememberCapacityDropped || dropped.Size != MaxPendingFailures {
		t.Fatalf("capacity result=%+v err=%v", dropped, err)
	}
	latest := testFailure("account-17", ErrorPhaseStream)
	latest.ErrorCode = "latest"
	replaced, err := tracker.Remember(latest)
	if err != nil || !replaced.Replaced || replaced.Outcome != RememberReplaced {
		t.Fatalf("replacement result=%+v err=%v", replaced, err)
	}
	snapshot := tracker.Snapshot()
	if len(snapshot) != MaxPendingFailures || snapshot[17].AccountID != "account-17" || snapshot[17].ErrorCode != "latest" {
		t.Fatalf("replacement order snapshot[%d]=%+v len=%d", 17, snapshot[17], len(snapshot))
	}
}

func TestAvoidanceTransferDeduplicatesInTargetOrderAndClearsSource(t *testing.T) {
	source := NewTracker(validScope("source"))
	target := NewTracker(validScope("target"))
	if _, err := target.Remember(failureWithCode("shared", "target-old")); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Remember(failureWithCode("target-only", "target-only")); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []Failure{
		failureWithCode("shared", "source-new"),
		failureWithCode("source-a", "source-a"),
		failureWithCode("source-b", "source-b"),
	} {
		if _, err := source.Remember(failure); err != nil {
			t.Fatal(err)
		}
	}
	result := Transfer(source, target)
	if !result.SourceCleared || result.Attempted != 3 || result.Replaced != 1 || result.Inserted != 2 || result.CapacityDropped != 0 {
		t.Fatalf("transfer result=%+v", result)
	}
	snapshot := target.Snapshot()
	wantIDs := []string{"shared", "target-only", "source-a", "source-b"}
	for i, want := range wantIDs {
		if snapshot[i].AccountID != want {
			t.Fatalf("target order=%v want index %d=%q", snapshot, i, want)
		}
	}
	if snapshot[0].ErrorCode != "source-new" || source.Len() != 0 {
		t.Fatalf("target replacement=%+v source=%+v", snapshot[0], source.Snapshot())
	}
}

func TestAvoidanceTransferReportsTargetCapacityDropsAndClearsSource(t *testing.T) {
	target := NewTracker(validScope("target"))
	for i := 0; i < MaxPendingFailures; i++ {
		if _, err := target.Remember(testFailure(accountID(i), ErrorPhaseUpstreamResponse)); err != nil {
			t.Fatal(err)
		}
	}
	source := NewTracker(validScope("source"))
	for _, id := range []string{"new-a", "new-b"} {
		if _, err := source.Remember(testFailure(id, ErrorPhaseUpstreamResponse)); err != nil {
			t.Fatal(err)
		}
	}
	result := Transfer(source, target)
	if !result.SourceCleared || result.Attempted != 2 || result.CapacityDropped != 2 || result.Dropped != 2 {
		t.Fatalf("capacity transfer result=%+v", result)
	}
	if target.Len() != MaxPendingFailures || source.Len() != 0 {
		t.Fatalf("target/source lengths target=%d source=%d", target.Len(), source.Len())
	}
}

func TestAvoidanceInvalidTransferDoesNotClearSource(t *testing.T) {
	source := NewTracker(validScope("source"))
	if _, err := source.Remember(testFailure("account-a", ErrorPhaseStream)); err != nil {
		t.Fatal(err)
	}
	result := Transfer(source, NewTracker(Scope{SystemAccountID: "system", APIKeyID: "key"}))
	if !result.NoOp || !result.InvalidTarget || result.SourceCleared || source.Len() != 1 {
		t.Fatalf("invalid target result=%+v source=%+v", result, source.Snapshot())
	}
}

func TestAvoidanceSelfTransferClearsSourceLikeNode(t *testing.T) {
	tracker := NewTracker(validScope("scope"))
	for _, id := range []string{"account-a", "account-b"} {
		if _, err := tracker.Remember(testFailure(id, ErrorPhaseUpstreamRequest)); err != nil {
			t.Fatal(err)
		}
	}
	result := Transfer(tracker, tracker)
	if !result.SourceCleared || result.Attempted != 2 || result.Replaced != 2 || result.Inserted != 0 || tracker.Len() != 0 {
		t.Fatalf("self transfer result=%+v tracker=%+v", result, tracker.Snapshot())
	}
}

func TestAvoidanceSnapshotIsCopySafe(t *testing.T) {
	tracker := NewTracker(validScope("scope"))
	if _, err := tracker.Remember(testFailure("account-a", ErrorPhaseUpstreamResponse)); err != nil {
		t.Fatal(err)
	}
	snapshot := tracker.Snapshot()
	snapshot[0].AccountID = "mutated"
	snapshot[0].ErrorMessage = "mutated"
	actual := tracker.Snapshot()
	if actual[0].AccountID != "account-a" || actual[0].ErrorMessage != "" {
		t.Fatalf("snapshot mutation leaked: %+v", actual)
	}
}

func TestAvoidanceConcurrentRememberAndSnapshot(t *testing.T) {
	tracker := NewTracker(validScope("scope"))
	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for i := 0; i < 100; i++ {
				_, _ = tracker.Remember(testFailure(accountID((worker*100+i)%64), ErrorPhaseUpstreamRequest))
				_ = tracker.Snapshot()
			}
		}(worker)
	}
	group.Wait()
	if got := tracker.Len(); got > 64 || got > MaxPendingFailures {
		t.Fatalf("concurrent tracker length=%d", got)
	}
}

func validScope(suffix string) Scope {
	return Scope{SystemAccountID: "system-" + suffix, APIKeyID: "key-" + suffix, ClientIP: "203.0.113.10"}
}

func testFailure(accountID string, phase ErrorPhase) Failure {
	return Failure{AccountID: accountID, ErrorPhase: phase}
}

func failureWithCode(accountID, code string) Failure {
	failure := testFailure(accountID, ErrorPhaseUpstreamResponse)
	failure.ErrorCode = code
	return failure
}

func accountID(index int) string {
	return "account-" + string(rune('0'+index/10)) + string(rune('0'+index%10))
}
