package main

// 媒体任务面出站代理消费 chain 级集成测试（与同步 lane 同源的账户绑定代理：
// 账户配了代理 → 任务面轮询/内容出站走该代理（取消同出口，不再单列）；未配
// 代理保持直连由既有全量媒体测试回归覆盖（它们不配代理）；档案停用/删除 →
// 503 代理不可用，绝不静默回落直连）。
//
// 观测拓扑：账户 base_url 直指 mockupstream 引擎，网关与引擎之间插一台本地
// httptest「代理服务器」（只收绝对 URI 的 HTTP 代理形态请求，逐笔记录
// Proxy-Authorization 与目标后转发引擎）。不变量：代理命中数 == 引擎收到数
// ⇔ 全部出站都经过代理、零直连旁路；代理档案带 AES-GCM 加密密码（走解密
// 分支），经代理的请求都应携带 Basic Proxy-Authorization（Go transport 对带
// userinfo 的 http 代理逐请求附加）。
import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// mediaProxyHit 是代理服务器捕获的一笔请求（方法/目标 host/路径/代理认证头）。
type mediaProxyHit struct {
	Method          string
	TargetHost      string
	Path            string
	ProxyAuthHeader string
}

// mediaProxyHitRecorder 逐笔记录代理服务器入站请求。
type mediaProxyHitRecorder struct {
	mu   sync.Mutex
	hits []mediaProxyHit
}

func (r *mediaProxyHitRecorder) record(hit mediaProxyHit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits = append(r.hits, hit)
}

func (r *mediaProxyHitRecorder) snapshot() []mediaProxyHit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mediaProxyHit(nil), r.hits...)
}

// newMediaForwardingProxy 启动本地转发代理：只接受绝对 URI（HTTP 代理形态）
// 请求，逐笔记录后转发到目标（mockupstream 引擎），响应原样回写。
func newMediaForwardingProxy(t *testing.T) (*mediaProxyHitRecorder, string) {
	t.Helper()
	recorder := &mediaProxyHitRecorder{}
	forwardClient := &http.Client{Timeout: 30 * time.Second}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(mediaProxyHit{
			Method:          r.Method,
			TargetHost:      r.URL.Host,
			Path:            r.URL.Path,
			ProxyAuthHeader: r.Header.Get("Proxy-Authorization"),
		})
		if r.URL.Scheme == "" || r.URL.Host == "" {
			// 非代理形态（相对 URI）不该出现在本拓扑：立即失败让用例暴露。
			http.Error(w, "expected absolute-form proxy request", http.StatusBadRequest)
			return
		}
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, "build forwarded request: "+err.Error(), http.StatusBadGateway)
			return
		}
		outReq.Header = r.Header.Clone()
		response, err := forwardClient.Do(outReq)
		if err != nil {
			http.Error(w, "forward: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = response.Body.Close() }()
		for name, values := range response.Header {
			if strings.EqualFold(name, "Transfer-Encoding") {
				continue // 帧形态由回写端自行收敛
			}
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(server.Close)
	return recorder, server.URL
}

// seedMediaProxyProfile 种 http 代理档案（proxyuser + AES-GCM 加密密码，覆盖
// 解密分支；fixture secret 与 MediaJobsSecret/selector 同为 chain-test-secret）
// 并把账户绑定到该档案，返回档案 id。
func seedMediaProxyProfile(t *testing.T, fixture *chainFixture, accountID, host string, port int, enabled bool) string {
	t.Helper()
	proxyPassword, err := accounts.EncryptJSON("chain-test-secret", map[string]any{"password": "proxypass"})
	if err != nil {
		t.Fatalf("encrypt proxy password: %v", err)
	}
	enabledValue := 1
	if !enabled {
		enabledValue = 0
	}
	now := "2026-10-16T00:00:00.000Z"
	if _, err := fixture.db.Exec(`INSERT INTO proxy_profiles (id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
		VALUES ('proxy_media', '任务面出口', 'http', ?, ?, 'proxyuser', ?, ?, ?, ?)`,
		host, port, proxyPassword, enabledValue, now, now); err != nil {
		t.Fatalf("seed proxy profile: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE accounts SET proxy_profile_id = 'proxy_media' WHERE id = ?`, accountID); err != nil {
		t.Fatalf("bind account proxy profile: %v", err)
	}
	return "proxy_media"
}

// mediaProxyAssertions 汇总两用例共用的观测断言入口（host 提取 + 凭据头期望值）。
func mediaProxyTargetOf(t *testing.T, raw string) (host string, port int) {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %s: %v", raw, err)
	}
	port, err = strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse port of %s: %v", raw, err)
	}
	return parsed.Hostname(), port
}

// TestChainMediaJobTaskPlaneConsumesAccountProxy 正向：qwen 万相视频账户绑
// http 代理档案，跑完整生命周期（创建走派发引擎 + 轮询/内容走任务面）。
// 断言代理服务器逐笔记录到创建/轮询/内容请求（含 Basic 代理凭据）且引擎收到
// 数一致（零直连旁路），生命周期响应正常（状态推进 + content mp4 + 行落表）。
// 覆盖两个出站函数：轮询经 mediaJobRequestUpstream、内容经
// mediaJobArtifactRequestUpstream（qwen ContentFromArtifact 绝对 URL）。
func TestChainMediaJobTaskPlaneConsumesAccountProxy(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	recorder, proxyRawURL := newMediaForwardingProxy(t)
	proxyHost, proxyPort := mediaProxyTargetOf(t, proxyRawURL)
	mockParsed, err := url.Parse(mock.URL)
	if err != nil {
		t.Fatalf("parse mock url: %v", err)
	}
	mockHostPort := mockParsed.Host

	apiKey := seedMediaQwenAccount(t, fixture, "acc_qwen_proxy", mock.URL, "sk-dashscope-proxy-key")
	seedMediaProxyProfile(t, fixture, "acc_qwen_proxy", proxyHost, proxyPort, true)

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	// 创建（引擎出站，同账户代理消费）：受理 + queued。
	job := mediaVideoCreateJob(t, server.URL, apiKey,
		`{"model":"wan2.2-t2v-plus","prompt":"一只猫在弹钢琴","seconds":5,"size":"1920x1080"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaQwenCreatePending)})
	jobID, _ := job["id"].(string)
	if jobID == "" || !strings.HasPrefix(jobID, "video_") {
		t.Fatalf("对外 job id 缺失或非 video_ 前缀: %#v", job)
	}
	if job["status"] != "queued" {
		t.Fatalf("创建响应 status = %v, want queued", job["status"])
	}
	providerJobID, _ := job["provider_job_id"].(string)

	// 轮询三次（任务面 mediaJobRequestUpstream 经代理）：queued→in_progress→
	// completed。
	if first := mediaVideoPollOnce(t, server.URL, apiKey, jobID); first["status"] != "queued" {
		t.Fatalf("第一轮轮询 status = %v, want queued", first["status"])
	}
	if second := mediaVideoPollOnce(t, server.URL, apiKey, jobID); second["status"] != "in_progress" {
		t.Fatalf("第二轮轮询 status = %v, want in_progress", second["status"])
	}
	if third := mediaVideoPollOnce(t, server.URL, apiKey, jobID); third["status"] != "completed" {
		t.Fatalf("第三轮轮询 status = %v, want completed: %v", third["status"], third)
	}

	// content 下载（任务面 mediaJobArtifactRequestUpstream 绝对 URL 经代理）：
	// mp4 流落客户端。
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/videos/"+jobID+"/content", apiKey, "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("content status=%d body=%s", response.StatusCode, payload)
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "video/mp4") {
		t.Fatalf("content-type = %q, want video/mp4", got)
	}
	if len(payload) < 12 || string(payload[4:8]) != "ftyp" {
		t.Fatalf("content 不是 mp4 载荷（ftyp magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}

	// 代理观测：1 创建 POST + 3 轮询 GET + 1 内容 GET，全部指向 mock 引擎、
	// 全部携带档案 Basic 凭据；引擎收到数与代理命中数一致（零直连旁路）。
	hits := recorder.snapshot()
	if len(hits) != 5 {
		t.Fatalf("代理命中数 = %d, want 5（1 创建 + 3 轮询 + 1 内容）: %+v", len(hits), hits)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxyuser:proxypass"))
	for i, hit := range hits {
		if hit.TargetHost != mockHostPort {
			t.Fatalf("代理命中 #%d 目标 = %q, want 引擎 %q", i, hit.TargetHost, mockHostPort)
		}
		if hit.ProxyAuthHeader != wantAuth {
			t.Fatalf("代理命中 #%d Proxy-Authorization = %q, want %q（档案凭据未按解密结果消费）", i, hit.ProxyAuthHeader, wantAuth)
		}
	}
	if hits[0].Method != http.MethodPost || hits[0].Path != "/api/v1/services/aigc/video-generation/video-synthesis" {
		t.Fatalf("创建出站未按预期经代理: %+v", hits[0])
	}
	for _, hit := range hits[1:4] {
		if hit.Method != http.MethodGet || hit.Path != "/api/v1/tasks/"+providerJobID {
			t.Fatalf("轮询出站未按预期经代理: %+v", hit)
		}
	}
	if hits[4].Method != http.MethodGet || !strings.HasSuffix(hits[4].Path, "/content") {
		t.Fatalf("内容出站未按预期经代理（绝对 URL artifact）: %+v", hits[4])
	}
	if got := len(mock.Requests()); got != 5 {
		t.Fatalf("引擎收到请求数 = %d, want 5（与代理命中一致，不得出现直连旁路）", got)
	}

	// 生命周期落表：media_jobs 行 completed + 上游任务 id 回填。
	var status, upstreamJobID string
	if err := fixture.db.QueryRow(`SELECT status, upstream_job_id FROM media_jobs WHERE id = ?`, jobID).Scan(&status, &upstreamJobID); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if status != "completed" || upstreamJobID != providerJobID {
		t.Fatalf("media_jobs = %s/%s, want completed/%s", status, upstreamJobID, providerJobID)
	}
}

// TestChainMediaJobTaskPlaneProxyUnavailableFailsClosed 失败语义：档案停用/
// 删除后任务面轮询 503「代理不可用」（含档案 id），mock 引擎未收到任何请求
// （无直连旁路、无回落），media_jobs 行无损坏。创建与首轮轮询在档案启用时
// 先行完成（经代理），随后逐臂收口。
func TestChainMediaJobTaskPlaneProxyUnavailableFailsClosed(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	recorder, proxyRawURL := newMediaForwardingProxy(t)
	proxyHost, proxyPort := mediaProxyTargetOf(t, proxyRawURL)

	seedMediaVideoAccount(t, fixture.db, fixture, "acc_video_proxy_off", mock.URL, "sk-video-proxy-off", 0, true)
	seedMediaProxyProfile(t, fixture, "acc_video_proxy_off", proxyHost, proxyPort, true)

	spoolDir := filepath.Join(t.TempDir(), "spool")
	chain := composeMediaVideoChain(t, fixture, spoolDir)
	server := httptest.NewServer(chain)
	defer server.Close()

	job := mediaVideoCreateJob(t, server.URL, fixture.apiKeySecret,
		`{"model":"sora-2","prompt":"一只猫在弹钢琴","seconds":"4","size":"1280x720"}`,
		map[string]string{"X-Mock-Scenario": string(platformmock.ScenarioMediaVideoOKPoll3)})
	jobID, _ := job["id"].(string)
	if first := mediaVideoPollOnce(t, server.URL, fixture.apiKeySecret, jobID); first["status"] != "in_progress" {
		t.Fatalf("首轮轮询 status = %v, want in_progress（档案启用臂经代理）", first["status"])
	}
	hitsBaseline := len(recorder.snapshot())
	mockBaseline := len(mock.Requests())

	// 臂 1：档案停用 → 轮询 503 代理不可用（含档案 id），无任何出站。
	if _, err := fixture.db.Exec(`UPDATE proxy_profiles SET enabled = 0 WHERE id = 'proxy_media'`); err != nil {
		t.Fatalf("disable proxy profile: %v", err)
	}
	response, payload := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/videos/"+jobID, fixture.apiKeySecret, "", nil)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("停用后轮询 status = %d, want 503, body=%s", response.StatusCode, payload)
	}
	assertMediaProxyUnavailablePayload(t, payload)

	// 臂 2：档案删除 → 同一失败语义（缺失臂），仍无任何出站。
	if _, err := fixture.db.Exec(`DELETE FROM proxy_profiles WHERE id = 'proxy_media'`); err != nil {
		t.Fatalf("delete proxy profile: %v", err)
	}
	response2, payload2 := mediaVideoClientDo(t, server.URL, http.MethodGet, "/v1/videos/"+jobID, fixture.apiKeySecret, "", nil)
	if response2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("删除后轮询 status = %d, want 503, body=%s", response2.StatusCode, payload2)
	}
	assertMediaProxyUnavailablePayload(t, payload2)

	// 无直连旁路：代理与引擎的请求计数都停在停用前（引擎侧任何增量都意味着
	// 绕过代理的直连）。
	if got := len(mock.Requests()); got != mockBaseline {
		t.Fatalf("引擎收到请求数 = %d, want 停用前基线 %d（出现直连回落）: %+v", got, mockBaseline, mock.Requests())
	}
	if got := len(recorder.snapshot()); got != hitsBaseline {
		t.Fatalf("代理命中数 = %d, want 停用前基线 %d（失败臂不得出站）", got, hitsBaseline)
	}
	// 任务记录无损坏：行仍停在最后一次成功轮询推进的 in_progress。
	var status string
	if err := fixture.db.QueryRow(`SELECT status FROM media_jobs WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("查询 media_jobs 行: %v", err)
	}
	if status != "in_progress" {
		t.Fatalf("media_jobs status = %s, want in_progress（失败臂不得改写任务行）", status)
	}
}

// assertMediaProxyUnavailablePayload 断言 503 错误信封含「代理不可用」、档案
// id 与 proxy_unavailable 错误码（不得含代理密码）。
func assertMediaProxyUnavailablePayload(t *testing.T, payload []byte) {
	t.Helper()
	body := string(payload)
	for _, want := range []string{"代理不可用", "proxy_media", "proxy_unavailable"} {
		if !strings.Contains(body, want) {
			t.Fatalf("错误信封缺少 %q: %s", want, body)
		}
	}
	if strings.Contains(body, "proxypass") {
		t.Fatalf("错误信封不得含代理密码: %s", body)
	}
}
