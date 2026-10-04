package acceptance

// M5b2 realtime 全链路验收（Realtime 设计 §8）：真 WS 客户端（gorilla）经
// 真实 gateway 二进制到 mockupstream WS 上游的全流程——
//
//   - Bearer 会话：受理边界臂 1（首账户升级拒绝换第二账户）+ 事件往返逐
//     字节（透传不重写）+ 上游 Bearer 账户凭据 + usage 终态落库（audio
//     token $32/$64 价，经 spool → jobs → usage-records 真实交接链）；
//   - 受理边界臂 2（close_after_established）：受理后上游断开不换账户（恰
//     好一次升级请求），客户端收到 close；空会话终态 0 计费 + usage_missing；
//   - 无 redis 运行态驱动（standalone 门禁）时 client_secrets 显式 503 降
//     级契约。
//
// 夹具注意：WS 升级不能经 fullchainMockUpstream 的 HTTP 场景代理（无 hijack
// 隧道能力），realtime 账户 base_url 直指 mockupstream 引擎；场景经 ?scenario=
// 查询参数由网关透传驱动，首账户（恒拒绝）经本测试自起的注入代理指向同一
// 引擎。ephemeral token 生命周期经链级测试覆盖（miniredis 真 redis 协议）——
// standalone 形态的热质量门禁强制 memory 运行态驱动（gatewayhotquality/
// runtime.go），token 面在隔离 gateway 二进制内不可装配。
import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// realtimeAccountExtra 是 realtime 验收账户相对 createAccount 默认值的覆盖
// 项：支持模型声明 gpt-realtime（maintenance 种子目录行）+ 凭据显式声明
// realtime_session 端点模式（候选过滤 opt-in）+ base_url 直指引擎。
func realtimeAccountExtra(baseURL string) map[string]any {
	return map[string]any{
		"supportedModels":  []string{"gpt-realtime"},
		"healthCheckModel": "gpt-realtime",
		"credentials": map[string]any{
			"base_url": baseURL,
			"supported_endpoint_modes": []string{
				"chat_json", "chat_sse", "responses_json", "responses_sse", "realtime_session",
			},
		},
	}
}

// realtimeDial 经真实网关拨 WS（Bearer 认证）。
func realtimeDial(t *testing.T, baseURL, target, apiKey string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	if apiKey != "" {
		header.Set("Authorization", "Bearer "+apiKey)
	}
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(baseURL, "http")+target, header)
}

// realtimeUsageRows 按 API Key 拉取 usage-records 读模型的 realtime 行（异
// 步交接链，轮询由调用方驱动；原始字段面供计量断言）。
func realtimeUsageRows(f *fullchainFixture, apiKeyID string) []map[string]any {
	_, payload := f.admin.do(http.MethodGet,
		"/__aisys__/api/usage-records?systemAccountId=sys_admin&page=1&pageSize=200", nil, wantStatus(http.StatusOK))
	items, _ := data(payload)["items"].([]any)
	out := []map[string]any{}
	for _, raw := range items {
		if item, _ := raw.(map[string]any); item != nil &&
			str(item["apiKeyId"]) == apiKeyID && str(item["endpoint"]) == "/v1/realtime" {
			out = append(out, item)
		}
	}
	return out
}

// realtimeUpstreamUpgrades 统计 mock 引擎收到的 GET /v1/realtime 升级请求。
func realtimeUpstreamUpgrades(mock *platformmock.Server) []platformmock.Request {
	out := []platformmock.Request{}
	for _, request := range mock.Requests() {
		if request.Method == http.MethodGet && request.Path == "/v1/realtime" {
			out = append(out, request)
		}
	}
	return out
}

// TestFullchainMediaRealtimeSession 验证 M5b realtime WS 全链路（Realtime
// 设计 §8）：受理边界两臂 + 事件往返逐字节 + usage 终态落库金额 + 无 redis
// 降级契约。
func TestFullchainMediaRealtimeSession(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	// 首账户恒拒绝升级（受理前 403，注入代理改写场景头——引擎按头选场景，
	// 与查询参数通道独立）；第二账户直连引擎承接。
	rejectProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Mock-Scenario", string(platformmock.ScenarioMediaRealtimeRejectUpgrade))
		f.mock.mock.Config.Handler.ServeHTTP(w, r)
	}))
	defer rejectProxy.Close()
	route := f.newRoute("RT", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			{extra: realtimeAccountExtra(rejectProxy.URL)},
			{extra: realtimeAccountExtra(f.mock.mock.URL)},
		}},
	}, nil, nil)

	// ---- Bearer 会话：受理边界臂 1 + echo 往返 + usage 终态 ----
	// usage_events=1,2：第 1/2 帧后各发一次 response.done（固定 usage：input
	// 100=20text+80audio、output 50=10text+40audio）；close_after=3：第 3 帧
	// 后上游 close 1000。
	conn, response, err := realtimeDial(t, f.gw.baseURL,
		"/v1/realtime?model=gpt-realtime&scenario=media_realtime_echo&usage_events=1,2&close_after=3", route.apiKey)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("realtime 会话拨号失败: %v (status=%d)", err, status)
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
			_, payload, readErr := conn.ReadMessage()
			if readErr != nil {
				closeErr <- readErr
				return
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
			t.Fatalf("realtime 帧发送失败: %v", err)
		}
	}
	select {
	case err := <-closeErr:
		if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			t.Fatalf("realtime 会话收尾 = %v, want close 1000", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("等待上游 close 超时")
	}
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

	// ---- 受理边界臂 2：close_after_established 不换账户（空会话） ----
	established, _, err := realtimeDial(t, f.gw.baseURL,
		"/v1/realtime?model=gpt-realtime&scenario=media_realtime_close_after_established", route.apiKey)
	if err != nil {
		t.Fatalf("受理后断开用例拨号失败: %v", err)
	}
	if _, _, readErr := established.ReadMessage(); readErr == nil {
		t.Fatal("受理后上游断开必须传递到客户端")
	} else if !websocket.IsCloseError(readErr, websocket.CloseNormalClosure) {
		t.Fatalf("受理后断开收尾 = %v, want close 1000", readErr)
	}
	_ = established.Close()

	// ---- 出站断言：每个会话都从首候选起派发（priority 序）——首账户各被拒
	// 一次后由第二账户承接（受理边界臂 1 证据：拒绝先于承接），携带账户
	// Bearer 凭据，无认证参数泄漏。----
	upgrades := realtimeUpstreamUpgrades(f.mock.mock)
	if len(upgrades) != 4 {
		t.Fatalf("上游升级请求数 = %d, want 4（两会话各：首账户拒绝 1 + 第二账户承接 1）", len(upgrades))
	}
	for index, wantKey := range []int{0, 1, 0, 1} {
		if upgrades[index].AuthHeader != "Bearer "+route.upstreamKeys[wantKey] {
			t.Fatalf("升级 #%d 认证头 = %q, want 账户 %d 凭据（候选顺序断言）", index, upgrades[index].AuthHeader, wantKey+1)
		}
	}
	for _, request := range upgrades {
		if !strings.Contains(request.RawQuery, "model=gpt-realtime") {
			t.Fatalf("上游查询串缺 model: %q", request.RawQuery)
		}
		if strings.Contains(request.RawQuery, "token=") {
			t.Fatalf("网关认证参数泄漏上游: %q", request.RawQuery)
		}
	}

	// ---- usage 终态：audio token 计量与金额经真实交接链落 usage-records ----
	//（160 input audio * $32/1M + 80 output audio * $64/1M = $0.01024；空会话
	// 终态 0 计费——usage_missing 标记不在 usage-records 读模型字段面，链级
	// spool 断言覆盖标记位，这里以"第二行无计量无金额"锚定空会话行。）
	deadline := time.Now().Add(30 * time.Second)
	var meteredRow, emptyRow map[string]any
	for time.Now().Before(deadline) && (meteredRow == nil || emptyRow == nil) {
		for _, row := range realtimeUsageRows(f, route.apiKeyID) {
			if realtimeInt64(row["inputAudioTokens"]) > 0 {
				meteredRow = row
			} else {
				emptyRow = row
			}
		}
		if meteredRow == nil || emptyRow == nil {
			time.Sleep(300 * time.Millisecond)
		}
	}
	if meteredRow == nil || emptyRow == nil {
		t.Fatalf("realtime usage 终态行未齐（metered=%#v empty=%#v；rows=%#v）",
			meteredRow, emptyRow, realtimeUsageRows(f, route.apiKeyID))
	}
	if got := realtimeInt64(meteredRow["inputAudioTokens"]); got != 160 {
		t.Fatalf("inputAudioTokens = %d, want 160", got)
	}
	if got := realtimeInt64(meteredRow["outputAudioTokens"]); got != 80 {
		t.Fatalf("outputAudioTokens = %d, want 80", got)
	}
	cost, _ := meteredRow["costUsd"].(float64)
	if cost < 0.01024-1e-9 || cost > 0.01024+1e-9 {
		t.Fatalf("costUsd = %v, want 0.01024（160*32/1M + 80*64/1M）", cost)
	}
	if meteredRow["model"] != "gpt-realtime" {
		t.Fatalf("usage 行模型 = %v", meteredRow["model"])
	}

	// ---- 无 redis 运行态驱动的降级契约：client_secrets 显式 503 ----
	//（standalone 热质量门禁强制 memory 驱动，token 面不可装配；ephemeral
	// token 生命周期由链级 miniredis 测试覆盖。）
	secrets := f.doRawT(t, realtimeJSONRequest(t, f.gw.baseURL, http.MethodPost,
		"/v1/realtime/client_secrets", route.apiKey, `{"model":"gpt-realtime"}`))
	if secrets.Status != http.StatusServiceUnavailable || !strings.Contains(secrets.Body, "service_unavailable") {
		t.Fatalf("client_secrets 无 redis 降级 = %d %s, want 503 service_unavailable", secrets.Status, secrets.Body)
	}
}

// realtimeJSONRequest 构造 /v1 JSON 请求。
func realtimeJSONRequest(t *testing.T, baseURL, method, path, apiKey, body string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, baseURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	return request
}

// realtimeInt64 解析 JSON 数值字段（缺失为 0）。
func realtimeInt64(value any) int64 {
	if number, ok := value.(float64); ok {
		return int64(number)
	}
	return 0
}
