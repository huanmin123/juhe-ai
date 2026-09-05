// Contract-alignment tests for the M03 review fixes (C1-C12). Each test cites
// the Node evidence lines it pins: backend/src/storage/system-team.repository.ts
// (repo) and backend/src/modules/system-teams/system-teams.routes.ts (routes),
// plus the authz cascade in backend/src/storage/resource-authorization-write.repository.ts
// (write).
package systemteams

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/group_dirty_cursor"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// The C9 ports must be satisfied by the existing production mechanisms
// (business/group_dirty_cursor Store and the inval Bus) — no bespoke
// implementations.
var (
	_ StatsDirtyMarker   = (*groupdirtycursor.Store)(nil)
	_ RuntimeInvalidator = (*inval.Bus)(nil)
)

// sortedKeys is a tiny helper for payload-shape assertions.
func sortedKeys(value map[string]any) []string {
	keys := []string{}
	for key := range value {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

// TestPatchContract_C3_C10 pins the PATCH response payload (repo :79-88,
// :1357-1374; routes :333 res.json(ok(outcome.result))) and the
// expectedUpdatedAt instant validation + canonical CAS (repo :1397-1401,
// routes :42/:301).
func TestPatchContract_C3_C10(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "admin", "admin-pass", "super_admin")
	teamID, revision := env.createTeamViaRoute(t, "契约组")

	// C10 negative: timezone-less instant → 400 团队版本格式不正确
	// (rfc3339InstantSchema rejects values without Z/offset).
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"2024-01-01T00:00:00","name":"x"}`)
	if code != http.StatusBadRequest || payload["message"] != "团队版本格式不正确" {
		t.Fatalf("timezone-less patch: %d %v", code, payload)
	}

	// C10 negative: garbage version.
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"not-a-time","name":"x"}`)
	if code != http.StatusBadRequest || payload["message"] != "团队版本格式不正确" {
		t.Fatalf("garbage patch: %d %v", code, payload)
	}

	// C10 canonical CAS: the same instant written with a +00:00 offset must
	// canonicalize to the stored Z form and succeed.
	offsetVersion := strings.TrimSuffix(revision, "Z") + "+00:00"
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"`+offsetVersion+`","name":"新名","description":null}`)
	if code != 200 {
		t.Fatalf("offset patch: %d %v", code, payload)
	}
	data := payload["data"].(map[string]any)
	// C3 payload shape: exactly {id, changedFields, rowPatch, updatedAt}.
	if got := strings.Join(sortedKeys(data), ","); got != "changedFields,id,rowPatch,updatedAt" {
		t.Fatalf("patch payload keys = %s (%v)", got, data)
	}
	changed := data["changedFields"].([]any)
	if len(changed) != 2 || changed[0] != "name" || changed[1] != "description" {
		t.Fatalf("changedFields = %v (repo :1345-1353 order)", changed)
	}
	rowPatch := data["rowPatch"].(map[string]any)
	name, hasName := rowPatch["name"]
	if !hasName || name != "新名" {
		t.Fatalf("rowPatch.name = %v", rowPatch)
	}
	description, hasDescription := rowPatch["description"]
	if !hasDescription || description != nil {
		t.Fatalf("rowPatch.description must be present JSON null (repo :82-87): %v", rowPatch)
	}

	// C3 noop payload: changedFields [] + rowPatch {} + updatedAt = the
	// canonical expected version (repo :448-450).
	currentRevision := env.teamRevision(t, teamID)
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"`+currentRevision+`","name":"新名"}`)
	if code != 200 {
		t.Fatalf("noop patch: %d %v", code, payload)
	}
	noopData := payload["data"].(map[string]any)
	if changed := noopData["changedFields"].([]any); len(changed) != 0 {
		t.Fatalf("noop changedFields = %v", changed)
	}
	if rowPatch := noopData["rowPatch"].(map[string]any); len(rowPatch) != 0 {
		t.Fatalf("noop rowPatch = %v", rowPatch)
	}
	if noopData["updatedAt"] != currentRevision {
		t.Fatalf("noop updatedAt = %v want %v", noopData["updatedAt"], currentRevision)
	}
}

// TestTeamScopeGuards_C5 pins the ?systemAccountId scope on the three admin
// write operations: the scoped row lookup renders not_found for accounts
// outside the membership (repo :826-845, :988-1007) and the CAS UPDATE carries
// the same EXISTS clause (repo :1376-1390, :461-462/:664-669/:764-769).
func TestTeamScopeGuards_C5(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "admin", "admin-pass", "super_admin")
	memberID := env.login(t, "member", "member-pass", "user")
	outsiderID := env.login(t, "outsider", "outsider-pass", "user")

	env.as("admin")
	teamID, revision := env.createTeamViaRoute(t, "越权组")
	code, _ := env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamID+"/members",
		`{"systemAccountIds":["`+memberID+`"],"expectedUpdatedAt":"`+revision+`"}`)
	if code != 200 {
		t.Fatalf("seed member: %d", code)
	}

	// PATCH scoped to a non-member → 404 团队不存在 (repo :826-845 visibility).
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID+"?systemAccountId="+outsiderID,
		`{"expectedUpdatedAt":"`+env.teamRevision(t, teamID)+`","name":"越权名"}`)
	if code != http.StatusNotFound || payload["message"] != "团队不存在" {
		t.Fatalf("scoped patch outsider: %d %v", code, payload)
	}
	// …and the team row is untouched.
	if got := mustQueryInt(t, env, `SELECT COUNT(*) FROM system_teams WHERE id = ? AND name = '越权组'`, teamID); got != 1 {
		t.Fatalf("outsider scoped patch mutated the team")
	}

	// PATCH scoped to the member → 200.
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID+"?systemAccountId="+memberID,
		`{"expectedUpdatedAt":"`+env.teamRevision(t, teamID)+`","name":"成员视角名"}`)
	if code != 200 {
		t.Fatalf("scoped patch member: %d %v", code, payload)
	}

	// addMembers scoped to a non-member → 404 团队不存在或已停用
	// (repo :988-1007 activeOnly lookup).
	outsiderRevision := env.teamRevision(t, teamID)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamID+"/members?systemAccountId="+outsiderID,
		`{"systemAccountIds":["`+outsiderID+`"],"expectedUpdatedAt":"`+outsiderRevision+`"}`)
	if code != http.StatusNotFound || payload["message"] != "团队不存在或已停用" {
		t.Fatalf("scoped addMembers outsider: %d %v", code, payload)
	}

	// removeMember scoped to a non-member → 404 团队成员不存在 (repo
	// :988-1007 team lookup before the member row is touched).
	members := env.memberIDs(t, teamID)
	code, payload = env.do(t, http.MethodDelete,
		"/__aisys__/api/system-teams/"+teamID+"/members/"+members[0]+"?systemAccountId="+outsiderID,
		`{"expectedUpdatedAt":"`+env.teamRevision(t, teamID)+`"}`)
	if code != http.StatusNotFound || payload["message"] != "团队成员不存在" {
		t.Fatalf("scoped removeMember outsider: %d %v", code, payload)
	}

	// addMembers scoped to the member → 200 and the outsider joins.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamID+"/members?systemAccountId="+memberID,
		`{"systemAccountIds":["`+outsiderID+`"],"expectedUpdatedAt":"`+env.teamRevision(t, teamID)+`"}`)
	if code != 200 {
		t.Fatalf("scoped addMembers member: %d %v", code, payload)
	}

	// removeMember scoped to the member → 200.
	code, _ = env.do(t, http.MethodDelete,
		"/__aisys__/api/system-teams/"+teamID+"/members/"+members[0]+"?systemAccountId="+memberID,
		`{"expectedUpdatedAt":"`+env.teamRevision(t, teamID)+`"}`)
	if code != 200 {
		t.Fatalf("scoped removeMember member: %d", code)
	}

	// The unfiltered admin scope still sees and mutates the team.
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"`+env.teamRevision(t, teamID)+`","name":"全量名"}`)
	if code != 200 {
		t.Fatalf("unscoped admin patch: %d", code)
	}
	_ = adminID
}

// TestRemoveMemberAtomicity_C11 pins the same-transaction revocation
// (repo :777): when revokeTeamSourcesForMemberAsync fails its fan-out ceiling
// (write :1610-1612), the member delete and team version bump roll back.
func TestRemoveMemberAtomicity_C11(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "admin", "admin-pass", "super_admin")
	memberAccountID := env.login(t, "victim", "victim-pass", "user")
	env.as("admin")
	teamID, _ := env.createTeamViaRoute(t, "原子组")
	revision := env.teamRevision(t, teamID)
	memberRowID := insertActiveMember(t, env, teamID, memberAccountID, revision)

	// Seed 21 active team sources carried by runtime rows of this member —
	// one above the write :1609 ceiling (grants+1).
	for i := 0; i < 21; i++ {
		insertRuntimeWithTeamSource(t, env, "auth_"+string(rune('a'+i)), "grp_"+string(rune('a'+i)),
			memberAccountID, "admin-id", teamID, revision)
	}

	code, payload := env.do(t, http.MethodDelete,
		"/__aisys__/api/system-teams/"+teamID+"/members/"+memberRowID,
		`{"expectedUpdatedAt":"`+revision+`"}`)
	if code != http.StatusBadRequest || payload["message"] != "单个授权团队最多支持 20 条有效授权，请先回收或停用部分授权" {
		t.Fatalf("over-limit removeMember: %d %v", code, payload)
	}

	// Atomicity: the member is still active and the team version is
	// unchanged (the whole transaction rolled back).
	if got := mustQueryInt(t, env, `SELECT COUNT(*) FROM system_team_members WHERE id = ? AND status = 'active'`, memberRowID); got != 1 {
		t.Fatalf("member was deleted despite revocation failure")
	}
	if got := env.teamRevision(t, teamID); got != revision {
		t.Fatalf("team revision moved on rolled-back remove: %s != %s", got, revision)
	}

	// Below the ceiling a removal succeeds (a second member keeps the
	// dedupe fingerprint distinct from the failed attempt above).
	secondAccountID := env.login(t, "secondvictim", "second-pass", "user")
	env.as("admin")
	insertActiveMember(t, env, teamID, secondAccountID, revision)
	secondRowID := mustQueryString(t, env, `SELECT id FROM system_team_members WHERE team_id = ? AND system_account_id = ?`, teamID, secondAccountID)
	code, payload = env.do(t, http.MethodDelete,
		"/__aisys__/api/system-teams/"+teamID+"/members/"+secondRowID,
		`{"expectedUpdatedAt":"`+env.teamRevision(t, teamID)+`"}`)
	if code != 200 {
		t.Fatalf("removeMember second: %d %v", code, payload)
	}
	if got := mustQueryInt(t, env, `SELECT COUNT(*) FROM system_team_members WHERE id = ? AND status = 'removed'`, secondRowID); got != 1 {
		t.Fatalf("second member not removed")
	}
}

// TestAddMembersFanoutRollback_C6 pins the in-transaction grant fan-out
// (repo :696): a fan-out failure (write :1564-1566 grants ceiling) rolls back
// the member inserts and the team version bump.
func TestAddMembersFanoutRollback_C6(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "admin", "admin-pass", "super_admin")
	newMemberID := env.login(t, "newmember", "newmember-pass", "user")
	env.as("admin")
	teamID, _ := env.createTeamViaRoute(t, "扇出组")
	revision := env.teamRevision(t, teamID)

	// 21 active team grants — one above the write :1563 ceiling.
	for i := 0; i < 21; i++ {
		insertTeamGrant(t, env, "g_"+string(rune('a'+i)), "grp_"+string(rune('a'+i)), teamID, "admin-id", revision)
	}

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamID+"/members",
		`{"systemAccountIds":["`+newMemberID+`"],"expectedUpdatedAt":"`+revision+`"}`)
	if code != http.StatusBadRequest || payload["message"] != "单个授权团队最多支持 20 条有效授权，请先回收或停用部分授权" {
		t.Fatalf("over-limit fanout: %d %v", code, payload)
	}
	if got := mustQueryInt(t, env, `SELECT COUNT(*) FROM system_team_members WHERE team_id = ?`, teamID); got != 0 {
		t.Fatalf("member rows survived the rolled-back fan-out: %d", got)
	}
	if got := env.teamRevision(t, teamID); got != revision {
		t.Fatalf("team revision moved on rolled-back add: %s != %s", got, revision)
	}
}

// TestAddMembersFanoutAndSideEffects_C6_C9 pins the happy-path fan-out —
// grant-driven team source + runtime rows carrying remark/expiresAt/limits,
// owner skipped (repo :1574-1596, write :1574-1596) — plus the committed-write
// side effects (repo :709-711 → :1478-1494): group-stats dirty marker and
// gateway runtime + authorization quota invalidation.
func TestAddMembersFanoutAndSideEffects_C6_C9(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "admin", "admin-pass", "super_admin")
	memberAccountID := env.login(t, "fanoutmember", "fanoutmember-pass", "user")
	ownerID := env.login(t, "resourceowner", "resourceowner-pass", "user")
	env.as("admin")
	teamID, revision := env.createTeamViaRoute(t, "扇出成功组")

	// One active team grant owned by a third account with a remark; the owner
	// must NOT receive their own runtime row (write :1579).
	mustExec(t, env, `INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_fan', 'Group grp_fan', ?, 'active')`, ownerID)
	insertTeamGrant(t, env, "grant_fan", "grp_fan", teamID, ownerID, revision)
	env.stats.reasons = nil
	env.inval.calls = nil

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamID+"/members",
		`{"systemAccountIds":["`+memberAccountID+`","`+ownerID+`"],"expectedUpdatedAt":"`+revision+`"}`)
	if code != 200 {
		t.Fatalf("add members: %d %v", code, payload)
	}

	// The member got a runtime row with team source and the grant remark.
	var status, effectiveType, remark string
	if err := env.store.db.QueryRow(`SELECT status, effective_source_type, COALESCE(remark,'') FROM resource_authorizations
		WHERE resource_id = 'grp_fan' AND grantee_system_account_id = ?`, memberAccountID).
		Scan(&status, &effectiveType, &remark); err != nil {
		t.Fatalf("member runtime missing: %v", err)
	}
	if status != "active" || effectiveType != "team" || remark != "" {
		t.Fatalf("member runtime = %s/%s/%q", status, effectiveType, remark)
	}
	if got := mustQueryInt(t, env, `SELECT COUNT(*) FROM resource_authorization_sources
		WHERE authorization_id IN (SELECT id FROM resource_authorizations WHERE grantee_system_account_id = ?)
		AND source_type = 'team' AND source_team_id = ? AND status = 'active'`, memberAccountID, teamID); got != 1 {
		t.Fatalf("member team source count = %d", got)
	}
	// Owner skipped (write :1579).
	if got := mustQueryInt(t, env, `SELECT COUNT(*) FROM resource_authorizations WHERE grantee_system_account_id = ?`, ownerID); got != 0 {
		t.Fatalf("owner must not receive their own team runtime row")
	}

	// C9 side effects fired with the Node reason string.
	reasons := env.stats.recorded()
	if len(reasons) != 1 || reasons[0] != "team_members_changed" {
		t.Fatalf("stats dirty reasons = %v", reasons)
	}
	calls := env.inval.recorded()
	joined := strings.Join(calls, ";")
	if !strings.Contains(joined, "topic:gateway_runtime_cache|team_members_changed") ||
		!strings.Contains(joined, "topic:authorization_quota_cache|team_members_changed") {
		t.Fatalf("invalidations = %v", calls)
	}
	_ = adminID
}

// TestPatchDisableEnableCascade_C7_C8 pins the enable/disable cascade inside
// the patch transaction (repo :525-533) with real actor bookkeeping
// (write :1645-1658) and the reactivate grant projection (write :1574-1596).
func TestPatchDisableEnableCascade_C7_C8(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "admin", "admin-pass", "super_admin")
	memberAccountID := env.login(t, "cascademember", "cascademember-pass", "user")
	ownerID := env.login(t, "cascadeowner", "cascadeowner-pass", "user")
	env.as("admin")
	teamID, revision := env.createTeamViaRoute(t, "级联组")

	mustExec(t, env, `INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_cas', 'Group grp_cas', ?, 'active')`, ownerID)
	insertTeamGrant(t, env, "grant_cas", "grp_cas", teamID, ownerID, revision)
	memberRowID := insertActiveMember(t, env, teamID, memberAccountID, revision)
	insertRuntimeWithTeamSource(t, env, "auth_cas", "grp_cas", memberAccountID, ownerID, teamID, revision)

	// Disable → all team sources revoked with revoked_by = the acting admin
	// (write :1652-1653), runtime refreshed to revoked.
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"`+env.teamRevision(t, teamID)+`","status":"disabled"}`)
	if code != 200 {
		t.Fatalf("disable patch: %d %v", code, payload)
	}
	var revokedBy string
	if err := env.store.db.QueryRow(`SELECT revoked_by FROM resource_authorization_sources
		WHERE authorization_id = 'auth_cas' AND source_type = 'team' AND status = 'revoked'`).Scan(&revokedBy); err != nil {
		t.Fatalf("disable cascade did not revoke the team source: %v", err)
	}
	if revokedBy != adminID {
		t.Fatalf("revoked_by = %q want the acting admin %q", revokedBy, adminID)
	}
	if got := mustQueryInt(t, env, `SELECT COUNT(*) FROM resource_authorizations WHERE id = 'auth_cas' AND status = 'revoked'`); got != 1 {
		t.Fatalf("runtime not refreshed to revoked")
	}
	// C9 team_authorization_changed side effects.
	if reasons := env.stats.recorded(); len(reasons) == 0 || reasons[len(reasons)-1] != "team_authorization_changed" {
		t.Fatalf("disable stats reasons = %v", env.stats.recorded())
	}

	// Rollback guard (C7): >20 grants aborts the reactivation and the team
	// stays disabled with its version unchanged.
	disabledRevision := env.teamRevision(t, teamID)
	for i := 0; i < 21; i++ {
		insertTeamGrant(t, env, "gb_"+string(rune('a'+i)), "grp_"+string(rune('a'+i)), teamID, ownerID, disabledRevision)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"`+disabledRevision+`","status":"active"}`)
	if code != http.StatusBadRequest || payload["message"] != "单个授权团队最多支持 20 条有效授权，请先回收或停用部分授权" {
		t.Fatalf("over-limit reactivate: %d %v", code, payload)
	}
	if got := mustQueryInt(t, env, `SELECT COUNT(*) FROM system_teams WHERE id = ? AND status = 'disabled' AND updated_at = ?`, teamID, disabledRevision); got != 1 {
		t.Fatalf("reactivate failure did not roll back the status transition")
	}

	// Trim below the ceiling → enable reactivates the grant fan-out: the
	// runtime row returns to active with the team source restored.
	mustExec(t, env, `DELETE FROM resource_authorization_grants WHERE id LIKE 'gb_%'`)
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"`+env.teamRevision(t, teamID)+`","status":"active"}`)
	if code != 200 {
		t.Fatalf("enable patch: %d %v", code, payload)
	}
	if got := mustQueryInt(t, env, `SELECT COUNT(*) FROM resource_authorizations WHERE id = 'auth_cas' AND status = 'active' AND effective_source_type = 'team'`); got != 1 {
		t.Fatalf("reactivate did not restore the member runtime")
	}
	if got := mustQueryInt(t, env, `SELECT COUNT(*) FROM resource_authorization_sources
		WHERE authorization_id = 'auth_cas' AND source_type = 'team' AND status = 'active'`); got != 1 {
		t.Fatalf("reactivate did not restore the team source")
	}
	if got := mustQueryInt(t, env, `SELECT COUNT(*) FROM system_team_members WHERE id = ? AND status = 'active'`, memberRowID); got != 1 {
		t.Fatalf("member must stay active through the cascade")
	}
}

// TestAddMembersCapAndDuplicate_C12 pins the cap semantics (repo :620-641):
// ceiling computed after excluding already-active members, batch duplicates
// rejected, and an all-active batch is a noop success with the unchanged
// version.
func TestAddMembersCapAndDuplicate_C12(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "admin", "admin-pass", "super_admin")
	ids := make([]string, 0, 22)
	for i := 0; i < 22; i++ {
		ids = append(ids, env.login(t, "capuser"+string(rune('a'+i)), "cap-pass-"+string(rune('a'+i)), "user"))
	}
	env.as("admin")

	// Branch 1: in-batch duplicates → 400 团队成员不能重复 (repo :1462-1464).
	teamA, revisionA := env.createTeamViaRoute(t, "重复组")
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamA+"/members",
		`{"systemAccountIds":["`+ids[0]+`","`+ids[0]+`"],"expectedUpdatedAt":"`+revisionA+`"}`)
	if code != http.StatusBadRequest || payload["message"] != "团队成员不能重复" {
		t.Fatalf("duplicate batch: %d %v", code, payload)
	}
	if got := env.teamRevision(t, teamA); got != revisionA {
		t.Fatalf("rejected batch moved the version: %s != %s", got, revisionA)
	}

	// Route-level batch ceiling (routes :54 max batch).
	overBatch := `{"systemAccountIds":[` + `"` + ids[0] + `"` + strings.Repeat(`,"`+ids[0]+`"`, 21) + `],"expectedUpdatedAt":"` + revisionA + `"}`
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamA+"/members", overBatch)
	if code != http.StatusBadRequest || payload["message"] != "单次最多添加 20 个团队成员" {
		t.Fatalf("batch ceiling: %d %v", code, payload)
	}

	// Branch 2: cap counts NEW members only — 19 seeded actives plus a batch
	// of [already-active, new] fits exactly at 20 (repo :631-633).
	teamB, revisionB := env.createTeamViaRoute(t, "容量组")
	for i := 0; i < 19; i++ {
		insertActiveMember(t, env, teamB, ids[i], revisionB)
	}
	// Batch = ids[0] (already active) + ids[19] (new) → next = [ids[19]]
	// → 19 existing + 1 new = 20 ≤ 20 → success adds only the new one.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamB+"/members",
		`{"systemAccountIds":["`+ids[0]+`","`+ids[19]+`"],"expectedUpdatedAt":"`+env.teamRevision(t, teamB)+`"}`)
	if code != 200 {
		t.Fatalf("cap-inclusive add: %d %v", code, payload)
	}
	added := payload["data"].(map[string]any)["addedMembers"].([]any)
	if len(added) != 1 {
		t.Fatalf("only the new member may be added: %v", added)
	}
	if got := payload["data"].(map[string]any)["memberCount"]; got != float64(20) {
		t.Fatalf("memberCount = %v", got)
	}

	// Cap exceeded: 20 actives + one more → 400 (repo :627-629/:632-634).
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamB+"/members",
		`{"systemAccountIds":["`+ids[20]+`"],"expectedUpdatedAt":"`+env.teamRevision(t, teamB)+`"}`)
	if code != http.StatusBadRequest || payload["message"] != "授权团队最多支持 20 个成员，请先移除部分成员后再添加" {
		t.Fatalf("cap exceeded: %d %v", code, payload)
	}

	// Branch 3: all requested already active → noop 200 with the UNCHANGED
	// team version (repo :635-641).
	revisionBefore := env.teamRevision(t, teamB)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamB+"/members",
		`{"systemAccountIds":["`+ids[0]+`","`+ids[19]+`"],"expectedUpdatedAt":"`+revisionBefore+`"}`)
	if code != 200 {
		t.Fatalf("all-active noop: %d %v", code, payload)
	}
	noop := payload["data"].(map[string]any)
	if noop["memberCount"] != float64(20) || noop["updatedAt"] != revisionBefore {
		t.Fatalf("noop payload = %v (want memberCount 20, updatedAt %s)", noop, revisionBefore)
	}
	if added := noop["addedMembers"].([]any); len(added) != 0 {
		t.Fatalf("noop addedMembers = %v", added)
	}
	if got := env.teamRevision(t, teamB); got != revisionBefore {
		t.Fatalf("noop bumped the version: %s != %s", got, revisionBefore)
	}
}

// TestMembersListPagination_C2 pins the dedicated paginated member list and
// its DTO (repo :292-332, :1227-1244, :1201-1208): active-only rows,
// joined_at ASC/id ASC, pageSize+1 lookahead, and the systemAccountName field
// name.
func TestMembersListPagination_C2(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "admin", "admin-pass", "super_admin")
	a := env.login(t, "lista", "lista-pass", "user")
	b := env.login(t, "listb", "listb-pass", "user")
	c := env.login(t, "listc", "listc-pass", "user")
	env.as("admin")
	teamID, _ := env.createTeamViaRoute(t, "分页组")
	insertActiveMember(t, env, teamID, a, "2024-01-01T00:00:00.000Z")
	insertActiveMember(t, env, teamID, b, "2024-01-02T00:00:00.000Z")
	insertActiveMember(t, env, teamID, c, "2024-01-03T00:00:00.000Z")

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/system-teams/"+teamID+"/members?page=1&pageSize=2", "")
	if code != 200 {
		t.Fatalf("members page 1: %d %v", code, payload)
	}
	page := payload["data"].(map[string]any)
	// Full DTO keys (repo :1234-1243): id/items/memberCount/updatedAt/total/
	// hasMore/page/pageSize.
	if got := strings.Join(sortedKeys(page), ","); got != "hasMore,id,items,memberCount,page,pageSize,total,updatedAt" {
		t.Fatalf("members payload keys = %s (%v)", got, page)
	}
	if page["memberCount"] != float64(3) || page["total"] != float64(3) {
		t.Fatalf("memberCount/total = %v/%v", page["memberCount"], page["total"])
	}
	if page["hasMore"] != true || page["page"] != float64(1) || page["pageSize"] != float64(2) {
		t.Fatalf("page envelope = %v", page)
	}
	currentRevision := env.teamRevision(t, teamID)
	if page["updatedAt"] != currentRevision {
		t.Fatalf("updatedAt = %v want %s", page["updatedAt"], currentRevision)
	}
	items := page["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	first := items[0].(map[string]any)
	if first["systemAccountId"] != a {
		t.Fatalf("joined_at ordering broken: %v", first)
	}
	if _, has := first["systemAccountName"]; !has {
		t.Fatalf("systemAccountName field missing (repo :1205): %v", first)
	}
	if _, has := first["displayName"]; has {
		t.Fatalf("legacy displayName field leaked: %v", first)
	}

	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/system-teams/"+teamID+"/members?page=2&pageSize=2", "")
	if code != 200 {
		t.Fatalf("members page 2: %d %v", code, payload)
	}
	page2 := payload["data"].(map[string]any)
	if page2["hasMore"] != false || page2["page"] != float64(2) {
		t.Fatalf("page2 envelope = %v", page2)
	}
	if items := page2["items"].([]any); len(items) != 1 || items[0].(map[string]any)["systemAccountId"] != c {
		t.Fatalf("page2 items = %v", items)
	}
}

// TestMemberHistoryFilterOrder_C4 pins the removed-only filter and the
// joined_at DESC, id DESC ordering (repo :338-348).
func TestMemberHistoryFilterOrder_C4(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "admin", "admin-pass", "super_admin")
	keeper := env.login(t, "keeper", "keeper-pass", "user")
	leaver1 := env.login(t, "leaver1", "leaver-pass", "user")
	leaver2 := env.login(t, "leaver2", "leaver-pass", "user")
	env.as("admin")
	teamID, revision := env.createTeamViaRoute(t, "历史组")
	insertActiveMember(t, env, teamID, keeper, revision)
	row1 := insertActiveMember(t, env, teamID, leaver1, "2024-01-01T00:00:00.000Z")
	row2 := insertActiveMember(t, env, teamID, leaver2, "2024-02-01T00:00:00.000Z")
	// Seed removed rows directly (the C4 history filter is read-side).
	mustExec(t, env, `UPDATE system_team_members SET status = 'removed', removed_at = '2024-03-01T00:00:00.000Z' WHERE id IN (?, ?)`, row1, row2)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/system-teams/"+teamID+"/members/history", "")
	if code != 200 {
		t.Fatalf("history: %d %v", code, payload)
	}
	data := payload["data"].(map[string]any)
	items := data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("history items = %v", items)
	}
	// joined_at DESC: leaver2 (2024-02) before leaver1 (2024-01).
	if items[0].(map[string]any)["systemAccountId"] != leaver2 || items[1].(map[string]any)["systemAccountId"] != leaver1 {
		t.Fatalf("history order = %v", items)
	}
	for _, item := range items {
		entry := item.(map[string]any)
		if entry["status"] != "removed" || entry["removedAt"] == nil {
			t.Fatalf("history entry = %v", entry)
		}
	}
	// The active member is filtered out.
	for _, item := range items {
		if item.(map[string]any)["systemAccountId"] == keeper {
			t.Fatalf("active member leaked into history: %v", item)
		}
	}
}

// TestTeamListOrdering_C1 pins ORDER BY status ASC, updated_at DESC, name ASC,
// id ASC (repo :895/:935).
func TestTeamListOrdering_C1(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "admin", "admin-pass", "super_admin")
	seed := []struct {
		name      string
		status    string
		updatedAt string
	}{
		{"a队", "active", "2024-01-01T00:00:00.000Z"},
		{"b队", "active", "2024-03-01T00:00:00.000Z"},
		{"c队", "disabled", "2024-05-01T00:00:00.000Z"},
		{"d队", "disabled", "2024-02-01T00:00:00.000Z"},
	}
	for _, team := range seed {
		mustExec(t, env, `INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
			VALUES (?, ?, ?, 'admin-id', '2024-01-01T00:00:00.000Z', ?)`,
			"team_"+team.name, team.name, team.status, team.updatedAt)
	}
	items, _, err := env.store.ListPage(nil, AccessScope{}, 1, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"b队", "a队", "c队", "d队"}
	if len(items) != len(want) {
		t.Fatalf("items = %v", items)
	}
	for i, item := range items {
		if item.Name != want[i] {
			t.Fatalf("order[%d] = %s want %s (status ASC, updated_at DESC)", i, item.Name, want[i])
		}
	}
}

// TestDisableFanoutCeiling_C8 pins the revoke-all ceiling
// members×grants+1 (write :1631-1643) and that the failure aborts the patch.
func TestDisableFanoutCeiling_C8(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "admin", "admin-pass", "super_admin")
	teamID, revision := env.createTeamViaRoute(t, "上限组")

	// 401 distinct active team sources — one above the fan-out ceiling.
	for i := 0; i < 401; i++ {
		insertRuntimeWithTeamSource(t, env, "auth_"+itoa(i), "grp_"+itoa(i), "ghost"+itoa(i), "admin-id", teamID, revision)
	}
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"`+revision+`","status":"disabled"}`)
	if code != http.StatusBadRequest || payload["message"] != "授权团队来源展开超过当前系统上限，请先拆分团队或回收部分授权" {
		t.Fatalf("fan-out ceiling: %d %v", code, payload)
	}
	if got := mustQueryInt(t, env, `SELECT COUNT(*) FROM system_teams WHERE id = ? AND status = 'active' AND updated_at = ?`, teamID, revision); got != 1 {
		t.Fatalf("failed disable mutated the team row")
	}
}

// TestCanonicalizeInstant_C10 pins the instant canonicalization primitive
// against shared/rfc3339.ts (rfc3339InstantPattern + toISOString).
func TestCanonicalizeInstant_C10(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"2024-01-02T03:04:05Z", "2024-01-02T03:04:05.000Z", true},
		{"2024-01-02T03:04:05.5Z", "2024-01-02T03:04:05.500Z", true},
		{"2024-01-02T10:04:05+07:00", "2024-01-02T03:04:05.000Z", true},
		{" 2024-01-02T03:04:05Z ", "2024-01-02T03:04:05.000Z", true},
		{"2024-01-02T03:04:05", "", false},             // timezone-less → 400
		{"2024-01-02 03:04:05Z", "", false},            // not RFC3339
		{"2024-13-02T03:04:05Z", "", false},            // month out of range
		{"2024-02-30T03:04:05Z", "", false},            // day out of range
		{"2024-01-02T03:04:05.1234567890Z", "", false}, // >9 fraction digits
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := CanonicalizeInstant(c.input)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("CanonicalizeInstant(%q) = %q,%v want %q,%v", c.input, got, ok, c.want, c.ok)
		}
	}
}

// TestNextVersionFloor_C10 pins nextSystemTeamUpdatedAt (repo :1397-1401):
// max(now, expected+1ms) in canonical UTC milliseconds.
func TestNextVersionFloor_C10(t *testing.T) {
	now := mustParseTime(t, "2024-01-02T03:04:05.000Z")
	// Now wins.
	got, err := NextVersion("2024-01-02T03:04:00.000Z", now)
	if err != nil || got != "2024-01-02T03:04:05.000Z" {
		t.Fatalf("NextVersion now-wins = %s,%v", got, err)
	}
	// Expected floor wins: expected + 1 millisecond.
	got, err = NextVersion("2024-01-02T03:04:05.500Z", now)
	if err != nil || got != "2024-01-02T03:04:05.501Z" {
		t.Fatalf("NextVersion floor = %s,%v", got, err)
	}
	// Invalid instant rejected with the verbatim message.
	_, err = NextVersion("2024-01-02T03:04:05", now)
	if err == nil || !strings.Contains(err.Error(), "系统团队 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间") {
		t.Fatalf("NextVersion invalid = %v", err)
	}
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
