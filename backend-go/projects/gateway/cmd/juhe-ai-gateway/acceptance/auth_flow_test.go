// X05 场景 2：认证流。admin 登录（seed 密码）→ /auth/me → captcha disabled
// 契约 → 登出后 401；错误密码 401 契约与连续失败后的登录守护 429 锁定路径。
package acceptance

import (
	"net/http"
	"testing"
)

func TestAcceptanceAuthFlow(t *testing.T) {
	fixture := startGateway(t, gatewayEnvOptions{})
	client := newClient(t, fixture.baseURL)

	// captcha disabled（JUHE_AI_AUTH_CAPTCHA_DISABLED=true 契约，authsys
	// routes.go getCaptcha：data.required=false）。Node 出处：
	// auth.routes.ts GET /auth/captcha disabled 分支。
	_, captcha := client.do(http.MethodGet, "/__aisys__/api/auth/captcha", nil, wantStatus(http.StatusOK))
	captchaData := data(captcha)
	if captchaData == nil || captchaData["required"] != false {
		t.Fatalf("captcha payload wrong: %#v", captcha)
	}

	// 未登录的受保护面：401 + 「请先登录」（authsys routes.go getMe；
	// Node auth.routes.ts requireAuth 的 401 消息）。
	status, payload := client.do(http.MethodGet, "/__aisys__/api/auth/me", nil, wantStatus(http.StatusUnauthorized))
	if payload["message"] != "请先登录" {
		t.Fatalf("unauthenticated /auth/me payload=%#v", payload)
	}
	_ = status

	// 错误密码：401 + 「账号或密码错误」（routes.go postLogin；Node
	// auth.service.ts login 失败消息）。
	client.do(http.MethodPost, "/__aisys__/api/auth/login",
		map[string]any{"username": "admin", "password": "wrong-password"}, wantStatus(http.StatusUnauthorized))

	// 空/带空格参数：400 + 「登录参数无效」。
	client.do(http.MethodPost, "/__aisys__/api/auth/login",
		map[string]any{"username": "admin"}, wantStatus(http.StatusBadRequest))

	// 正确登录：seed 密码 admin/admin。
	_, login := client.do(http.MethodPost, "/__aisys__/api/auth/login",
		map[string]any{"username": "admin", "password": acceptanceAdminPassword}, wantStatus(http.StatusOK))
	me := data(login)
	if me["username"] != "admin" || me["role"] != "super_admin" || me["mustChangePassword"] != false {
		t.Fatalf("login summary wrong: %#v", me)
	}

	// 登录后会话可用：/auth/me 返回同一账户。
	_, mePayload := client.do(http.MethodGet, "/__aisys__/api/auth/me", nil, wantStatus(http.StatusOK))
	if data(mePayload)["id"] != "sys_admin" {
		t.Fatalf("/auth/me payload wrong: %#v", mePayload)
	}

	// PATCH /auth/me 修改显示名称（self 路径契约）。
	_, patched := client.do(http.MethodPatch, "/__aisys__/api/auth/me",
		map[string]any{"displayName": "验收管理员"}, wantStatus(http.StatusOK))
	if data(patched)["displayName"] != "验收管理员" {
		t.Fatalf("patch me payload wrong: %#v", patched)
	}

	// 登出：200 {loggedOut:true}；随后会话失效 → 401「请先登录」。
	_, logout := client.do(http.MethodPost, "/__aisys__/api/auth/logout", nil, wantStatus(http.StatusOK))
	if data(logout)["loggedOut"] != true {
		t.Fatalf("logout payload wrong: %#v", logout)
	}
	_, afterLogout := client.do(http.MethodGet, "/__aisys__/api/auth/me", nil, wantStatus(http.StatusUnauthorized))
	if afterLogout["message"] != "请先登录" {
		t.Fatalf("after-logout payload wrong: %#v", afterLogout)
	}

	// 登录守护锁定路径（modelcheckauth login_guard.go：10 分钟窗口内累计
	// 10 次失败 → 15 分钟锁，IP 记录与用户名记录同时生效；Node 出处：
	// login-guard.service.ts 同窗口/阈值/消息）。成功登录会清零计数器
	// （Success），此后重新累计：9 次 401，第 10 次失败即触发 429
	// （Failed 返回 IP 维度消息）。
	guardClient := newClient(t, fixture.baseURL)
	for i := 0; i < 9; i++ {
		guardClient.do(http.MethodPost, "/__aisys__/api/auth/login",
			map[string]any{"username": "admin", "password": "still-wrong"}, wantStatus(http.StatusUnauthorized))
	}
	_, triggering := guardClient.do(http.MethodPost, "/__aisys__/api/auth/login",
		map[string]any{"username": "admin", "password": "still-wrong"}, wantStatus(http.StatusTooManyRequests))
	if triggering["message"] != "尝试过于频繁，请稍后再试" {
		t.Fatalf("lockout trigger payload wrong: %#v", triggering)
	}
	// 锁定期内正确密码也被拒绝（Check 先于凭据校验）。
	_, locked := guardClient.do(http.MethodPost, "/__aisys__/api/auth/login",
		map[string]any{"username": "admin", "password": acceptanceAdminPassword}, wantStatus(http.StatusTooManyRequests))
	if locked["message"] != "尝试过于频繁，请稍后再试" {
		t.Fatalf("locked login payload wrong: %#v", locked)
	}
}
