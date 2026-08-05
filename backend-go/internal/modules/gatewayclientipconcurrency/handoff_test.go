package gatewayclientipconcurrency

import (
	"sync"
	"testing"

	"juhe-ai/backend-go/internal/domain/groupscheduling"
)

func TestLeaseHandoffReleasesSourceOnlyAfterTargetAcquired(t *testing.T) {
	service := NewService(nil)
	policy := testPolicy(2, "reject", 100, 1)
	sourceInput := testInput(&policy)
	targetInput := handoffTargetInput(&policy)
	source, err := service.Acquire(t.Context(), sourceInput)
	if err != nil || source.Lease == nil {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	handoff, err := NewLeaseHandoff(sourceInput, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff.CompleteTargetPreparation(targetInput, Decision{}); err == nil || handoff.TargetPrepared() || len(service.Snapshot()) != 1 {
		t.Fatalf("invalid target released source snapshots=%+v err=%v", service.Snapshot(), err)
	}
	if err := handoff.CompleteTargetPreparation(targetInput, Decision{Enabled: true, Acquired: true, Lease: &Lease{}}); err == nil || handoff.TargetPrepared() || len(service.Snapshot()) != 1 {
		t.Fatalf("malformed disabled target released source snapshots=%+v err=%v", service.Snapshot(), err)
	}
	target, err := service.Acquire(t.Context(), targetInput)
	if err != nil || target.Lease == nil {
		t.Fatalf("target=%+v err=%v", target, err)
	}
	if err := handoff.CompleteTargetPreparation(targetInput, target); err != nil {
		t.Fatal(err)
	}
	if !handoff.TargetPrepared() {
		t.Fatal("completed handoff was not observable")
	}
	if snapshots := service.Snapshot(); len(snapshots) != 1 || snapshots[0].Current != 1 {
		t.Fatalf("source was not released after target handoff: %+v", snapshots)
	}
	if err := handoff.CompleteTargetPreparation(targetInput, target); err == nil {
		t.Fatal("duplicate target handoff accepted")
	}
	target.Lease.Release()
	if snapshots := service.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("target release snapshots=%+v", snapshots)
	}
}

func TestLeaseHandoffAllowsDisabledPreparedTarget(t *testing.T) {
	service := NewService(nil)
	policy := testPolicy(1, "reject", 100, 1)
	sourceInput := testInput(&policy)
	targetInput := handoffTargetInput(&policy)
	source, err := service.Acquire(t.Context(), sourceInput)
	if err != nil || source.Lease == nil {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	handoff, err := NewLeaseHandoff(sourceInput, source)
	if err != nil {
		t.Fatal(err)
	}
	targetInput.ClientIP = ""
	if err := handoff.CompleteTargetPreparation(targetInput, Decision{Enabled: false, Acquired: true}); err != nil {
		t.Fatal(err)
	}
	if !handoff.TargetPrepared() || len(service.Snapshot()) != 0 {
		t.Fatalf("disabled target did not complete source handoff snapshots=%+v", service.Snapshot())
	}
}

func TestLeaseHandoffCloseSourceReleasesOnce(t *testing.T) {
	service := NewService(nil)
	policy := testPolicy(1, "reject", 100, 1)
	sourceInput := testInput(&policy)
	source, err := service.Acquire(t.Context(), sourceInput)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := NewLeaseHandoff(sourceInput, source)
	if err != nil {
		t.Fatal(err)
	}
	handoff.CloseSource()
	handoff.CloseSource()
	if handoff.TargetPrepared() {
		t.Fatal("source close was reported as target preparation")
	}
	if snapshots := service.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("source close snapshots=%+v", snapshots)
	}
}

func TestLeaseHandoffRejectsReleasedOrSourceTargetLease(t *testing.T) {
	service := NewService(nil)
	policy := testPolicy(2, "reject", 100, 1)
	sourceInput := testInput(&policy)
	targetInput := handoffTargetInput(&policy)
	source, err := service.Acquire(t.Context(), sourceInput)
	if err != nil || source.Lease == nil {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	handoff, err := NewLeaseHandoff(sourceInput, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff.CompleteTargetPreparation(sourceInput, source); err == nil || handoff.TargetPrepared() {
		t.Fatal("source lease accepted as target")
	}
	target, err := service.Acquire(t.Context(), targetInput)
	if err != nil || target.Lease == nil {
		t.Fatalf("target=%+v err=%v", target, err)
	}
	target.Lease.Release()
	if err := handoff.CompleteTargetPreparation(targetInput, target); err == nil || handoff.TargetPrepared() {
		t.Fatal("released target lease accepted")
	}
	handoff.CloseSource()
}

func TestLeaseHandoffRejectsSameScopeAndDifferentPolicyTarget(t *testing.T) {
	service := NewService(nil)
	sourcePolicy := testPolicy(3, "reject", 100, 1)
	sourceInput := testInput(&sourcePolicy)
	source, err := service.Acquire(t.Context(), sourceInput)
	if err != nil || source.Lease == nil {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	handoff, err := NewLeaseHandoff(sourceInput, source)
	if err != nil {
		t.Fatal(err)
	}
	sameScope, err := service.Acquire(t.Context(), sourceInput)
	if err != nil || sameScope.Lease == nil {
		t.Fatalf("same scope=%+v err=%v", sameScope, err)
	}
	if err := handoff.CompleteTargetPreparation(sourceInput, sameScope); err == nil || handoff.TargetPrepared() {
		t.Fatal("same source scope was accepted as target")
	}
	sameScope.Lease.Release()

	targetInput := handoffTargetInput(&sourcePolicy)
	target, err := service.Acquire(t.Context(), targetInput)
	if err != nil || target.Lease == nil {
		t.Fatalf("target=%+v err=%v", target, err)
	}
	wrongPolicy := testPolicy(1, "reject", 100, 1)
	targetInput.Policy = &wrongPolicy
	if err := handoff.CompleteTargetPreparation(targetInput, target); err == nil || handoff.TargetPrepared() {
		t.Fatal("target lease issued under another policy was accepted")
	}
	target.Lease.Release()
	handoff.CloseSource()
}

func TestLeaseHandoffConcurrentCloseDoesNotForgeTargetPreparation(t *testing.T) {
	service := NewService(nil)
	policy := testPolicy(2, "reject", 100, 1)
	sourceInput := testInput(&policy)
	targetInput := handoffTargetInput(&policy)
	source, err := service.Acquire(t.Context(), sourceInput)
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.Acquire(t.Context(), targetInput)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := NewLeaseHandoff(sourceInput, source)
	if err != nil {
		t.Fatal(err)
	}
	var completeErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); completeErr = handoff.CompleteTargetPreparation(targetInput, target) }()
	go func() { defer wait.Done(); handoff.CloseSource() }()
	wait.Wait()
	if handoff.TargetPrepared() != (completeErr == nil) {
		t.Fatalf("target prepared=%t complete err=%v", handoff.TargetPrepared(), completeErr)
	}
	target.Lease.Release()
}

func handoffTargetInput(policy *groupscheduling.Policy) Input {
	input := testInput(policy)
	input.GroupID = "target-group"
	return input
}
