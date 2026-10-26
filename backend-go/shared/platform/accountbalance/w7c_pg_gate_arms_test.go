//go:build windows

package accountbalance

// w7c gated runner/scan/service-loop arms: malformed proxy and schedule rows,
// and the Service.Run loop recovering readiness after a cycle error.
// （2026-10-25 sqlite 臂退役：release 失败经 RAISE ABORT 触发器注入的失败面
// 随 SQLite 生产臂删除，无 PG 等价断言——D9 裁决。）

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestW7CRunnerReleaseAndScanArms(t *testing.T) {
	db, _ := w7cGateDatabase(t)
	credential, err := NewCredentialEnvelope(w7cBalanceSecret, "api_key", map[string]string{"api_key": "sk-w7c", "base_url": "https://w7c-upstream.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	notJSON, err := EncryptV1Envelope(w7cBalanceSecret, []byte("not-json"))
	if err != nil {
		t.Fatal(err)
	}
	// Decrypts fine, but the plaintext JSON carries a non-string password.
	wrongTypeCiphertext, err := EncryptV1Envelope(w7cBalanceSecret, []byte(`{"password":1}`))
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	w7cSeedProxy(t, db, "w7c-proxy-badport", "http", "10.0.0.20", 70000, "", "", true)
	w7cSeedCandidate(t, db, "w7c-acct-badport", "", credential.Ciphertext, `{"adapter":"builtin","intervalMinutes":5}`, 1, past, "w7c-proxy-badport")
	w7cSeedProxy(t, db, "w7c-proxy-badpass", "http", "10.0.0.21", 8080, "u", notJSON, true)
	w7cSeedCandidate(t, db, "w7c-acct-badpass", "", credential.Ciphertext, `{"adapter":"builtin","intervalMinutes":5}`, 1, past, "w7c-proxy-badpass")
	w7cSeedProxy(t, db, "w7c-proxy-wrongpass", "http", "10.0.0.22", 8080, "u", wrongTypeCiphertext, true)
	w7cSeedCandidate(t, db, "w7c-acct-wrongpass", "", credential.Ciphertext, `{"adapter":"builtin","intervalMinutes":5}`, 1, past, "w7c-proxy-wrongpass")

	reader, err := NewPostgresDirectInputReader(db, w7cBalanceSecret, 15*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	due, err := reader.LoadDue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	// The non-string proxy password is tolerated as an empty password; every
	// other malformed row must be skipped.
	if len(due) != 1 || len(due) == 1 && due[0].AccountID != "w7c-acct-wrongpass" {
		t.Fatalf("only the tolerated candidate may survive, got %v", w7cIDsOf(due))
	}
}

func w7cIDsOf(candidates []Candidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.AccountID)
	}
	return ids
}

func TestW7CServiceRunLoopErrorClearsReadiness(t *testing.T) {
	_, dsn := w7cGateDatabase(t)
	config := RuntimeConfig{
		Enabled: true, OwnerID: w7cOwnerID + "-loop", Store: StoreConfig{Mode: StorePostgres, PostgresURL: dsn},
		BusinessPostgresURL: dsn, CredentialSecret: w7cBalanceSecret,
		ScanInterval: 200 * time.Millisecond, OwnerLease: 10 * time.Minute, AccountLease: time.Minute,
		InputTTL: 15 * time.Minute, ProbeTimeout: 15 * time.Second,
		// A 1ns cycle budget makes every cycle fail its first read, so the
		// loop exercises the error arm and the timer reset continuously.
		CycleBudget:    time.Nanosecond,
		MaxConcurrency: 2, IOConcurrency: 1, DBConcurrency: 1, DBQueueSize: 4,
		BatchSize: 4, RecoveryBatchSize: 2,
	}
	service, err := NewService(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- service.Run(ctx) }()
	time.Sleep(600 * time.Millisecond)
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("run return: %v", err)
	}
	if service.Ready() {
		t.Fatal("failing cycles must keep readiness cleared")
	}
}
