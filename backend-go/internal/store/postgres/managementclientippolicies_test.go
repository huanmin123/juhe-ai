package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestManagementClientIPPolicySQLUsesLockedBoundedMutations(t *testing.T) {
	source, err := os.ReadFile("queries/w6_system_api_client_ip_allowlist.sql")
	if err != nil {
		t.Fatalf("read client IP policy SQL: %v", err)
	}
	sql := string(source)

	lockSection := managementClientIPPolicyNamedSQLSection(
		t,
		sql,
		"LockManagementClientIPRegistry",
	)
	if !regexp.MustCompile(`(?im)^\s*FOR\s+UPDATE\s*;\s*$`).MatchString(lockSection) {
		t.Fatalf("registry lock query must contain independent FOR UPDATE:\n%s", lockSection)
	}
	if !strings.Contains(lockSection, "WHERE ip_hash = sqlc.arg(ip_hash)::text") {
		t.Fatalf("registry lock query must target one exact hash:\n%s", lockSection)
	}

	replaceSection := managementClientIPPolicyNamedSQLSection(
		t,
		sql,
		"DisableActiveManagementClientIPPolicies",
	)
	if !strings.Contains(replaceSection, "AND status = 'active'") ||
		strings.Contains(replaceSection, "policy_type =") {
		t.Fatalf("allowlist replacement must disable every active policy for the hash:\n%s", replaceSection)
	}

	unallowlistSection := managementClientIPPolicyNamedSQLSection(
		t,
		sql,
		"DisableActiveManagementClientIPAllowlistPolicies",
	)
	for _, required := range []string{
		"AND policy_type = 'allowlist'",
		"AND status = 'active'",
	} {
		if !strings.Contains(unallowlistSection, required) {
			t.Fatalf("unallowlist query missing %q:\n%s", required, unallowlistSection)
		}
	}

	insertSection := managementClientIPPolicyNamedSQLSection(
		t,
		sql,
		"InsertManagementClientIPAllowlistPolicy",
	)
	for _, required := range []string{
		"'allowlist'",
		"'active'",
		"NULL",
		"RETURNING",
	} {
		if !strings.Contains(insertSection, required) {
			t.Fatalf("allowlist insert query missing %q:\n%s", required, insertSection)
		}
	}
}

func TestManagementClientIPPolicyStoreMapsLockedRegistryAndNotFound(t *testing.T) {
	q := &managementClientIPPolicyQueriesStub{
		lockRow: postgresqueries.LockManagementClientIPRegistryRow{
			IpHash:   strings.Repeat("a", 64),
			ClientIp: "203.0.113.8",
		},
	}
	row, found, err := lockManagementClientIPRegistry(
		context.Background(),
		q,
		strings.Repeat("a", 64),
	)
	if err != nil || !found ||
		row.IPHash != strings.Repeat("a", 64) ||
		row.ClientIP != "203.0.113.8" ||
		q.lockIPHash != strings.Repeat("a", 64) {
		t.Fatalf("locked registry row=%+v found=%v error=%v query=%q", row, found, err, q.lockIPHash)
	}

	q.lockErr = pgx.ErrNoRows
	row, found, err = lockManagementClientIPRegistry(
		context.Background(),
		q,
		strings.Repeat("b", 64),
	)
	if err != nil || found || !reflect.DeepEqual(row, port.ManagementClientIPRegistryRow{}) {
		t.Fatalf("missing registry row=%+v found=%v error=%v", row, found, err)
	}
}

func TestManagementClientIPPolicyInTxUsesTransactionBoundStoreAndCommits(t *testing.T) {
	tx := &managementClientIPPolicyTxStub{}
	q := &managementClientIPPolicyQueriesStub{
		lockRow: postgresqueries.LockManagementClientIPRegistryRow{
			IpHash:   strings.Repeat("a", 64),
			ClientIp: "203.0.113.8",
		},
		disableAllowlistCount: 0,
	}
	var queriesTx pgx.Tx
	callbackCalls := 0

	err := managementClientIPPolicyInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			return tx, nil
		},
		func(received pgx.Tx) managementClientIPPolicyQueries {
			queriesTx = received
			return q
		},
		func(ctx context.Context, store port.ManagementClientIPPolicyStore) error {
			callbackCalls++
			row, found, err := store.LockManagementClientIPRegistry(
				ctx,
				strings.Repeat("a", 64),
			)
			if err != nil || !found || row.ClientIP != "203.0.113.8" {
				t.Fatalf("locked row=%+v found=%v error=%v", row, found, err)
			}
			count, err := store.DisableActiveManagementClientIPAllowlistPolicies(
				ctx,
				port.ManagementClientIPPolicyDisableInput{
					IPHash:               strings.Repeat("a", 64),
					ActorSystemAccountID: "sys_admin",
					Reason:               "管理员解除策略",
					Now:                  time.Date(2026, 7, 12, 8, 30, 0, 0, time.UTC),
				},
			)
			if err != nil || count != 0 {
				t.Fatalf("zero-row unallowlist count=%d error=%v", count, err)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("managementClientIPPolicyInTx() error = %v", err)
	}
	if queriesTx != tx {
		t.Fatalf("queries transaction = %T %p, want tx %p", queriesTx, queriesTx, tx)
	}
	if callbackCalls != 1 || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf(
			"callback/commit/rollback calls = %d/%d/%d, want 1/1/0",
			callbackCalls,
			tx.commitCalls,
			tx.rollbackCalls,
		)
	}
}

func TestManagementClientIPPolicyInTxRollsBackCallbackFailure(t *testing.T) {
	tx := &managementClientIPPolicyTxStub{}
	callbackErr := errors.New("policy mutation failed")

	err := managementClientIPPolicyInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			return tx, nil
		},
		func(pgx.Tx) managementClientIPPolicyQueries {
			return &managementClientIPPolicyQueriesStub{}
		},
		func(context.Context, port.ManagementClientIPPolicyStore) error {
			return callbackErr
		},
	)
	if !errors.Is(err, callbackErr) {
		t.Fatalf("managementClientIPPolicyInTx() error = %v, want callback failure", err)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("commit/rollback calls = %d/%d, want 0/1", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestManagementClientIPPolicyInTxRollsBackCommitFailure(t *testing.T) {
	commitErr := errors.New("commit failed")
	tx := &managementClientIPPolicyTxStub{commitErr: commitErr}

	err := managementClientIPPolicyInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			return tx, nil
		},
		func(pgx.Tx) managementClientIPPolicyQueries {
			return &managementClientIPPolicyQueriesStub{}
		},
		func(context.Context, port.ManagementClientIPPolicyStore) error {
			return nil
		},
	)
	if !errors.Is(err, commitErr) ||
		!strings.Contains(err.Error(), "commit management client IP policy tx") {
		t.Fatalf("managementClientIPPolicyInTx() error = %v, want commit failure", err)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 1 {
		t.Fatalf("commit/rollback calls = %d/%d, want 1/1", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestManagementClientIPPolicyStoreWritesUTCAndMapsSummary(t *testing.T) {
	now := time.Date(2026, time.July, 12, 16, 30, 0, 123456789, time.FixedZone("UTC+8", 8*60*60))
	reason := "可信来源"
	q := &managementClientIPPolicyQueriesStub{
		insertRow: postgresqueries.JuheStatsClientIpPolicy{
			ID:                       "ip_policy_1",
			IpHash:                   strings.Repeat("c", 64),
			PolicyType:               "allowlist",
			Status:                   "active",
			Reason:                   pgtype.Text{String: reason, Valid: true},
			CreatedBySystemAccountID: "sys_admin",
			CreatedAt:                "2026-07-12T08:30:00.123Z",
			UpdatedAt:                "2026-07-12T08:30:00.123Z",
		},
		disableAllCount:       2,
		disableAllowlistCount: 1,
	}
	disableInput := port.ManagementClientIPPolicyDisableInput{
		IPHash:               strings.Repeat("c", 64),
		ActorSystemAccountID: "sys_admin",
		Reason:               "被新的白名单策略替换",
		Now:                  now,
	}
	count, err := disableActiveManagementClientIPPolicies(context.Background(), q, disableInput)
	if err != nil || count != 2 {
		t.Fatalf("disable all count=%d error=%v", count, err)
	}
	if q.disableAllArg.DisabledAt != "2026-07-12T08:30:00.123Z" ||
		q.disableAllArg.UpdatedAt != q.disableAllArg.DisabledAt ||
		q.disableAllArg.DisabledBySystemAccountID != "sys_admin" ||
		q.disableAllArg.DisabledReason != "被新的白名单策略替换" {
		t.Fatalf("disable all arg=%+v", q.disableAllArg)
	}

	summary, err := insertManagementClientIPAllowlistPolicy(
		context.Background(),
		q,
		port.ManagementClientIPAllowlistCreateInput{
			ID:                   "ip_policy_1",
			IPHash:               strings.Repeat("c", 64),
			Reason:               &reason,
			ActorSystemAccountID: "sys_admin",
			Now:                  now,
		},
	)
	if err != nil {
		t.Fatalf("insert allowlist: %v", err)
	}
	if summary.ID != "ip_policy_1" ||
		summary.PolicyType != port.ManagementClientIPPolicyTypeAllowlist ||
		summary.Status != port.ManagementClientIPPolicyStatusActive ||
		summary.Reason == nil || *summary.Reason != reason ||
		!summary.CreatedAt.Equal(now.UTC().Truncate(time.Millisecond)) ||
		!summary.UpdatedAt.Equal(now.UTC().Truncate(time.Millisecond)) ||
		summary.ExpiresAt != nil ||
		summary.DisabledAt != nil ||
		summary.DisabledBySystemAccountID != nil ||
		summary.DisabledReason != nil {
		t.Fatalf("allowlist summary=%+v", summary)
	}
	if q.insertArg.CreatedAt != "2026-07-12T08:30:00.123Z" ||
		q.insertArg.UpdatedAt != q.insertArg.CreatedAt ||
		!q.insertArg.Reason.Valid || q.insertArg.Reason.String != reason {
		t.Fatalf("insert arg=%+v", q.insertArg)
	}

	disableInput.Reason = "管理员解除策略"
	count, err = disableActiveManagementClientIPAllowlistPolicies(
		context.Background(),
		q,
		disableInput,
	)
	if err != nil || count != 1 ||
		q.disableAllowlistArg.DisabledReason != "管理员解除策略" {
		t.Fatalf(
			"disable allowlist count=%d arg=%+v error=%v",
			count,
			q.disableAllowlistArg,
			err,
		)
	}
}

func TestManagementClientIPPolicyStoreRejectsInvalidPersistedTimes(t *testing.T) {
	q := &managementClientIPPolicyQueriesStub{
		insertRow: postgresqueries.JuheStatsClientIpPolicy{
			ID:                       "ip_policy_bad",
			IpHash:                   strings.Repeat("d", 64),
			PolicyType:               "allowlist",
			Status:                   "active",
			CreatedBySystemAccountID: "sys_admin",
			CreatedAt:                "not-a-time",
			UpdatedAt:                time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	_, err := insertManagementClientIPAllowlistPolicy(
		context.Background(),
		q,
		port.ManagementClientIPAllowlistCreateInput{
			ID:                   "ip_policy_bad",
			IPHash:               strings.Repeat("d", 64),
			ActorSystemAccountID: "sys_admin",
			Now:                  time.Now(),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "created_at") {
		t.Fatalf("invalid persisted time error=%v", err)
	}
}

func TestManagementClientIPPolicyStoreWrapsQueryErrors(t *testing.T) {
	queryErr := errors.New("query unavailable")
	q := &managementClientIPPolicyQueriesStub{lockErr: queryErr}
	_, _, err := lockManagementClientIPRegistry(context.Background(), q, strings.Repeat("e", 64))
	if !errors.Is(err, queryErr) ||
		!strings.Contains(err.Error(), "lock management client IP registry") {
		t.Fatalf("lock error=%v", err)
	}
}

func managementClientIPPolicyNamedSQLSection(
	t *testing.T,
	source string,
	name string,
) string {
	t.Helper()
	marker := "-- name: " + name + " "
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("named SQL query %s not found", name)
	}
	rest := source[start+len(marker):]
	next := strings.Index(rest, "\n-- name: ")
	if next >= 0 {
		return rest[:next]
	}
	return rest
}

type managementClientIPPolicyQueriesStub struct {
	lockRow    postgresqueries.LockManagementClientIPRegistryRow
	lockErr    error
	lockIPHash string

	disableAllCount int64
	disableAllErr   error
	disableAllArg   postgresqueries.DisableActiveManagementClientIPPoliciesParams

	insertRow postgresqueries.JuheStatsClientIpPolicy
	insertErr error
	insertArg postgresqueries.InsertManagementClientIPAllowlistPolicyParams

	insertBlacklistRow postgresqueries.JuheStatsClientIpPolicy
	insertBlacklistErr error
	insertBlacklistArg postgresqueries.InsertManagementClientIPBlacklistPolicyParams

	disableAllowlistCount int64
	disableAllowlistErr   error
	disableAllowlistArg   postgresqueries.DisableActiveManagementClientIPAllowlistPoliciesParams

	disableBlacklistCount int64
	disableBlacklistErr   error
	disableBlacklistArg   postgresqueries.DisableActiveManagementClientIPBlacklistPoliciesParams
}

type managementClientIPPolicyTxStub struct {
	pgx.Tx
	commitErr     error
	rollbackErr   error
	commitCalls   int
	rollbackCalls int
}

func (s *managementClientIPPolicyTxStub) Commit(context.Context) error {
	s.commitCalls++
	return s.commitErr
}

func (s *managementClientIPPolicyTxStub) Rollback(context.Context) error {
	s.rollbackCalls++
	return s.rollbackErr
}

var _ pgx.Tx = (*managementClientIPPolicyTxStub)(nil)

func (s *managementClientIPPolicyQueriesStub) LockManagementClientIPRegistry(
	_ context.Context,
	ipHash string,
) (postgresqueries.LockManagementClientIPRegistryRow, error) {
	s.lockIPHash = ipHash
	return s.lockRow, s.lockErr
}

func (s *managementClientIPPolicyQueriesStub) DisableActiveManagementClientIPPolicies(
	_ context.Context,
	arg postgresqueries.DisableActiveManagementClientIPPoliciesParams,
) (int64, error) {
	s.disableAllArg = arg
	return s.disableAllCount, s.disableAllErr
}

func (s *managementClientIPPolicyQueriesStub) InsertManagementClientIPAllowlistPolicy(
	_ context.Context,
	arg postgresqueries.InsertManagementClientIPAllowlistPolicyParams,
) (postgresqueries.JuheStatsClientIpPolicy, error) {
	s.insertArg = arg
	return s.insertRow, s.insertErr
}

func (s *managementClientIPPolicyQueriesStub) InsertManagementClientIPBlacklistPolicy(
	_ context.Context,
	arg postgresqueries.InsertManagementClientIPBlacklistPolicyParams,
) (postgresqueries.JuheStatsClientIpPolicy, error) {
	s.insertBlacklistArg = arg
	return s.insertBlacklistRow, s.insertBlacklistErr
}

func (s *managementClientIPPolicyQueriesStub) DisableActiveManagementClientIPAllowlistPolicies(
	_ context.Context,
	arg postgresqueries.DisableActiveManagementClientIPAllowlistPoliciesParams,
) (int64, error) {
	s.disableAllowlistArg = arg
	return s.disableAllowlistCount, s.disableAllowlistErr
}

func (s *managementClientIPPolicyQueriesStub) DisableActiveManagementClientIPBlacklistPolicies(
	_ context.Context,
	arg postgresqueries.DisableActiveManagementClientIPBlacklistPoliciesParams,
) (int64, error) {
	s.disableBlacklistArg = arg
	return s.disableBlacklistCount, s.disableBlacklistErr
}
