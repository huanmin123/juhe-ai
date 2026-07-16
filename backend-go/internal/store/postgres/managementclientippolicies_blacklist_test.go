package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestManagementClientIPBlacklistSQLUsesBoundedMutations(t *testing.T) {
	source, err := os.ReadFile("queries/w6_system_api_client_ip_allowlist.sql")
	if err != nil {
		t.Fatalf("read client IP policy SQL: %v", err)
	}
	sql := string(source)

	unblockSection := managementClientIPPolicyNamedSQLSection(
		t,
		sql,
		"DisableActiveManagementClientIPBlacklistPolicies",
	)
	for _, required := range []string{
		"UPDATE juhe_stats.client_ip_policies",
		"WHERE ip_hash = sqlc.arg(ip_hash)::text",
		"AND policy_type = 'blacklist'",
		"AND status = 'active'",
	} {
		if !strings.Contains(unblockSection, required) {
			t.Fatalf("unblock query missing %q:\n%s", required, unblockSection)
		}
	}
	upperUnblock := strings.ToUpper(unblockSection)
	if strings.Count(upperUnblock, "UPDATE JUHE_STATS.CLIENT_IP_POLICIES") != 1 ||
		strings.Contains(upperUnblock, "SELECT ") ||
		strings.Contains(upperUnblock, " IN (") {
		t.Fatalf("unblock must use one bounded UPDATE without policy ID pre-read:\n%s", unblockSection)
	}

	insertSection := managementClientIPPolicyNamedSQLSection(
		t,
		sql,
		"InsertManagementClientIPBlacklistPolicy",
	)
	for _, required := range []string{
		"'blacklist'",
		"'active'",
		"sqlc.narg(expires_at)::text",
		"RETURNING",
	} {
		if !strings.Contains(insertSection, required) {
			t.Fatalf("blacklist insert query missing %q:\n%s", required, insertSection)
		}
	}
}

func TestManagementClientIPBlacklistStoreWritesFixedMillisecondExpiryAndMapsSummary(t *testing.T) {
	now := time.Date(
		2026,
		time.July,
		12,
		16,
		30,
		0,
		123456789,
		time.FixedZone("UTC+8", 8*60*60),
	)
	expiresAt := time.Date(
		2026,
		time.July,
		20,
		18,
		45,
		12,
		987654321,
		time.FixedZone("UTC+8", 8*60*60),
	)
	reason := "异常流量"
	q := &managementClientIPPolicyQueriesStub{
		insertBlacklistRow: postgresqueries.JuheStatsClientIpPolicy{
			ID:                       "ip_policy_blacklist",
			IpHash:                   strings.Repeat("a", 64),
			PolicyType:               "blacklist",
			Status:                   "active",
			Reason:                   pgtype.Text{String: reason, Valid: true},
			ExpiresAt:                pgtype.Text{String: "2026-07-20T10:45:12.987Z", Valid: true},
			CreatedBySystemAccountID: "sys_admin",
			CreatedAt:                "2026-07-12T08:30:00.123Z",
			UpdatedAt:                "2026-07-12T08:30:00.123Z",
		},
		disableBlacklistCount: 1,
	}

	summary, err := insertManagementClientIPBlacklistPolicy(
		context.Background(),
		q,
		port.ManagementClientIPBlacklistCreateInput{
			ID:                   "ip_policy_blacklist",
			IPHash:               strings.Repeat("a", 64),
			Reason:               &reason,
			ExpiresAt:            &expiresAt,
			ActorSystemAccountID: "sys_admin",
			Now:                  now,
		},
	)
	if err != nil {
		t.Fatalf("insert blacklist: %v", err)
	}
	if q.insertBlacklistArg.CreatedAt != "2026-07-12T08:30:00.123Z" ||
		q.insertBlacklistArg.UpdatedAt != q.insertBlacklistArg.CreatedAt ||
		!q.insertBlacklistArg.ExpiresAt.Valid ||
		q.insertBlacklistArg.ExpiresAt.String != "2026-07-20T10:45:12.987Z" ||
		!q.insertBlacklistArg.Reason.Valid ||
		q.insertBlacklistArg.Reason.String != reason {
		t.Fatalf("insert blacklist arg = %+v", q.insertBlacklistArg)
	}
	if summary.ID != "ip_policy_blacklist" ||
		summary.PolicyType != port.ManagementClientIPPolicyTypeBlacklist ||
		summary.Status != port.ManagementClientIPPolicyStatusActive ||
		summary.ExpiresAt == nil ||
		summary.ExpiresAt.Format("2006-01-02T15:04:05.000Z") != "2026-07-20T10:45:12.987Z" ||
		!summary.CreatedAt.Equal(now.UTC().Truncate(time.Millisecond)) ||
		!summary.UpdatedAt.Equal(now.UTC().Truncate(time.Millisecond)) {
		t.Fatalf("blacklist summary = %+v", summary)
	}

	disableInput := port.ManagementClientIPPolicyDisableInput{
		IPHash:               strings.Repeat("a", 64),
		ActorSystemAccountID: "sys_admin",
		Reason:               "管理员解除策略",
		Now:                  now,
	}
	count, err := disableActiveManagementClientIPBlacklistPolicies(
		context.Background(),
		q,
		disableInput,
	)
	if err != nil || count != 1 ||
		q.disableBlacklistArg.DisabledAt != "2026-07-12T08:30:00.123Z" ||
		q.disableBlacklistArg.UpdatedAt != q.disableBlacklistArg.DisabledAt ||
		q.disableBlacklistArg.DisabledBySystemAccountID != "sys_admin" ||
		q.disableBlacklistArg.DisabledReason != "管理员解除策略" ||
		q.disableBlacklistArg.IpHash != strings.Repeat("a", 64) {
		t.Fatalf(
			"disable blacklist count=%d arg=%+v error=%v",
			count,
			q.disableBlacklistArg,
			err,
		)
	}
}

func TestManagementClientIPBlacklistStoreWritesNullPermanentExpiry(t *testing.T) {
	now := time.Date(2026, time.July, 12, 8, 30, 0, 0, time.UTC)
	q := &managementClientIPPolicyQueriesStub{
		insertBlacklistRow: postgresqueries.JuheStatsClientIpPolicy{
			ID:                       "ip_policy_permanent",
			IpHash:                   strings.Repeat("b", 64),
			PolicyType:               "blacklist",
			Status:                   "active",
			CreatedBySystemAccountID: "sys_admin",
			CreatedAt:                "2026-07-12T08:30:00.000Z",
			UpdatedAt:                "2026-07-12T08:30:00.000Z",
		},
	}

	summary, err := insertManagementClientIPBlacklistPolicy(
		context.Background(),
		q,
		port.ManagementClientIPBlacklistCreateInput{
			ID:                   "ip_policy_permanent",
			IPHash:               strings.Repeat("b", 64),
			ActorSystemAccountID: "sys_admin",
			Now:                  now,
		},
	)
	if err != nil {
		t.Fatalf("insert permanent blacklist: %v", err)
	}
	if q.insertBlacklistArg.ExpiresAt.Valid || summary.ExpiresAt != nil {
		t.Fatalf("permanent blacklist expiry arg=%+v summary=%+v", q.insertBlacklistArg.ExpiresAt, summary.ExpiresAt)
	}
}

func TestManagementClientIPBlacklistStoreWrapsMutationErrors(t *testing.T) {
	queryErr := errors.New("query unavailable")
	now := time.Date(2026, time.July, 12, 8, 30, 0, 0, time.UTC)

	_, err := insertManagementClientIPBlacklistPolicy(
		context.Background(),
		&managementClientIPPolicyQueriesStub{insertBlacklistErr: queryErr},
		port.ManagementClientIPBlacklistCreateInput{Now: now},
	)
	if !errors.Is(err, queryErr) ||
		!strings.Contains(err.Error(), "insert management client IP blacklist policy") {
		t.Fatalf("insert blacklist error = %v", err)
	}

	_, err = disableActiveManagementClientIPBlacklistPolicies(
		context.Background(),
		&managementClientIPPolicyQueriesStub{disableBlacklistErr: queryErr},
		port.ManagementClientIPPolicyDisableInput{Now: now},
	)
	if !errors.Is(err, queryErr) ||
		!strings.Contains(err.Error(), "disable active management client IP blacklist policies") {
		t.Fatalf("disable blacklist error = %v", err)
	}
}
