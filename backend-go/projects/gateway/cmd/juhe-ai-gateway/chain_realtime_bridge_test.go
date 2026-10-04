package main

// M5b2 realtime WS 桥接链级测试（Realtime 设计 §8 验收 1-6 的链内覆盖；
// 全链 E2E 见 acceptance/fullchain_media_realtime_test.go）：进程内 fixture
//（SQLite 业务库 + miniredis token 面）+ mockupstream 真 WS 上游，覆盖：
//   - echo 会话往返逐字节（透传不重写）+ usage 终态落库金额（$32/$64 价）；
//   - 受理边界两臂：reject_upgrade 换账户（mock 请求记录断言候选顺序）、
//     close_after_established 不换（恰好一次上游请求）；
//   - ephemeral token 生命周期：签发→连接→model 不符拒；
//   - 空闲超时（缩短设置键）与每 API Key 并发上限。
import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/realtimetoken"
	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// seedRealtimeCatalogRow 种 gpt-realtime 目录行（openai 聚合目标的 global
// 自定义行；audio token 价 $32/$64 per 1M，文本价留空——契约 §4.4 不编造）。
func seedRealtimeCatalogRow(t *testing.T, db *sql.DB) {
	t.Helper()
	now := "2026-10-04T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO custom_provider_models (
			id, provider_code, model, scope, system_account_id, status, catalog_visible, mode,
			supported_api_protocols_json, audio_input_usd_per_1m, audio_output_usd_per_1m,
			created_by, created_at, updated_at)
		VALUES ('cat_realtime_1', 'openai', 'gpt-realtime', 'global', NULL, 'active', 1, 'audio',
			'["realtime"]', 32, 64, ?, ?, ?)`, "sys_owner", now, now); err != nil {
		t.Fatalf("seed realtime catalog: %v", err)
	}
}

// seedRealtimeAccount 种一个 openai 族 realtime 测试账户（凭据带 base_url 与
// api_key；supported_endpoint_modes 显式含 realtime_session——候选过滤 opt-in
// 语义）。模型约束种 gpt-realtime。
func seedRealtimeAccount(t *testing.T, fixture *chainFixture, id, baseURL, apiKey string, priority int) {
	t.Helper()
	credentials := map[string]any{
		"api_key":  apiKey,
		"base_url": baseURL,
		"supported_endpoint_modes": []string{
			"chat_json", "chat_sse", "responses_json", "responses_sse", "realtime_session",
		},
	}
	if _, err := fixture.db.Exec(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'openai', 'prof_1', 'openai', 'v1', ?, 'api_key', 'active', 1, 0, ?, ?, NULL)`,
		id, fixture.systemAccount, id, priority, mustEncryptCredentials(t, credentials)); err != nil {
		t.Fatalf("seed realtime account: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES (?, ?, ?, 1, '2026-10-04T00:00:00.000Z')`, fixture.groupID, fixture.systemAccount, id); err != nil {
		t.Fatalf("seed realtime group binding: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
		VALUES (?, 'openai', 'gpt-realtime', '2026-10-04T00:00:00.000Z')`, id); err != nil {
		t.Fatalf("seed realtime supported model: %v", err)
	}
}

// realtimeBridgeCompose 组装带 miniredis token 服务与 realtime 目录行的链，
// 返回链、API Key 明文、spool 目录与 token 服务。
func realtimeBridgeCompose(t *testing.T, fixture *chainFixture, withRedis bool) (*gatewayChain, string, string, *realtimetoken.Service) {
	t.Helper()
	seedRealtimeCatalogRow(t, fixture.db)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	var tokens *realtimetoken.Service
	if withRedis {
		tokens = realtimetoken.NewService(realtimetoken.GoRedisClient{Client: client}, "")
	}
	chain, spoolDir, shutdown := mergeComposeChain(t, fixture, func(deps *chainRuntimeDeps) {
		deps.RealtimeTokens = tokens
	})
	t.Cleanup(shutdown)
	return chain, fixture.apiKeySecret, spoolDir, tokens
}

// realtimeDial 以 gorilla 客户端拨网关 WS（Bearer 形态；target 已含查询串）。
func realtimeDial(t *testing.T, serverURL, target, apiKey string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	if apiKey != "" {
		header.Set("Authorization", "Bearer "+apiKey)
	}
	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(serverURL, "http")+target, header)
	return conn, response, err
}

// realtimeUpstreamRequests 过滤 mock 记录中的 GET /v1/realtime 请求。
func realtimeUpstreamRequests(mock *platformmock.Server) []platformmock.Request {
	out := []platformmock.Request{}
	for _, request := range mock.Requests() {
		if request.Method == http.MethodGet && request.Path == "/v1/realtime" {
			out = append(out, request)
		}
	}
	return out
}

// TestChainRealtimeEchoRoundTripAndUsageTerminal 验收 1/4（设计 §8）：echo
// 会话帧内容逐字节等价（透传不重写）；usage 事件累计终态落库（audio token
// $32/$64 价），连接由上游 close_after 正常关闭。
func TestChainRealtimeEchoRoundTripAndUsageTerminal(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedRealtimeAccount(t, fixture, "acc_realtime_echo", mock.URL, "sk-realtime-echo", 0)
	chain, apiKey, spoolDir, _ := realtimeBridgeCompose(t, fixture, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	// usage_events=1,2：第 1/2 帧后各发一次 response.done（固定 usage：input
	// 100=20text+80audio、output 50=10text+40audio）；close_after=3：第 3 帧
	// 后上游 close 1000。
	conn, response, err := realtimeDial(t, server.URL, "/v1/realtime?model=gpt-realtime&scenario=media_realtime_echo&usage_events=1,2&close_after=3", apiKey)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("dial gateway: %v (status=%d)", err, status)
	}
	frames := []string{
		`{"type":"session.update","session":{"voice":"alloy"}}`,
		`{"type":"input_audio_buffer.append","audio":"aGVsbG8="}`,
		`{"type":"response.create"}`,
	}
	seen := []string{}
	usageEvents := 0
	closeErr := make(chan error, 1)
	go func() {
		for {
			messageType, payload, readErr := conn.ReadMessage()
			if readErr != nil {
				closeErr <- readErr
				return
			}
			if messageType != websocket.TextMessage {
				t.Errorf("帧类型 = %d, want 文本帧", messageType)
				continue
			}
			if strings.Contains(string(payload), `"response.done"`) {
				usageEvents++
				continue
			}
			seen = append(seen, string(payload))
		}
	}()
	for _, frame := range frames {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("write frame: %v", err)
		}
	}
	select {
	case err := <-closeErr:
		if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			t.Fatalf("会话收尾 = %v, want close 1000", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("等待上游 close 超时")
	}
	// 透传不重写：echo 帧与发送帧逐字节等价且保序。
	if len(seen) != len(frames) {
		t.Fatalf("echo 帧数 = %d, want %d（seen=%q）", len(seen), len(frames), seen)
	}
	for index, frame := range frames {
		if seen[index] != frame {
			t.Fatalf("echo 帧 %d 被改写:\n got %s\nwant %s", index, seen[index], frame)
		}
	}
	if usageEvents != 2 {
		t.Fatalf("usage 事件数 = %d, want 2", usageEvents)
	}
	// 出站断言：上游收到唯一一次升级请求，model 与 Bearer 账户凭据正确。
	upstreamRequests := realtimeUpstreamRequests(mock)
	if len(upstreamRequests) != 1 {
		t.Fatalf("上游 realtime 请求数 = %d, want 1", len(upstreamRequests))
	}
	if upstreamRequests[0].AuthHeader != "Bearer sk-realtime-echo" {
		t.Fatalf("上游认证头 = %q", upstreamRequests[0].AuthHeader)
	}
	if !strings.Contains(upstreamRequests[0].RawQuery, "model=gpt-realtime") {
		t.Fatalf("上游查询串缺 model: %q", upstreamRequests[0].RawQuery)
	}
	if strings.Contains(upstreamRequests[0].RawQuery, "token=") {
		t.Fatalf("网关认证参数泄漏上游: %q", upstreamRequests[0].RawQuery)
	}
	// 终态 usage 落库：2 次 usage 事件累计 input audio 160 / output audio 80
	//（$32/$64 per 1M → 160*32/1e6 + 80*64/1e6 = 0.01024）。
	records := waitForSpoolRecords(t, spoolDir)
	found := false
	for _, record := range records {
		if record["endpoint"] != "/v1/realtime" {
			continue
		}
		found = true
		if record["inputAudioTokens"] != float64(160) || record["outputAudioTokens"] != float64(80) {
			t.Fatalf("audio token 计量 = input %v / output %v, want 160/80", record["inputAudioTokens"], record["outputAudioTokens"])
		}
		if record["usageMissing"] == true {
			t.Fatalf("有 usage 事件不得标记 usage_missing: %v", record)
		}
		cost, _ := record["costUsd"].(float64)
		if cost < 0.01024-1e-9 || cost > 0.01024+1e-9 {
			t.Fatalf("costUsd = %v, want 0.01024（160*32/1M + 80*64/1M）", cost)
		}
		if record["inputTokens"] != nil {
			t.Fatalf("目录无文本价不得报 text token 维度: %v", record["inputTokens"])
		}
	}
	if !found {
		t.Fatalf("usage spool 未出现 /v1/realtime 终态记录（records=%d）", len(records))
	}
	// 连接位与并发槽释放（同 key 再连一次成功）。
	second, _, err := realtimeDial(t, server.URL, "/v1/realtime?model=gpt-realtime&scenario=media_realtime_close_after_established", apiKey)
	if err != nil {
		t.Fatalf("资源释放后二次连接失败: %v", err)
	}
	_ = second.Close()
}

// TestChainRealtimeRejectUpgradeSwitchesAccount 验收 2 受理边界臂 1（设计
// §8）：升级拒绝（403）= 受理前失败换账户——第一账户（priority 0）恒 403，
// 第二账户（priority 10）echo 承接；mock 请求记录断言候选顺序（各恰好一次）。
func TestChainRealtimeRejectUpgradeSwitchesAccount(t *testing.T) {
	fixture := newChainFixture(t)
	mockOK := platformmock.New()
	defer mockOK.Close()
	rejectInner := platformmock.New()
	defer rejectInner.Close()
	// 包装上游恒注入 reject_upgrade 场景（受理前 403；X-Mock-Scenario 头在
	// 包装层注入，不依赖查询参数）。
	rejectMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Mock-Scenario", string(platformmock.ScenarioMediaRealtimeRejectUpgrade))
		rejectInner.Config.Handler.ServeHTTP(w, r)
	}))
	defer rejectMock.Close()
	seedRealtimeAccount(t, fixture, "acc_realtime_reject", rejectMock.URL, "sk-realtime-reject", 0)
	seedRealtimeAccount(t, fixture, "acc_realtime_ok", mockOK.URL, "sk-realtime-ok", 10)
	chain, apiKey, _, _ := realtimeBridgeCompose(t, fixture, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	conn, response, err := realtimeDial(t, server.URL,
		"/v1/realtime?model=gpt-realtime&scenario=media_realtime_echo&close_after=1", apiKey)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("换账户后拨号失败: %v (status=%d)", err, status)
	}
	// 发一帧触发 close_after=1 收尾。
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, readErr := conn.ReadMessage(); readErr != nil {
			break
		}
	}
	_ = conn.Close()
	// 受理边界证据：第一账户恰好一次被拒升级，第二账户恰好一次成功升级。
	rejected := realtimeUpstreamRequests(rejectInner)
	if len(rejected) != 1 || rejected[0].AuthHeader != "Bearer sk-realtime-reject" {
		t.Fatalf("被拒账户请求数/认证头错误: %+v", rejected)
	}
	okRequests := realtimeUpstreamRequests(mockOK)
	if len(okRequests) != 1 || okRequests[0].AuthHeader != "Bearer sk-realtime-ok" {
		t.Fatalf("承接账户请求数/认证头错误: %+v", okRequests)
	}
}

// TestChainRealtimeCloseAfterEstablishedNoSwitch 验收 2 受理边界臂 2（设计
// §8）：上游 101 升级成功后立即断开 = 受理后断开——不换账户（恰好一次上
// 游请求），客户端收到 close。
func TestChainRealtimeCloseAfterEstablishedNoSwitch(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedRealtimeAccount(t, fixture, "acc_realtime_close", mock.URL, "sk-realtime-close", 0)
	// 第二账户承接探测器：若错误换账户会在这里出现第二次请求。
	seedRealtimeAccount(t, fixture, "acc_realtime_close_ok", mock.URL, "sk-realtime-close-ok", 10)
	chain, apiKey, _, _ := realtimeBridgeCompose(t, fixture, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	conn, _, err := realtimeDial(t, server.URL,
		"/v1/realtime?model=gpt-realtime&scenario=media_realtime_close_after_established", apiKey)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, _, readErr := conn.ReadMessage()
	if readErr == nil {
		t.Fatal("受理后上游断开必须传递到客户端")
	}
	if !websocket.IsCloseError(readErr, websocket.CloseNormalClosure) {
		t.Fatalf("客户端收尾 = %v, want close 1000", readErr)
	}
	_ = conn.Close()
	// 不换账户：恰好一次升级请求（且属第一账户——认证头区分）。
	requests := realtimeUpstreamRequests(mock)
	if len(requests) != 1 || requests[0].AuthHeader != "Bearer sk-realtime-close" {
		t.Fatalf("受理后断开不得换账户: %+v", requests)
	}
}

// TestChainRealtimeTokenLifecycle 验收 3（设计 §8）：ephemeral token 签发 →
// ?token= 连接（无 Bearer）→ model 不符拒（403）→ 无效 token 拒（401）。
func TestChainRealtimeTokenLifecycle(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedRealtimeAccount(t, fixture, "acc_realtime_token", mock.URL, "sk-realtime-token", 0)
	chain, apiKey, _, _ := realtimeBridgeCompose(t, fixture, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	// 签发（Bearer）。
	issue, issuePayload := realtimeSecretsRequest(t, server, http.MethodPost, "/v1/realtime/client_secrets", apiKey, `{"model":"gpt-realtime"}`)
	if issue.StatusCode != http.StatusOK {
		t.Fatalf("签发 status=%d body=%s", issue.StatusCode, issuePayload)
	}
	var issued struct {
		Value     string `json:"value"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(issuePayload), &issued); err != nil {
		t.Fatalf("decode issued: %v", err)
	}
	// ?token= 连接（无 Bearer；idle 场景承接后由测试侧主动关闭）。
	conn, _, err := realtimeDial(t, server.URL,
		"/v1/realtime?model=gpt-realtime&scenario=media_realtime_idle&token="+issued.Value, "")
	if err != nil {
		t.Fatalf("token 连接失败: %v", err)
	}
	_ = conn.Close()
	// TTL 内允许再次连接（浏览器重连场景，设计 §4）。
	conn2, _, err := realtimeDial(t, server.URL,
		"/v1/realtime?model=gpt-realtime&scenario=media_realtime_idle&token="+issued.Value, "")
	if err != nil {
		t.Fatalf("token 二次连接失败: %v", err)
	}
	_ = conn2.Close()
	// model 不符拒（不记名复用防护）：直接对同一 token 以另一 model 连接。
	_, response, err := realtimeDial(t, server.URL,
		"/v1/realtime?model=gpt-realtime-mini&scenario=media_realtime_idle&token="+issued.Value, "")
	if err == nil {
		t.Fatal("model 不符必须拒绝")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("model 不符状态 = %v, want 403", response)
	}
	// 过期失效：TTL 收紧不可行（固定 120s），以不存在的 token 断言同型拒绝。
	_, response, err = realtimeDial(t, server.URL,
		"/v1/realtime?model=gpt-realtime&scenario=media_realtime_idle&token=definitely-not-a-real-token", "")
	if err == nil {
		t.Fatal("无效 token 必须拒绝")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无效 token 状态 = %v, want 401", response)
	}
	// 无认证（既无 Bearer 也无 token）拒。
	_, response, err = realtimeDial(t, server.URL, "/v1/realtime?model=gpt-realtime", "")
	if err == nil {
		t.Fatal("无认证必须拒绝")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无认证状态 = %v, want 401", response)
	}
}

// TestChainRealtimeIdleTimeout 验收 6（设计 §8）：空闲超时（设置键缩短到
// 最小值 10s——settings 值域下限）双向无帧断开；空会话终态 0 计费 +
// usage_missing（验收 4 空会话臂）。
func TestChainRealtimeIdleTimeout(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedRealtimeAccount(t, fixture, "acc_realtime_idle", mock.URL, "sk-realtime-idle", 0)
	// 空闲超时缩到值域下限 10s（settings 校验与 runtimecache 投影同源下限）。
	if _, err := fixture.db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'realtimeIdleTimeoutSeconds', '10', '2026-10-04T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed idle timeout: %v", err)
	}
	chain, apiKey, spoolDir, _ := realtimeBridgeCompose(t, fixture, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	started := time.Now()
	conn, _, err := realtimeDial(t, server.URL,
		"/v1/realtime?model=gpt-realtime&scenario=media_realtime_idle", apiKey)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, _, readErr := conn.ReadMessage()
	elapsed := time.Since(started)
	if readErr == nil {
		t.Fatal("空闲会话必须被服务端断开")
	}
	if elapsed < 9*time.Second || elapsed > 20*time.Second {
		t.Fatalf("空闲断开时距 = %v, want ~10s（ping 20s 间隔不得先于空闲触发）", elapsed)
	}
	// 空会话终态：0 计费 + usage_missing（无 usage 事件）。
	deadline := time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		for _, record := range waitForSpoolRecordsSilent(spoolDir) {
			if record["endpoint"] == "/v1/realtime" {
				found = true
				if record["usageMissing"] != true {
					t.Fatalf("空会话必须 usage_missing: %v", record)
				}
				if record["inputAudioTokens"] != nil || record["costUsd"] != nil {
					t.Fatalf("空会话不得计费: %v", record)
				}
			}
		}
		if !found {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if !found {
		t.Fatalf("空会话终态 usage 未落库: %v", waitForSpoolRecordsSilent(spoolDir))
	}
}

// waitForSpoolRecordsSilent 是 waitForSpoolRecords 的非致命轮询版（超时返
// 回已见记录）。
func waitForSpoolRecordsSilent(spoolDir string) []map[string]any {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if records := readSpoolRecordsOnce(spoolDir); len(records) > 0 {
			return records
		}
		time.Sleep(25 * time.Millisecond)
	}
	return readSpoolRecordsOnce(spoolDir)
}

// readSpoolRecordsOnce 读一遍 spool 目录的记录文件（缺目录返回空）。
func readSpoolRecordsOnce(spoolDir string) []map[string]any {
	instances, err := os.ReadDir(spoolDir)
	if err != nil {
		return nil
	}
	records := []map[string]any{}
	for _, instance := range instances {
		if !instance.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(spoolDir, instance.Name()))
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(spoolDir, instance.Name(), file.Name()))
			if err != nil {
				continue
			}
			var record map[string]any
			if json.Unmarshal(raw, &record) == nil && len(record) > 0 {
				records = append(records, record)
			}
		}
	}
	return records
}

// TestChainRealtimeConnectionLimit 验收 6（设计 §8）：每 API Key 并发连接上
// 限（设置键 1）——占用中二次连接 429；释放后可再连。
func TestChainRealtimeConnectionLimit(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedRealtimeAccount(t, fixture, "acc_realtime_limit", mock.URL, "sk-realtime-limit", 0)
	if _, err := fixture.db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'realtimeMaxConnectionsPerApiKey', '1', '2026-10-04T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed connection limit: %v", err)
	}
	chain, apiKey, _, _ := realtimeBridgeCompose(t, fixture, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	first, _, err := realtimeDial(t, server.URL,
		"/v1/realtime?model=gpt-realtime&scenario=media_realtime_idle", apiKey)
	if err != nil {
		t.Fatalf("首连失败: %v", err)
	}
	_, response, err := realtimeDial(t, server.URL,
		"/v1/realtime?model=gpt-realtime&scenario=media_realtime_idle", apiKey)
	if err == nil {
		t.Fatal("超限连接必须拒绝")
	}
	if response == nil || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("超限状态 = %v, want 429", response)
	}
	// 释放后可再连（连接位回收）。
	_ = first.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if conn, _, dialErr := realtimeDial(t, server.URL,
			"/v1/realtime?model=gpt-realtime&scenario=media_realtime_idle", apiKey); dialErr == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("连接位释放后仍无法连接")
}

// TestChainRealtimeUpstreamIdleTimeout 上游侧空闲超时记账（终审 m-2）：
// 客户端持续发帧续期客户端侧 deadline，上游静默（idle 场景不回帧）→ 上游
// 读超时与客户端侧对称记 idle_timeout（close 1001），不落 upstream_closed。
func TestChainRealtimeUpstreamIdleTimeout(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedRealtimeAccount(t, fixture, "acc_realtime_upidle", mock.URL, "sk-realtime-upidle", 0)
	if _, err := fixture.db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'realtimeIdleTimeoutSeconds', '10', '2026-10-04T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed idle timeout: %v", err)
	}
	chain, apiKey, spoolDir, _ := realtimeBridgeCompose(t, fixture, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	started := time.Now()
	conn, _, err := realtimeDial(t, server.URL,
		"/v1/realtime?model=gpt-realtime&scenario=media_realtime_idle", apiKey)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// 后台循环每 3s 发一帧：客户端侧 deadline 持续续期，上游侧（无回帧）
	// 10s 后读超时。读侧等待服务端 close。
	writeDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"input_audio_buffer.append","audio":"aGVsbG8="}`)); err != nil {
					writeDone <- err
					return
				}
			case <-writeDone:
				return
			}
		}
	}()
	_, _, readErr := conn.ReadMessage()
	elapsed := time.Since(started)
	close(writeDone)
	_ = conn.Close()
	if readErr == nil {
		t.Fatal("上游静默必须触发空闲断开")
	}
	if !websocket.IsCloseError(readErr, websocket.CloseGoingAway) {
		t.Fatalf("客户端收尾 = %v, want close 1001 (GoingAway)", readErr)
	}
	if elapsed < 9*time.Second || elapsed > 20*time.Second {
		t.Fatalf("上游空闲断开时距 = %v, want ~10s", elapsed)
	}
	// 终态落账：closeReason=idle_timeout（不得 upstream_closed）。
	deadline := time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		for _, record := range waitForSpoolRecordsSilent(spoolDir) {
			if record["endpoint"] != "/v1/realtime" {
				continue
			}
			found = true
			snapshot, _ := record["requestSnapshot"].(map[string]any)
			if snapshot["closeReason"] != "idle_timeout" {
				t.Fatalf("上游读超时 closeReason = %v, want idle_timeout", snapshot["closeReason"])
			}
		}
		if !found {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if !found {
		t.Fatalf("终态 usage 未落库: %v", waitForSpoolRecordsSilent(spoolDir))
	}
}

// TestChainRealtimeMaxSessionDuration 最大会话时长（终审 m-3，设计 §3 timer
// 路径）：设置键缩到值域下限 60s（min 60，2s 不可注入），echo 场景客户端静
// 默；idle 默认 120s 不竞争，60s 后服务端 close 1000 且终态 closeReason=
// max_session。
func TestChainRealtimeMaxSessionDuration(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedRealtimeAccount(t, fixture, "acc_realtime_maxdur", mock.URL, "sk-realtime-maxdur", 0)
	if _, err := fixture.db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'realtimeMaxSessionSeconds', '60', '2026-10-04T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed max session: %v", err)
	}
	chain, apiKey, spoolDir, _ := realtimeBridgeCompose(t, fixture, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	started := time.Now()
	conn, _, err := realtimeDial(t, server.URL,
		"/v1/realtime?model=gpt-realtime&scenario=media_realtime_echo", apiKey)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// 客户端静默：echo 场景无请求帧则上游无回帧，等待服务端 timer close。
	_, _, readErr := conn.ReadMessage()
	elapsed := time.Since(started)
	_ = conn.Close()
	if readErr == nil {
		t.Fatal("最大会话时长到期必须由服务端 close")
	}
	if !websocket.IsCloseError(readErr, websocket.CloseNormalClosure) {
		t.Fatalf("客户端收尾 = %v, want close 1000", readErr)
	}
	if elapsed < 58*time.Second || elapsed > 75*time.Second {
		t.Fatalf("最大会话断开时距 = %v, want ~60s（值域下限）", elapsed)
	}
	// 终态落账：closeReason=max_session。
	deadline := time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		for _, record := range waitForSpoolRecordsSilent(spoolDir) {
			if record["endpoint"] != "/v1/realtime" {
				continue
			}
			found = true
			snapshot, _ := record["requestSnapshot"].(map[string]any)
			if snapshot["closeReason"] != "max_session" {
				t.Fatalf("最大会话 closeReason = %v, want max_session", snapshot["closeReason"])
			}
		}
		if !found {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if !found {
		t.Fatalf("终态 usage 未落库: %v", waitForSpoolRecordsSilent(spoolDir))
	}
}

// TestChainRealtimeModelNotInCatalog 模型解析边界：非 realtime 目录模型 400
//（不进派发链）。
func TestChainRealtimeModelNotInCatalog(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedRealtimeAccount(t, fixture, "acc_realtime_catalog", mock.URL, "sk-realtime-catalog", 0)
	chain, apiKey, _, _ := realtimeBridgeCompose(t, fixture, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	_, response, err := realtimeDial(t, server.URL, "/v1/realtime?model=gpt-4.1", apiKey)
	if err == nil {
		t.Fatal("非 realtime 模型必须拒绝")
	}
	if response == nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("非 realtime 模型状态 = %v, want 400", response)
	}
	// 缺 model 同样 400（上游契约镜像）。
	_, response, err = realtimeDial(t, server.URL, "/v1/realtime", apiKey)
	if err == nil {
		t.Fatal("缺 model 必须拒绝")
	}
	if response == nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 model 状态 = %v, want 400", response)
	}
}

// TestChainRealtimeNoCandidateAccountsExhausted 候选耗尽 503：唯一账户不持
// realtime_session 端点模式（opt-in 过滤剔除）。
func TestChainRealtimeNoCandidateAccountsExhausted(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	// 复用 media 视频账户种子形态（无 realtime_session 模式）。
	seedMediaVideoAccount(t, fixture.db, fixture, "acc_realtime_nosession", mock.URL, "sk-nosession", 0, true)
	chain, apiKey, _, _ := realtimeBridgeCompose(t, fixture, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	_, response, err := realtimeDial(t, server.URL, "/v1/realtime?model=gpt-realtime", apiKey)
	if err == nil {
		t.Fatal("无 realtime_session 候选必须拒绝")
	}
	if response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("候选耗尽状态 = %v, want 503", response)
	}
}
