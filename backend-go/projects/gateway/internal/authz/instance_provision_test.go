// BUG-0175 (D-64/D-74) provisioning tests: the create-tail instance
// derivation + grantee-group binding ported from the archived Node
// resource-authorization-write-state.repository.ts (:183-224 bind,
// :225-257 source-rename sync, :259-370 ensure, :371-423 name/group helpers).
package authz

import (
	"context"
	"strings"
	"testing"
)

// seedProvisionSource inserts a source account carrying the columns the
// restore/insert arms inherit (provider, protocol, concurrency, health,
// probe switch) plus non-default credentials so the empty-credential
// assertion is meaningful.
func (f *fixture) seedProvisionSource(t *testing.T, id, ownerID, name, providerCode string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name, status,
		provider_code, provider_protocol_profile_id, protocol_code, protocol_version, type,
		credentials_encrypted, credential_mask, concurrency_limit,
		temporary_unavailable_continuous_probe_enabled, health_check_model, health_check_endpoint_mode,
		schedulable, created_at, updated_at)
		VALUES (?, ?, ?, 'active', ?, 'prof-1', 'chat_json', 'v1', 'oauth',
		'{"api_key":"owner-secret"}', 'sk-****cret', 7000,
		1, 'gpt-4o-mini', 'chat_json', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, ownerID, name, providerCode); err != nil {
		t.Fatal(err)
	}
}

// seedGranteeGroup inserts a group owned by the grantee with the provider,
// enabled and default flags the binding validations read.
func (f *fixture) seedGranteeGroup(t *testing.T, id, granteeID, providerCode string, enabled, isDefault int) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO groups (id, name, system_account_id, status,
		provider_code, enabled, is_default, updated_at)
		VALUES (?, ?, ?, 'active', ?, ?, ?, '2026-01-01T00:00:00Z')`,
		id, "Group "+id, granteeID, providerCode, enabled, isDefault); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) seedNamedAccount(t *testing.T, id, ownerID, name string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO accounts (id, system_account_id, name, status, created_at, updated_at)
		VALUES (?, ?, ?, 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, ownerID, name); err != nil {
		t.Fatal(err)
	}
}

// provisionInstanceRow loads the full instance row the assertions inspect.
type provisionInstanceRow struct {
	id                      string
	systemAccountID         string
	name                    string
	status                  string
	schedulable             int
	credentialsEncrypted    string
	credentialFingerprint   *string
	providerCode            string
	providerProtocolProfile string
	protocolCode            string
	protocolVersion         string
	accountType             string
	concurrencyLimit        int
	continuousProbeEnabled  int
	healthCheckModel        string
	healthCheckEndpointMode string
	sourceAccountID         string
	authorizationID         string
	ownerSystemAccountID    string
	deletedAt               *string
	dispatchRevision        int
}

// provisionInstanceByAuthorization loads the full instance row the assertions
// inspect. Instances stamp the runtime authorization id (resource_
// authorizations), so the lookup resolves that id first.
func (f *fixture) provisionInstanceByAuthorization(t *testing.T, resourceID, granteeID string) *provisionInstanceRow {
	t.Helper()
	runtimeID := accountRuntimeIDFor(t, f, resourceID, granteeID)
	row := &provisionInstanceRow{}
	err := f.db.QueryRow(`SELECT id, system_account_id, name, status, schedulable,
		credentials_encrypted, credential_fingerprint, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, type, concurrency_limit,
		temporary_unavailable_continuous_probe_enabled, health_check_model, health_check_endpoint_mode,
		authorization_instance_source_account_id, authorization_instance_authorization_id,
		authorization_instance_owner_system_account_id, deleted_at, dispatch_revision
		FROM accounts WHERE authorization_instance_authorization_id = ?`, runtimeID).
		Scan(&row.id, &row.systemAccountID, &row.name, &row.status, &row.schedulable,
			&row.credentialsEncrypted, &row.credentialFingerprint, &row.providerCode, &row.providerProtocolProfile,
			&row.protocolCode, &row.protocolVersion, &row.accountType, &row.concurrencyLimit,
			&row.continuousProbeEnabled, &row.healthCheckModel, &row.healthCheckEndpointMode,
			&row.sourceAccountID, &row.authorizationID, &row.ownerSystemAccountID,
			&row.deletedAt, &row.dispatchRevision)
	if err != nil {
		t.Fatalf("instance row for runtime %s: %v", runtimeID, err)
	}
	return row
}

func (f *fixture) provisionBinding(t *testing.T, instanceID, resourceID, granteeID string) (groupID string, enabled int) {
	t.Helper()
	runtimeID := accountRuntimeIDFor(t, f, resourceID, granteeID)
	err := f.db.QueryRow(`SELECT group_id, enabled FROM group_accounts
		WHERE account_id = ? AND account_authorization_id = ?`, instanceID, runtimeID).
		Scan(&groupID, &enabled)
	if err != nil {
		t.Fatalf("group_accounts binding: %v", err)
	}
	return groupID, enabled
}

func TestProvisionInstanceOnCreateWithTargetGroup(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)

	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: strPtr("grp-target"),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	_ = result

	row := f.provisionInstanceByAuthorization(t, "acc-src", "grantee")
	if row.id == "" || row.systemAccountID != "grantee" {
		t.Fatalf("instance namespace: %+v", row)
	}
	// Name: the unique ladder returns the untouched source name first.
	if row.name != "生产账户" {
		t.Fatalf("instance name = %q", row.name)
	}
	// Static configuration inherits the source; runtime/credential state stays
	// empty and active (archived :355-370 column set).
	if row.status != "active" || row.schedulable != 1 {
		t.Fatalf("instance runtime state: status=%q schedulable=%d", row.status, row.schedulable)
	}
	if row.credentialsEncrypted != "{}" || row.credentialFingerprint != nil {
		t.Fatalf("instance credentials must stay empty: %q %v", row.credentialsEncrypted, row.credentialFingerprint)
	}
	if row.providerCode != "gpt" || row.providerProtocolProfile != "prof-1" ||
		row.protocolCode != "chat_json" || row.protocolVersion != "v1" || row.accountType != "oauth" {
		t.Fatalf("provider inheritance: %+v", row)
	}
	if row.concurrencyLimit != 7000 || row.continuousProbeEnabled != 1 ||
		row.healthCheckModel != "gpt-4o-mini" || row.healthCheckEndpointMode != "chat_json" {
		t.Fatalf("health/concurrency inheritance: %+v", row)
	}
	if row.sourceAccountID != "acc-src" || row.ownerSystemAccountID != "owner" {
		t.Fatalf("instance correlation: %+v", row)
	}
	// New instances keep the seeded dispatch revision; only the restore arm
	// advances it.
	if row.dispatchRevision != 1 {
		t.Fatalf("fresh instance dispatch_revision = %d", row.dispatchRevision)
	}

	groupID, enabled := f.provisionBinding(t, row.id, "acc-src", "grantee")
	if groupID != "grp-target" || enabled != 1 {
		t.Fatalf("binding = %q enabled=%d", groupID, enabled)
	}

	// The grantee read surface resolves the instance through the runtime row.
	readable, err := f.store.AuthorizedReadableAccountIDs(context.Background(), "grantee")
	if err != nil {
		t.Fatal(err)
	}
	if !readable[row.id] {
		t.Fatalf("grantee cannot read instance %q: %v", row.id, readable)
	}

	// Name search terms back the account list search.
	var documentCount, termCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM account_name_search_documents WHERE account_id = ?`, row.id).Scan(&documentCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM account_name_search_terms WHERE account_id = ?`, row.id).Scan(&termCount); err != nil {
		t.Fatal(err)
	}
	if documentCount != 1 || termCount == 0 {
		t.Fatalf("search terms: documents=%d terms=%d", documentCount, termCount)
	}
}

func TestProvisionInstanceNameFallsBackWhenTaken(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)
	// A live grantee account already owns the candidate base name.
	f.seedNamedAccount(t, "acc-other", "grantee", "生产账户")

	_, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: strPtr("grp-target"),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}

	row := f.provisionInstanceByAuthorization(t, "acc-src", "grantee")
	// The name ladder keys off the runtime authorization id suffix (the id the
	// instance stamps), not the grant id.
	runtimeID := accountRuntimeIDFor(t, f, "acc-src", "grantee")
	shortID := runtimeID
	if idx := strings.LastIndex(shortID, "_"); idx >= 0 {
		shortID = shortID[idx+1:]
	}
	if len(shortID) > 6 {
		shortID = shortID[:6]
	}
	want := "生产账户-" + shortID
	if row.name != want {
		t.Fatalf("instance name = %q, want %q", row.name, want)
	}
}

func TestProvisionTargetGroupValidationsVerbatim(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(f *fixture)
		target  string
		wantErr string
	}{
		{
			name:    "group missing or not owned by grantee",
			setup:   func(f *fixture) { f.seedGranteeGroup(t, "grp-owner", "owner", "gpt", 1, 0) },
			target:  "grp-owner",
			wantErr: "目标分组不存在或不属于被授权用户",
		},
		{
			name:    "provider mismatch",
			setup:   func(f *fixture) { f.seedGranteeGroup(t, "grp-claude", "grantee", "claude", 1, 0) },
			target:  "grp-claude",
			wantErr: "目标分组供应商与授权账户不一致",
		},
		{
			name:    "group disabled",
			setup:   func(f *fixture) { f.seedGranteeGroup(t, "grp-off", "grantee", "gpt", 0, 0) },
			target:  "grp-off",
			wantErr: "目标分组已停用，请选择启用分组",
		},
		{
			name:    "no enabled default group fallback",
			setup:   func(f *fixture) {},
			target:  "",
			wantErr: "目标用户缺少启用的默认分组，请按当前数据契约修复目标用户分组后再授权",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t)
			f.seedAccount(t, "owner", "active")
			f.seedAccount(t, "grantee", "active")
			f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
			testCase.setup(f)

			var target *string
			if testCase.target != "" {
				target = strPtr(testCase.target)
			}
			result, err := f.store.Create(context.Background(), CreateInput{
				ResourceType: "account", ResourceID: "acc-src",
				GranteeType: "system_account", GranteeID: "grantee",
				TargetGroupID: target,
			}, "owner")
			if err == nil {
				t.Fatalf("expected rejection, got %+v", result)
			}
			fail, ok := err.(*Fail)
			if !ok || fail.Message != testCase.wantErr {
				t.Fatalf("error = %v, want Fail %q", err, testCase.wantErr)
			}
			// The rejection rolls the whole create back: no grant, no instance,
			// no binding.
			var grantCount, instanceCount, bindingCount int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM resource_authorization_grants WHERE resource_id = 'acc-src'`).Scan(&grantCount); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE authorization_instance_source_account_id = 'acc-src'`).Scan(&instanceCount); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM group_accounts WHERE account_authorization_id IS NOT NULL`).Scan(&bindingCount); err != nil {
				t.Fatal(err)
			}
			if grantCount != 0 || instanceCount != 0 || bindingCount != 0 {
				t.Fatalf("rollback incomplete: grants=%d instances=%d bindings=%d", grantCount, instanceCount, bindingCount)
			}
		})
	}
}

func TestProvisionRestoresSoftDeletedInstance(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)

	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: strPtr("grp-target"),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	first := f.provisionInstanceByAuthorization(t, "acc-src", "grantee")

	// Simulate the archived deleted-instance state (the production path is the
	// source-account delete cascade in internal/accounts/delete.go): the
	// terminal grant revives through a re-create while the instance row stays
	// soft-deleted with degraded runtime fields.
	if _, err := f.db.Exec(`UPDATE accounts SET deleted_at = '2026-02-01T00:00:00Z', deleted_by = 'owner',
		status = 'disabled', schedulable = 0, cooldown_until = '2026-02-01T01:00:00Z',
		last_error_code = 'upstream_401', stream_failure_count = 3,
		health_check_model = 'stale-model', temporary_unavailable_continuous_probe_enabled = 0,
		name = '陈旧实例名'
		WHERE id = ?`, first.id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants SET status = 'revoked',
		revoked_by = 'owner', revoked_at = '2026-02-01T00:00:00Z' WHERE id = ?`, result.Item.ID); err != nil {
		t.Fatal(err)
	}

	revived, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: strPtr("grp-target"),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if revived.Created {
		t.Fatalf("re-create must revive the terminal grant, got created=true")
	}

	restored := f.provisionInstanceByAuthorization(t, "acc-src", "grantee")
	if restored.id != first.id {
		t.Fatalf("restore must reuse the deleted row: %q vs %q", restored.id, first.id)
	}
	if restored.deletedAt != nil {
		t.Fatalf("restored row still deleted: %v", restored.deletedAt)
	}
	if restored.status != "active" || restored.schedulable != 1 {
		t.Fatalf("restored runtime state: %q %d", restored.status, restored.schedulable)
	}
	if restored.name == "陈旧实例名" || restored.name != "生产账户" {
		t.Fatalf("restored name = %q", restored.name)
	}
	if restored.healthCheckModel != "gpt-4o-mini" || restored.continuousProbeEnabled != 1 {
		t.Fatalf("restored health inheritance: %+v", restored)
	}
	// The restore arm advances the dispatch revision and lands the circuit
	// outbox row (archived advanceAccountCircuitDispatchRevision...).
	if restored.dispatchRevision != first.dispatchRevision+1 {
		t.Fatalf("dispatch_revision = %d, want %d", restored.dispatchRevision, first.dispatchRevision+1)
	}
	var outboxCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM account_circuit_outbox
		WHERE account_id = ? AND event_type = 'dispatch_revision_changed' AND status = 'pending'`,
		restored.id).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Fatalf("circuit outbox rows = %d", outboxCount)
	}

	// The binding survives through the restored grant.
	groupID, enabled := f.provisionBinding(t, restored.id, "acc-src", "grantee")
	if groupID != "grp-target" || enabled != 1 {
		t.Fatalf("restored binding = %q %d", groupID, enabled)
	}
}

func TestRevokeKeepsInstanceTerminalState(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)

	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: strPtr("grp-target"),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	row := f.provisionInstanceByAuthorization(t, "acc-src", "grantee")

	var updatedAt string
	if err := f.db.QueryRow(`SELECT updated_at FROM resource_authorization_grants WHERE id = ?`, result.Item.ID).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	outcome, err := f.store.Revoke(context.Background(), result.Item.ID, updatedAt, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "updated" {
		t.Fatalf("revoke status = %q", outcome.Status)
	}

	// Archived revoke semantics: the grant flips, the instance row and its
	// group binding stay untouched (the terminal cascade is the source-account
	// delete path, already covered by internal/accounts/delete.go). The read
	// surface drops the instance because the runtime row is no longer active.
	after := f.provisionInstanceByAuthorization(t, "acc-src", "grantee")
	if after.deletedAt != nil || after.id != row.id {
		t.Fatalf("revoke must not delete the instance: %+v", after)
	}
	readable, err := f.store.AuthorizedReadableAccountIDs(context.Background(), "grantee")
	if err != nil {
		t.Fatal(err)
	}
	if readable[row.id] {
		t.Fatalf("revoked grant must not stay readable: %v", readable)
	}
}

func TestSyncInstanceNamesFollowsSourceRename(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)

	_, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: strPtr("grp-target"),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	row := f.provisionInstanceByAuthorization(t, "acc-src", "grantee")

	// A blocking live account forces the ladder past the plain base name.
	f.seedNamedAccount(t, "acc-block", "grantee", "renamed 生产账户")
	changed, err := f.store.SyncAccountAuthorizationInstanceNamesForSourceAccount(context.Background(), "acc-src", " renamed 生产账户 ")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != row.id {
		t.Fatalf("changed ids = %v", changed)
	}
	renamed := f.provisionInstanceByAuthorization(t, "acc-src", "grantee")
	shortID := accountRuntimeIDFor(t, f, "acc-src", "grantee")
	if idx := strings.LastIndex(shortID, "_"); idx >= 0 {
		shortID = shortID[idx+1:]
	}
	if len(shortID) > 6 {
		shortID = shortID[:6]
	}
	if renamed.name != "renamed 生产账户-"+shortID {
		t.Fatalf("renamed instance = %q", renamed.name)
	}
	var normalizedName string
	if err := f.db.QueryRow(`SELECT normalized_name FROM account_name_search_documents WHERE account_id = ?`, row.id).Scan(&normalizedName); err != nil {
		t.Fatal(err)
	}
	if normalizedName != "renamed 生产账户-"+shortID {
		t.Fatalf("normalized search document = %q", normalizedName)
	}

	// An unchanged candidate is a no-op.
	changedAgain, err := f.store.SyncAccountAuthorizationInstanceNamesForSourceAccount(context.Background(), "acc-src", " renamed 生产账户 ")
	if err != nil {
		t.Fatal(err)
	}
	if len(changedAgain) != 0 {
		t.Fatalf("second sync changed %v", changedAgain)
	}
}

func TestProvisionSkipsTeamGrantsAndExpiredExpiry(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedTeamWithMember(t, "team_1", "member")
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)

	// Team grants fan out runtime rows only; instance provisioning per member
	// is the follow-up slice (archived fans the bind through per-user upserts).
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "team", GranteeID: "team_1",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	var instanceCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE authorization_instance_source_account_id = 'acc-src'`).Scan(&instanceCount); err != nil {
		t.Fatal(err)
	}
	if instanceCount != 0 {
		t.Fatalf("team grant provisioned %d instances", instanceCount)
	}

	// A past expiry is rejected before provisioning (create validation), so no
	// instance appears; the bind gating inside provisioning is the second,
	// archived-parity guard for revive-style callers.
	expired := "2020-01-01T00:00:00Z"
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: strPtr("grp-target"),
		ExpiresAt:     &expired,
	}, "owner"); err == nil {
		t.Fatalf("past expiry must be rejected by create validation")
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE authorization_instance_source_account_id = 'acc-src' AND system_account_id = 'grantee'`).Scan(&instanceCount); err != nil {
		t.Fatal(err)
	}
	if instanceCount != 0 {
		t.Fatalf("rejected grant provisioned %d instances", instanceCount)
	}
}

func strPtr(value string) *string { return &value }
