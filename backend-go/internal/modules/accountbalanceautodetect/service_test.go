package accountbalanceautodetect

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type storeStub struct {
	candidate port.AccountBalanceAutoDetectCandidate
	found     bool
	loadErr   error
	commitOK  bool
	commitErr error
	loads     []port.AccountBalanceAutoDetectLookup
	commits   []port.AccountBalanceAutoDetectCommit
}

func (s *storeStub) LoadAccountBalanceAutoDetectCandidate(_ context.Context, input port.AccountBalanceAutoDetectLookup) (port.AccountBalanceAutoDetectCandidate, bool, error) {
	s.loads = append(s.loads, input)
	return s.candidate, s.found, s.loadErr
}

func (s *storeStub) CommitAccountBalanceAutoDetect(_ context.Context, input port.AccountBalanceAutoDetectCommit) (bool, error) {
	s.commits = append(s.commits, input)
	return s.commitOK, s.commitErr
}

type codecStub struct {
	credentials map[string]any
	err         error
}

func (s codecStub) DecryptJSON(string) (map[string]any, error) {
	return s.credentials, s.err
}

type detectorStub struct {
	result ProbeResult
	err    error
	calls  []ProbeCandidate
}

func (s *detectorStub) DetectBuiltin(_ context.Context, candidate ProbeCandidate) (ProbeResult, error) {
	s.calls = append(s.calls, candidate)
	return s.result, s.err
}

func TestRunReturnsStaleWithoutProbingWhenCandidateIsNoLongerCurrent(t *testing.T) {
	store := &storeStub{}
	detector := &detectorStub{}
	service := NewService(ServiceOptions{Store: store, Codec: codecStub{}, Detector: detector})

	result, err := service.Run(context.Background(), Input{AccountID: " account-1 ", ConfigRevision: 7})
	if err != nil || result != ResultStale {
		t.Fatalf("Run() = %q, %v; want stale, nil", result, err)
	}
	if len(store.loads) != 1 || store.loads[0] != (port.AccountBalanceAutoDetectLookup{AccountID: "account-1", ConfigRevision: 7}) {
		t.Fatalf("loads = %#v", store.loads)
	}
	if len(detector.calls) != 0 || len(store.commits) != 0 {
		t.Fatalf("detector calls = %d, commits = %d; want zero", len(detector.calls), len(store.commits))
	}
}

func TestRunReturnsUnsupportedWithoutCommitting(t *testing.T) {
	store := currentCandidateStore()
	detector := &detectorStub{result: ProbeResult{Supported: false}}
	service := NewService(ServiceOptions{
		Store: store, Codec: codecStub{credentials: map[string]any{"api_key": "secret"}}, Detector: detector,
	})

	result, err := service.Run(context.Background(), Input{AccountID: "account-1", ConfigRevision: 7})
	if err != nil || result != ResultUnsupported {
		t.Fatalf("Run() = %q, %v; want unsupported, nil", result, err)
	}
	if len(store.commits) != 0 {
		t.Fatalf("commits = %#v, want none", store.commits)
	}
}

func TestRunPropagatesDetectorErrorForAsynqRetry(t *testing.T) {
	store := currentCandidateStore()
	detectorErr := errors.New("upstream timeout")
	service := NewService(ServiceOptions{
		Store: store, Codec: codecStub{credentials: map[string]any{"api_key": "secret"}},
		Detector: &detectorStub{err: detectorErr},
	})

	result, err := service.Run(context.Background(), Input{AccountID: "account-1", ConfigRevision: 7})
	if result != "" || !errors.Is(err, detectorErr) {
		t.Fatalf("Run() = %q, %v; want wrapped detector error", result, err)
	}
	if len(store.commits) != 0 {
		t.Fatalf("commits = %#v, want none", store.commits)
	}
}

func TestRunCommitsDetectedConfigAndSnapshotWithOneCAS(t *testing.T) {
	now := time.Date(2026, 7, 20, 2, 3, 4, 0, time.UTC)
	store := currentCandidateStore()
	store.commitOK = true
	detector := &detectorStub{result: ProbeResult{
		Supported: true,
		Adapter:   "newapi",
		Snapshot:  Snapshot{Status: "fresh", RemainingUSD: "8.50", Basis: "api_key_quota"},
	}}
	service := NewService(ServiceOptions{
		Store: store, Codec: codecStub{credentials: map[string]any{"api_key": "secret", "base_url": "https://relay.example/v1"}},
		Detector: detector, Now: func() time.Time { return now },
	})

	result, err := service.Run(context.Background(), Input{AccountID: "account-1", ConfigRevision: 7})
	if err != nil || result != ResultEnabled {
		t.Fatalf("Run() = %q, %v; want enabled, nil", result, err)
	}
	if len(detector.calls) != 1 || detector.calls[0].Credentials["api_key"] != "secret" {
		t.Fatalf("detector calls = %#v", detector.calls)
	}
	if len(store.commits) != 1 {
		t.Fatalf("commits = %d, want one", len(store.commits))
	}
	commit := store.commits[0]
	if commit.AccountID != "account-1" || commit.SystemAccountID != "system-1" || commit.ExpectedConfigRevision != 7 {
		t.Fatalf("commit identity = %#v", commit)
	}
	if commit.ConfigJSON != `{"adapter":"builtin","intervalMinutes":5,"preferredBuiltinAdapter":"newapi"}` {
		t.Fatalf("config JSON = %s", commit.ConfigJSON)
	}
	if commit.SnapshotJSON != `{"status":"fresh","remainingUsd":"8.50","basis":"api_key_quota","lastAttemptAt":"2026-07-20T02:03:04Z","lastSuccessAt":"2026-07-20T02:03:04Z"}` {
		t.Fatalf("snapshot JSON = %s", commit.SnapshotJSON)
	}
	if !commit.CompletedAt.Equal(now) || !commit.NextRefreshAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("commit times = completed %s, next %s", commit.CompletedAt, commit.NextRefreshAt)
	}
}

func TestRunReturnsStaleWhenCommitCASLosesRace(t *testing.T) {
	store := currentCandidateStore()
	detector := &detectorStub{result: ProbeResult{Supported: true, Adapter: "sub2api", Snapshot: Snapshot{Status: "unlimited"}}}
	service := NewService(ServiceOptions{
		Store: store, Codec: codecStub{credentials: map[string]any{"api_key": "secret"}}, Detector: detector,
	})

	result, err := service.Run(context.Background(), Input{AccountID: "account-1", ConfigRevision: 7})
	if err != nil || result != ResultStale {
		t.Fatalf("Run() = %q, %v; want stale, nil", result, err)
	}
	if len(store.commits) != 1 {
		t.Fatalf("commits = %d, want one CAS attempt", len(store.commits))
	}
}

func currentCandidateStore() *storeStub {
	return &storeStub{
		found: true,
		candidate: port.AccountBalanceAutoDetectCandidate{
			AccountID: "account-1", SystemAccountID: "system-1", ConfigRevision: 7,
			CredentialsEncrypted: "encrypted", ProxyProfileID: "proxy-1",
		},
	}
}
