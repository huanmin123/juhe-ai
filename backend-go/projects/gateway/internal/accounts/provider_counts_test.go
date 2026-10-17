// 供应商卡片墙全量计数读面（2026-10-17 读时聚合契约）：/accounts 与
// /my-accounts 两面共链。覆盖：provider_code 分组聚合、domain 过滤（合法/
// all/非法 400）、两面 scope 边界（用户面只见本人、管理面随 systemAccountId
// 过滤）、软删行排除。
package accounts

import (
	"net/http"
	"testing"
)

func providerCountsPayload(t *testing.T, payload map[string]any) map[string]int64 {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing counts data: %v", payload)
	}
	counts := map[string]int64{}
	for code, raw := range data {
		number, ok := raw.(float64)
		if !ok {
			t.Fatalf("count for %s must be a number: %v", code, raw)
		}
		counts[code] = int64(number)
	}
	return counts
}

func TestProviderCountsGroupingDomainAndScope(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	env.seedAccount(t, "pc-chat-gpt", adminID, "pc-chat-gpt", "active")
	env.seedAccount(t, "pc-xai-1", adminID, "pc-xai-1", "active")
	env.seedAccount(t, "pc-xai-2", adminID, "pc-xai-2", "active")
	env.exec(t, `UPDATE accounts SET provider_code = 'xai' WHERE id IN ('pc-xai-1', 'pc-xai-2')`)
	env.exec(t, `UPDATE accounts SET capability_domain = 'video' WHERE id = 'pc-xai-2'`)

	// 缺省 = 全量分组：gpt 1、xai 2（跨域合计）。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/provider-counts", "")
	if code != http.StatusOK {
		t.Fatalf("provider-counts 应 200：%d %v", code, payload)
	}
	counts := providerCountsPayload(t, payload)
	if counts["gpt"] != 1 || counts["xai"] != 2 || len(counts) != 2 {
		t.Fatalf("全量分组计数应为 gpt=1 xai=2：%v", counts)
	}

	// domain=video 只命中 video 行。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/provider-counts?domain=video", "")
	if code != http.StatusOK {
		t.Fatalf("domain=video 应 200：%d %v", code, payload)
	}
	counts = providerCountsPayload(t, payload)
	if counts["xai"] != 1 || len(counts) != 1 {
		t.Fatalf("domain=video 应只命中 xai=1：%v", counts)
	}

	// domain=all 与缺省同义。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/provider-counts?domain=all", "")
	if code != http.StatusOK {
		t.Fatalf("domain=all 应 200：%d %v", code, payload)
	}
	counts = providerCountsPayload(t, payload)
	if counts["gpt"] != 1 || counts["xai"] != 2 {
		t.Fatalf("domain=all 应等价缺省：%v", counts)
	}

	// 非法域按列表同语义 400。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts/provider-counts?domain=whatever", "")
	if code != http.StatusBadRequest {
		t.Fatalf("非法 domain 应 400：%d", code)
	}

	// 软删行不计入。
	env.exec(t, `UPDATE accounts SET deleted_at = '2026-10-17T00:00:00.000Z' WHERE id = 'pc-chat-gpt'`)
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/provider-counts", "")
	if code != http.StatusOK {
		t.Fatalf("软删后应 200：%d %v", code, payload)
	}
	counts = providerCountsPayload(t, payload)
	if _, exists := counts["gpt"]; exists {
		t.Fatalf("软删行不应计入：%v", counts)
	}
}

func TestProviderCountsSelfSurfaceScope(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "pc-admin-1", adminID, "pc-admin-1", "active")
	env.seedAccount(t, "pc-admin-2", adminID, "pc-admin-2", "active")
	env.exec(t, `UPDATE accounts SET provider_code = 'xai' WHERE id = 'pc-admin-2'`)

	// 管理面随 systemAccountId 过滤：指定不存在的范围 = 0 行计数。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/provider-counts?systemAccountId=sys-nobody", "")
	if code != http.StatusOK {
		t.Fatalf("管理面范围过滤应 200：%d %v", code, payload)
	}
	if counts := providerCountsPayload(t, payload); len(counts) != 0 {
		t.Fatalf("范围外计数应为空：%v", counts)
	}

	// 用户面强制本人范围：alice 只见自己的账户，管理员账户不出现。
	aliceID := env.login(t, "alice", "alice-pass", "user")
	env.seedProviderAndDefaultGroup(t, aliceID)
	env.seedAccount(t, "pc-alice-1", aliceID, "pc-alice-1", "active")
	env.seedAccount(t, "pc-alice-2", aliceID, "pc-alice-2", "active")
	env.seedAccount(t, "pc-alice-3", aliceID, "pc-alice-3", "active")
	env.exec(t, `UPDATE accounts SET provider_code = 'xai' WHERE id IN ('pc-alice-2', 'pc-alice-3')`)

	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/provider-counts", "")
	if code != http.StatusOK {
		t.Fatalf("用户面计数应 200：%d %v", code, payload)
	}
	counts := providerCountsPayload(t, payload)
	if counts["gpt"] != 1 || counts["xai"] != 2 || len(counts) != 2 {
		t.Fatalf("用户面应只见本人 gpt=1 xai=2：%v", counts)
	}
}
