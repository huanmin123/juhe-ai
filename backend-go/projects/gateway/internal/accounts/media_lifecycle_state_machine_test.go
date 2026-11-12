package accounts

// 媒体账户（video/audio/image）与对话账户统一使用 J1 健康状态机：创建后
// pending_test 等待首次激活探测、连接变化降级 pending_test、error 人工恢复
// 进入 pending_test 复检；手动模式仍然保持显式状态。

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

// TestMediaDomainCreateDefaultsPendingTest：所有场景缺省创建落 pending_test
// 等待 J1 首次激活检查（显式 active 携带 skipInitialHealthCheck 才 active；
// 显式 disabled 仍 disabled），并写入健康检查快照 outbox。
func TestMediaDomainCreateDefaultsPendingTest(t *testing.T) {
	cases := []struct {
		domain          string
		statusField     string
		wantStatus      string
		wantSchedulable int
		wantNotice      bool
	}{
		{domain: "audio", statusField: "", wantStatus: "pending_test", wantSchedulable: 0, wantNotice: true},
		{domain: "video", statusField: "", wantStatus: "pending_test", wantSchedulable: 0, wantNotice: true},
		{domain: "image", statusField: "", wantStatus: "pending_test", wantSchedulable: 0, wantNotice: true},
		{domain: "audio", statusField: "active", wantStatus: "active", wantSchedulable: 1},
		{domain: "video", statusField: "active", wantStatus: "active", wantSchedulable: 1},
		{domain: "image", statusField: "active", wantStatus: "active", wantSchedulable: 1},
		{domain: "audio", statusField: "disabled", wantStatus: "disabled", wantSchedulable: 0},
		{domain: "chat", statusField: "", wantStatus: "pending_test", wantSchedulable: 0, wantNotice: true},
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
			if healthRows == 0 {
				t.Fatalf("%s 域创建应写入健康检查快照 outbox", testCase.domain)
			}
		})
	}
}

// TestMediaDomainEditConnectionChangeFallsToPendingTest：所有场景在后台探测
// 开启时，连接面（base_url）变化且不带显式 status 都降级 pending_test 等 J1
// 复检，不再有媒体域例外。
func TestMediaDomainEditConnectionChangeFallsToPendingTest(t *testing.T) {
	for _, domain := range []string{"audio", "video", "image", "chat"} {
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
			if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'pending_test'
				AND schedulable = 0 AND cooldown_until IS NULL`, id) != 1 {
				t.Fatalf("%s 域连接变化编辑后应落 pending_test", domain)
			}
			if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox WHERE account_id = ?`, id) == 0 {
				t.Fatalf("%s 域连接编辑应排队健康快照", domain)
			}
		})
	}
}

// TestMediaDomainClearFailureStateErrorFallsToPendingTest：所有场景 error 行
// 人工恢复都落 pending_test 等 J1 复检（手动模式例外另由总开关测试覆盖）。
func TestMediaDomainClearFailureStateErrorFallsToPendingTest(t *testing.T) {
	for _, domain := range []string{"audio", "video", "image", "chat"} {
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
			if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'pending_test'
				AND schedulable = 0 AND cooldown_until IS NULL
				AND last_error_code IS NULL AND last_error_message IS NULL`, id) != 1 {
				t.Fatalf("%s 域 error 人工恢复应落 pending_test", domain)
			}
		})
	}
}

// TestMediaDomainClearFailureStateCoolingStillActive：冷却态行
// （temporary_unavailable/rate_limited）的人工恢复目标本就是 active，场景
// 不改变冷却臂语义。
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
