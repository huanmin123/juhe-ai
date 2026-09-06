package authz

import (
	"context"
	"testing"
)

// seedPhysicalAccount inserts the grantable physical account row backing an
// 'account' resource (resolveResourceOwner requires authorization_instance_
// authorization_id IS NULL).
func seedPhysicalAccount(t *testing.T, f *fixture, id, ownerID string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, provider_code, type, status,
		config_revision, dispatch_revision, deleted_at)
		VALUES (?, ?, 'openai', 'oauth', 'active', 1, 1, NULL)`, id, ownerID); err != nil {
		t.Fatal(err)
	}
}

// seedDownstreamInstance inserts an authorization-instance account cloned from
// the physical account for granteeUserID, optionally outside the fanout
// whitelists (providerCode/accountType "") or soft-deleted.
func seedDownstreamInstance(t *testing.T, f *fixture, id, sourceAccountID, granteeUserID, runtimeID, providerCode, accountType, deletedAt string) {
	t.Helper()
	if accountType == "" {
		accountType = "oauth"
	}
	var instanceAuthorization any
	if runtimeID != "" {
		instanceAuthorization = runtimeID
	}
	var deletedArg any
	if deletedAt != "" {
		deletedArg = deletedAt
	}
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, provider_code, type, status,
		config_revision, dispatch_revision, authorization_instance_source_account_id,
		authorization_instance_authorization_id, deleted_at)
		VALUES (?, ?, ?, ?, 'active', 3, 5, ?, ?, ?)`,
		id, granteeUserID, providerCode, accountType, sourceAccountID, instanceAuthorization, deletedArg); err != nil {
		t.Fatal(err)
	}
}

func hourlyLimits(hours int) *string {
	document := `{"hourly":{"enabled":true,"hours":` + itoa(hours) + `,"limit":10}}`
	return &document
}

func countGrantBindings(t *testing.T, f *fixture, grantID string) []scopeBinding {
	t.Helper()
	rows, err := f.db.Query(`SELECT system_account_id, scope_type, scope_id, source_type, source_id, window_hours
		FROM request_quota_hourly_window_scope_bindings
		WHERE source_type = 'resource_authorization_grant' AND source_id = ?
		ORDER BY scope_type ASC, scope_id ASC`, grantID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	bindings := []scopeBinding{}
	for rows.Next() {
		var binding scopeBinding
		if err := rows.Scan(&binding.systemAccountID, &binding.scopeType, &binding.scopeID,
			&binding.sourceType, &binding.sourceID, &binding.windowHours); err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return bindings
}

type outboxRow struct {
	accountID        string
	inputVersion     int
	eventKind        string
	reason           string
	configRevision   int
	dispatchRevision int
}

func countOutboxRows(t *testing.T, f *fixture) []outboxRow {
	t.Helper()
	rows, err := f.db.Query(`SELECT account_id, input_version, event_kind, reason, config_revision, dispatch_revision
		FROM account_health_jobs_input_outbox ORDER BY account_id ASC, input_version ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []outboxRow{}
	for rows.Next() {
		var row outboxRow
		if err := rows.Scan(&row.accountID, &row.inputVersion, &row.eventKind, &row.reason,
			&row.configRevision, &row.dispatchRevision); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func grantIDByResource(t *testing.T, f *fixture, resourceType, resourceID string) string {
	t.Helper()
	var id string
	if err := f.db.QueryRow(`SELECT id FROM resource_authorization_grants
		WHERE resource_type = ? AND resource_id = ?`, resourceType, resourceID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func accountRuntimeIDFor(t *testing.T, f *fixture, resourceID, granteeID string) string {
	t.Helper()
	var id string
	if err := f.db.QueryRow(`SELECT id FROM resource_authorizations
		WHERE resource_type = 'account' AND resource_id = ? AND grantee_system_account_id = ?`,
		resourceID, granteeID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestCreateResyncsQuotaScopeBindingsWithoutHealthFanout covers the create
// tail (write.repository.ts:219/:233/:402/:439): an hourly-limited account
// grant lands its direct + team bindings and never enqueues health inputs.
func TestCreateResyncsQuotaScopeBindingsWithoutHealthFanout(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	seedPhysicalAccount(t, f, "res-1", "owner")
	// A team grantee carries a second member so the team binding branch runs.
	f.seedAccount(t, "member", "active")
	f.seedTeamWithMember(t, "team-1", "grantee")
	f.seedTeamWithMember(t, "team-1", "member")

	limits := hourlyLimits(6)
	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "res-1",
		GranteeType: "system_account", GranteeID: "grantee", LimitsJSON: limits,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Fatalf("create should be new: %+v", result)
	}
	runtimeID := accountRuntimeIDFor(t, f, "res-1", "grantee")
	bindings := countGrantBindings(t, f, result.Item.ID)
	if len(bindings) != 1 {
		t.Fatalf("direct grant bindings = %+v, want one account_authorization row", bindings)
	}
	if bindings[0].systemAccountID != "grantee" || bindings[0].scopeType != "account_authorization" ||
		bindings[0].scopeID != runtimeID || bindings[0].sourceID != result.Item.ID || bindings[0].windowHours != 6 {
		t.Fatalf("direct grant binding = %+v", bindings[0])
	}
	if rows := countOutboxRows(t, f); len(rows) != 0 {
		t.Fatalf("create must not enqueue health inputs: %+v", rows)
	}

	// Team grant: the direct member row additionally renders the team-scope
	// binding keyed by the authorization instance account (write.repository
	// account_authorization_team branch), which requires the instance join.
	seedDownstreamInstance(t, f, "inst-1", "res-1", "grantee", runtimeID, "openai", "oauth", "")
	teamResult, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "res-1",
		GranteeType: "team", GranteeID: "team-1", LimitsJSON: limits,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	teamBindings := countGrantBindings(t, f, teamResult.Item.ID)
	// One direct binding per member runtime plus the team-scope binding for the
	// grantee's authorization instance (the member has no instance row, so its
	// team binding is skipped exactly like the Node instance join).
	if len(teamBindings) != 3 {
		t.Fatalf("team grant bindings = %+v, want two member rows + one team row", teamBindings)
	}
	var team *scopeBinding
	for i := range teamBindings {
		if teamBindings[i].scopeType == "account_authorization_team" {
			team = &teamBindings[i]
		}
	}
	if team == nil || team.scopeID != "inst-1:team-1" || team.systemAccountID != "grantee" || team.windowHours != 6 {
		t.Fatalf("team binding = %+v", teamBindings)
	}

	// A grant without hourly limits renders no bindings at all.
	seedPhysicalAccount(t, f, "res-2", "owner")
	noLimits, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "res-2",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if rows := countGrantBindings(t, f, noLimits.Item.ID); len(rows) != 0 {
		t.Fatalf("limits without hourly window must render no bindings: %+v", rows)
	}
}

// TestRevokeResyncsBindingsAndFansOutHealthInputs covers the
// syncResourceAuthorizationGrantRuntimeAsync tail (:1001-1002) reached through
// Revoke: the revoked runtime drops its bindings and whitelisted instance
// accounts reserve input epochs in the same transaction.
func TestRevokeResyncsBindingsAndFansOutHealthInputs(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	seedPhysicalAccount(t, f, "res-1", "owner")
	limits := hourlyLimits(6)
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "res-1",
		GranteeType: "system_account", GranteeID: "grantee", LimitsJSON: limits,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := accountRuntimeIDFor(t, f, "res-1", "grantee")
	seedDownstreamInstance(t, f, "inst-1", "res-1", "grantee", runtimeID, "openai", "oauth", "")
	// Excluded by each fanout filter: soft-deleted, provider outside the
	// whitelist, type outside the whitelist, and an instance of another source.
	seedDownstreamInstance(t, f, "inst-deleted", "res-1", "grantee", runtimeID, "openai", "oauth", "2026-01-01T00:00:00.000Z")
	seedDownstreamInstance(t, f, "inst-provider", "res-1", "grantee", runtimeID, "unknown_provider", "oauth", "")
	seedDownstreamInstance(t, f, "inst-type", "res-1", "grantee", runtimeID, "openai", "completion", "")
	seedDownstreamInstance(t, f, "inst-other", "res-9", "grantee", runtimeID, "openai", "oauth", "")

	version, err := f.store.GetGrantForMutation(context.Background(), nil, created.Item.ID)
	if err != nil || version == nil {
		t.Fatalf("load grant: %v %v", version, err)
	}
	mutation, err := f.store.Revoke(context.Background(), created.Item.ID, version.UpdatedAt, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if mutation.Status != "updated" {
		t.Fatalf("revoke status = %s", mutation.Status)
	}
	if bindings := countGrantBindings(t, f, created.Item.ID); len(bindings) != 0 {
		t.Fatalf("revoked grant must drop its bindings: %+v", bindings)
	}
	rows := countOutboxRows(t, f)
	if len(rows) != 1 {
		t.Fatalf("fanout rows = %+v, want exactly the whitelisted inst-1", rows)
	}
	row := rows[0]
	if row.accountID != "inst-1" || row.inputVersion != 1 || row.eventKind != "snapshot" ||
		row.reason != AuthorizationGrantHealthFanoutReason || row.configRevision != 3 || row.dispatchRevision != 5 {
		t.Fatalf("fanout row = %+v", row)
	}

	// A second grant reuses the next epoch for the same instance account.
	created2, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "res-1",
		GranteeType: "system_account", GranteeID: "grantee", LimitsJSON: limits,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	version2, err := f.store.GetGrantForMutation(context.Background(), nil, created2.Item.ID)
	if err != nil || version2 == nil {
		t.Fatalf("load grant 2: %v %v", version2, err)
	}
	if _, err := f.store.Revoke(context.Background(), created2.Item.ID, version2.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	rows = countOutboxRows(t, f)
	if len(rows) != 2 || rows[1].accountID != "inst-1" || rows[1].inputVersion != 2 {
		t.Fatalf("second fanout must reserve version 2: %+v", rows)
	}
}

// TestRevokeNonAccountResourceSkipsHealthFanout keeps the group-revoke path on
// the bindings rebuild while the fanout short-circuits (fanout.repository
// :30/:56 resource_type !== 'account').
func TestRevokeNonAccountResourceSkipsHealthFanout(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp-1", "owner")
	limits := hourlyLimits(4)
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp-1",
		GranteeType: "system_account", GranteeID: "grantee", LimitsJSON: limits,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	bindings := countGrantBindings(t, f, created.Item.ID)
	if len(bindings) != 1 || bindings[0].scopeType != "group_authorization" ||
		bindings[0].systemAccountID != "owner" || bindings[0].windowHours != 4 {
		t.Fatalf("group bindings = %+v, want owner-scoped group_authorization", bindings)
	}
	version, err := f.store.GetGrantForMutation(context.Background(), nil, created.Item.ID)
	if err != nil || version == nil {
		t.Fatalf("load grant: %v %v", version, err)
	}
	if _, err := f.store.Revoke(context.Background(), created.Item.ID, version.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	if bindings := countGrantBindings(t, f, created.Item.ID); len(bindings) != 0 {
		t.Fatalf("revoked group grant must drop its bindings: %+v", bindings)
	}
	if rows := countOutboxRows(t, f); len(rows) != 0 {
		t.Fatalf("group resource must not enqueue health inputs: %+v", rows)
	}
}

// TestReturnResyncsBindingsAndFansOutHealthInputs covers the
// returnResourceAuthorizationGrantAsync tail (return.repository.ts:587-596):
// the returned runtime drops its bindings and the fanout still runs.
func TestReturnResyncsBindingsAndFansOutHealthInputs(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	seedPhysicalAccount(t, f, "res-1", "owner")
	limits := hourlyLimits(6)
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "res-1",
		GranteeType: "system_account", GranteeID: "grantee", LimitsJSON: limits,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := accountRuntimeIDFor(t, f, "res-1", "grantee")
	seedDownstreamInstance(t, f, "inst-1", "res-1", "grantee", runtimeID, "openai", "oauth", "")
	version, err := f.store.GetGrantForMutation(context.Background(), nil, created.Item.ID)
	if err != nil || version == nil {
		t.Fatalf("load grant: %v %v", version, err)
	}
	mutation, err := f.store.Return(context.Background(), created.Item.ID, version.UpdatedAt, "grantee")
	if err != nil {
		t.Fatal(err)
	}
	if mutation.Status != "updated" {
		t.Fatalf("return status = %s", mutation.Status)
	}
	if bindings := countGrantBindings(t, f, created.Item.ID); len(bindings) != 0 {
		t.Fatalf("returned grant must drop its bindings: %+v", bindings)
	}
	rows := countOutboxRows(t, f)
	if len(rows) != 1 || rows[0].accountID != "inst-1" || rows[0].reason != AuthorizationGrantHealthFanoutReason {
		t.Fatalf("return fanout rows = %+v", rows)
	}
}

// TestReturnGroupResyncsBindingsAndFansOutHealthInputs covers the groups-route
// return (return_group.go) carrying the same downstream tail.
func TestReturnGroupResyncsBindingsAndFansOutHealthInputs(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp-1", "owner")
	limits := hourlyLimits(4)
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp-1",
		GranteeType: "system_account", GranteeID: "grantee", LimitsJSON: limits,
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.store.ReturnGroupForGrantee(context.Background(), "grp-1", "grantee", "grantee")
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil {
		t.Fatal("return receipt missing")
	}
	grantID := grantIDByResource(t, f, "group", "grp-1")
	if bindings := countGrantBindings(t, f, grantID); len(bindings) != 0 {
		t.Fatalf("returned group grant must drop its bindings: %+v", bindings)
	}
	if rows := countOutboxRows(t, f); len(rows) != 0 {
		t.Fatalf("group resource must not enqueue health inputs: %+v", rows)
	}
}

// TestPatchResyncsBindingsAfterRuntimeSync covers the patch path (:809 →
// syncResourceAuthorizationGrantRuntimeAsync): pausing drops the binding while
// the runtime keeps the row, and the fanout runs for account resources.
func TestPatchResyncsBindingsAfterRuntimeSync(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	seedPhysicalAccount(t, f, "res-1", "owner")
	limits := hourlyLimits(6)
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "res-1",
		GranteeType: "system_account", GranteeID: "grantee", LimitsJSON: limits,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := accountRuntimeIDFor(t, f, "res-1", "grantee")
	seedDownstreamInstance(t, f, "inst-1", "res-1", "grantee", runtimeID, "openai", "oauth", "")
	version, err := f.store.GetGrantForMutation(context.Background(), nil, created.Item.ID)
	if err != nil || version == nil {
		t.Fatalf("load grant: %v %v", version, err)
	}
	paused := StatusPaused
	outcome, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{Status: &paused}, version.UpdatedAt, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "updated" {
		t.Fatalf("patch status = %s", outcome.Status)
	}
	if bindings := countGrantBindings(t, f, created.Item.ID); len(bindings) != 0 {
		t.Fatalf("paused runtime must drop its bindings: %+v", bindings)
	}
	rows := countOutboxRows(t, f)
	if len(rows) != 1 || rows[0].accountID != "inst-1" {
		t.Fatalf("patch fanout rows = %+v", rows)
	}
	// Resuming with the same limits restores the binding.
	version2, err := f.store.GetGrantForMutation(context.Background(), nil, created.Item.ID)
	if err != nil || version2 == nil {
		t.Fatalf("load grant 2: %v %v", version2, err)
	}
	active := StatusActive
	if _, err := f.store.Patch(context.Background(), created.Item.ID, PatchInput{Status: &active}, version2.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	bindings := countGrantBindings(t, f, created.Item.ID)
	if len(bindings) != 1 || bindings[0].scopeType != "account_authorization" || bindings[0].windowHours != 6 {
		t.Fatalf("resumed bindings = %+v", bindings)
	}
}

// TestExpireSweepFansOutHealthInputs pins the sweep tail (:957 → :1001-1002):
// a due account grant lands its fanout rows inside the sweep transaction.
func TestExpireSweepFansOutHealthInputs(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	seedPhysicalAccount(t, f, "res-1", "owner")
	// Seed the grant directly in the past so the sweep picks it up.
	past := f.now.UTC().Add(-24 * 3600 * 1e9).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := f.db.Exec(`INSERT INTO resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		 grantee_system_account_id, scope, status, limits_json, created_by, created_at, updated_at, expires_at)
		VALUES ('g-due', 'account', 'res-1', 'owner', 'system_account', 'grantee', 'use', 'active', ?, 'owner', ?, ?, ?)`,
		*hourlyLimits(6), f.now.UTC().Format("2006-01-02T15:04:05.000Z"),
		f.now.UTC().Format("2006-01-02T15:04:05.000Z"), past); err != nil {
		t.Fatal(err)
	}
	// The runtime row the sweep re-projects must exist for the fanout join.
	if _, err := f.db.Exec(`INSERT INTO resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		 scope, status, effective_source_type, activated_at, last_source_changed_at,
		 created_by, created_at, updated_at)
		VALUES ('ra-due', 'account', 'res-1', 'owner', 'grantee', 'use', 'active', 'manual', ?, ?, 'owner', ?, ?)`,
		f.now.UTC().Format("2006-01-02T15:04:05.000Z"), f.now.UTC().Format("2006-01-02T15:04:05.000Z"),
		f.now.UTC().Format("2006-01-02T15:04:05.000Z"), f.now.UTC().Format("2006-01-02T15:04:05.000Z")); err != nil {
		t.Fatal(err)
	}
	seedDownstreamInstance(t, f, "inst-1", "res-1", "grantee", "ra-due", "openai", "oauth", "")
	expired, err := f.store.ExpireSweep(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if expired != 1 {
		t.Fatalf("expired = %d, want 1", expired)
	}
	var status string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorization_grants WHERE id = 'g-due'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusExpired {
		t.Fatalf("grant status = %s", status)
	}
	if bindings := countGrantBindings(t, f, "g-due"); len(bindings) != 0 {
		t.Fatalf("expired runtime must render no bindings: %+v", bindings)
	}
	rows := countOutboxRows(t, f)
	if len(rows) != 1 || rows[0].accountID != "inst-1" || rows[0].reason != AuthorizationGrantHealthFanoutReason {
		t.Fatalf("sweep fanout rows = %+v", rows)
	}
}

// TestQuotaBindingUpsertReplacesStaleSource pins the delete-then-upsert
// contract: a re-created grant over the same runtime replaces the binding
// source_id without duplicating rows.
func TestQuotaBindingUpsertReplacesStaleSource(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	seedPhysicalAccount(t, f, "res-1", "owner")
	first, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "res-1",
		GranteeType: "system_account", GranteeID: "grantee", LimitsJSON: hourlyLimits(6),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := accountRuntimeIDFor(t, f, "res-1", "grantee")
	seedDownstreamInstance(t, f, "inst-1", "res-1", "grantee", runtimeID, "openai", "oauth", "")
	// Revoke → the runtime loses its source → a fresh create revives the grant
	// and rebuilds the binding against the same runtime row.
	version, err := f.store.GetGrantForMutation(context.Background(), nil, first.Item.ID)
	if err != nil || version == nil {
		t.Fatalf("load grant: %v %v", version, err)
	}
	if _, err := f.store.Revoke(context.Background(), first.Item.ID, version.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	second, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "res-1",
		GranteeType: "system_account", GranteeID: "grantee", LimitsJSON: hourlyLimits(8),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.PreviousStatus == nil {
		t.Fatalf("revive outcome = %+v", second)
	}
	bindings := countGrantBindings(t, f, second.Item.ID)
	if len(bindings) != 1 || bindings[0].sourceID != second.Item.ID || bindings[0].windowHours != 8 {
		t.Fatalf("revived bindings = %+v, want refreshed source and hours", bindings)
	}
	// The revive path must not enqueue health inputs (create has no fanout in
	// Node); the revoke above already consumed the only expected epoch.
	if rows := countOutboxRows(t, f); len(rows) != 1 {
		t.Fatalf("revive must not fanout: %+v", rows)
	}
}
