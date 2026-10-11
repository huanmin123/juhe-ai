// AI 账户能力域拆分 M1 读通道（PLAN-20261011T141004000Z）：列表/详情 DTO
// capabilityDomain 回显、domain 列表过滤参数（合法/非法/缺省/all）与
// normalizeListOptions 的越界兜底。管理面 /accounts 与用户面 /my-accounts
// 共用 d.list 处理器，两面各验证一条主链。
package accounts

import (
	"net/http"
	"testing"
)

func TestCapabilityDomainListEchoAndFilter(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	env.seedAccount(t, "acc-capdom-chat", adminID, "capdom-chat", "active")
	env.seedAccount(t, "acc-capdom-video", adminID, "capdom-video", "active")
	env.exec(t, `UPDATE accounts SET capability_domain = 'video' WHERE id = 'acc-capdom-video'`)
	env.seedAccount(t, "acc-capdom-image", adminID, "capdom-image", "active")
	env.exec(t, `UPDATE accounts SET capability_domain = 'image' WHERE id = 'acc-capdom-image'`)

	// 回显：缺省 'chat' 与显式域都按列原样投影。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("列表应 200：%d %v", code, payload)
	}
	items := listItems(t, payload)
	if items["acc-capdom-chat"]["capabilityDomain"] != "chat" {
		t.Fatalf("缺省域应回显 chat：%v", items["acc-capdom-chat"]["capabilityDomain"])
	}
	if items["acc-capdom-video"]["capabilityDomain"] != "video" || items["acc-capdom-image"]["capabilityDomain"] != "image" {
		t.Fatalf("显式域应按列回显：%v %v", items["acc-capdom-video"]["capabilityDomain"], items["acc-capdom-image"]["capabilityDomain"])
	}

	// domain 过滤：video 只命中 video 行。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts?domain=video", "")
	if code != http.StatusOK {
		t.Fatalf("domain=video 应 200：%d %v", code, payload)
	}
	items = listItems(t, payload)
	if len(items) != 1 || items["acc-capdom-video"] == nil {
		t.Fatalf("domain=video 应只命中 video 账户：%v", items)
	}

	// domain 过滤：image 只命中 image 行。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts?domain=image", "")
	if code != http.StatusOK {
		t.Fatalf("domain=image 应 200：%d %v", code, payload)
	}
	items = listItems(t, payload)
	if len(items) != 1 || items["acc-capdom-image"] == nil {
		t.Fatalf("domain=image 应只命中 image 账户：%v", items)
	}

	// 缺省 = 不过滤；显式 all 与 providerCode/type 既有语义一致 = 不过滤。
	for _, query := range []string{"", "?domain="} {
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts"+query, "")
		if code != http.StatusOK {
			t.Fatalf("domain 缺省应 200：%d %v", code, payload)
		}
		if got := len(listItems(t, payload)); got != 3 {
			t.Fatalf("domain 缺省应返回全部 3 行，got %d", got)
		}
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts?domain=all", "")
	if code != http.StatusOK {
		t.Fatalf("domain=all 应 200：%d %v", code, payload)
	}
	if got := len(listItems(t, payload)); got != 3 {
		t.Fatalf("domain=all 应返回全部 3 行，got %d", got)
	}

	// 非法枚举：既有参数错误语义 400，不静默退化成全量列表。
	for _, domain := range []string{"voice", "CHAT", "video_json", "../chat"} {
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts?domain="+domain, "")
		if code != http.StatusBadRequest {
			t.Fatalf("domain=%s 应 400，got %d %v", domain, code, payload)
		}
	}
}

func TestCapabilityDomainMyAccountsFilter(t *testing.T) {
	env := newTestEnv(t)
	userID := env.login(t, "user1", "user-pass", "user")
	env.seedProviderAndDefaultGroup(t, userID)
	env.seedAccount(t, "acc-capdom-my-chat", userID, "capdom-my-chat", "active")
	env.seedAccount(t, "acc-capdom-my-audio", userID, "capdom-my-audio", "active")
	env.exec(t, `UPDATE accounts SET capability_domain = 'audio' WHERE id = 'acc-capdom-my-audio'`)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts?domain=audio", "")
	if code != http.StatusOK {
		t.Fatalf("my-accounts domain=audio 应 200：%d %v", code, payload)
	}
	items := listItems(t, payload)
	if len(items) != 1 || items["acc-capdom-my-audio"] == nil {
		t.Fatalf("my-accounts domain=audio 应只命中 audio 账户：%v", items)
	}
	if items["acc-capdom-my-audio"]["capabilityDomain"] != "audio" {
		t.Fatalf("my-accounts 应回显 audio：%v", items["acc-capdom-my-audio"]["capabilityDomain"])
	}

	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-accounts?domain=bogus", "")
	if code != http.StatusBadRequest {
		t.Fatalf("my-accounts domain=bogus 应 400，got %d %v", code, payload)
	}
}

func TestCapabilityDomainEditBasicDetailEcho(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-capdom-detail", adminID, "capdom-detail", "active")
	env.exec(t, `UPDATE accounts SET capability_domain = 'video' WHERE id = 'acc-capdom-detail'`)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-capdom-detail/edit-basic", "")
	if code != http.StatusOK {
		t.Fatalf("编辑详情应 200：%d %v", code, payload)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("详情缺少 data：%v", payload)
	}
	if data["capabilityDomain"] != "video" {
		t.Fatalf("详情应回显 video：%v", data["capabilityDomain"])
	}
}

func TestNormalizeListOptionsClampsInvalidDomain(t *testing.T) {
	// store 层防线：路由面 400 之外的任何越界输入在规范化层归一为不过滤，
	// SQL 谓词永不消费越界值。
	normalized := normalizeListOptions(ListOptions{Domain: "voice"})
	if normalized.Domain != "" {
		t.Fatalf("非法域应归一为空串，got %q", normalized.Domain)
	}
	normalized = normalizeListOptions(ListOptions{Domain: " video "})
	if normalized.Domain != "video" {
		t.Fatalf("合法域应去空白保留，got %q", normalized.Domain)
	}
	normalized = normalizeListOptions(ListOptions{Domain: "all"})
	if normalized.Domain != "" {
		t.Fatalf("all 应归一为不过滤，got %q", normalized.Domain)
	}
}
