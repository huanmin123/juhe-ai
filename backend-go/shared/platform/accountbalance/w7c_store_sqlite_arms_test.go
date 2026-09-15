package accountbalance

// w7c store contract completion on the isolated SQLite writer: lease
// renewal/release arms, CAS rejection fences, LoadSnapshot/LoadOutcome error
// arms, the EnsureSchema committed-column migration branch, OpenStore config
// validation, and the small SQL value decoders. PostgreSQL arms live in the
// gated w7c_store_pg_gate_test.go file.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func w7cNewSQLiteStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "\\w7c-balance.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func w7cOwner(t *testing.T, store *Store, id string) OwnerLease {
	t.Helper()
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), id, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire owner %s: %v %t", id, err, acquired)
	}
	return lease
}

func w7cAccount(t *testing.T, store *Store, owner OwnerLease, id string) AccountLease {
	t.Helper()
	lease, acquired, err := store.AcquireAccountLease(context.Background(), owner, id, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire account %s: %v %t", id, err, acquired)
	}
	return lease
}

func w7cOpenRawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestW7COwnerLeaseRenewReleaseContract(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	ctx := context.Background()

	if _, _, err := store.AcquireOwnerLease(ctx, "  ", time.Minute); err == nil {
		t.Fatal("empty owner id must be rejected")
	}
	if _, _, err := store.AcquireOwnerLease(ctx, "w7c-owner", 0); err == nil {
		t.Fatal("non-positive duration must be rejected")
	}

	owner := w7cOwner(t, store, "w7c-owner")

	if ok, err := store.RenewOwnerLease(ctx, OwnerLease{}, time.Minute); err == nil || ok {
		t.Fatalf("invalid renew params must fail: %t %v", ok, err)
	}
	if ok, err := store.RenewOwnerLease(ctx, OwnerLease{OwnerID: "w7c-other", FenceToken: owner.FenceToken}, time.Minute); err != nil || ok {
		t.Fatalf("foreign owner renew must report not renewed: %t %v", ok, err)
	}
	if ok, err := store.RenewOwnerLease(ctx, owner, time.Minute); err != nil || !ok {
		t.Fatalf("active owner renew: %t %v", ok, err)
	}

	expired := OwnerLease{OwnerID: "w7c-expired", FenceToken: 77}
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO account_balance_owner_leases (lease_key,owner_id,fence_token,lease_until,updated_at) VALUES ('account-balance-owner',?,?,?,?) ON CONFLICT(lease_key) DO UPDATE SET owner_id=excluded.owner_id, fence_token=excluded.fence_token, lease_until=excluded.lease_until, updated_at=excluded.updated_at`,
		expired.OwnerID, expired.FenceToken, past, past); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.RenewOwnerLease(ctx, expired, time.Minute); err != nil || ok {
		t.Fatalf("expired lease renew must report false: %t %v", ok, err)
	}
	if err := store.ReleaseOwnerLease(ctx, expired); err != nil {
		t.Fatalf("release expired lease: %v", err)
	}
	if err := store.ReleaseOwnerLease(ctx, OwnerLease{OwnerID: "w7c-owner"}); err == nil {
		t.Fatal("invalid release params must be rejected")
	}
}

func TestW7CAccountLeaseRenewReleaseContract(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	ctx := context.Background()
	owner := w7cOwner(t, store, "w7c-lease-owner")
	account := w7cAccount(t, store, owner, "w7c-acct")

	badOwner := OwnerLease{OwnerID: "w7c-lease-owner", FenceToken: 0}
	if _, _, err := store.AcquireAccountLease(ctx, badOwner, "w7c-acct", time.Minute); err == nil {
		t.Fatal("invalid owner token must be rejected")
	}
	if _, _, err := store.AcquireAccountLease(ctx, owner, " ", time.Minute); err == nil {
		t.Fatal("empty account id must be rejected")
	}
	if _, _, err := store.AcquireAccountLease(ctx, owner, "w7c-acct", 0); err == nil {
		t.Fatal("non-positive duration must be rejected")
	}

	if ok, err := store.RenewAccountLease(ctx, owner, AccountLease{}, time.Minute); err == nil || ok {
		t.Fatalf("invalid account renew params: %t %v", ok, err)
	}
	if ok, err := store.RenewAccountLease(ctx, OwnerLease{OwnerID: "w7c-someone-else", FenceToken: owner.FenceToken}, account, time.Minute); err == nil || ok {
		t.Fatalf("owner mismatch renew must fail: %t %v", ok, err)
	}

	// Owner lease expired: renew must surface ErrOwnerLeaseLost.
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE account_balance_owner_leases SET lease_until=? WHERE lease_key='account-balance-owner'`, past); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RenewAccountLease(ctx, owner, account, time.Minute); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("expired owner must fence account renew, got %v", err)
	}

	// Restore owner lease, then expire the account lease: renew reports false.
	if _, err := store.db.Exec(`UPDATE account_balance_owner_leases SET lease_until=? WHERE lease_key='account-balance-owner'`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE account_balance_account_leases SET lease_until=? WHERE account_id=?`, past, account.AccountID); err != nil {
		t.Fatal(err)
	}
	// An expired account lease cannot be renewed (only re-acquired bumps the
	// fence), so a second renew still reports false without error.
	if ok, err := store.RenewAccountLease(ctx, owner, account, time.Minute); err != nil || ok {
		t.Fatalf("expired account lease renew must report false: %t %v", ok, err)
	}
	// Restore a live account lease row, then renew succeeds.
	if _, err := store.db.Exec(`UPDATE account_balance_account_leases SET lease_until=? WHERE account_id=?`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), account.AccountID); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.RenewAccountLease(ctx, owner, account, time.Minute); err != nil || !ok {
		t.Fatalf("live account lease renew: %t %v", ok, err)
	}

	if err := store.ReleaseAccountLease(ctx, OwnerLease{OwnerID: owner.OwnerID, FenceToken: owner.FenceToken}, AccountLease{}); err == nil {
		t.Fatal("invalid account release params must be rejected")
	}
	if err := store.ReleaseAccountLease(ctx, owner, account); err != nil {
		t.Fatalf("release account lease: %v", err)
	}
	if err := store.ReleaseAccountLease(ctx, OwnerLease{}, AccountLease{AccountID: "w7c-acct", OwnerID: "w7c-lease-owner", FenceToken: 1}); err == nil {
		t.Fatal("owner mismatch on release must be rejected")
	}
}

func TestW7CLoadSnapshotArms(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	ctx := context.Background()

	if _, _, err := store.LoadSnapshot(ctx, "  "); err == nil {
		t.Fatal("empty account id must be rejected")
	}
	if _, found, err := store.LoadSnapshot(ctx, "w7c-missing"); err != nil || found {
		t.Fatalf("missing snapshot: %t %v", found, err)
	}

	owner := w7cOwner(t, store, "w7c-snap-owner")
	account := w7cAccount(t, store, owner, "w7c-snap-acct")
	next := time.Now().UTC().Add(time.Minute)
	if _, err := store.WriteSnapshotCAS(ctx, owner, account, SnapshotMutation{
		Input:         w7cValidBalanceInput("w7c-snap-acct"),
		Snapshot:      Snapshot{Status: StatusFresh, RemainingUSD: "3.5"},
		NextRefreshAt: &next,
	}); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.LoadSnapshot(ctx, "w7c-snap-acct")
	if err != nil || !found || record.Snapshot.Status != StatusFresh || record.NextRefreshAt == nil {
		t.Fatalf("load written snapshot: %#v %t %v", record, found, err)
	}

	// Corrupt rows directly to exercise decode arms.
	if _, err := store.db.Exec(`UPDATE account_balance_snapshots SET snapshot_json='{broken' WHERE account_id=?`, account.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadSnapshot(ctx, account.AccountID); err == nil || !strings.Contains(err.Error(), "解析 account-balance snapshot 失败") {
		t.Fatalf("corrupt snapshot json: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE account_balance_snapshots SET snapshot_json='{}', updated_at='not-a-time' WHERE account_id=?`, account.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadSnapshot(ctx, account.AccountID); err == nil || !strings.Contains(err.Error(), "updated_at 无效") {
		t.Fatalf("corrupt updated_at: %v", err)
	}
}

func TestW7CLoadOutcomeArms(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	ctx := context.Background()

	if _, _, err := store.LoadOutcome(ctx, ""); err == nil {
		t.Fatal("empty outcome id must be rejected")
	}
	if _, found, err := store.LoadOutcome(ctx, "w7c-missing-outcome"); err != nil || found {
		t.Fatalf("missing outcome: %t %v", found, err)
	}

	owner := w7cOwner(t, store, "w7c-outcome-owner")
	account := w7cAccount(t, store, owner, "w7c-outcome-acct")
	if _, err := store.AppendOutcome(ctx, owner, account, Outcome{
		OutcomeID: "w7c-outcome-1", RequestID: "w7c-request-1", AccountID: account.AccountID,
		InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: time.Now().UTC(),
		Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "1"},
	}); err != nil {
		t.Fatal(err)
	}
	if outcome, found, err := store.LoadOutcome(ctx, "w7c-outcome-1"); err != nil || !found || outcome.AccountID != account.AccountID {
		t.Fatalf("load outcome: %#v %t %v", outcome, found, err)
	}

	if _, err := store.db.Exec(`UPDATE account_balance_outcomes SET payload='{bad' WHERE outcome_id='w7c-outcome-1'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadOutcome(ctx, "w7c-outcome-1"); err == nil {
		t.Fatal("malformed payload must fail")
	}
	if _, err := store.db.Exec(`UPDATE account_balance_outcomes SET payload='{"outcome_id":"other"}' WHERE outcome_id='w7c-outcome-1'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadOutcome(ctx, "w7c-outcome-1"); err == nil || !strings.Contains(err.Error(), "identity 不一致") {
		t.Fatalf("identity mismatch: %v", err)
	}
}

func TestW7CWriteSnapshotCASArms(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	ctx := context.Background()
	owner := w7cOwner(t, store, "w7c-cas-owner")
	account := w7cAccount(t, store, owner, "w7c-cas-acct")
	input := w7cValidBalanceInput("w7c-cas-acct")

	mismatchAccount := account
	mismatchAccount.OwnerID = "w7c-not-owner"
	if _, err := store.WriteSnapshotCAS(ctx, owner, mismatchAccount, SnapshotMutation{Input: input}); err == nil || !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("lease/input mismatch: %v", err)
	}

	// Expired owner lease fences the CAS write.
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE account_balance_owner_leases SET lease_until=? WHERE lease_key='account-balance-owner'`, past); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteSnapshotCAS(ctx, owner, account, SnapshotMutation{Input: input, Snapshot: Snapshot{Status: StatusFailed}}); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("expired owner must fence CAS: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE account_balance_owner_leases SET lease_until=? WHERE lease_key='account-balance-owner'`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	// Expired account lease fences too.
	if _, err := store.db.Exec(`UPDATE account_balance_account_leases SET lease_until=? WHERE account_id=?`, past, account.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteSnapshotCAS(ctx, owner, account, SnapshotMutation{Input: input, Snapshot: Snapshot{Status: StatusFailed}}); !errors.Is(err, ErrAccountLeaseLost) {
		t.Fatalf("expired account lease must fence CAS: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE account_balance_account_leases SET lease_until=? WHERE account_id=?`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), account.AccountID); err != nil {
		t.Fatal(err)
	}

	// First insert creates the row.
	accepted, err := store.WriteSnapshotCAS(ctx, owner, account, SnapshotMutation{Input: input, Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "9"}, NextRefreshAt: &input.ExpiresAt})
	if err != nil || !accepted {
		t.Fatalf("initial snapshot write: %t %v", accepted, err)
	}
	current, _, err := store.LoadSnapshot(ctx, account.AccountID)
	if err != nil {
		t.Fatal(err)
	}

	// Rejected CAS arms against the existing row.
	if accepted, err := store.WriteSnapshotCAS(ctx, owner, account, SnapshotMutation{Input: input, Snapshot: Snapshot{Status: StatusFailed}, ExpectedInput: current.InputVersion + 5, ExpectedConfig: current.ConfigRevision}); err != nil || accepted {
		t.Fatalf("expected input mismatch must reject: %t %v", accepted, err)
	}
	// The expected next-refresh fence is the frozen input due value.
	wrongNextInput := input
	wrong := input.ExpiresAt.Add(-time.Hour)
	wrongNextInput.NextRefreshAt = &wrong
	if accepted, err := store.WriteSnapshotCAS(ctx, owner, account, SnapshotMutation{Input: wrongNextInput, Snapshot: Snapshot{Status: StatusFailed}}); err != nil || accepted {
		t.Fatalf("expected next_refresh mismatch must reject: %t %v", accepted, err)
	}

	// Matching expectations accept the update (alias CASSnapshot).
	matchInput := input
	matchInput.NextRefreshAt = current.NextRefreshAt
	accepted, err = store.CASSnapshot(ctx, owner, account, SnapshotMutation{
		Input: matchInput, Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "10"},
		ExpectedInput: current.InputVersion, ExpectedConfig: current.ConfigRevision,
	})
	if err != nil || !accepted {
		t.Fatalf("matching CAS: %t %v", accepted, err)
	}

	// Regression (older revision) is rejected.
	staleInput := input
	staleInput.InputVersion = current.InputVersion - 1
	if accepted, err := store.WriteSnapshotCAS(ctx, owner, account, SnapshotMutation{Input: staleInput, Snapshot: Snapshot{Status: StatusFresh}}); err != nil || accepted {
		t.Fatalf("stale input version must reject: %t %v", accepted, err)
	}
}

func TestW7CAppendOutcomeValidationArms(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	ctx := context.Background()
	owner := w7cOwner(t, store, "w7c-outv-owner")
	account := w7cAccount(t, store, owner, "w7c-outv-acct")
	now := time.Now().UTC()

	base := Outcome{OutcomeID: "w7c-outv-1", RequestID: "w7c-outv-req-1", AccountID: account.AccountID, InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now}
	if _, err := store.AppendOutcome(ctx, owner, account, Outcome{}); err == nil || !strings.Contains(err.Error(), "缺少幂等或 fence 字段") {
		t.Fatalf("empty outcome: %v", err)
	}
	noObserved := base
	noObserved.ObservedAt = time.Time{}
	if _, err := store.AppendOutcome(ctx, owner, account, noObserved); err == nil {
		t.Fatal("zero observed_at must be rejected")
	}
	wrongAccount := base
	wrongAccount.AccountID = "w7c-other"
	if _, err := store.AppendOutcome(ctx, owner, account, wrongAccount); err == nil || !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("account mismatch: %v", err)
	}

	// Snapshot-write rejection after outcome insert turns into ErrOutcomeStale
	// (input version regression against the existing snapshot).
	seedInput := w7cValidBalanceInput(account.AccountID)
	if _, err := store.WriteSnapshotCAS(ctx, owner, account, SnapshotMutation{Input: seedInput, Snapshot: Snapshot{Status: StatusFresh}}); err != nil {
		t.Fatal(err)
	}
	regressed := base
	regressed.InputVersion = 1
	if _, err := store.AppendOutcome(ctx, owner, account, regressed); !errors.Is(err, ErrOutcomeStale) {
		t.Fatalf("regressed snapshot CAS must be stale: %v", err)
	}

	// Accepted path through the CASOutcome alias marks the outcome committed.
	acceptedOutcome := base
	acceptedOutcome.OutcomeID = "w7c-outv-2"
	acceptedOutcome.RequestID = "w7c-outv-req-2"
	acceptedOutcome.InputVersion = 6
	acceptedOutcome.ConfigRevision = 6
	acceptedOutcome.Snapshot = Snapshot{Status: StatusFresh, RemainingUSD: "2"}
	inserted, err := store.CASOutcome(ctx, owner, account, acceptedOutcome)
	if err != nil || !inserted {
		t.Fatalf("accepted outcome: %t %v", inserted, err)
	}
	stored, found, err := store.LoadOutcome(ctx, "w7c-outv-2")
	if err != nil || !found {
		t.Fatalf("load accepted outcome: %t %v", found, err)
	}
	var committedRaw any
	if err := store.db.QueryRow(`SELECT committed FROM account_balance_outcomes WHERE outcome_id=?`, "w7c-outv-2").Scan(&committedRaw); err != nil {
		t.Fatal(err)
	}
	committed, err := balanceSQLBool(committedRaw)
	if err != nil || !committed {
		t.Fatalf("accepted outcome must be committed: %#v %v", committedRaw, err)
	}
	_ = stored
}

func TestW7CEnsureSchemaMigratesLegacyOutcomeTable(t *testing.T) {
	path := t.TempDir() + "\\w7c-legacy.sqlite"
	db := w7cOpenRawSQLite(t, path)
	legacy := strings.Replace(balanceSQLiteSchema, ", committed INTEGER NOT NULL DEFAULT 0", "", 1)
	legacy = strings.Replace(legacy, "CREATE INDEX IF NOT EXISTS idx_account_balance_outcomes_account ON account_balance_outcomes(account_id, observed_at);", "", 1)
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("legacy migration: %v", err)
	}
	if _, err := store.db.Exec(`SELECT committed FROM account_balance_outcomes LIMIT 0`); err != nil {
		t.Fatalf("committed column missing after migration: %v", err)
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		t.Fatalf("sqlite CheckSchema must delegate to EnsureSchema: %v", err)
	}
}

func TestW7COpenStoreValidationArms(t *testing.T) {
	if _, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: "   "}); err == nil || !strings.Contains(err.Error(), "缺少数据库路径") {
		t.Fatalf("sqlite empty path: %v", err)
	}
	if _, err := OpenStore(StoreConfig{Mode: StoreMode("memory")}); err == nil || !strings.Contains(err.Error(), "sqlite 或 postgres") {
		t.Fatalf("invalid mode: %v", err)
	}
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 8}); err == nil || !strings.Contains(err.Error(), "1 <= idle <= open") {
		t.Fatalf("idle above open: %v", err)
	}
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresMaxOpenConns: 0, PostgresMaxIdleConns: -1}); err == nil {
		t.Fatal("negative idle must be rejected")
	}
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres}); err == nil || !strings.Contains(err.Error(), "缺少连接 URL") {
		t.Fatalf("missing postgres url: %v", err)
	}

	// Injected pool handle is adopted without opening a new pool.
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresPool: w7cFakePool{db: w7cOpenRawSQLite(t, t.TempDir()+"\\w7c-pool.sqlite")}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.db == nil {
		t.Fatal("pool-backed store must expose the pool DB")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("pool close: %v", err)
	}
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store close: %v", err)
	}
}

type w7cFakePool struct{ db *sql.DB }

func (p w7cFakePool) DB() *sql.DB  { return p.db }
func (p w7cFakePool) Close() error { return p.db.Close() }

func TestW7CSQLValueDecoders(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	if _, err := balanceSQLTime(struct{}{}); err == nil {
		t.Fatal("unknown time type must fail")
	}
	if got, err := balanceSQLTime([]byte(now.Format(time.RFC3339Nano))); err != nil || got.Unix() != now.Unix() {
		t.Fatalf("[]byte time: %v %v", got, err)
	}
	if _, err := balanceSQLTime([]byte("garbage")); err == nil {
		t.Fatal("garbage time text must fail")
	}

	if ok, err := balanceSQLBool(int(1)); err != nil || !ok {
		t.Fatalf("int bool: %t %v", ok, err)
	}
	if ok, err := balanceSQLBool("TRUE"); err != nil || !ok {
		t.Fatalf("string true: %t %v", ok, err)
	}
	if ok, err := balanceSQLBool("0"); err != nil || ok {
		t.Fatalf("string 0: %t %v", ok, err)
	}
	if ok, err := balanceSQLBool([]byte("false")); err != nil || ok {
		t.Fatalf("bytes false: %t %v", ok, err)
	}
	if _, err := balanceSQLBool(3.14); err == nil {
		t.Fatal("unknown bool type must fail")
	}
	if _, err := balanceSQLBool("yes"); err == nil {
		t.Fatal("unknown bool text must fail")
	}

	if got, err := balanceStringValue([]byte("raw")); err != nil || got != "raw" {
		t.Fatalf("bytes string: %q %v", got, err)
	}
	if _, err := balanceStringValue(42); err == nil {
		t.Fatal("unknown text type must fail")
	}

	if parseBalanceNullableTime(nil) != nil {
		t.Fatal("nil must map to nil")
	}
	if parseBalanceNullableTime("not-a-time") != nil {
		t.Fatal("invalid time must map to nil")
	}
	if parseBalanceNullableTime(now.Format(time.RFC3339Nano)) == nil {
		t.Fatal("valid time must map to pointer")
	}

	if nilIfEmpty("") != nil || nilIfEmpty("x") != "x" {
		t.Fatal("nilIfEmpty contract broken")
	}
}

func w7cValidBalanceInput(accountID string) Input {
	now := time.Now().UTC()
	due := now.Add(-time.Minute)
	credential, err := NewCredentialEnvelope("w7c-balance-secret", "api_key", map[string]string{"api_key": "sk-w7c"})
	if err != nil {
		panic(err)
	}
	return Input{
		AccountID: accountID, SystemAccountID: "w7c-sys", InputVersion: 5, ConfigRevision: 5,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BaseURL: "https://example.test", Config: QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5},
		APIKey: credential, Credential: credential,
		Trigger: TriggerPeriodic, IssuedAt: now, ExpiresAt: now.Add(time.Minute), NextRefreshAt: &due,
	}
}
