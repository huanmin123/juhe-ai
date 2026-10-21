// 超级分组设计 §7 外层目标 fixture：failover/weighted 绑定多个超级分组时的
// 目标回退、全无候选与上游耗尽终态，以及目标级失败分类的审计投影。
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// superOuterMember 描述超级分组的一个成员组及其账号。
type superOuterMember struct {
	groupID   string
	accountID string
}

// superOuterTarget 描述外层目标：kind=super 时 members 为成员组；kind=standard
// 时直接用 groupID + accountID。
type superOuterTarget struct {
	groupID   string
	kind      string
	members   []superOuterMember
	accountID string
}

// seedSuperOuterFixture 构造一个策略绑定多个目标；每个成员/标准组各有一个
// 支持 gpt-test 的账号。
func seedSuperOuterFixture(t *testing.T, mode string, targets ...superOuterTarget) *chainFixture {
	t.Helper()
	fixture := newChainFixture(t)
	now := chainRouteTestNow
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed outer fixture: %v: %v", query, err)
		}
	}
	addAccount := func(groupID, accountID string) {
		seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES (?, ?, 'openai', 1, 'personal')`,
			groupID, fixture.systemAccount)
		seed(`INSERT INTO accounts (
				id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
				name, type, status, schedulable, credentials_encrypted, deleted_at
			) VALUES (?, ?, 'openai', 'prof_1', 'openai', 'v1', ?, 'api_key', 'active', 1, ?, NULL)`,
			accountID, fixture.systemAccount, accountID, w1vEncrypt(t, map[string]any{"api_key": "sk-outer-" + accountID}))
		seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
			groupID, fixture.systemAccount, accountID, now)
		chainSeedAccountConnection(t, fixture.db, accountID, "https://outer-"+accountID+".example.invalid/v1")
		seed(`INSERT INTO account_supported_models (account_id, provider_code, model, model_id, created_at) VALUES (?, 'openai', 'gpt-test', 'mdl_gpt-test', ?)`,
			accountID, now)
	}
	for _, target := range targets {
		if target.kind == "super" {
			seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type, group_kind) VALUES (?, ?, 'openai', 1, 'personal', 'super')`,
				target.groupID, fixture.systemAccount)
			for index, member := range target.members {
				addAccount(member.groupID, member.accountID)
				seed(`INSERT INTO super_group_members (id, super_group_id, member_group_id, priority, enabled, created_at, updated_at)
					VALUES (?, ?, ?, ?, 1, ?, ?)`, "sgm_"+target.groupID+"_"+member.groupID, target.groupID, member.groupID, index, now, now)
			}
		} else {
			addAccount(target.groupID, target.accountID)
		}
	}
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status) VALUES ('rs_outer', ?, '外层遍历', ?, NULL, 'active')`,
		fixture.systemAccount, mode)
	for index, target := range targets {
		seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
			VALUES (?, 'rs_outer', ?, ?, ?, 1, 'active', ?)`, "rsg_outer_"+target.groupID, fixture.systemAccount, target.groupID, index+1, now)
	}
	seed(`UPDATE api_keys SET route_strategy_id = 'rs_outer' WHERE id = 'key_1'`)
	fixture.cache = w1FreshCacheFor(t, fixture)
	return fixture
}

func TestSuperGroupOuterFixtures(t *testing.T) {
	t.Run("dispatch-upstream-exhausted", func(t *testing.T) {
		fixture := seedSuperOuterFixture(t, "failover",
			superOuterTarget{groupID: "grp_super_primary", kind: "super", members: []superOuterMember{
				{groupID: "group_super_member", accountID: "acc_super_member"},
				{groupID: "group_super_member2", accountID: "acc_super_member2"},
			}},
			superOuterTarget{groupID: "group_backup", kind: "standard", accountID: "acc_backup"},
		)
		pointAccountAt(t, fixture, "acc_super_member", superFailureUpstream(t).URL)
		pointAccountAt(t, fixture, "acc_super_member2", superFailureUpstream(t).URL)
		pointAccountAt(t, fixture, "acc_backup", superSuccessUpstream(t, "备用组命中").URL)

		chain, spoolDir, shutdown := composeChainWithSpool(t, fixture, nil)
		defer shutdown()
		status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`, nil)
		if status != http.StatusOK || !strings.Contains(body, "备用组命中") {
			t.Fatalf("outer fallback did not serve via G2: %d %s", status, body)
		}
		// usage 实际服务组为备用标准组。
		records := waitForSpoolRecordsUntil(t, spoolDir, func(snapshots []gatewayusage.UsageRecordInput) bool {
			for _, record := range snapshots {
				if record.Success && record.AccountID == "acc_backup" {
					return true
				}
			}
			return false
		})
		for _, record := range records {
			if record.Success && record.AccountID == "acc_backup" && record.GroupID != "group_backup" {
				t.Fatalf("usage group = %q, want group_backup", record.GroupID)
			}
		}
	})

	t.Run("outer-all-no-candidate", func(t *testing.T) {
		fixture := seedSuperOuterFixture(t, "weighted",
			superOuterTarget{groupID: "grp_super_a", kind: "super", members: []superOuterMember{
				{groupID: "group_sa1", accountID: "acc_sa1"},
				{groupID: "group_sa2", accountID: "acc_sa2"},
			}},
			superOuterTarget{groupID: "grp_super_b", kind: "super", members: []superOuterMember{
				{groupID: "group_sb1", accountID: "acc_sb1"},
				{groupID: "group_sb2", accountID: "acc_sb2"},
			}},
		)
		for _, accountID := range []string{"acc_sa1", "acc_sa2", "acc_sb1", "acc_sb2"} {
			if _, err := fixture.db.Exec(`DELETE FROM account_supported_models WHERE account_id = ?`, accountID); err != nil {
				t.Fatal(err)
			}
		}
		fixture.cache = w1FreshCacheFor(t, fixture)
		chain, _, shutdown := composeChainWithSpool(t, fixture, nil)
		defer shutdown()
		status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`, nil)
		if status != http.StatusServiceUnavailable || !strings.Contains(body, "model_target_group_unavailable") {
			t.Fatalf("want 503 model_target_group_unavailable, got %d %s", status, body)
		}
	})

	t.Run("dispatch-non-replayable-stop", func(t *testing.T) {
		fixture := seedSuperOuterFixture(t, "failover",
			superOuterTarget{groupID: "grp_super_primary", kind: "super", members: []superOuterMember{
				{groupID: "group_super_member", accountID: "acc_super_member"},
				{groupID: "group_super_member2", accountID: "acc_super_member2"},
			}},
			superOuterTarget{groupID: "group_backup", kind: "standard", accountID: "acc_backup"},
		)
		// 上游返回 2xx 后正文截断：语义已提交，不可安全重放。
		broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"partial"`))
		}))
		t.Cleanup(broken.Close)
		pointAccountAt(t, fixture, "acc_super_member", broken.URL)
		pointAccountAt(t, fixture, "acc_super_member2", broken.URL)
		pointAccountAt(t, fixture, "acc_backup", superSuccessUpstream(t, "备份命中").URL)

		chain, spoolDir, shutdown := composeChainWithSpool(t, fixture, nil)
		defer shutdown()
		status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`, nil)
		if status == http.StatusOK || strings.Contains(body, "备份命中") {
			t.Fatalf("non-replayable failure must not switch to G2: %d %s", status, body)
		}
		// 无 G2 attempt：给失败 usage 行留出异步落盘时间后确认没有 acc_backup 记录。
		time.Sleep(750 * time.Millisecond)
		for _, record := range spoolRecords(spoolDir) {
			if record.AccountID == "acc_backup" {
				t.Fatalf("G2 must not be attempted after semantic commit: %+v", record)
			}
		}
	})

	t.Run("outer-503-upstream-exhausted", func(t *testing.T) {
		fixture := seedSuperOuterFixture(t, "weighted",
			superOuterTarget{groupID: "grp_super_a", kind: "super", members: []superOuterMember{
				{groupID: "group_sa1", accountID: "acc_sa1"},
				{groupID: "group_sa2", accountID: "acc_sa2"},
			}},
			superOuterTarget{groupID: "grp_super_b", kind: "super", members: []superOuterMember{
				{groupID: "group_sb1", accountID: "acc_sb1"},
				{groupID: "group_sb2", accountID: "acc_sb2"},
			}},
		)
		for _, accountID := range []string{"acc_sa1", "acc_sa2", "acc_sb1", "acc_sb2"} {
			pointAccountAt(t, fixture, accountID, superFailureUpstream(t).URL)
		}
		chain, _, shutdown := composeChainWithSpool(t, fixture, nil)
		defer shutdown()
		status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`, nil)
		if status != http.StatusServiceUnavailable || !strings.Contains(body, "upstream_candidates_exhausted") {
			t.Fatalf("want 503 upstream_candidates_exhausted, got %d %s", status, body)
		}
	})
}
