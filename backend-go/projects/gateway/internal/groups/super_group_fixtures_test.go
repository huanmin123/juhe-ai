package groups

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// 超级分组 API fixture 套件（docs/functions/超级分组设计.md §7 的
// topology-* / member-* / revision-* 行）。dispatch/mode/affinity/usage 类
// fixture 由路由运行时与组合根套件承载。
//
// 每个用例使用独立 in-memory 环境，fixture_id 作为子测试名；集合唯一性由
// superGroupAPIFixtureIDs 断言。

var superGroupAPIFixtureIDs = []string{
	"topology-standard",
	"topology-super-1",
	"topology-super-2",
	"topology-super-20",
	"topology-super-21",
	"topology-super-disabled",
	"member-self",
	"member-duplicate",
	"member-nested",
	"member-cross-owner",
	"member-deleted",
	"member-kind-conflict",
	"member-dispatch-mismatch",
	"member-dispatch-conflict",
	"member-reference-conflict",
	"member-unauthorized",
	"member-disabled",
	"member-atomic-update",
	"revision-cas-conflict",
	"revision-noop",
}

type superFixtureEnv struct {
	env    *testEnv
	admin  string
	prefix string
}

func newSuperFixtureEnv(t *testing.T) *superFixtureEnv {
	t.Helper()
	env := newTestEnv(t)
	admin := env.login(t, "root", "root-pass", "super_admin")
	return &superFixtureEnv{env: env, admin: admin, prefix: "/__aisys__/api"}
}

func (f *superFixtureEnv) createStandard(t *testing.T, name string) string {
	t.Helper()
	id, _ := f.env.createGroup(t, f.prefix+"/groups", name)
	return id
}

func (f *superFixtureEnv) createSuper(t *testing.T, name string) string {
	t.Helper()
	code, payload := f.env.do(t, http.MethodPost, f.prefix+"/groups",
		`{"name":"`+name+`","groupKind":"super","enabled":false}`)
	if code != http.StatusCreated {
		t.Fatalf("create super %s: %d %v", name, code, payload)
	}
	data := payload["data"].(map[string]any)
	if data["groupKind"] != "super" {
		t.Fatalf("created group kind = %v", data["groupKind"])
	}
	return data["id"].(string)
}

func (f *superFixtureEnv) members(t *testing.T, superID string) (string, []any) {
	t.Helper()
	code, payload := f.env.do(t, http.MethodGet, f.prefix+"/groups/"+superID+"/super-members", "")
	if code != http.StatusOK {
		t.Fatalf("get members: %d %v", code, payload)
	}
	data := payload["data"].(map[string]any)
	rows, _ := data["members"].([]any)
	return data["revision"].(string), rows
}

func membersJSON(ids ...string) string {
	parts := make([]string, 0, len(ids))
	for index, id := range ids {
		enabled := "true"
		if strings.HasPrefix(id, "-") {
			id = strings.TrimPrefix(id, "-")
			enabled = "false"
		}
		parts = append(parts, fmt.Sprintf(`{"memberGroupId":%q,"priority":%d,"enabled":%s}`, id, index, enabled))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func (f *superFixtureEnv) patchMembers(t *testing.T, superID, revision, members string) (int, map[string]any) {
	t.Helper()
	return f.env.do(t, http.MethodPatch, f.prefix+"/groups/"+superID+"/super-members",
		`{"expectedRevision":`+fmt.Sprintf("%q", revision)+`,"members":`+members+`}`)
}

func (f *superFixtureEnv) enableGroup(t *testing.T, groupID, revision string) (int, map[string]any) {
	t.Helper()
	return f.env.do(t, http.MethodPatch, f.prefix+"/groups/"+groupID,
		`{"expectedUpdatedAt":`+fmt.Sprintf("%q", revision)+`,"enabled":true}`)
}

func (f *superFixtureEnv) disableGroup(t *testing.T, groupID, revision string) (int, map[string]any) {
	t.Helper()
	return f.env.do(t, http.MethodPatch, f.prefix+"/groups/"+groupID,
		`{"expectedUpdatedAt":`+fmt.Sprintf("%q", revision)+`,"enabled":false}`)
}

func (f *superFixtureEnv) groupRevision(t *testing.T, groupID string) string {
	t.Helper()
	code, payload := f.env.do(t, http.MethodGet, f.prefix+"/groups/"+groupID, "")
	if code != http.StatusOK {
		t.Fatalf("get group %s: %d %v", groupID, code, payload)
	}
	return payload["data"].(map[string]any)["updatedAt"].(string)
}

func assertCode(t *testing.T, status int, payload map[string]any, wantStatus int, wantCode string) {
	t.Helper()
	if status != wantStatus || payload["code"] != wantCode {
		t.Fatalf("want %d/%s, got %d/%v", wantStatus, wantCode, status, payload)
	}
}

func TestSuperGroupAPIFixtureRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, id := range superGroupAPIFixtureIDs {
		if seen[id] {
			t.Fatalf("duplicate fixture id %q", id)
		}
		seen[id] = true
	}
	if len(superGroupAPIFixtureIDs) != 20 {
		t.Fatalf("fixture count = %d, want 20", len(superGroupAPIFixtureIDs))
	}
}

func TestSuperGroupAPIFixtures(t *testing.T) {
	t.Run("topology-standard", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		id := f.createStandard(t, "标准组")
		code, payload := f.env.do(t, http.MethodGet, f.prefix+"/groups/"+id, "")
		if code != http.StatusOK || payload["data"].(map[string]any)["groupKind"] != "standard" {
			t.Fatalf("standard group kind: %d %v", code, payload)
		}
	})

	t.Run("topology-super-1", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "单成员超级组")
		member := f.createStandard(t, "单成员")
		revision, _ := f.members(t, super)
		if code, payload := f.patchMembers(t, super, revision, membersJSON(member)); code != http.StatusOK {
			t.Fatalf("save single member: %d %v", code, payload)
		}
		after, rows := f.members(t, super)
		if after == revision {
			t.Fatal("saving members must advance revision")
		}
		if len(rows) != 1 {
			t.Fatalf("member rows = %d, want 1", len(rows))
		}
		if code, payload := f.enableGroup(t, super, after); code != http.StatusOK {
			t.Fatalf("enable super with one member: %d %v", code, payload)
		}
		if got := f.env.count(t, `SELECT enabled FROM groups WHERE id = ?`, super); got != 1 {
			t.Fatalf("super must be enabled, enabled=%d", got)
		}
	})

	t.Run("topology-super-2", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "超级二")
		g1 := f.createStandard(t, "组一")
		g2 := f.createStandard(t, "组二")
		revision, _ := f.members(t, super)
		code, payload := f.patchMembers(t, super, revision, membersJSON(g1, g2))
		if code != http.StatusOK {
			t.Fatalf("patch members: %d %v", code, payload)
		}
		revision = payload["data"].(map[string]any)["revision"].(string)
		if code, payload = f.enableGroup(t, super, revision); code != http.StatusOK {
			t.Fatalf("enable super: %d %v", code, payload)
		}
		_, rows := f.members(t, super)
		if len(rows) != 2 {
			t.Fatalf("member rows = %d", len(rows))
		}
		if rows[0].(map[string]any)["priority"] != float64(0) || rows[1].(map[string]any)["priority"] != float64(1) {
			t.Fatalf("priorities = %v", rows)
		}
	})

	t.Run("topology-super-20", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "超级二十")
		ids := make([]string, 0, 20)
		for index := 0; index < 20; index++ {
			ids = append(ids, f.createStandard(t, fmt.Sprintf("组-%02d", index)))
		}
		revision, _ := f.members(t, super)
		if code, payload := f.patchMembers(t, super, revision, membersJSON(ids...)); code != http.StatusOK {
			t.Fatalf("20 members: %d %v", code, payload)
		}
		if got := f.env.count(t, `SELECT COUNT(*) FROM super_group_members WHERE super_group_id = ?`, super); got != 20 {
			t.Fatalf("member rows = %d", got)
		}
	})

	t.Run("topology-super-21", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "超级二十一")
		ids := make([]string, 0, 21)
		for index := 0; index < 21; index++ {
			ids = append(ids, f.createStandard(t, fmt.Sprintf("超限组-%02d", index)))
		}
		revision, _ := f.members(t, super)
		status, payload := f.patchMembers(t, super, revision, membersJSON(ids...))
		assertCode(t, status, payload, http.StatusBadRequest, CodeSuperGroupMemberLimit)
		if got := f.env.count(t, `SELECT COUNT(*) FROM super_group_members WHERE super_group_id = ?`, super); got != 0 {
			t.Fatalf("members must rollback, got %d", got)
		}
	})

	t.Run("topology-super-disabled", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "空草稿")
		revision, _ := f.members(t, super)
		if code, payload := f.patchMembers(t, super, revision, "[]"); code != http.StatusOK {
			t.Fatalf("empty draft: %d %v", code, payload)
		}
		revision, _ = f.members(t, super)
		if code, payload := f.enableGroup(t, super, revision); code != http.StatusOK {
			t.Fatalf("enable empty super: %d %v", code, payload)
		}
		if got := f.env.count(t, `SELECT enabled FROM groups WHERE id = ?`, super); got != 1 {
			t.Fatalf("super must be enabled, enabled=%d", got)
		}
	})

	t.Run("member-self", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "自引用")
		revision, _ := f.members(t, super)
		status, payload := f.patchMembers(t, super, revision, membersJSON(super))
		assertCode(t, status, payload, http.StatusBadRequest, CodeSuperGroupMemberInvalid)
	})

	t.Run("member-duplicate", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "重复成员")
		g1 := f.createStandard(t, "重复组")
		revision, _ := f.members(t, super)
		status, payload := f.patchMembers(t, super, revision, membersJSON(g1, g1))
		assertCode(t, status, payload, http.StatusBadRequest, CodeSuperGroupMemberInvalid)
	})

	t.Run("member-nested", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "外层")
		inner := f.createSuper(t, "内层")
		revision, _ := f.members(t, super)
		status, payload := f.patchMembers(t, super, revision, membersJSON(inner))
		assertCode(t, status, payload, http.StatusBadRequest, CodeSuperGroupMemberNested)
	})

	t.Run("member-cross-owner", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "跨属主")
		now := "2026-10-01T00:00:00Z"
		f.env.exec(t, `INSERT INTO groups (id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
			VALUES ('grp-other', 'other-owner', '他人组', NULL, 1, 0, 'personal', 'standard', NULL, ?, ?)`, now, now)
		revision, _ := f.members(t, super)
		status, payload := f.patchMembers(t, super, revision, membersJSON("grp-other"))
		assertCode(t, status, payload, http.StatusBadRequest, CodeSuperGroupMemberOwnerMismatch)
	})

	t.Run("member-deleted", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "缺失成员")
		revision, _ := f.members(t, super)
		status, payload := f.patchMembers(t, super, revision, membersJSON("grp-missing"))
		assertCode(t, status, payload, http.StatusNotFound, CodeSuperGroupMemberInvalid)
	})

	t.Run("member-kind-conflict", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		standard := f.createStandard(t, "普通组")
		status, payload := f.env.do(t, http.MethodPatch, f.prefix+"/groups/"+standard+"/super-members",
			`{"expectedRevision":"x","members":[]}`)
		assertCode(t, status, payload, http.StatusBadRequest, CodeGroupKindConflict)
	})

	t.Run("member-dispatch-mismatch", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "指纹不一致")
		personal := f.createStandard(t, "个人组")
		code, payload := f.env.do(t, http.MethodPost, f.prefix+"/groups",
			`{"name":"高并发组","groupType":"high_concurrency"}`)
		if code != http.StatusCreated {
			t.Fatalf("create high concurrency: %d %v", code, payload)
		}
		high := payload["data"].(map[string]any)["id"].(string)
		revision, _ := f.members(t, super)
		status, payload := f.patchMembers(t, super, revision, membersJSON(personal, high))
		assertCode(t, status, payload, http.StatusBadRequest, CodeSuperGroupMemberDispatchMismatch)
	})

	t.Run("member-dispatch-conflict", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "指纹复验")
		g1 := f.createStandard(t, "成员一")
		g2 := f.createStandard(t, "成员二")
		revision, _ := f.members(t, super)
		code, payload := f.patchMembers(t, super, revision, membersJSON(g1, g2))
		if code != http.StatusOK {
			t.Fatalf("patch members: %d %v", code, payload)
		}
		revision = payload["data"].(map[string]any)["revision"].(string)
		if code, payload = f.enableGroup(t, super, revision); code != http.StatusOK {
			t.Fatalf("enable super: %d %v", code, payload)
		}
		memberRevision := f.groupRevision(t, g1)
		status, payload := f.env.do(t, http.MethodPatch, f.prefix+"/groups/"+g1,
			`{"expectedUpdatedAt":`+fmt.Sprintf("%q", memberRevision)+`,"groupType":"high_concurrency"}`)
		assertCode(t, status, payload, http.StatusConflict, CodeSuperGroupMemberDispatchConflict)
	})

	t.Run("member-reference-conflict", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "引用冲突")
		g1 := f.createStandard(t, "被引用组")
		g2 := f.createStandard(t, "陪衬组")
		revision, _ := f.members(t, super)
		if code, payload := f.patchMembers(t, super, revision, membersJSON(g1, g2)); code != http.StatusOK {
			t.Fatalf("patch members: %d %v", code, payload)
		}
		memberRevision := f.groupRevision(t, g1)
		status, payload := f.disableGroup(t, g1, memberRevision)
		assertCode(t, status, payload, http.StatusConflict, CodeSuperGroupMemberReferenceConflict)
	})

	t.Run("member-unauthorized", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "授权只读")
		g1 := f.createStandard(t, "只读组")
		revision, _ := f.members(t, super)
		if code, payload := f.patchMembers(t, super, revision, membersJSON(g1)); code != http.StatusOK {
			t.Fatalf("patch members: %d %v", code, payload)
		}
		revision, _ = f.members(t, super)
		viewer := f.env.login(t, "viewer", "viewer-pass", "user")
		now := "2026-10-01T00:00:00Z"
		f.env.exec(t, `INSERT INTO resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, created_by, created_at, updated_at)
			VALUES ('ra-viewer', 'group', ?, ?, ?, 'use', 'active', ?, ?, ?)`, super, f.admin, viewer, f.admin, now, now)
		status, payload := f.env.do(t, http.MethodPatch, "/__aisys__/api/my-groups/"+super+"/super-members",
			`{"expectedRevision":`+fmt.Sprintf("%q", revision)+`,"members":`+membersJSON(g1)+`}`)
		assertCode(t, status, payload, http.StatusForbidden, CodeSuperGroupMemberUnauthorized)
	})

	t.Run("member-disabled", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "行级停用")
		g1 := f.createStandard(t, "启用成员")
		g2 := f.createStandard(t, "停用成员")
		revision, _ := f.members(t, super)
		if code, payload := f.patchMembers(t, super, revision, membersJSON(g1, "-"+g2)); code != http.StatusOK {
			t.Fatalf("disabled draft rows: %d %v", code, payload)
		}
		if got := f.env.count(t, `SELECT COUNT(*) FROM super_group_members WHERE super_group_id = ? AND enabled = 1`, super); got != 1 {
			t.Fatalf("enabled rows = %d", got)
		}
		if got := f.env.count(t, `SELECT enabled FROM groups WHERE id = ?`, super); got != 0 {
			t.Fatalf("super must stay disabled, enabled=%d", got)
		}
		_, rows := f.members(t, super)
		if len(rows) != 2 {
			t.Fatalf("rows = %d", len(rows))
		}
	})

	t.Run("member-atomic-update", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "原子替换")
		g1 := f.createStandard(t, "原子一")
		g2 := f.createStandard(t, "原子二")
		now := "2026-10-01T00:00:00Z"
		f.env.exec(t, `INSERT INTO groups (id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
			VALUES ('grp-atomic-other', 'other-owner', '原子他人组', NULL, 1, 0, 'personal', 'standard', NULL, ?, ?)`, now, now)
		revision, _ := f.members(t, super)
		if code, payload := f.patchMembers(t, super, revision, membersJSON(g1, g2)); code != http.StatusOK {
			t.Fatalf("seed members: %d %v", code, payload)
		}
		revision, _ = f.members(t, super)
		status, payload := f.patchMembers(t, super, revision, membersJSON(g1, "grp-atomic-other"))
		assertCode(t, status, payload, http.StatusBadRequest, CodeSuperGroupMemberOwnerMismatch)
		if got := f.env.count(t, `SELECT COUNT(*) FROM super_group_members WHERE super_group_id = ?`, super); got != 2 {
			t.Fatalf("rows must rollback, got %d", got)
		}
		afterRevision, _ := f.members(t, super)
		if afterRevision != revision {
			t.Fatalf("revision must not advance: %s -> %s", revision, afterRevision)
		}
	})

	t.Run("revision-cas-conflict", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "CAS")
		status, payload := f.patchMembers(t, super, "stale-revision", "[]")
		assertCode(t, status, payload, http.StatusConflict, CodeSuperGroupRevisionConflict)
	})

	t.Run("revision-noop", func(t *testing.T) {
		f := newSuperFixtureEnv(t)
		super := f.createSuper(t, "noop")
		g1 := f.createStandard(t, "noop-组")
		revision, _ := f.members(t, super)
		if code, payload := f.patchMembers(t, super, revision, membersJSON(g1)); code != http.StatusOK {
			t.Fatalf("patch members: %d %v", code, payload)
		}
		revision, _ = f.members(t, super)
		code, payload := f.patchMembers(t, super, revision, membersJSON(g1))
		if code != http.StatusOK {
			t.Fatalf("noop patch: %d %v", code, payload)
		}
		if payload["data"].(map[string]any)["revision"] != revision {
			t.Fatalf("noop must keep revision: got %v want %s", payload["data"].(map[string]any)["revision"], revision)
		}
	})
}

// TestSuperGroupEnableAndMemberSaveWithoutMinimum 覆盖 2026-10-22 契约变更：
// 超级分组创建即可启用；已启用超级分组保存 1 个或 0 个成员均成功。该测试
// 不是 §7 fixture，不进入 fixture 注册表。
func TestSuperGroupEnableAndMemberSaveWithoutMinimum(t *testing.T) {
	f := newSuperFixtureEnv(t)

	code, payload := f.env.do(t, http.MethodPost, f.prefix+"/groups",
		`{"name":"创建即启用超级组","groupKind":"super","enabled":true}`)
	if code != http.StatusCreated {
		t.Fatalf("create enabled super: %d %v", code, payload)
	}
	created := payload["data"].(map[string]any)
	if created["groupKind"] != "super" || created["enabled"] != true {
		t.Fatalf("created super = %v", created)
	}
	super := created["id"].(string)
	if got := f.env.count(t, `SELECT enabled FROM groups WHERE id = ?`, super); got != 1 {
		t.Fatalf("created super must be enabled, enabled=%d", got)
	}

	member := f.createStandard(t, "单成员")
	revision, _ := f.members(t, super)
	code, payload = f.patchMembers(t, super, revision, membersJSON(member))
	if code != http.StatusOK {
		t.Fatalf("save single member on enabled super: %d %v", code, payload)
	}
	revision = payload["data"].(map[string]any)["revision"].(string)

	code, payload = f.patchMembers(t, super, revision, "[]")
	if code != http.StatusOK {
		t.Fatalf("save zero members on enabled super: %d %v", code, payload)
	}
	_, rows := f.members(t, super)
	if len(rows) != 0 {
		t.Fatalf("member rows = %d, want 0", len(rows))
	}
	if got := f.env.count(t, `SELECT enabled FROM groups WHERE id = ?`, super); got != 1 {
		t.Fatalf("super must stay enabled, enabled=%d", got)
	}
}
