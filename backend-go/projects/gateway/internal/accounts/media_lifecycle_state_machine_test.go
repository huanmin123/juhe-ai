package accounts

// 媒体账户生命周期定案表（R1 状态机恢复闭环）的域守卫覆盖：audio/video 域
// 账户不参与对话健康检查状态机——创建默认 active（显式 disabled 仍 disabled）、
// 编辑连接变化不降级 pending_test、人工恢复（ClearFailureState）error 直达
// active；chat/image 域行为不变（对照臂）。

import (
	"fmt"
	"net/http"
	"testing"
)

// mediaCreatePayload 构造带 capabilityDomain 的创建请求体（gpt 档案四域全
// 支持；status 为空串 = 不携带 status 字段，走缺省哨兵）。B1 断点批（设计
// §8）：连接集合的 endpointModes 与健康检查三件套按域显式提交（连接 modes =
// 域 lane 全集，检查形态 = DefaultHealthCheckEndpointModeForDomain 同值）。
func mediaCreatePayload(name, domain, status string) string {
	modes := map[string][]string{
		"chat":  {"chat_json", "chat_sse"},
		"image": {"images_json"},
		"video": {"video_create", "video_get", "video_content", "video_cancel"},
		"audio": {"audio_speech"},
	}[domain]
	healthMode := map[string]string{
		"chat": "chat_json", "image": "images_json", "video": "video_create", "audio": "audio_speech",
	}[domain]
	body := `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"` + name +
		`","type":"api_key","capabilityDomain":"` + domain +
		`","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},` +
		`"supportedModels":["gpt-4o-mini"]` +
		`,"connections":` + b1ConnectionsJSON(modes...) +
		`,"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"` + healthMode + `","healthCheckModel":"gpt-4o-mini"`
	if status != "" {
		body += `,"status":"` + status + `"`
	}
	return body + `}`
}

// createMediaAccount 走真实创建链建一个指定域账户并返回行 id。
func createMediaAccount(t *testing.T, env *testEnv, name, domain, status string) string {
	t.Helper()
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", mediaCreatePayload(name, domain, status))
	if code != http.StatusCreated {
		t.Fatalf("create %s(%s): %d %v", name, domain, code, payload)
	}
	return dataMap(t, payload)["id"].(string)
}

// TestMediaDomainCreateDefaultsActive：audio/video 域创建默认落 active + 可调
// 度（显式 disabled 仍 disabled；缺省 pending_test 哨兵与显式 active 同收敛
// active，且不落「等待后台健康检查」提示）；chat/image 域行为不变（缺省
// pending_test + 暂停调度 + 等待检查提示）。
func TestMediaDomainCreateDefaultsActive(t *testing.T) {
	cases := []struct {
		domain          string
		statusField     string
		wantStatus      string
		wantSchedulable int
		wantNotice      bool
	}{
		{domain: "audio", statusField: "", wantStatus: "active", wantSchedulable: 1},
		{domain: "video", statusField: "", wantStatus: "active", wantSchedulable: 1},
		{domain: "audio", statusField: "active", wantStatus: "active", wantSchedulable: 1},
		{domain: "video", statusField: "active", wantStatus: "active", wantSchedulable: 1},
		{domain: "audio", statusField: "disabled", wantStatus: "disabled", wantSchedulable: 0},
		// 对照臂：chat/image 域行为不变。
		{domain: "chat", statusField: "", wantStatus: "pending_test", wantSchedulable: 0, wantNotice: true},
		{domain: "image", statusField: "", wantStatus: "pending_test", wantSchedulable: 0, wantNotice: true},
	}
	for index, testCase := range cases {
		t.Run(fmt.Sprintf("%s/%s", testCase.domain, testCase.statusField), func(t *testing.T) {
			env := newTestEnv(t)
			adminID := env.login(t, "root", "root-pass", "super_admin")
			env.seedProviderAndDefaultGroup(t, adminID)
			id := createMediaAccount(t, env, fmt.Sprintf("media-create-%d", index), testCase.domain, testCase.statusField)
			if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = ?
				AND schedulable = ?`, id, testCase.wantStatus, testCase.wantSchedulable) != 1 {
				t.Fatalf("%s 域创建应落 %s：行不符", testCase.domain, testCase.wantStatus)
			}
			if testCase.wantNotice {
				if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
					AND last_error_message = '账户已保存，等待后台健康检查'`, id) != 1 {
					t.Fatalf("%s 域缺省创建应保留等待检查提示", testCase.domain)
				}
			} else if env.queryCell(t, `SELECT COALESCE(last_error_message, '') FROM accounts WHERE id = ?`, id) != "" {
				t.Fatalf("%s 域 active 创建不应带等待检查提示", testCase.domain)
			}
			healthRows := env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox WHERE account_id = ?`, id)
			if testCase.domain == "audio" || testCase.domain == "video" {
				if healthRows != 0 {
					t.Fatalf("%s 域创建不应写入健康检查快照 outbox: got %d", testCase.domain, healthRows)
				}
			} else if healthRows == 0 {
				t.Fatalf("%s 域对照账户应保留健康检查快照", testCase.domain)
			}
		})
	}
}

// TestMediaDomainEditConnectionChangeStaysActive：audio/video 域账户编辑连接
// 面（base_url 变化）不带显式 status 时保持 active，不落 pending_test；
// chat 域对照行在同样编辑下降级 pending_test（既有语义）。
func TestMediaDomainEditConnectionChangeStaysActive(t *testing.T) {
	for _, domain := range []string{"audio", "video", "chat"} {
		t.Run(domain, func(t *testing.T) {
			env := newTestEnv(t)
			adminID := env.login(t, "root", "root-pass", "super_admin")
			env.seedProviderAndDefaultGroup(t, adminID)
			id := createMediaAccount(t, env, "conn-change-"+domain, domain, "active")

			code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
				`{"expectedConfigRevision":1,"credentialsPatch":{"base_url":"https://api.openai.com/v2"}}`)
			if code != http.StatusOK {
				t.Fatalf("patch %s: %d %v", domain, code, payload)
			}
			// chat 域降级 pending_test 并强制禁调度（既有语义）；媒体域保持
			// active + 可调度。
			wantStatus, wantSchedulable := "pending_test", 0
			if domain != "chat" {
				wantStatus, wantSchedulable = "active", 1
			}
			if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = ?
				AND schedulable = ? AND cooldown_until IS NULL`, id, wantStatus, wantSchedulable) != 1 {
				t.Fatalf("%s 域连接变化编辑后应保持 %s（schedulable=%d）", domain, wantStatus, wantSchedulable)
			}
			if domain != "chat" && env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox WHERE account_id = ?`, id) != 0 {
				t.Fatalf("%s 域连接编辑不应排队健康快照", domain)
			}
		})
	}
}

// TestMediaDomainClearFailureStateErrorRestoresActive：audio/video 域 error 行
// 人工恢复直达 active + 可调度（无探针复检，落 pending_test 会永久滞留）；
// chat 域对照行维持 error → pending_test 既有语义。
func TestMediaDomainClearFailureStateErrorRestoresActive(t *testing.T) {
	for _, domain := range []string{"audio", "video", "chat"} {
		t.Run(domain, func(t *testing.T) {
			env := newTestEnv(t)
			adminID := env.login(t, "root", "root-pass", "super_admin")
			env.seedProviderAndDefaultGroup(t, adminID)
			id := createMediaAccount(t, env, "restore-error-"+domain, domain, "active")
			env.seedCoolingRetestState(t, id, "error")

			code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
				`{"expectedConfigRevision":1,"clearFailureState":true}`)
			if code != http.StatusOK {
				t.Fatalf("clearFailureState %s: %d %v", domain, code, payload)
			}
			wantStatus := "pending_test"
			wantSchedulable := 0
			if domain != "chat" {
				wantStatus = "active"
				wantSchedulable = 1
			}
			if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = ?
				AND schedulable = ? AND cooldown_until IS NULL
				AND last_error_code IS NULL AND last_error_message IS NULL`, id, wantStatus, wantSchedulable) != 1 {
				t.Fatalf("%s 域 error 人工恢复应落 %s（schedulable=%d）", domain, wantStatus, wantSchedulable)
			}
		})
	}
}

// TestMediaDomainClearFailureStateCoolingStillActive：audio 域冷却态行
// （temporary_unavailable/rate_limited）的人工恢复目标本就是 active，域守卫
// 不改变冷却臂语义（对齐既有 BUG-0288 行为的媒体域平移）。
func TestMediaDomainClearFailureStateCoolingStillActive(t *testing.T) {
	for _, status := range []string{"temporary_unavailable", "rate_limited"} {
		t.Run(status, func(t *testing.T) {
			env := newTestEnv(t)
			adminID := env.login(t, "root", "root-pass", "super_admin")
			env.seedProviderAndDefaultGroup(t, adminID)
			id := createMediaAccount(t, env, "restore-cooling-audio", "audio", "active")
			env.seedCoolingRetestState(t, id, status)
			env.exec(t, `UPDATE accounts SET schedulable = 0 WHERE id = ?`, id)

			code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
				`{"expectedConfigRevision":1,"clearFailureState":true}`)
			if code != http.StatusOK {
				t.Fatalf("clearFailureState %s: %d %v", status, code, payload)
			}
			if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
				AND status = 'active' AND schedulable = 1 AND cooldown_until IS NULL`, id) != 1 {
				t.Fatalf("%s 冷却态人工恢复应保持原子翻转到 active", status)
			}
		})
	}
}
