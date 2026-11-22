// BUG-0175 (D-64/D-74) provisioning tests: the claim-time instance
// derivation + grantee-group binding (instance_provision.go ensure /
// bind / name+group helpers). The send-and-claim model provisions at
// AcceptSource: one step creates the instance and binds it into the
// grantee-selected group (empty target falls back to the grantee's enabled
// default group).
package authz

import (
	"context"
	"strings"
	"testing"
	"time"
)

// seedProvisionSource inserts a source account carrying the columns the
// restore/insert arms inherit (vendor, protocol, concurrency, health,
// probe switch) plus non-default credentials so the empty-credential
// assertion is meaningful.
func (f *fixture) seedProvisionSource(t *testing.T, id, ownerID, name, providerCode string) {
	t.Helper()
	// vendor_code 由 providerCode 参数承载（generic 语义落 NULL + generic_http）。
	vendor, driver := provisionVendorIdentity(providerCode)
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO accounts (id, system_account_id, name, status,
		vendor_code,
		connection_driver_code, protocol_code, protocol_version, type,
		credentials_encrypted, credential_mask, concurrency_limit,
		temporary_unavailable_continuous_probe_enabled, health_check_model, health_check_endpoint_mode,
		schedulable, created_at, updated_at)
		VALUES (?, ?, ?, 'active', ?, ?, 'chat_json', 'v1', 'oauth',
		?, 'sk-****cret', 7000,
		1, 'gpt-4o-mini', 'chat_json', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`),
		id, ownerID, name, vendor, driver,
		sealedTestCredentials(t, map[string]any{"api_key": "owner-secret"})); err != nil {
		t.Fatal(err)
	}
}

// provisionVendorIdentity maps provider semantics onto the target account
// identity columns (设计 §2.1): the 9 real vendors carry vendor_code = driver =
// providerCode; generic semantics (openai/hybrid 等) keep vendor_code NULL and
// driver generic_http.
func provisionVendorIdentity(providerCode string) (any, string) {
	switch providerCode {
	case "gpt", "xai", "deepseek", "anthropic", "gemini", "glm", "minimax", "volcengine", "qwen":
		return providerCode, providerCode
	}
	return nil, "generic_http"
}

// seedGranteeGroup inserts a group owned by the grantee with the enabled and
// default flags the binding validations read.
func (f *fixture) seedGranteeGroup(t *testing.T, id, granteeID, _ string, enabled, isDefault int) {
	t.Helper()
	// 组无 provider 维度（设计 §8）：providerCode 入参仅为调用点兼容保留。
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO groups (id, name, system_account_id, status, enabled, is_default, updated_at)
		VALUES (?, ?, ?, 'active', ?, ?, '2026-01-01T00:00:00Z')`), id, "Group "+id, granteeID, enabled, isDefault); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) seedNamedAccount(t *testing.T, id, ownerID, name string) {
	t.Helper()
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO accounts (id, system_account_id, name, status, created_at, updated_at)
		VALUES (?, ?, ?, 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`), id, ownerID, name); err != nil {
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
	capabilityDomain        string
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
	err := f.db.QueryRow(fixtureBind(`SELECT id, system_account_id, name, status, schedulable,
		credentials_encrypted, credential_fingerprint, COALESCE(vendor_code, '') AS provider_code, '' AS provider_protocol_profile_id,
		protocol_code, protocol_version, type, concurrency_limit,
		temporary_unavailable_continuous_probe_enabled, health_check_model, health_check_endpoint_mode,
		capability_domain,
		authorization_instance_source_account_id, authorization_instance_authorization_id,
		authorization_instance_owner_system_account_id, deleted_at, dispatch_revision
		FROM accounts WHERE authorization_instance_authorization_id = ?`), runtimeID).
		Scan(&row.id, &row.systemAccountID, &row.name, &row.status, &row.schedulable,
			&row.credentialsEncrypted, &row.credentialFingerprint, &row.providerCode, &row.providerProtocolProfile,
			&row.protocolCode, &row.protocolVersion, &row.accountType, &row.concurrencyLimit,
			&row.continuousProbeEnabled, &row.healthCheckModel, &row.healthCheckEndpointMode,
			&row.capabilityDomain,
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
	err := f.db.QueryRow(fixtureBind(`SELECT group_id, enabled FROM group_accounts
		WHERE account_id = ? AND account_authorization_id = ?`), instanceID, runtimeID).
		Scan(&groupID, &enabled)
	if err != nil {
		t.Fatalf("group_accounts binding: %v", err)
	}
	return groupID, enabled
}

func TestProvisionInstanceOnClaimWithTargetGroup(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)

	f.claimAccountOffer(t, "acc-src", "grantee", "grp-target")

	row := f.provisionInstanceByAuthorization(t, "acc-src", "grantee")
	if row.id == "" || row.systemAccountID != "grantee" {
		t.Fatalf("instance namespace: %+v", row)
	}
	// Name: the unique ladder returns the untouched source name first.
	if row.name != "生产账户" {
		t.Fatalf("instance name = %q", row.name)
	}
	// Static configuration inherits the source; runtime/credential state stays
	// empty and active.
	if row.status != "active" || row.schedulable != 1 {
		t.Fatalf("instance runtime state: status=%q schedulable=%d", row.status, row.schedulable)
	}
	if row.credentialsEncrypted != "{}" || row.credentialFingerprint != nil {
		t.Fatalf("instance credentials must stay empty: %q %v", row.credentialsEncrypted, row.credentialFingerprint)
	}
	// 供应商解耦（设计 §9.11）：provider 身份取 vendor_code，档案列已退役。
	if row.providerCode != "gpt" || row.providerProtocolProfile != "" ||
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
}

func TestProvisionInstanceNameFallsBackWhenTaken(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)
	// A live grantee account already owns the candidate base name.
	f.seedNamedAccount(t, "acc-other", "grantee", "生产账户")

	f.claimAccountOffer(t, "acc-src", "grantee", "grp-target")

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

// TestProvisionTargetGroupValidationsVerbatim: the claim path carries the
// verbatim group rejections, an empty target is rejected with
// target_group_required (no default-group fallback, 2026-11-22 用户裁决), and a
// rejected claim leaves the pending offer without instance/binding side
// effects (the offer itself stays claimable).
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
		// 供应商解耦（设计 §8）：组无 provider 维度，「目标分组供应商与授权账户
		// 不一致」校验随列退役，跨厂商分组不再是拒绝条件。
		{
			name:    "group disabled",
			setup:   func(f *fixture) { f.seedGranteeGroup(t, "grp-off", "grantee", "gpt", 0, 0) },
			target:  "grp-off",
			wantErr: "目标分组已停用，请选择启用分组",
		},
		{
			// 必选目标分组（2026-11-22 用户裁决）：空值在更早的校验处拒绝，
			// 无缺省回落默认分组。
			name:    "empty target group rejected without default fallback",
			setup:   func(f *fixture) {},
			target:  "",
			wantErr: "请选择领取后绑入的分组",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t)
			f.seedAccount(t, "owner", "active")
			f.seedAccount(t, "grantee", "active")
			f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
			testCase.setup(f)

			offer, err := f.store.CreateOffer(context.Background(), OfferInput{
				ResourceType: "account", ResourceID: "acc-src",
				GranteeType: "system_account", GranteeUsername: "grantee",
			}, "owner", false, "owner")
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.store.AcceptSource(context.Background(), "grantee", offer.SourceID,
				AcceptInput{TargetGroupID: testCase.target}, f.now.UTC().Format(time.RFC3339Nano))
			if err == nil {
				t.Fatalf("expected rejection")
			}
			fail, ok := err.(*Fail)
			if !ok || fail.Message != testCase.wantErr {
				t.Fatalf("error = %v, want Fail %q", err, testCase.wantErr)
			}
			// The rejection keeps the pending offer but writes no instance and no
			// binding; the source stays pending so the grantee can retry.
			var instanceCount, bindingCount int
			var pendingSources int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE authorization_instance_source_account_id = 'acc-src'`).Scan(&instanceCount); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM group_accounts WHERE account_authorization_id IS NOT NULL`).Scan(&bindingCount); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*) FROM resource_authorization_sources WHERE id = ? AND status = 'pending'`), offer.SourceID).Scan(&pendingSources); err != nil {
				t.Fatal(err)
			}
			if instanceCount != 0 || bindingCount != 0 || pendingSources != 1 {
				t.Fatalf("rejected claim side effects: instances=%d bindings=%d pendingSources=%d", instanceCount, bindingCount, pendingSources)
			}
		})
	}
}

// TestProvisionSuperGroupTargetRejected pins the super-group binding guard
// (超级分组设计 §5): a super group must never directly hold group_accounts
// rows.
func TestProvisionSuperGroupTargetRejected(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src-super", "owner", "生产账户", "gpt")
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO groups (id, name, system_account_id, status, enabled, is_default, group_kind, updated_at)
		VALUES ('grp-super', '超级', 'grantee', 'active', 1, 0, 'super', '2026-01-01T00:00:00Z')`)); err != nil {
		t.Fatal(err)
	}
	offer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "account", ResourceID: "acc-src-super",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.AcceptSource(context.Background(), "grantee", offer.SourceID,
		AcceptInput{TargetGroupID: "grp-super"}, f.now.UTC().Format(time.RFC3339Nano))
	fail, ok := err.(*Fail)
	if !ok || fail.Code != "group_kind_conflict" {
		t.Fatalf("super group target must return group_kind_conflict: %#v", err)
	}
}

func TestProvisionRestoresSoftDeletedInstance(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)

	grantID := f.claimAccountOffer(t, "acc-src", "grantee", "grp-target")
	first := f.provisionInstanceByAuthorization(t, "acc-src", "grantee")

	// Simulate the archived deleted-instance state (the production path is the
	// source-account delete cascade in internal/accounts/delete.go): the
	// terminal grant revives through a re-send + re-claim while the instance row
	// stays soft-deleted with degraded runtime fields.
	if _, err := f.db.Exec(fixtureBind(`UPDATE accounts SET deleted_at = '2026-02-01T00:00:00Z', deleted_by = 'owner',
		status = 'disabled', schedulable = 0, cooldown_until = '2026-02-01T01:00:00Z',
		last_error_code = 'upstream_401', stream_failure_count = 3,
		health_check_model = 'stale-model', temporary_unavailable_continuous_probe_enabled = 0,
		name = '陈旧实例名'
		WHERE id = ?`), first.id); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RevokeOffer(context.Background(), grantID, "owner", false,
		f.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	// 终态 grant 不复用：重发是一条全新 pending grant，再次领取复用实例行。
	second := f.claimAccountOffer(t, "acc-src", "grantee", "grp-target")
	if second == grantID {
		t.Fatalf("re-offer must create a fresh grant id, got %q again", second)
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
	if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*) FROM account_circuit_outbox
		WHERE account_id = ? AND event_type = 'dispatch_revision_changed' AND status = 'pending'`), restored.id).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	// One event per circuit projection consumer (J3a + J3b), same transition.
	if outboxCount != 2 {
		t.Fatalf("circuit outbox rows = %d, want one per projection consumer", outboxCount)
	}

	// The binding survives through the restored grant.
	groupID, enabled := f.provisionBinding(t, restored.id, "acc-src", "grantee")
	if groupID != "grp-target" || enabled != 1 {
		t.Fatalf("restored binding = %q %d", groupID, enabled)
	}
}

// TestRevokeDisablesInstanceTerminalState pins the terminal side effect of
// the send-and-claim model: the grant flips, the instance row stays in place
// but is disabled and unschedulable, and the read surface drops it.
func TestRevokeDisablesInstanceTerminalState(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)

	grantID := f.claimAccountOffer(t, "acc-src", "grantee", "grp-target")
	row := f.provisionInstanceByAuthorization(t, "acc-src", "grantee")

	if err := f.store.RevokeOffer(context.Background(), grantID, "owner", false,
		f.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	// The instance row stays (the delete cascade is the source-account delete
	// path) but lands disabled and unschedulable; the group binding flips to
	// disabled.
	after := f.provisionInstanceByAuthorization(t, "acc-src", "grantee")
	if after.deletedAt != nil || after.id != row.id {
		t.Fatalf("revoke must not delete the instance: %+v", after)
	}
	if after.status != "disabled" || after.schedulable != 0 {
		t.Fatalf("revoke must disable the instance: %+v", after)
	}
	_, bindingEnabled := f.provisionBinding(t, row.id, "acc-src", "grantee")
	if bindingEnabled != 0 {
		t.Fatalf("revoke must disable the group binding: %d", bindingEnabled)
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

	f.claimAccountOffer(t, "acc-src", "grantee", "grp-target")
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
	// 名称搜索直读 accounts.name：改名后的名称列即搜索事实。
	var storedName string
	if err := f.db.QueryRow(fixtureBind(`SELECT name FROM accounts WHERE id = ?`), row.id).Scan(&storedName); err != nil {
		t.Fatal(err)
	}
	if storedName != "renamed 生产账户-"+shortID {
		t.Fatalf("stored instance name = %q", storedName)
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

// TestProvisionTeamClaimRequiresExplicitTargetGroup pins the team-account
// claim path (2026-11-22 用户裁决): a member claiming without an explicit
// target group fails with target_group_required — no default-group fallback,
// the pending offer and the other members stay intact — while explicit
// targets claim normally. A past expiry is still rejected before any source
// exists.
func TestProvisionTeamClaimRequiresExplicitTargetGroup(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedTeamWithMember(t, "team_1", "member")
	f.seedTeamWithMember(t, "team_1", "grantee")
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)
	f.seedGranteeGroup(t, "grp-member", "member", "gpt", 1, 0)

	offer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "team", TeamID: "team_1",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// member 缺省 targetGroupId → target_group_required（绑定完成才是领取，
	// 无缺省回落）；来源保持 pending，grantee 不受影响。
	_, memberErr := f.store.AcceptSource(context.Background(), "member",
		pendingTeamSourceFor(t, f, offer.GrantID, "team_1", "member"),
		AcceptInput{}, f.now.UTC().Format(time.RFC3339Nano))
	fail, ok := memberErr.(*Fail)
	if !ok || fail.Code != "target_group_required" {
		t.Fatalf("member claim without target group must fail with target_group_required: %#v", memberErr)
	}
	if _, err := f.store.AcceptSource(context.Background(), "member",
		pendingTeamSourceFor(t, f, offer.GrantID, "team_1", "member"),
		AcceptInput{TargetGroupID: "grp-member"}, f.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("member explicit-target claim must succeed: %v", err)
	}
	if _, err := f.store.AcceptSource(context.Background(), "grantee",
		pendingTeamSourceFor(t, f, offer.GrantID, "team_1", "grantee"),
		AcceptInput{TargetGroupID: "grp-target"}, f.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("grantee claim must succeed: %v", err)
	}

	// 过期时间校验发生在来源写入之前：过去时间被拒绝，不产生任何领取面。
	expired := "2020-01-01T00:00:00Z"
	if _, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeUsername: "grantee",
		ExpiresAt: &expired,
	}, "owner", false, "owner"); err == nil {
		t.Fatalf("past expiry must be rejected by create validation")
	}
}

func strPtr(value string) *string { return &value }
