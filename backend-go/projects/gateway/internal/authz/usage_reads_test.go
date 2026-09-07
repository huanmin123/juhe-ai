// Store-level replay tests for the authorization usage window reads and the
// revoke owner scope (BUG-0165): window rows are inserted verbatim and the
// aggregated reads are asserted against the Node
// authorization-usage.repository.ts behavior (scope key, stable ordering,
// pagination upper bound, zero-value summaries, timezone window defaults).
package authz

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

const usageFixtureDDL = `
	CREATE TABLE IF NOT EXISTS authorization_team_usage_range_windows (
		system_account_id TEXT NOT NULL,
		start_date TEXT NOT NULL,
		end_date TEXT NOT NULL,
		team_filter_id TEXT NOT NULL DEFAULT '',
		resource_filter_type TEXT NOT NULL DEFAULT 'all',
		resource_filter_id TEXT NOT NULL DEFAULT '',
		request_count INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_cost_usd REAL NOT NULL DEFAULT 0,
		cache_write_tokens INTEGER NOT NULL DEFAULT 0,
		cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
		cache_write_cost_usd REAL NOT NULL DEFAULT 0,
		thinking_tokens INTEGER NOT NULL DEFAULT 0,
		input_image_tokens INTEGER NOT NULL DEFAULT 0,
		output_image_tokens INTEGER NOT NULL DEFAULT 0,
		total_cost_usd REAL NOT NULL DEFAULT 0,
		last_used_at TEXT,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id)
	);
	CREATE TABLE IF NOT EXISTS authorization_user_usage_range_windows (
		system_account_id TEXT NOT NULL,
		start_date TEXT NOT NULL,
		end_date TEXT NOT NULL,
		team_filter_id TEXT NOT NULL DEFAULT '',
		grantee_filter_system_account_id TEXT NOT NULL DEFAULT '',
		resource_filter_type TEXT NOT NULL DEFAULT 'all',
		resource_filter_id TEXT NOT NULL DEFAULT '',
		request_count INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_cost_usd REAL NOT NULL DEFAULT 0,
		cache_write_tokens INTEGER NOT NULL DEFAULT 0,
		cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
		cache_write_cost_usd REAL NOT NULL DEFAULT 0,
		thinking_tokens INTEGER NOT NULL DEFAULT 0,
		input_image_tokens INTEGER NOT NULL DEFAULT 0,
		output_image_tokens INTEGER NOT NULL DEFAULT 0,
		total_cost_usd REAL NOT NULL DEFAULT 0,
		last_used_at TEXT,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)
	);
	CREATE TABLE IF NOT EXISTS usage_scope_range_windows (
		system_account_id TEXT NOT NULL,
		scope_type TEXT NOT NULL,
		scope_id TEXT NOT NULL,
		start_date TEXT NOT NULL,
		end_date TEXT NOT NULL,
		request_count INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_cost_usd REAL NOT NULL DEFAULT 0,
		cache_write_tokens INTEGER NOT NULL DEFAULT 0,
		cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
		cache_write_cost_usd REAL NOT NULL DEFAULT 0,
		thinking_tokens INTEGER NOT NULL DEFAULT 0,
		input_image_tokens INTEGER NOT NULL DEFAULT 0,
		output_image_tokens INTEGER NOT NULL DEFAULT 0,
		total_cost_usd REAL NOT NULL DEFAULT 0,
		last_used_at TEXT,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, scope_type, scope_id, start_date, end_date)
	);
	CREATE TABLE IF NOT EXISTS system_settings (
		system_account_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value_json TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, key)
	);
`

func newUsageFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	if _, err := f.db.Exec(usageFixtureDDL); err != nil {
		t.Fatal(err)
	}
	// The usage reads target the stats database; the single-file fixture
	// shares the business handle exactly like the Node single-file SQLite
	// mode reaches both schemas through one process.
	f.store.AttachStatsDatabase(f.db)
	f.store.AttachTimezoneSource(func(ctx context.Context) (string, error) {
		return "UTC", nil
	})
	return f
}

// seedResourceAccount seeds the accounts projection the resource name/owner
// lookups read (loadAccountLookupMap).
func seedResourceAccount(t *testing.T, f *fixture, id, ownerID, name string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name) VALUES (?, ?, ?)`,
		id, ownerID, name); err != nil {
		t.Fatal(err)
	}
}

func insertTeamWindowRow(t *testing.T, f *fixture, systemAccountID, teamID, resourceType, resourceID string, requestCount, inputTokens, outputTokens, totalCost float64, lastUsedAt any) {
	t.Helper()
	_, err := f.db.Exec(`INSERT INTO authorization_team_usage_range_windows
		(system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
		 request_count, input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at)
		VALUES (?, '2026-08-08', '2026-09-06', ?, ?, ?, ?, ?, ?, ?, ?, '2026-09-06T00:00:00.000Z')`,
		systemAccountID, teamID, resourceType, resourceID, requestCount, inputTokens, outputTokens, totalCost, lastUsedAt)
	if err != nil {
		t.Fatal(err)
	}
}

func insertUserWindowRow(t *testing.T, f *fixture, systemAccountID, teamID, granteeID, resourceType, resourceID string, requestCount, inputTokens, outputTokens, totalCost float64, lastUsedAt any) {
	t.Helper()
	_, err := f.db.Exec(`INSERT INTO authorization_user_usage_range_windows
		(system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
		 request_count, input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at)
		VALUES (?, '2026-08-08', '2026-09-06', ?, ?, ?, ?, ?, ?, ?, ?, ?, '2026-09-06T00:00:00.000Z')`,
		systemAccountID, teamID, granteeID, resourceType, resourceID, requestCount, inputTokens, outputTokens, totalCost, lastUsedAt)
	if err != nil {
		t.Fatal(err)
	}
}

func decodeUsageJSON(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("decode usage payload: %v (%s)", err, payload)
	}
	return doc
}

// TestUsageWindowTeamReadsReplay replays the team window family: scope keys
// (global for unscoped admins, filter for scoped admins, self for users),
// cost-desc stable ordering, pagination upper bound and the precise summary
// row lookup with the zero-value fallback.
func TestUsageWindowTeamReadsReplay(t *testing.T) {
	f := newUsageFixture(t)
	f.seedTeamWithMember(t, "team_cheap", "member1")
	f.seedTeamWithMember(t, "team_pricey", "member2")
	f.seedAccount(t, "owner1", "active")
	f.seedAccount(t, "owner2", "active")
	f.seedGroup(t, "grp1", "owner1")
	seedResourceAccount(t, f, "acc1", "owner1", "Account acc1")
	seedResourceAccount(t, f, "acc2", "owner2", "Account acc2")
	// Window rows mirror the writer shape (usage-stats-authorization-daily-writer.ts
	// authorizationReportScopeRows): owner rows plus the identical global
	// copy; team_filter_id carries the team source and the empty team id marks
	// direct grants (excluded from the team report by `team_filter_id <> ''`).
	insertTeamWindowRow(t, f, "owner1", "team_cheap", "account", "acc1", 10, 100, 50, 1.5, "2026-09-06T01:00:00.000Z")
	insertTeamWindowRow(t, f, "owner1", "team_pricey", "group", "grp1", 5, 200, 20, 2.5, "2026-09-06T02:00:00.000Z")
	insertTeamWindowRow(t, f, "owner1", "", "account", "acc1", 77, 1, 1, 9.9, "2026-09-06T06:00:00.000Z")
	insertTeamWindowRow(t, f, "owner2", "team_other", "account", "acc2", 99, 1, 1, 9.9, "2026-09-06T03:00:00.000Z")
	insertTeamWindowRow(t, f, "global", "team_pricey", "group", "grp1", 15, 300, 70, 4.0, "2026-09-06T04:00:00.000Z")
	rng := UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06", Days: 31, MaxDays: 31}
	ctx := context.Background()

	// Unscoped administrator reads the 'global' aggregate rows only
	// (authorizationReportFilterKey: systemAccountId ?? 'global').
	result, err := f.store.teamUsageRows(ctx, UsageFilters{}, accessInfo{ViewerID: "admin", IsAdmin: true}, rng, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].TeamID != "team_pricey" || result.Rows[0].ResourceID != "grp1" {
		t.Fatalf("global rows = %+v", result.Rows)
	}
	if result.Rows[0].TeamName != "Team team_pricey" || result.Rows[0].ResourceName != "Group grp1" {
		t.Fatalf("global projection = %+v", result.Rows[0])
	}
	if result.Rows[0].Usage.RequestCount != 15 || result.Rows[0].Usage.TotalTokens != 370 || result.Rows[0].Usage.TotalCost != 4 {
		t.Fatalf("global usage = %+v", result.Rows[0].Usage)
	}

	// Scoped administrator: the owner scope narrows and the stable order is
	// total_cost DESC first (Node ORDER BY).
	scoped, err := f.store.teamUsageRows(ctx, UsageFilters{}, accessInfo{ViewerID: "admin", IsAdmin: true, FilterID: "owner1"}, rng, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Rows) != 2 {
		t.Fatalf("scoped rows = %+v", scoped.Rows)
	}
	if scoped.Rows[0].TeamID != "team_pricey" || scoped.Rows[1].TeamID != "team_cheap" {
		t.Fatalf("scoped order = %s,%s", scoped.Rows[0].TeamID, scoped.Rows[1].TeamID)
	}
	if scoped.Rows[0].TeamName != "Team team_pricey" || scoped.Rows[0].ResourceName != "Group grp1" {
		t.Fatalf("scoped projection = %+v", scoped.Rows[0])
	}
	if scoped.Rows[0].AccountOwnerSystemAccountID != "owner1" || scoped.Rows[0].AccountOwnerSystemAccountName != "owner1" {
		t.Fatalf("scoped owner projection = %+v", scoped.Rows[0])
	}

	// Team filter keeps only the matching rows.
	filtered, err := f.store.teamUsageRows(ctx, UsageFilters{TeamID: "team_cheap"}, accessInfo{ViewerID: "admin", IsAdmin: true, FilterID: "owner1"}, rng, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Rows) != 1 || filtered.Rows[0].TeamID != "team_cheap" {
		t.Fatalf("filtered rows = %+v", filtered.Rows)
	}

	// Pagination: pageSize 1 reports hasMore and the (page-1)*n+loaded+1 upper
	// bound (pagedTotalUpperBound).
	paged, err := f.store.teamUsageRows(ctx, UsageFilters{}, accessInfo{ViewerID: "admin", IsAdmin: true, FilterID: "owner1"}, rng, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(paged.Rows) != 1 || !paged.HasMore || paged.Total != 2 || paged.Page != 1 || paged.PageSize != 1 {
		t.Fatalf("paged result = %+v", paged)
	}

	// A plain user is pinned to their own owner key and never sees other
	// owners' aggregates.
	self, err := f.store.teamUsageRows(ctx, UsageFilters{}, accessInfo{ViewerID: "owner1"}, rng, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(self.Rows) != 2 {
		t.Fatalf("self rows = %+v", self.Rows)
	}
	other, err := f.store.teamUsageRows(ctx, UsageFilters{}, accessInfo{ViewerID: "owner2"}, rng, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Rows) != 1 || other.Rows[0].TeamID != "team_other" {
		t.Fatalf("owner2 rows = %+v", other.Rows)
	}
	// No identity → no scope key → the empty page (never another owner's
	// data).
	denied, err := f.store.teamUsageRows(ctx, UsageFilters{}, accessInfo{}, rng, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(denied.Rows) != 0 || denied.Total != 0 || denied.HasMore {
		t.Fatalf("denied rows = %+v", denied)
	}

	// Resource filter without a type leaves the resource predicate open;
	// with type+id only that row matches.
	resource, err := f.store.teamUsageRows(ctx, UsageFilters{ResourceType: "group", ResourceID: "grp1"}, accessInfo{ViewerID: "admin", IsAdmin: true, FilterID: "owner1"}, rng, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(resource.Rows) != 1 || resource.Rows[0].ResourceID != "grp1" {
		t.Fatalf("resource rows = %+v", resource.Rows)
	}

	// Summary reads the exact pre-aggregated row (the same (owner, window,
	// team, resource) key Node uses).
	summary, err := f.store.teamUsageSummary(ctx, UsageFilters{TeamID: "team_cheap", ResourceType: "account", ResourceID: "acc1"}, accessInfo{ViewerID: "admin", IsAdmin: true, FilterID: "owner1"}, rng)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Summary.RequestCount != 10 || summary.Summary.InputTokens != 100 || summary.Summary.CacheWriteTokens != 0 ||
		summary.Summary.TotalTokens != 150 || summary.Summary.TotalCost != 1.5 {
		t.Fatalf("summary = %+v", summary.Summary)
	}
	if summary.Summary.LastUsedAt == nil || *summary.Summary.LastUsedAt != "2026-09-06T01:00:00.000Z" {
		t.Fatalf("summary lastUsedAt = %v", summary.Summary.LastUsedAt)
	}
	missing, err := f.store.teamUsageSummary(ctx, UsageFilters{TeamID: "team_none"}, accessInfo{ViewerID: "admin", IsAdmin: true, FilterID: "owner1"}, rng)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Summary.RequestCount != 0 || missing.Summary.TotalCost != 0 || missing.Summary.LastUsedAt != nil {
		t.Fatalf("missing summary = %+v", missing.Summary)
	}
	if missing.Range.StartDate != "2026-08-08" || missing.Range.EndDate != "2026-09-06" {
		t.Fatalf("missing range = %+v", missing.Range)
	}
}

// TestUsageWindowUserReadsReplay replays the user window family: the team
// filter matches unconditionally (the empty team_filter_id rows), the grantee
// filter is optional, and the projections carry the member principal and the
// resource owner name.
func TestUsageWindowUserReadsReplay(t *testing.T) {
	f := newUsageFixture(t)
	f.seedAccount(t, "owner1", "active")
	f.seedAccount(t, "grantee1", "active")
	f.seedTeamWithMember(t, "team_1", "grantee1")
	f.seedGroup(t, "grp1", "owner1")
	seedResourceAccount(t, f, "acc1", "owner1", "Account acc1")
	// Direct rows carry the empty team_filter_id and are the only ones the
	// unfiltered user report matches (`report.team_filter_id = ''`).
	insertUserWindowRow(t, f, "owner1", "", "grantee1", "account", "acc1", 7, 70, 35, 0.7, "2026-09-06T05:00:00.000Z")
	insertUserWindowRow(t, f, "owner1", "team_1", "grantee1", "group", "grp1", 3, 30, 15, 0.3, nil)
	insertUserWindowRow(t, f, "owner2", "", "grantee9", "account", "acc9", 42, 1, 1, 9.9, nil)
	rng := UsageStatsRange{StartDate: "2026-08-08", EndDate: "2026-09-06", Days: 31, MaxDays: 31}
	ctx := context.Background()

	result, err := f.store.userUsageRows(ctx, UsageFilters{}, accessInfo{ViewerID: "owner1"}, rng, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("user rows = %+v", result.Rows)
	}
	first := result.Rows[0]
	if first.ID != "grantee1:account:acc1" || first.UserName != "grantee1" || first.Username != "grantee1" {
		t.Fatalf("user projection = %+v", first)
	}
	if first.AccountOwnerSystemAccountName != "owner1" {
		t.Fatalf("user owner name = %+v", first)
	}
	if first.Usage.RequestCount != 7 || first.Usage.TotalTokens != 105 || first.Usage.TotalCost != 0.7 {
		t.Fatalf("user usage = %+v", first.Usage)
	}
	if first.LastUsedAt != "2026-09-06T05:00:00.000Z" {
		t.Fatalf("user lastUsedAt = %q", first.LastUsedAt)
	}

	// The team filter matches unconditionally: only the team-sourced row
	// survives, projected with the team name array.
	teamFiltered, err := f.store.userUsageRows(ctx, UsageFilters{TeamID: "team_1"}, accessInfo{ViewerID: "owner1"}, rng, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(teamFiltered.Rows) != 1 || teamFiltered.Rows[0].ResourceType != "group" {
		t.Fatalf("team filtered rows = %+v", teamFiltered.Rows)
	}
	if len(teamFiltered.Rows[0].TeamNames) != 1 || teamFiltered.Rows[0].TeamNames[0] != "Team team_1" {
		t.Fatalf("team names = %+v", teamFiltered.Rows[0].TeamNames)
	}
	if teamFiltered.Rows[0].Usage.TotalTokens != 45 || teamFiltered.Rows[0].LastUsedAt != "" {
		t.Fatalf("team filtered usage = %+v", teamFiltered.Rows[0].Usage)
	}

	// Grantee filter narrows within the team window.
	granteeFiltered, err := f.store.userUsageRows(ctx, UsageFilters{TeamID: "team_1", GranteeID: "grantee1"}, accessInfo{ViewerID: "owner1"}, rng, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(granteeFiltered.Rows) != 1 {
		t.Fatalf("grantee filtered rows = %+v", granteeFiltered.Rows)
	}
	granteeMiss, err := f.store.userUsageRows(ctx, UsageFilters{TeamID: "team_1", GranteeID: "grantee9"}, accessInfo{ViewerID: "owner1"}, rng, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(granteeMiss.Rows) != 0 {
		t.Fatalf("grantee miss rows = %+v", granteeMiss.Rows)
	}

	// Cross-owner rows stay invisible.
	other, err := f.store.userUsageRows(ctx, UsageFilters{}, accessInfo{ViewerID: "grantee1"}, rng, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Rows) != 0 {
		t.Fatalf("user denied rows = %+v", other.Rows)
	}

	// Summary: exact key match including the empty team filter.
	summary, err := f.store.userUsageSummary(ctx, UsageFilters{GranteeID: "grantee1", ResourceType: "account", ResourceID: "acc1"}, accessInfo{ViewerID: "owner1"}, rng)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Summary.RequestCount != 7 || summary.Summary.TotalCost != 0.7 {
		t.Fatalf("user summary = %+v", summary.Summary)
	}
	globalSummary, err := f.store.userUsageSummary(ctx, UsageFilters{GranteeID: "grantee9", ResourceType: "account", ResourceID: "acc9"}, accessInfo{ViewerID: "admin", IsAdmin: true}, rng)
	if err != nil {
		t.Fatal(err)
	}
	// Unscoped admins read the 'global' key only: owner2's row never leaks.
	if globalSummary.Summary.RequestCount != 0 {
		t.Fatalf("global user summary = %+v", globalSummary.Summary)
	}
}

// TestUsageStatsRangeTimezone pins the timezone window semantics: the default
// range follows the configured usageStatsTimezone, clamps to today and the
// trailing 31-day window, and collapses inverted inputs
// (normalizeAccountUsageStatsRange + fixedUsageStatsDefaultRange).
func TestUsageStatsRangeTimezone(t *testing.T) {
	now := time.Date(2026, 9, 5, 18, 0, 0, 0, time.UTC) // 2026-09-06 02:00 in Asia/Shanghai
	utcRange := defaultUsageStatsRange("UTC", now)
	if utcRange.EndDate != "2026-09-05" || utcRange.StartDate != "2026-08-06" || utcRange.Days != 31 {
		t.Fatalf("utc default range = %+v", utcRange)
	}
	shanghaiRange := defaultUsageStatsRange("Asia/Shanghai", now)
	if shanghaiRange.EndDate != "2026-09-06" || shanghaiRange.StartDate != "2026-08-07" {
		t.Fatalf("shanghai default range = %+v", shanghaiRange)
	}
	// Explicit dates clamp into the supported window and collapse inversion
	// (the past end clamps to today, the far future start clamps to earliest).
	normalized := normalizeUsageStatsRange("2020-01-01", "2030-01-01", "UTC", now)
	if normalized.StartDate != "2026-08-06" || normalized.EndDate != "2026-09-05" {
		t.Fatalf("clamped range = %+v", normalized)
	}
	short := normalizeUsageStatsRange("2026-08-25", "2026-08-20", "UTC", now)
	if short.StartDate != "2026-08-20" || short.EndDate != "2026-08-20" || short.Days != 1 {
		t.Fatalf("inverted range = %+v", short)
	}
	long := normalizeUsageStatsRange("2026-01-01", "2026-09-04", "UTC", now)
	if long.StartDate != "2026-08-06" || long.EndDate != "2026-09-04" || long.Days != 30 {
		t.Fatalf("long range = %+v", long)
	}
}

// TestUsagePageOptionsAndUpperBound pins the 1001-row window pagination
// contract (normalizeAuthorizationUsagePageOptions + pagedTotalUpperBound).
func TestUsagePageOptionsAndUpperBound(t *testing.T) {
	if page, pageSize := normalizeUsagePageOptions(0, 0); page != 1 || pageSize != 20 {
		t.Fatalf("defaults = %d/%d", page, pageSize)
	}
	if page, pageSize := normalizeUsagePageOptions(1, 500); page != 1 || pageSize != 200 {
		t.Fatalf("capped = %d/%d", page, pageSize)
	}
	if page, _ := normalizeUsagePageOptions(999, 20); page != 50 {
		t.Fatalf("clamped page = %d", page)
	}
	if total := pagedTotalUpperBound(3, 20, 5, false); total != 45 {
		t.Fatalf("total bound = %d", total)
	}
	if total := pagedTotalUpperBound(3, 20, 20, true); total != 61 {
		t.Fatalf("total bound with more = %d", total)
	}
}

// TestRevokeOwnerScopeNegative closes BUG-0165 item 1: an administrator with
// ?systemAccountId cannot revoke a grant owned outside the scope (Node
// resource_owner_system_account_id filter → not_found, no state change).
func TestRevokeOwnerScopeNegative(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner1", "active")
	f.seedAccount(t, "owner2", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_scope", "owner1")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_scope",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner1")
	if err != nil {
		t.Fatal(err)
	}

	// Out-of-scope revoke: not_found and the grant stays active with the
	// original version.
	mutation, err := f.store.RevokeForOwner(context.Background(), created.Item.ID, created.Item.UpdatedAt, "admin", "owner2")
	if err != nil {
		t.Fatal(err)
	}
	if mutation.Status != "not_found" {
		t.Fatalf("out-of-scope revoke = %+v", mutation)
	}
	var status, version string
	if err := f.db.QueryRow(`SELECT status, updated_at FROM resource_authorization_grants WHERE id = ?`, created.Item.ID).
		Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if status != StatusActive || version != created.Item.UpdatedAt {
		t.Fatalf("grant after denied revoke = %s@%s", status, version)
	}

	// In-scope revoke succeeds and flips the status.
	updated, err := f.store.RevokeForOwner(context.Background(), created.Item.ID, created.Item.UpdatedAt, "admin", "owner1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "updated" || updated.Result.Status != StatusRevoked || *updated.PreviousStatus != StatusActive {
		t.Fatalf("in-scope revoke = %+v", updated)
	}

	// An unscoped revoke keeps the administrator contract (no owner filter).
	created2, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_scope",
		GranteeType: "system_account", GranteeID: "owner2",
	}, "owner1")
	if err != nil {
		t.Fatal(err)
	}
	unscoped, err := f.store.Revoke(context.Background(), created2.Item.ID, created2.Item.UpdatedAt, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if unscoped.Status != "updated" {
		t.Fatalf("unscoped revoke = %+v", unscoped)
	}
}

// TestListItemProjectionContract pins the ResourceAuthorizationListItem
// projection (BUG-0165 item 4): name lookups, the manage-gated limits and
// account expiry, the effective source summary and the permission bits
// (resourceAuthorizationListItemFromRow :743-818).
func TestListItemProjectionContract(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner1", "active")
	f.seedAccount(t, "owner2", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedTeamWithMember(t, "team_proj", "grantee")
	f.seedGroup(t, "grp_proj", "owner1")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_proj",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner1")
	if err != nil {
		t.Fatal(err)
	}
	teamGrant, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_proj",
		GranteeType: "team", GranteeID: "team_proj",
		LimitsJSON: func() *string { v := `{"daily":{"enabled":true,"limit":1.234567}}`; return &v }(),
	}, "owner1")
	if err != nil {
		t.Fatal(err)
	}
	// A second owner's grant the scoped admin must not manage.
	if _, err := f.db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_scope2', 'Group grp_scope2', 'owner2', 'active')`); err != nil {
		t.Fatal(err)
	}
	otherGrant, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_scope2",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner2")
	if err != nil {
		t.Fatal(err)
	}

	// The route sets ViewerSystemAccountID from the admin ?systemAccountId
	// filter (list handler), so the scoped page carries both the SQL scope and
	// the manage gate.
	items, total, hasMore, err := f.store.ListItemsPage(context.Background(), Filters{Status: "all", ViewerSystemAccountID: "owner1"}, 1, 50, accessInfo{ViewerID: "admin", IsAdmin: true, FilterID: "owner1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || total != 2 || hasMore {
		t.Fatalf("scoped page = %d items total %d hasMore %v", len(items), total, hasMore)
	}
	byID := map[string]ListItem{}
	for _, item := range items {
		byID[item.ID] = item
	}
	direct := byID[created.Item.ID]
	if direct.ResourceName == nil || *direct.ResourceName != "Group grp_proj" {
		t.Fatalf("direct resourceName = %+v", direct.ResourceName)
	}
	if direct.OwnerName == nil || *direct.OwnerName != "owner1" || direct.GranteeName == nil || *direct.GranteeName != "grantee" {
		t.Fatalf("direct names = %+v", direct)
	}
	if direct.GranteeUsername == nil || *direct.GranteeUsername != "grantee" {
		t.Fatalf("direct username = %+v", direct.GranteeUsername)
	}
	if direct.EffectiveSourceType != "manual" || direct.SourceSummary.ActiveSourceCount != 1 ||
		!direct.SourceSummary.HasManual || direct.SourceSummary.HasTeam || len(direct.SourceSummary.TeamSources) != 0 {
		t.Fatalf("direct source summary = %+v", direct.SourceSummary)
	}
	if !direct.Permissions.CanEdit || !direct.Permissions.CanAuthorize {
		t.Fatalf("direct permissions = %+v", direct.Permissions)
	}
	// The grant was created without limits: the NULL column projects as the
	// omitted limits key even for a managing admin (Node parse → undefined).
	if direct.Limits != nil {
		t.Fatalf("unmanaged limits = %+v", direct.Limits)
	}

	team := byID[teamGrant.Item.ID]
	if team.EffectiveSourceType != "team" || team.EffectiveSourceTeamID == nil || *team.EffectiveSourceTeamID != "team_proj" ||
		team.EffectiveSourceTeamName == nil || *team.EffectiveSourceTeamName != "Team team_proj" {
		t.Fatalf("team source = %+v", team)
	}
	if len(team.SourceSummary.TeamSources) != 1 || team.SourceSummary.TeamSources[0].SourceTeamID != "team_proj" {
		t.Fatalf("team teamSources = %+v", team.SourceSummary)
	}

	// The out-of-scope grant is not on the scoped page at all.
	if _, ok := byID[otherGrant.Item.ID]; ok {
		t.Fatalf("out-of-scope grant leaked into the filtered page")
	}

	// Unscoped admin: manages everything; the owner2 grant projects with its
	// own names and manage-gated fields.
	unscoped, _, _, err := f.store.ListItemsPage(context.Background(), Filters{Status: "all"}, 1, 50, accessInfo{ViewerID: "admin", IsAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(unscoped) != 3 {
		t.Fatalf("unscoped page = %d", len(unscoped))
	}
	for _, item := range unscoped {
		if !item.Permissions.CanEdit {
			t.Fatalf("unscoped admin must manage %s", item.ID)
		}
	}

	// A plain user manages nothing (canManageResourceOwner pins the viewer).
	userItems, _, _, err := f.store.ListItemsPage(context.Background(), Filters{Status: "all"}, 1, 50, accessInfo{ViewerID: "grantee"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range userItems {
		if item.Permissions.CanEdit || item.Permissions.CanAuthorize {
			t.Fatalf("user must not manage %s", item.ID)
		}
		if item.Limits != nil {
			t.Fatalf("user must not see limits of %s", item.ID)
		}
		if item.ResourceAccountExpiresAt != nil {
			t.Fatalf("user must not see account expiry of %s", item.ID)
		}
	}
}
