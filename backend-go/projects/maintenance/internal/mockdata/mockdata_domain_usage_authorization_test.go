package mockdata

// 授权归属测试：business 域的授权实例账户与团队来源分组授权，经 usage 域回填
// account/group_authorization_* 列后，必须满足 statsagg 授权日报
// （jobs internal/statsagg/authorization.go）两条分支的聚合前提：
//   - account 分支：account_authorization_id / account_id / account_owner 非空
//     且 account_owner_system_account_id != system_account_id（命中
//     authorization_user_usage_summary_daily）；
//   - group 分支：group_authorization_id / group_id / group_owner 非空且
//     group_owner_system_account_id != system_account_id；team 来源还需要
//     group_authorization_source_type='team' + 非空 team id（命中
//     authorization_team_usage_summary_daily 的 team 维度展开）。
//
// 断言跑在 stats 库 usage_records 镜像上：镜像列集合与分片同形
// （TestSeedUsageColumnMirrorMatchesJobsAuthority 钉住），单库可查，等价于对
// 全部分片求和。引用的授权 ID 逐条回 business 库核对，不允许猜 ID。
//
// 全部数据写在 t.TempDir() 下：测试绝不碰 .local/dev/data。

import (
	"context"
	"database/sql"
	"testing"
)

func TestSeedUsageAuthorizationAttributionFeedsStatsagg(t *testing.T) {
	e, _ := usageEnvAtRoot(t, businessTestRoot(t), usageTestOptions(7, 20))
	business, err := e.openExisting(StoreBusiness)
	if err != nil || business == nil {
		t.Fatalf("business 库应已存在: %v", err)
	}
	stats, err := e.openExisting(StoreStats)
	if err != nil || stats == nil {
		t.Fatalf("stats 库应已存在: %v", err)
	}

	// —— account 分支：授权实例记录的账户归属人与调用者错开，access type 是
	// jobs usagewriter 的 account_authorized 词汇。
	accountBranch := usageScalar(t, stats, `SELECT COUNT(*) FROM usage_records
		WHERE account_authorization_id IS NOT NULL AND account_id IS NOT NULL
		  AND account_owner_system_account_id IS NOT NULL
		  AND account_owner_system_account_id <> system_account_id`)
	if accountBranch == 0 {
		t.Fatal("没有满足 statsagg account 分支前提的记录（authorization_user_usage_summary_daily 会为空）")
	}
	if got := usageScalar(t, stats, `SELECT COUNT(*) FROM usage_records
		WHERE account_authorization_id IS NOT NULL
		  AND (account_access_type <> 'account_authorized'
		       OR COALESCE(account_authorization_source_type,'') = '')`); got != 0 {
		t.Fatalf("授权实例记录的 account_access_type / source_type 不合法: %d 行", got)
	}

	// 每一条实例账户授权（business 域为活跃账户级授权建的克隆账户）都要有
	// 「调用者=被授权人、归属人=原 owner」的持续记录。
	instanceRows, err := business.QueryContext(context.Background(), `SELECT id,
		authorization_instance_authorization_id, system_account_id,
		authorization_instance_owner_system_account_id
	FROM accounts WHERE id LIKE ? AND authorization_instance_authorization_id IS NOT NULL
	ORDER BY id`, CleanupIDPrefix+"%")
	if err != nil {
		t.Fatalf("读取授权实例账户: %v", err)
	}
	var instances []struct{ accountID, authorizationID, grantee, owner string }
	for instanceRows.Next() {
		var row struct{ accountID, authorizationID, grantee, owner string }
		if err := instanceRows.Scan(&row.accountID, &row.authorizationID, &row.grantee, &row.owner); err != nil {
			instanceRows.Close()
			t.Fatal(err)
		}
		instances = append(instances, row)
	}
	if err := instanceRows.Err(); err != nil {
		instanceRows.Close()
		t.Fatal(err)
	}
	instanceRows.Close()
	if len(instances) == 0 {
		t.Fatal("business 域必须产出授权实例账户（否则本测试的前提不成立）")
	}
	for _, instance := range instances {
		got := usageScalar(t, stats, `SELECT COUNT(*) FROM usage_records
			WHERE account_id = ? AND account_authorization_id = ?
			  AND system_account_id = ?
			  AND account_owner_system_account_id = ?
			  AND account_owner_system_account_id <> system_account_id`,
			instance.accountID, instance.authorizationID, instance.grantee, instance.owner)
		if got == 0 {
			t.Errorf("实例账户 %s（授权 %s）没有满足 account 分支的记录", instance.accountID, instance.authorizationID)
		}
	}

	// account 分支引用的授权 ID 必须真实存在（不猜 ID）。
	usageAssertAllIDsExist(t, stats, business,
		`SELECT DISTINCT account_authorization_id FROM usage_records WHERE account_authorization_id IS NOT NULL`,
		"account_authorization_id")

	// —— group 分支 + team 来源：authorization_team_usage_summary_daily 的输入。
	groupTeamBranch := usageScalar(t, stats, `SELECT COUNT(*) FROM usage_records
		WHERE group_authorization_id IS NOT NULL AND group_id IS NOT NULL
		  AND group_owner_system_account_id IS NOT NULL
		  AND group_owner_system_account_id <> system_account_id
		  AND group_authorization_source_type = 'team'
		  AND COALESCE(group_authorization_source_team_id,'') <> ''`)
	if groupTeamBranch == 0 {
		t.Fatal("没有满足 statsagg group 分支（team 来源）前提的记录（authorization_team_usage_summary_daily 会为空）")
	}
	// 维度词汇必须落在 statsagg ShouldAggregateUsageStatsRecord 的合法集合里，
	// 否则整条记录被聚合器丢弃（team 汇总会静默为空）。
	if got := usageScalar(t, stats, `SELECT COUNT(*) FROM usage_records
		WHERE group_authorization_id IS NOT NULL
		  AND (account_access_type NOT IN ('owner','account_authorized','group_authorized')
		       OR group_access_type NOT IN ('owner','authorized'))`); got != 0 {
		t.Fatalf("分组授权记录的访问类型词汇不合法: %d 行", got)
	}
	if got := usageScalar(t, stats, `SELECT COUNT(*) FROM usage_records
		WHERE group_authorization_id IS NOT NULL
		  AND (account_access_type <> 'group_authorized' OR group_access_type <> 'authorized')`); got != 0 {
		t.Fatalf("分组授权记录必须是 account_authorized=group_authorized + group=authorized 组合: %d 行", got)
	}
	// 引用的分组授权必须是 business 库里 active + team 来源的运行时行。
	pending := usageScalar(t, stats, `SELECT COUNT(*) FROM usage_records
		WHERE group_authorization_id IS NOT NULL AND (
		  group_authorization_source_type <> 'team' OR COALESCE(group_authorization_source_team_id,'') = '')`)
	if pending != 0 {
		t.Fatalf("带 group_authorization_id 的记录必须全部携带 team 来源: %d 行例外", pending)
	}
	usageAssertAllIDsExist(t, stats, business,
		`SELECT DISTINCT group_authorization_id FROM usage_records WHERE group_authorization_id IS NOT NULL`,
		"group_authorization_id")

	// team 维度可展开：存在「调用者成员 × 团队」组合（team 摘要行的键来源）。
	teamKeys := usageScalar(t, stats, `SELECT COUNT(DISTINCT system_account_id || ':' || group_authorization_source_team_id)
		FROM usage_records WHERE group_authorization_source_type = 'team'`)
	if teamKeys == 0 {
		t.Fatal("team 来源记录没有任何「成员×团队」组合，team 摘要无法展开")
	}
	// user 维度可展开：account 分支与 group 分支各自至少一条非 global owner 记录。
	userKeys := usageScalar(t, stats, `SELECT COUNT(DISTINCT system_account_id) FROM usage_records
		WHERE account_authorization_id IS NOT NULL OR group_authorization_id IS NOT NULL`)
	if userKeys == 0 {
		t.Fatal("授权记录没有任何被授权人维度")
	}
}

// usageAssertAllIDsExist 核对镜像里引用的全部授权 ID 都能在 business 库的
// resource_authorizations 里找到。
func usageAssertAllIDsExist(t *testing.T, stats, business *sql.DB, query, label string) {
	t.Helper()
	ids := map[string]bool{}
	rows, err := stats.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("读取 %s: %v", label, err)
	}
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if id.Valid && id.String != "" {
			ids[id.String] = false
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(ids) == 0 {
		t.Fatalf("%s 没有任何引用", label)
	}
	known, err := business.QueryContext(context.Background(), `SELECT id FROM resource_authorizations`)
	if err != nil {
		t.Fatalf("读取 resource_authorizations: %v", err)
	}
	for known.Next() {
		var id string
		if err := known.Scan(&id); err != nil {
			known.Close()
			t.Fatal(err)
		}
		if _, ok := ids[id]; ok {
			ids[id] = true
		}
	}
	if err := known.Err(); err != nil {
		known.Close()
		t.Fatal(err)
	}
	known.Close()
	for id, found := range ids {
		if !found {
			t.Errorf("%s 引用了 business 库不存在的授权 %s", label, id)
		}
	}
}
