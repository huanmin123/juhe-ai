package authz

// authz 团队级联/资源删除/到期重放与选项读面补充测试（wd_ 前缀，独占新增）：
//   - team_cascade.go：成员移除/团队停用/重新启用的级联语义与扇出上限。
//   - delete_resource.go：资源删除时非终态 grant 经运行时同步域回收。
//   - expiry_reconcile.go：jobs sweep 翻转后的投影重放（幂等）。
//   - options_routes/options_store：被授权人选项读面与查询参数镜像。
// 全部走 SQLite 内存 fixture，断言业务契约而非自述。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// wdGrantRuntimeStatus 按资源三元组读取运行时投影行（运行时行主键与 grant id
// 不同源，与既有测试一致按 resource+grantee 定位）。
func wdGrantRuntimeStatus(t *testing.T, f *fixture, resourceType, resourceID, grantee string) (string, string) {
	t.Helper()
	var status, effective string
	if err := f.db.QueryRow(`SELECT status, COALESCE(effective_source_type,'') FROM resource_authorizations
		WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ?`, resourceType, resourceID, grantee).
		Scan(&status, &effective); err != nil {
		t.Fatalf("runtime row: %v", err)
	}
	return status, effective
}

func wdSourceRows(t *testing.T, f *fixture, runtimeID string) []map[string]string {
	t.Helper()
	rows, err := f.db.Query(`SELECT source_team_id, status, COALESCE(ended_reason,'') FROM resource_authorization_sources
		WHERE authorization_id = ? ORDER BY source_team_id`, runtimeID)
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var teamID, status, reason sql.NullString
		if err := rows.Scan(&teamID, &status, &reason); err != nil {
			t.Fatalf("scan source: %v", err)
		}
		result = append(result, map[string]string{"team": teamID.String, "status": status.String, "reason": reason.String})
	}
	return result
}

func wdCreateTeamGrant(t *testing.T, f *fixture, resourceType, resourceID, owner, teamID string) CreateResult {
	t.Helper()
	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: resourceType, ResourceID: resourceID,
		GranteeType: "team", GranteeID: teamID,
	}, owner)
	if err != nil {
		t.Fatalf("create team grant: %v", err)
	}
	if !result.Created {
		t.Fatalf("team grant must be created: %+v", result)
	}
	return *result
}

func TestWdCascadeLimitConstants(t *testing.T) {
	if cascadeTeamMemberLimit() != MaxTeamMembersPerTeam+1 {
		t.Fatalf("成员上限镜像错误: %d", cascadeTeamMemberLimit())
	}
	if cascadeTeamGrantLimit() != MaxTeamActiveGrantCount+1 {
		t.Fatalf("授权上限镜像错误: %d", cascadeTeamGrantLimit())
	}
	if cascadeFanoutLimit() != MaxTeamMembersPerTeam*MaxTeamActiveGrantCount+1 {
		t.Fatalf("扇出上限镜像错误: %d", cascadeFanoutLimit())
	}
}

func TestWdTeamCascadeRevokeMemberRevokeAllReactivate(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member1", "active")
	f.seedAccount(t, "member2", "active")
	f.seedTeamWithMember(t, "team_wd", "member1")
	f.seedTeamWithMember(t, "team_wd", "member2")
	f.seedGroup(t, "grp_wd", "owner")
	wdCreateTeamGrant(t, f, "group", "grp_wd", "owner", "team_wd")

	// 团队授权创建后：成员运行时行带 team 生效来源。
	status, effective := wdGrantRuntimeStatus(t, f, "group", "grp_wd", "member1")
	if status != StatusActive || effective != "team" {
		t.Fatalf("创建后运行时错误: %s/%s", status, effective)
	}

	// 移除成员：该成员的 team source 翻转 revoked（ended_reason member_removed），
	// 运行时经 refresh 后无剩余来源 → 终态 revoked；其余成员不受影响。
	if err := f.store.RevokeTeamSourcesForMember(context.Background(), "team_wd", "member1", "admin-actor"); err != nil {
		t.Fatalf("revoke member: %v", err)
	}
	member1Status, member1Effective := wdGrantRuntimeStatus(t, f, "group", "grp_wd", "member1")
	if member1Status != StatusRevoked || member1Effective != "" {
		t.Fatalf("移除成员后运行时应终态 revoked 且无来源: %s/%s", member1Status, member1Effective)
	}
	_, member2Effective := wdGrantRuntimeStatus(t, f, "group", "grp_wd", "member2")
	if member2Effective != "team" {
		t.Fatalf("其余成员不受影响: %q", member2Effective)
	}

	// 重新启用团队：active 成员重挂 team 运行时。
	if err := f.store.ReactivateTeamGrants(context.Background(), "team_wd", "admin-actor"); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if _, effective := wdGrantRuntimeStatus(t, f, "group", "grp_wd", "member1"); effective != "team" {
		t.Fatalf("重新激活后成员应恢复 team 来源: %q", effective)
	}

	// 停用团队：全部 team source 翻转 revoked（保留真实 actor 与 reason）。
	if err := f.store.RevokeAllTeamSources(context.Background(), "team_wd", "admin-actor", "team_disabled"); err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	sources := wdSourceRows(t, f, wdRuntimeID(t, f, "group", "grp_wd", "member1"))
	if len(sources) == 0 {
		t.Fatalf("团队来源行缺失")
	}
	for _, source := range sources {
		if source["status"] != "revoked" {
			t.Fatalf("停用后来源应 revoked: %#v", source)
		}
	}
	var revokedBy string
	if err := f.db.QueryRow(`SELECT COALESCE(revoked_by,'') FROM resource_authorization_sources
		WHERE authorization_id = ? AND source_team_id = 'team_wd' LIMIT 1`, wdRuntimeID(t, f, "group", "grp_wd", "member1")).Scan(&revokedBy); err != nil {
		t.Fatalf("revoked_by: %v", err)
	}
	if revokedBy != "admin-actor" {
		t.Fatalf("revoked_by 应为真实 actor: %q", revokedBy)
	}
	// 停用团队后所有成员的运行时行失去来源 → 终态 revoked。
	if status, effective := wdGrantRuntimeStatus(t, f, "group", "grp_wd", "member1"); status != StatusRevoked || effective != "" {
		t.Fatalf("停用团队后运行时应 revoked 且无来源: %s/%s", status, effective)
	}
	if status, _ := wdGrantRuntimeStatus(t, f, "group", "grp_wd", "member2"); status != StatusRevoked {
		t.Fatalf("停用团队后其余成员运行时也应 revoked: %s", status)
	}
}

func TestWdCascadeMemberOverLimitRejected(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedGroup(t, "grp_limit", "owner")
	f.seedTeamWithMember(t, "team_limit", "owner")
	now := f.now.UTC().Format(time.RFC3339Nano)
	// 超过 MaxTeamMembersPerTeam 的活跃成员：重放应拒绝。
	for index := 0; index <= MaxTeamMembersPerTeam; index++ {
		memberID := "m" + strconv.Itoa(index)
		f.seedAccount(t, memberID, "active")
		if _, err := f.db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
			VALUES (?, 'team_limit', ?, 'active', ?, ?, ?)`, "tm_"+memberID, memberID, now, now, now); err != nil {
			t.Fatalf("seed member: %v", err)
		}
	}
	err := f.store.ReactivateTeamGrants(context.Background(), "team_limit", "actor")
	if err == nil || !strings.Contains(err.Error(), "授权团队最多支持") {
		t.Fatalf("超限成员应拒绝: %v", err)
	}
}

func TestWdCascadeGrantsOverLimitRejected(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member", "active")
	f.seedTeamWithMember(t, "team_gl", "member")
	now := f.now.UTC().Format(time.RFC3339Nano)
	// 直接插入 MaxTeamActiveGrantCount+1 条活跃团队 grant（绕过 store 的写入上限校验），
	// 级联重放必须拒绝而不是部分应用。
	for index := 0; index <= MaxTeamActiveGrantCount; index++ {
		groupID := "grp_gl_" + strconv.Itoa(index)
		f.seedGroup(t, groupID, "owner")
		grantID := "glg_" + strconv.Itoa(index)
		if _, err := f.db.Exec(`INSERT INTO resource_authorization_grants
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_team_id, status, created_by, created_at, updated_at)
			VALUES (?, 'group', ?, 'owner', 'team', 'team_gl', 'active', 'owner', ?, ?)`, grantID, groupID, now, now); err != nil {
			t.Fatalf("seed grant: %v", err)
		}
		if _, err := f.db.Exec(`INSERT INTO resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_by, created_at, updated_at)
			VALUES (?, 'group', ?, 'owner', 'member', 'returned', 'owner', ?, ?)`, grantID, groupID, now, now); err != nil {
			t.Fatalf("seed runtime: %v", err)
		}
	}
	err := f.store.ReactivateTeamGrants(context.Background(), "team_gl", "actor")
	if err == nil || !strings.Contains(err.Error(), "单个授权团队最多支持") {
		t.Fatalf("超限团队授权应拒绝: %v", err)
	}
}

func TestWdRevokeGrantsForResourceDeleted(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedAccount(t, "owner2x", "active")
	f.seedGroup(t, "grp_del", "owner")
	f.seedGroup(t, "grp_del2", "owner2x")
	active, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_del",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// 另一条已终态（returned）的 grant：资源删除时保持不动。
	returned2, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_del2",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner2x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Return(context.Background(), returned2.Item.ID, returned2.Item.UpdatedAt, "grantee"); err != nil {
		t.Fatalf("return seed: %v", err)
	}

	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := f.now.UTC().Format(time.RFC3339Nano)
	if err := f.store.RevokeGrantsForResourceDeleted(context.Background(), tx, "group", "grp_del", "delete-actor", now); err != nil {
		t.Fatalf("revoke for deleted resource: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var grantStatus, revokedBy string
	if err := f.db.QueryRow(`SELECT status, COALESCE(revoked_by,'') FROM resource_authorization_grants WHERE id = ?`, active.Item.ID).
		Scan(&grantStatus, &revokedBy); err != nil {
		t.Fatal(err)
	}
	if grantStatus != StatusRevoked || revokedBy != "delete-actor" {
		t.Fatalf("活跃 grant 应被删除回收: %s by %s", grantStatus, revokedBy)
	}
	// 终态 grant 保持 returned。
	if err := f.db.QueryRow(`SELECT status FROM resource_authorization_grants WHERE id = ?`, returned2.Item.ID).
		Scan(&grantStatus); err != nil {
		t.Fatal(err)
	}
	if grantStatus != StatusReturned {
		t.Fatalf("终态 grant 不应被触碰: %s", grantStatus)
	}
	// 活跃 grant 的运行时行同步为 revoked。
	runtimeStatus, _ := wdGrantRuntimeStatus(t, f, "group", "grp_del", "grantee")
	if runtimeStatus != StatusRevoked {
		t.Fatalf("运行时应同步 revoked: %s", runtimeStatus)
	}
	for _, source := range wdSourceRows(t, f, wdRuntimeID(t, f, "group", "grp_del", "grantee")) {
		if source["status"] != "revoked" {
			t.Fatalf("直接授权的 manual source 应 revoked: %#v", source)
		}
	}
}

func TestWdReconcileExpiredGrantsReplaysProjection(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_exp", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_exp",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// 模拟 jobs sweep：把 grant 翻成 expired——真实到期由 expires_at 过去时
	// 驱动，refreshEffectiveSource 也据此刻画运行时状态。
	past := f.now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants SET status = 'expired', expires_at = ?,
		revoked_by = 'jobs-sweep', revoked_at = ?, updated_at = ? WHERE id = ?`, past, past, past, created.Item.ID); err != nil {
		t.Fatal(err)
	}
	reconciled, err := f.store.ReconcileExpiredGrants(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if reconciled != 1 {
		t.Fatalf("应重放 1 条 expired grant: %d", reconciled)
	}
	// 重放幂等：再跑一次仍为 1（无新增写入差异）。
	again, err := f.store.ReconcileExpiredGrants(context.Background(), 0, 0)
	if err != nil || again != 1 {
		t.Fatalf("幂等重放错误: %d %v", again, err)
	}
	var grantStatus, runtimeStatus string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorization_grants WHERE id = ?`, created.Item.ID).Scan(&grantStatus); err != nil {
		t.Fatal(err)
	}
	runtimeStatus, _ = wdGrantRuntimeStatus(t, f, "group", "grp_exp", "grantee")
	if grantStatus != StatusExpired {
		t.Fatalf("grant 应保持 expired: %s", grantStatus)
	}
	if runtimeStatus != StatusExpired {
		t.Fatalf("运行时应收敛到 expired: %s", runtimeStatus)
	}
}

// wdRuntimeID 按资源三元组解析运行时行 id（resource_authorization_sources
// .authorization_id 指向该 id，而非 grant id）。
func wdRuntimeID(t *testing.T, f *fixture, resourceType, resourceID, grantee string) string {
	t.Helper()
	var runtimeID string
	if err := f.db.QueryRow(`SELECT id FROM resource_authorizations
		WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ?`,
		resourceType, resourceID, grantee).Scan(&runtimeID); err != nil {
		t.Fatalf("runtime id: %v", err)
	}
	return runtimeID
}

// ---------------------------------------------------------------------------
// options_routes / options_store
// ---------------------------------------------------------------------------

func TestWdOptionQueryHelpers(t *testing.T) {
	if got := queryTextList([]string{"a, b", ",c", "b", "d,e"}, 50); len(got) != 5 || got[0] != "a" || got[4] != "e" {
		t.Fatalf("queryTextList 错误: %#v", got)
	}
	if got := queryTextList([]string{"1", "2", "3"}, 2); len(got) != 2 {
		t.Fatalf("queryTextList 截断错误: %#v", got)
	}
	if got := queryTextList([]string{"x"}, 0); len(got) != 1 {
		t.Fatalf("maxItems<1 应钳位为 1: %#v", got)
	}
	if got := integerQueryOrZero(" 42 "); got != 42 {
		t.Fatalf("integerQueryOrZero 整数错误: %d", got)
	}
	if got := integerQueryOrZero("12x"); got != 0 {
		t.Fatalf("非数字应为 0: %d", got)
	}
	if got := integerQueryOrZero(""); got != 0 {
		t.Fatalf("空串应为 0: %d", got)
	}
	if !integerQueryPresent(" 1 ") || integerQueryPresent("") {
		t.Fatalf("integerQueryPresent 错误")
	}
	if got := optionLimitValue(0, false); got != 50 {
		t.Fatalf("缺省 limit 应为 50: %d", got)
	}
	if got := optionLimitValue(0, true); got != 1 {
		t.Fatalf("下限钳位错误: %d", got)
	}
	if got := optionLimitValue(99, true); got != 50 {
		t.Fatalf("上限钳位错误: %d", got)
	}
	if got := optionLimitValue(7, true); got != 7 {
		t.Fatalf("透传错误: %d", got)
	}
	if booleanQueryValue("yes") == nil || *booleanQueryValue("TRUE") != true {
		t.Fatalf("真值解析错误")
	}
	if booleanQueryValue("no") == nil || *booleanQueryValue("0") != false {
		t.Fatalf("假值解析错误")
	}
	if booleanQueryValue("maybe") != nil {
		t.Fatalf("未知值应为 nil")
	}
	if got := normalizeTextList([]string{" b ", "a", "b", ""}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("normalizeTextList 应去重并排序: %#v", got)
	}
	if normalizeTextList(nil) != nil {
		t.Fatalf("空输入应为 nil")
	}
	if got := textPrefixUpperBound("abc"); got != "abd" {
		t.Fatalf("前缀上界错误: %q", got)
	}
	if got := keywordUpperBound("xyz"); got != "xy{" {
		t.Fatalf("关键字上界错误: %q", got)
	}
	if got := keywordUpperBound(""); got != "" {
		t.Fatalf("空关键字上界应为空: %q", got)
	}
	failValue := Fail{Message: "x"}
	if failValue.Error() != "x" || (&Conflict{CurrentUpdatedAt: "t"}).Error() == "" {
		t.Fatalf("Fail/Conflict Error 契约错误")
	}
	if got := failf("单个授权团队最多支持 %d 条", 20).Message; got != "单个授权团队最多支持 20 条" {
		t.Fatalf("failf 格式化错误: %q", got)
	}
	var target *Fail
	if !errorsAsFail(failf("boom"), &target) || target.Message != "boom" {
		t.Fatalf("errorsAsFail 错误")
	}
	var conflictTarget *Conflict
	if errorsAsConflict(failf("boom"), &conflictTarget) {
		t.Fatalf("Fail 不是 Conflict")
	}
	if orDefault("", "fb") != "fb" || orDefault("v", "fb") != "v" {
		t.Fatalf("orDefault 错误")
	}
	empty := ""
	full := "full"
	if orText(nil, "fb") != "fb" || orText(&empty, "fb") != "fb" || orText(&full, "fb") != "full" {
		t.Fatalf("orText 错误")
	}
}

func TestWdJSONHelperAndMutationVersion(t *testing.T) {
	var value map[string]any
	if err := jsonUnmarshal([]byte(`{"a":1}`), &value); err != nil || value["a"] != float64(1) {
		t.Fatalf("jsonUnmarshal 错误: %v", err)
	}
	encoded, err := jsonMarshal(map[string]int{"b": 2})
	if err != nil || string(encoded) != `{"b":2}` {
		t.Fatalf("jsonMarshal 错误: %s %v", encoded, err)
	}
	if _, ok := normalizeMutationVersion(""); ok {
		t.Fatalf("空版本应无效")
	}
	if _, ok := normalizeMutationVersion("not-a-time"); ok {
		t.Fatalf("非 RFC3339 版本应无效")
	}
	canonical, ok := normalizeMutationVersion(" 2026-09-04T12:00:00+08:00 ")
	if !ok || canonical == "" {
		t.Fatalf("合法版本应规范化: %q %v", canonical, ok)
	}
	if rawJSONPresent(nil) || rawJSONPresent(json.RawMessage("null")) || !rawJSONPresent(json.RawMessage(" 5 ")) {
		t.Fatalf("rawJSONPresent 错误")
	}
	if !rawJSONNull(json.RawMessage("  null  ")) || !rawJSONNull(json.RawMessage("  ")) || rawJSONNull(json.RawMessage(`"x"`)) {
		t.Fatalf("rawJSONNull 错误")
	}
	if status, ok := parseStatusInput(json.RawMessage(`"active"`)); !ok || status == nil || *status != StatusActive {
		t.Fatalf("status active 解析错误")
	}
	if status, ok := parseStatusInput(json.RawMessage(`"paused"`)); !ok || status == nil || *status != StatusPaused {
		t.Fatalf("status paused 解析错误")
	}
	if _, ok := parseStatusInput(json.RawMessage(`"weird"`)); ok {
		t.Fatalf("非法 status 应拒绝")
	}
	if _, ok := parseStatusInput(json.RawMessage(`5`)); ok {
		t.Fatalf("非字符串 status 应拒绝")
	}
	// 缺失与 null 都按"输入合法但无值"处理（value=nil, ok=true），
	// 存在与否由调用方通过 rawJSONPresent 区分。
	if value, ok := parseStatusInput(json.RawMessage("")); !ok || value != nil {
		t.Fatalf("缺失 status 应 (nil,true): %v %v", value, ok)
	}
	if value, ok := parseStatusInput(json.RawMessage("null")); !ok || value != nil {
		t.Fatalf("null status 应 (nil,true): %v %v", value, ok)
	}
	if _, set, ok := parseExpiresAtInput(json.RawMessage("")); set || !ok {
		t.Fatalf("缺失 expiresAt: ok=%v set=%v", ok, set)
	}
	if _, set, ok := parseExpiresAtInput(json.RawMessage("null")); !set || !ok {
		t.Fatalf("null expiresAt 应清除: ok=%v set=%v", ok, set)
	}
	if _, _, ok := parseExpiresAtInput(json.RawMessage(`"bogus"`)); ok {
		t.Fatalf("非法 expiresAt 应拒绝")
	}
	if value, set, ok := parseExpiresAtInput(json.RawMessage(`" 2026-09-04T12:00:00Z "`)); !ok || !set || value == nil {
		t.Fatalf("合法 expiresAt 解析错误")
	}
}

func TestWdGranteeOptionReadsAndHandlers(t *testing.T) {
	f := newFixture(t)
	// 账户与团队选项：active 优先、display_name 排序、keyword 前缀过滤。
	if _, err := f.db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES ('u1','user1',' DISPLAY','user','active','h','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES ('u2','user2','Zeta','user','disabled','h','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	f.seedTeamWithMember(t, "team_a", "u1")
	f.seedTeamWithMember(t, "team_b", "u2")

	accounts, err := f.store.ListAuthorizationGranteeAccounts(context.Background(), authorizationPrincipalOptionListOptions{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].ID != "u1" || accounts[0].DisplayName != " DISPLAY" {
		t.Fatalf("账户选项错误: %#v", accounts)
	}
	filtered, err := f.store.ListAuthorizationGranteeAccounts(context.Background(), authorizationPrincipalOptionListOptions{
		IDs: []string{"u2", "u2", "u1"}, Keyword: "use", Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("ids+keyword 过滤错误: %#v", filtered)
	}
	teams, err := f.store.ListAuthorizationGranteeTeams(context.Background(), authorizationPrincipalOptionListOptions{Keyword: "Team team_", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 1 || teams[0].ID != "team_a" {
		t.Fatalf("团队选项过滤错误: %#v", teams)
	}

	// 分组选项：需要 grantee active 门 + grantee 自己命名空间的 enabled 分组。
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_opt", "grantee")
	if _, err := f.db.Exec(`UPDATE groups SET enabled = 1, is_default = 1, provider_code = 'openai' WHERE id = 'grp_opt'`); err != nil {
		t.Fatal(err)
	}
	groups, err := f.store.ListAuthorizationGranteeGroups(context.Background(), authorizationGranteeGroupOptionListOptions{
		authorizationPrincipalOptionListOptions: authorizationPrincipalOptionListOptions{Limit: 50},
		GranteeSystemAccountID:                  "grantee",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].ID != "grp_opt" {
		t.Fatalf("分组选项错误: %#v", groups)
	}
	// grantee 非活跃：空结果而非错误。
	if _, err := f.db.Exec(`UPDATE system_accounts SET status='disabled' WHERE id='grantee'`); err != nil {
		t.Fatal(err)
	}
	empty, err := f.store.ListAuthorizationGranteeGroups(context.Background(), authorizationGranteeGroupOptionListOptions{
		authorizationPrincipalOptionListOptions: authorizationPrincipalOptionListOptions{Limit: 50},
		GranteeSystemAccountID:                  "grantee",
	})
	if err != nil || len(empty) != 0 {
		t.Fatalf("非活跃 grantee 应得空集: %#v %v", empty, err)
	}

	// HTTP handler：缺 granteeSystemAccountId → 400；错误路径 500。
	deps := &Deps{Store: f.store}
	recorder := wdAuthzGet(t, deps.granteeGroups, "/?keyword=x", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺 grantee 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = wdAuthzGet(t, deps.granteeGroups, "/?granteeSystemAccountId=u1&preferDefault=false", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("分组选项 handler 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = wdAuthzGet(t, deps.granteeAccounts, "/?ids=u1,u2&limit=5", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("账户选项 handler 应 200: %d", recorder.Code)
	}
	recorder = wdAuthzGet(t, deps.granteeTeams, "/?limit=x", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("limit 非数字按缺省处理应 200: %d", recorder.Code)
	}
	// Store 指向已关闭的库 → 500。
	broken := newFixture(t)
	closedDB := broken.db
	depsBroken := &Deps{Store: broken.store}
	if err := closedDB.Close(); err != nil {
		t.Fatal(err)
	}
	if recorder := wdAuthzGet(t, depsBroken.granteeAccounts, "/", nil); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("读失败应 500: %d", recorder.Code)
	}
	if recorder := wdAuthzGet(t, depsBroken.granteeTeams, "/", nil); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("读失败应 500: %d", recorder.Code)
	}
}

func wdAuthzGet(t *testing.T, handler http.HandlerFunc, target string, auth *authsys.AuthContext) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if auth != nil {
		request = request.WithContext(authsys.WithAuthContext(request.Context(), auth))
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

// MountAuthorizationOptions 的注册契约：挂载后未认证请求得到门禁 401
// （而非内核 404）。
func TestWdMountAuthorizationOptionsRegistersRoutes(t *testing.T) {
	f := newFixture(t)
	deps := &Deps{Store: f.store, Auth: &authsys.Deps{}}
	gateway := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuthorizationOptions(gateway)
	handler := gateway.Handler()
	for _, target := range []string{
		"/__aisys__/api/authorization-options/grantee-accounts",
		"/__aisys__/api/authorization-options/grantee-teams",
		"/__aisys__/api/authorization-options/grantee-groups",
		"/__aisys__/api/my-authorization-options/grantee-accounts",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s 未认证应 401（未注册会是 404）: %d %s", target, recorder.Code, recorder.Body.String())
		}
	}
}
