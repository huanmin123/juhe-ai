// 超级分组设计 §7 的 mode-* 验收矩阵：四种可写模式的 standard/super 绑定
// 落库断言 + merge 三写入口（管理/个人在此，公开面见 aipublic 包）拒绝。
// 目标序列与权重分布等 dispatch 断言由 gatewayrouting / chain 层 fixture 覆盖。
package routestrategies

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// modeFixtureIDs 是本文件覆盖的 fixture 集合，供 §7 集合唯一性校验引用。
var modeFixtureIDs = []string{
	"mode-unsupported-merge",
	"mode-normal-standard",
	"mode-normal-super",
	"mode-weighted-standard",
	"mode-weighted-super",
	"mode-failover-standard",
	"mode-failover-super",
	"mode-round-robin-standard",
	"mode-round-robin-super",
}

// modeFixtureEnv 包装 testEnv，补超级分组构造与绑定事实读取。
type modeFixtureEnv struct {
	*testEnv
	admin string
}

func newModeFixtureEnv(t *testing.T) *modeFixtureEnv {
	t.Helper()
	env := newTestEnv(t)
	return &modeFixtureEnv{testEnv: env, admin: env.login(t, "root", "root-pass", "super_admin")}
}

// createSuperGroup 直插一个启用超级分组及其启用成员行（priority 0..n-1）。
func (e *modeFixtureEnv) createSuperGroup(t *testing.T, ownerID, name string, memberIDs ...string) string {
	t.Helper()
	id := "grp-super-" + name
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, group_kind, created_at, updated_at)
		VALUES (?, ?, ?, 'openai', 1, 0, 'personal', 'super', ?, ?)`, id, ownerID, name, now, now)
	for index, memberID := range memberIDs {
		e.exec(t, `INSERT INTO super_group_members (id, super_group_id, member_group_id, priority, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, ?)`, fmt.Sprintf("sgm-%s-%d", id, index), id, memberID, index, now, now)
	}
	return id
}

// bindings 读取策略绑定事实（按 priority 排序）。
func (e *modeFixtureEnv) bindings(t *testing.T, strategyID string) []map[string]any {
	t.Helper()
	rows, err := e.db.Query(`SELECT group_id, priority, weight, status FROM route_strategy_groups
		WHERE route_strategy_id = ? ORDER BY priority ASC, group_id ASC`, strategyID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var groupID, status string
		var priority, weight int
		if err := rows.Scan(&groupID, &priority, &weight, &status); err != nil {
			t.Fatal(err)
		}
		out = append(out, map[string]any{"groupId": groupID, "priority": priority, "weight": weight, "status": status})
	}
	return out
}

// createModeStrategy 通过管理面创建策略并返回策略 ID；要求 201。
func (e *modeFixtureEnv) createModeStrategy(t *testing.T, body string) string {
	t.Helper()
	code, payload := e.createStrategy(t, "/__aisys__/api/route-strategies", body)
	if code != http.StatusCreated {
		t.Fatalf("create strategy: %d %v", code, payload)
	}
	return dataMap(t, payload)["id"].(string)
}

func modeBinding(groupID string, priority, weight int) string {
	if weight > 0 {
		return fmt.Sprintf(`{"groupId":%q,"priority":%d,"weight":%d}`, groupID, priority, weight)
	}
	return fmt.Sprintf(`{"groupId":%q,"priority":%d}`, groupID, priority)
}

func TestSuperGroupModeFixtures(t *testing.T) {
	if len(modeFixtureIDs) != 9 {
		t.Fatalf("mode fixture count = %d, want 9", len(modeFixtureIDs))
	}

	t.Run("mode-unsupported-merge", func(t *testing.T) {
		env := newModeFixtureEnv(t)
		groupA := env.createGroup(t, env.admin, "merge-a", true)
		groupB := env.createGroup(t, env.admin, "merge-b", true)
		adminPath := "/__aisys__/api/route-strategies"
		myPath := "/__aisys__/api/my-route-strategies"

		for _, body := range []string{
			`{"name":"m-one","mode":"merge","groupBindings":[` + modeBinding(groupA, 1, 0) + `]}`,
			`{"name":"m-two","mode":"merge","groupBindings":[` + modeBinding(groupA, 1, 0) + `,` + modeBinding(groupB, 2, 0) + `]}`,
		} {
			code, payload := env.createStrategy(t, adminPath, body)
			if code != http.StatusBadRequest || payload["code"] != "unsupported_route_mode" || payload["message"] != "不支持的路由模式" {
				t.Fatalf("admin merge create: %d %v", code, payload)
			}
			code, payload = env.createStrategy(t, myPath, strings.Replace(strings.Replace(body, `"name":"m-`, `"name":"self-m-`, 1), `"mode":"merge"`, `"mode":"merge","description":"self-entry"`, 1))
			if code != http.StatusBadRequest || payload["code"] != "unsupported_route_mode" || payload["message"] != "不支持的路由模式" {
				t.Fatalf("personal merge create: %d %v", code, payload)
			}
		}
		if got := env.count(t, `SELECT COUNT(*) FROM route_strategies`); got != 0 {
			t.Fatalf("rejected merge creates must not persist, rows=%d", got)
		}

		normalID := env.createModeStrategy(t, `{"name":"m-normal","groupBindings":[`+modeBinding(groupA, 1, 0)+`]}`)
		for _, path := range []string{adminPath, myPath} {
			code, payload := env.do(t, http.MethodPatch, path+"/"+normalID,
				`{"expectedUpdatedAt":"`+env.strategyUpdatedAt(t, normalID)+`","mode":"merge"}`)
			if code != http.StatusBadRequest || payload["code"] != "unsupported_route_mode" || payload["message"] != "不支持的路由模式" {
				t.Fatalf("%s merge patch: %d %v", path, code, payload)
			}
		}
		if got := env.bindings(t, normalID); len(got) != 1 || got[0]["groupId"] != groupA {
			t.Fatalf("rejected merge patch must not touch bindings: %v", got)
		}
	})

	t.Run("mode-normal-standard", func(t *testing.T) {
		env := newModeFixtureEnv(t)
		group := env.createGroup(t, env.admin, "normal-standard", true)
		id := env.createModeStrategy(t, `{"name":"ns","groupBindings":[`+modeBinding(group, 1, 0)+`]}`)
		bindings := env.bindings(t, id)
		if len(bindings) != 1 || bindings[0]["groupId"] != group || bindings[0]["status"] != "active" {
			t.Fatalf("normal binding: %v", bindings)
		}
	})

	t.Run("mode-normal-super", func(t *testing.T) {
		env := newModeFixtureEnv(t)
		memberA := env.createGroup(t, env.admin, "normal-super-a", true)
		memberB := env.createGroup(t, env.admin, "normal-super-b", true)
		super := env.createSuperGroup(t, env.admin, "normal-super", memberA, memberB)
		id := env.createModeStrategy(t, `{"name":"nsuper","groupBindings":[`+modeBinding(super, 1, 0)+`]}`)
		bindings := env.bindings(t, id)
		if len(bindings) != 1 || bindings[0]["groupId"] != super {
			t.Fatalf("super binding: %v", bindings)
		}
		if got := env.count(t, `SELECT COUNT(*) FROM groups WHERE id = ? AND group_kind = 'super'`, super); got != 1 {
			t.Fatalf("bound target kind must be super")
		}
		if got := env.count(t, `SELECT COUNT(*) FROM super_group_members WHERE super_group_id = ? AND enabled = 1`, super); got != 2 {
			t.Fatalf("super members = %d", got)
		}
		// topology-super-1 的绑定侧 oracle：只有 1 个启用成员的超级分组可以正常绑定。
		only := env.createGroup(t, env.admin, "normal-super-only", true)
		single := env.createSuperGroup(t, env.admin, "normal-super-single", only)
		singleID := env.createModeStrategy(t,
			`{"name":"nsuper-single","groupBindings":[`+modeBinding(single, 1, 0)+`]}`)
		singleBindings := env.bindings(t, singleID)
		if len(singleBindings) != 1 || singleBindings[0]["groupId"] != single || singleBindings[0]["status"] != "active" {
			t.Fatalf("single-member super binding: %v", singleBindings)
		}
	})

	t.Run("mode-weighted-standard", func(t *testing.T) {
		env := newModeFixtureEnv(t)
		groupA := env.createGroup(t, env.admin, "weighted-a", true)
		groupB := env.createGroup(t, env.admin, "weighted-b", true)
		id := env.createModeStrategy(t, `{"name":"ws","mode":"weighted","groupBindings":[`+
			modeBinding(groupA, 1, 50)+`,`+modeBinding(groupB, 2, 50)+`]}`)
		bindings := env.bindings(t, id)
		if len(bindings) != 2 {
			t.Fatalf("weighted bindings = %v", bindings)
		}
		sum := 0
		for _, binding := range bindings {
			if binding["status"] != "active" {
				t.Fatalf("binding inactive: %v", binding)
			}
			sum += binding["weight"].(int)
		}
		if sum != 100 {
			t.Fatalf("weight sum = %d, want 100", sum)
		}
	})

	t.Run("mode-weighted-super", func(t *testing.T) {
		env := newModeFixtureEnv(t)
		members := []string{
			env.createGroup(t, env.admin, "weighted-super-a1", true),
			env.createGroup(t, env.admin, "weighted-super-a2", true),
			env.createGroup(t, env.admin, "weighted-super-b1", true),
			env.createGroup(t, env.admin, "weighted-super-b2", true),
		}
		superA := env.createSuperGroup(t, env.admin, "ws-a", members[0], members[1])
		superB := env.createSuperGroup(t, env.admin, "ws-b", members[2], members[3])
		id := env.createModeStrategy(t, `{"name":"ws-super","mode":"weighted","groupBindings":[`+
			modeBinding(superA, 1, 50)+`,`+modeBinding(superB, 2, 50)+`]}`)
		bindings := env.bindings(t, id)
		if len(bindings) != 2 || bindings[0]["groupId"] != superA || bindings[1]["groupId"] != superB {
			t.Fatalf("weighted super bindings: %v", bindings)
		}
		for _, binding := range bindings {
			groupID := binding["groupId"].(string)
			if got := env.count(t, `SELECT COUNT(*) FROM super_group_members WHERE super_group_id = ? AND enabled = 1`, groupID); got != 2 {
				t.Fatalf("%s enabled members = %d", groupID, got)
			}
		}
	})

	t.Run("mode-failover-standard", func(t *testing.T) {
		env := newModeFixtureEnv(t)
		primary := env.createGroup(t, env.admin, "failover-primary", true)
		backup := env.createGroup(t, env.admin, "failover-backup", true)
		id := env.createModeStrategy(t, `{"name":"fs","mode":"failover","groupBindings":[`+
			modeBinding(primary, 1, 0)+`,`+modeBinding(backup, 2, 0)+`]}`)
		bindings := env.bindings(t, id)
		if len(bindings) != 2 || bindings[0]["groupId"] != primary || bindings[0]["priority"] != 1 ||
			bindings[1]["groupId"] != backup || bindings[1]["priority"] != 2 {
			t.Fatalf("failover bindings: %v", bindings)
		}
	})

	t.Run("mode-failover-super", func(t *testing.T) {
		env := newModeFixtureEnv(t)
		primaryA := env.createGroup(t, env.admin, "failover-super-a1", true)
		primaryB := env.createGroup(t, env.admin, "failover-super-a2", true)
		backupA := env.createGroup(t, env.admin, "failover-super-b1", true)
		backupB := env.createGroup(t, env.admin, "failover-super-b2", true)
		superPrimary := env.createSuperGroup(t, env.admin, "fo-super-a", primaryA, primaryB)
		superBackup := env.createSuperGroup(t, env.admin, "fo-super-b", backupA, backupB)
		id := env.createModeStrategy(t, `{"name":"fss","mode":"failover","groupBindings":[`+
			modeBinding(superPrimary, 1, 0)+`,`+modeBinding(superBackup, 2, 0)+`]}`)
		bindings := env.bindings(t, id)
		if len(bindings) != 2 || bindings[0]["groupId"] != superPrimary || bindings[1]["groupId"] != superBackup {
			t.Fatalf("failover super bindings: %v", bindings)
		}
		// 成员不得提升为外层目标：绑定的 group_id 只出现两个超级分组。
		if got := env.count(t, `SELECT COUNT(*) FROM route_strategy_groups WHERE route_strategy_id = ? AND group_id IN (?, ?, ?, ?)`,
			id, primaryA, primaryB, backupA, backupB); got != 0 {
			t.Fatalf("member groups promoted to outer bindings: %d", got)
		}
	})

	t.Run("mode-round-robin-standard", func(t *testing.T) {
		env := newModeFixtureEnv(t)
		groupA := env.createGroup(t, env.admin, "rr-a", true)
		groupB := env.createGroup(t, env.admin, "rr-b", true)
		id := env.createModeStrategy(t, `{"name":"rrs","mode":"round_robin","groupBindings":[`+
			modeBinding(groupA, 1, 0)+`,`+modeBinding(groupB, 2, 0)+`]}`)
		bindings := env.bindings(t, id)
		if len(bindings) != 2 || bindings[0]["groupId"] != groupA || bindings[1]["groupId"] != groupB {
			t.Fatalf("round robin bindings: %v", bindings)
		}
		if got := env.count(t, `SELECT COUNT(*) FROM groups WHERE id IN (?, ?) AND group_kind = 'standard'`, groupA, groupB); got != 2 {
			t.Fatalf("standard binding kinds: %d", got)
		}
	})

	t.Run("mode-round-robin-super", func(t *testing.T) {
		env := newModeFixtureEnv(t)
		members := []string{
			env.createGroup(t, env.admin, "rr-super-a1", true),
			env.createGroup(t, env.admin, "rr-super-a2", true),
			env.createGroup(t, env.admin, "rr-super-b1", true),
			env.createGroup(t, env.admin, "rr-super-b2", true),
		}
		superA := env.createSuperGroup(t, env.admin, "rr-super-a", members[0], members[1])
		superB := env.createSuperGroup(t, env.admin, "rr-super-b", members[2], members[3])
		id := env.createModeStrategy(t, `{"name":"rrs-super","mode":"round_robin","groupBindings":[`+
			modeBinding(superA, 1, 0)+`,`+modeBinding(superB, 2, 0)+`]}`)
		bindings := env.bindings(t, id)
		if len(bindings) != 2 || bindings[0]["groupId"] != superA || bindings[1]["groupId"] != superB {
			t.Fatalf("round robin super bindings: %v", bindings)
		}
		if got := env.count(t, `SELECT COUNT(*) FROM route_strategy_groups WHERE route_strategy_id = ? AND group_id IN (?, ?, ?, ?)`,
			id, members[0], members[1], members[2], members[3]); got != 0 {
			t.Fatalf("rr members promoted: %d", got)
		}
	})
}
