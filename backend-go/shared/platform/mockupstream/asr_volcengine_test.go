package mockupstream

// M3f volcengine (SeedASR AUC, 录音文件识别 2.0) ASR face tests, per contract
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension)
// and §9.2 (seedasr header-driven wire contract, adjudicated 2026-10-17).
// Covered: the header-driven submit acceptance shape (200 + empty body {}
// + X-Api-Status-Code/X-Api-Message headers), the four rejection shapes
// (missing X-Api-Key / resource not granted / missing X-Api-Request-Id /
// missing audio.url), the per-scenario query scripts (create_ok running →
// succeeded with the pinned duration:1118 payload, poll_running stuck,
// poll_succeeded_text fast path, poll_failed terminal), the task-table
// miss, the Request-side header recording, and the method/path whitelist.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

const (
	volcengineASRTestAPIKey       = "mock-volc-asr-key"
	volcengineASRTestRequestID    = "mock-req-volc-asr-0001"
	volcengineASRTestAudioURL     = "https://mock.invalid/audio/carry-oily-rag.wav"
	volcengineASRSubmitBodySample = `{"user":{"uid":"mock-user"},"audio":{"url":"https://mock.invalid/audio/carry-oily-rag.wav","format":"wav"},"request":{"model_name":"bigmodel"}}`
)

// parsedVolcengineASRBody covers both §9.2 body families: the header
// envelope (450000xx faces) and the flat {"code","message"} form (401).
type parsedVolcengineASRBody struct {
	Header *struct {
		ReqID   string `json:"reqid"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"header"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// parsedVolcengineASRSucceeded mirrors the pinned succeeded payload fields
// the gateway metering/text arms consume (§9.2): audio_info.duration in
// MILLISECONDS, result.additions.duration as a string, utterances[].words.
type parsedVolcengineASRSucceeded struct {
	AudioInfo struct {
		Duration int `json:"duration"`
	} `json:"audio_info"`
	Result struct {
		Additions struct {
			Duration string `json:"duration"`
		} `json:"additions"`
		Text       string `json:"text"`
		Utterances []struct {
			Text      string `json:"text"`
			StartTime int    `json:"start_time"`
			EndTime   int    `json:"end_time"`
			Words     []struct {
				Text      string `json:"text"`
				StartTime int    `json:"start_time"`
				EndTime   int    `json:"end_time"`
			} `json:"words"`
		} `json:"utterances"`
	} `json:"result"`
}

// doVolcengineASR posts one face request with the explicit header trio
// (X-Api-Key / X-Api-Resource-Id / X-Api-Request-Id): "" omits the header
// so the missing-header rejection shapes stay reachable. scenario "" omits
// the X-Mock-Scenario header.
func doVolcengineASR(t *testing.T, m *Server, path, apiKey, resourceID, requestID, scenario string, body []byte) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, m.Server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if apiKey != "" {
		req.Header.Set("X-Api-Key", apiKey)
	}
	if resourceID != "" {
		req.Header.Set("X-Api-Resource-Id", resourceID)
	}
	if requestID != "" {
		req.Header.Set("X-Api-Request-Id", requestID)
	}
	if scenario != "" {
		req.Header.Set("X-Mock-Scenario", scenario)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

// submitVolcengineASR posts a fully-valid create under the scenario.
func submitVolcengineASR(t *testing.T, m *Server, scenario string) (int, http.Header, []byte) {
	t.Helper()
	return doVolcengineASR(t, m, volcengineASRSubmitPath, volcengineASRTestAPIKey, volcengineASRResourceID, volcengineASRTestRequestID, scenario, []byte(volcengineASRSubmitBodySample))
}

// queryVolcengineASR polls the task with an empty {} body (the real query
// form) and NO scenario header: poll behavior is decided by the creating
// request's frozen script.
func queryVolcengineASR(t *testing.T, m *Server) (int, http.Header, []byte) {
	t.Helper()
	return doVolcengineASR(t, m, volcengineASRQueryPath, volcengineASRTestAPIKey, volcengineASRResourceID, volcengineASRTestRequestID, "", []byte("{}"))
}

// assertVolcengineASREnvelope asserts one header-envelope rejection: the
// X-Api-Status-Code/X-Api-Message paired response headers plus the
// header.code/message/reqid envelope body (wantReqID "" = the envelope
// body carries no reqid field, per the pinned 45000001 forms).
func assertVolcengineASREnvelope(t *testing.T, raw []byte, header http.Header, wantCode int, wantMessage, wantReqID string) {
	t.Helper()
	var parsed parsedVolcengineASRBody
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("envelope body: %v (%s)", err, raw)
	}
	if parsed.Header == nil {
		t.Fatalf("envelope body = %s, want header 信封", raw)
	}
	if parsed.Header.Code != wantCode || parsed.Header.Message != wantMessage {
		t.Fatalf("envelope = %s, want code %d/message %q", raw, wantCode, wantMessage)
	}
	if parsed.Header.ReqID != wantReqID {
		t.Fatalf("envelope reqid = %q, want %q (%s)", parsed.Header.ReqID, wantReqID, raw)
	}
	if got := header.Get("X-Api-Status-Code"); got != strconv.Itoa(wantCode) {
		t.Fatalf("X-Api-Status-Code = %q, want %q", got, strconv.Itoa(wantCode))
	}
	if got := header.Get("X-Api-Message"); got != wantMessage {
		t.Fatalf("X-Api-Message = %q, want %q", got, wantMessage)
	}
}

// TestMediaVolcengineASRSubmitAcceptance pins the header-driven acceptance
// shape (contract §9.2): HTTP 200 + EMPTY body {} + X-Api-Status-Code:
// 20000000 + X-Api-Message: OK, the task registered under the caller-
// defined X-Api-Request-Id with the submitted audio.url/format, and the
// three outbound headers recorded on the Request (批次 D/E 断言点).
func TestMediaVolcengineASRSubmitAcceptance(t *testing.T) {
	m := New()
	defer m.Close()

	code, header, raw := submitVolcengineASR(t, m, string(ScenarioMediaVolcengineASRCreateOK))
	if code != http.StatusOK {
		t.Fatalf("volcengine asr submit status = %d (%s), want 200", code, raw)
	}
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "{}" {
		t.Fatalf("volcengine asr submit body = %s, want 空对象 {}", trimmed)
	}
	if got := header.Get("X-Api-Status-Code"); got != volcengineASRStatusOK {
		t.Fatalf("volcengine asr submit X-Api-Status-Code = %q, want %q", got, volcengineASRStatusOK)
	}
	if got := header.Get("X-Api-Message"); got != "OK" {
		t.Fatalf("volcengine asr submit X-Api-Message = %q, want OK", got)
	}

	m.mu.Lock()
	task, ok := m.volcengineASRTasks[volcengineASRTestRequestID]
	m.mu.Unlock()
	if !ok || task == nil {
		t.Fatalf("volcengine asr task %q 未入任务表（受理凭据 = X-Api-Request-Id）", volcengineASRTestRequestID)
	}
	if task.audioURL != volcengineASRTestAudioURL || task.format != "wav" {
		t.Fatalf("volcengine asr task audio.url/format 未记录: %+v", task)
	}
	if task.scenario != volcengineASRScriptFor(ScenarioMediaVolcengineASRCreateOK) {
		t.Fatalf("volcengine asr task script = %q, want %q", task.scenario, ScenarioMediaVolcengineASRCreateOK)
	}

	recorded := m.Requests()
	if len(recorded) != 1 {
		t.Fatalf("recorded requests = %d, want 1", len(recorded))
	}
	req := recorded[0]
	if req.APIKeyHeader != volcengineASRTestAPIKey {
		t.Fatalf("recorded X-Api-Key = %q, want %q", req.APIKeyHeader, volcengineASRTestAPIKey)
	}
	if req.ResourceIDHeader != volcengineASRResourceID {
		t.Fatalf("recorded X-Api-Resource-Id = %q, want %q", req.ResourceIDHeader, volcengineASRResourceID)
	}
	if req.RequestIDHeader != volcengineASRTestRequestID {
		t.Fatalf("recorded X-Api-Request-Id = %q, want %q", req.RequestIDHeader, volcengineASRTestRequestID)
	}
}

// TestMediaVolcengineASRSubmitRejections pins the four submit rejection
// shapes (contract §9.2) and that none of them registers a task.
func TestMediaVolcengineASRSubmitRejections(t *testing.T) {
	m := New()
	defer m.Close()

	// 缺 X-Api-Key：401 平铺 {"code":45000010}（TTS mock 同族先例）。
	code, _, raw := doVolcengineASR(t, m, volcengineASRSubmitPath, "", volcengineASRResourceID, volcengineASRTestRequestID, "", []byte(volcengineASRSubmitBodySample))
	if code != http.StatusUnauthorized {
		t.Fatalf("missing X-Api-Key status = %d (%s), want 401", code, raw)
	}
	var flat parsedVolcengineASRBody
	if err := json.Unmarshal(raw, &flat); err != nil {
		t.Fatalf("missing X-Api-Key body: %v (%s)", err, raw)
	}
	if flat.Header != nil {
		t.Fatalf("missing X-Api-Key body = %s, want 平铺形态（无 header 信封）", raw)
	}
	if flat.Code != 45000010 || flat.Message != "Invalid X-Api-Key" {
		t.Fatalf("missing X-Api-Key body = %s, want 平铺 45000010/Invalid X-Api-Key", raw)
	}

	// 资源未授权（1.0 系资源 ID）：HTTP 200 + 45000030 信封，reqid 回显。
	code, header, raw := doVolcengineASR(t, m, volcengineASRSubmitPath, volcengineASRTestAPIKey, "volc.bigasr.auc", volcengineASRTestRequestID, "", []byte(volcengineASRSubmitBodySample))
	if code != http.StatusOK {
		t.Fatalf("wrong resource id status = %d (%s), want 200", code, raw)
	}
	assertVolcengineASREnvelope(t, raw, header, 45000030, "[resource_id=volc.bigasr.auc] requested resource not granted", volcengineASRTestRequestID)

	// 缺 X-Api-Request-Id：45000001 信封（无 reqid）。
	code, header, raw = doVolcengineASR(t, m, volcengineASRSubmitPath, volcengineASRTestAPIKey, volcengineASRResourceID, "", "", []byte(volcengineASRSubmitBodySample))
	if code != http.StatusOK {
		t.Fatalf("missing X-Api-Request-Id status = %d (%s), want 200", code, raw)
	}
	assertVolcengineASREnvelope(t, raw, header, 45000001, "X-Api-Request-Id is required", "")

	// 缺 audio.url：45000001 信封（无 reqid）。
	code, header, raw = doVolcengineASR(t, m, volcengineASRSubmitPath, volcengineASRTestAPIKey, volcengineASRResourceID, volcengineASRTestRequestID, "", []byte(`{"user":{"uid":"u"},"audio":{},"request":{"model_name":"bigmodel"}}`))
	if code != http.StatusOK {
		t.Fatalf("missing audio.url status = %d (%s), want 200", code, raw)
	}
	assertVolcengineASREnvelope(t, raw, header, 45000001, "audio.url is required", "")

	// 四类拒绝都不得注册任务。
	m.mu.Lock()
	count := len(m.volcengineASRTasks)
	m.mu.Unlock()
	if count != 0 {
		t.Fatalf("拒绝形态注册了 %d 个任务, want 0", count)
	}
}

// TestMediaVolcengineASRCreateOKScript walks the family OK script end to
// end: submit acceptance, query #1 processing (20000001), query #2+ terminal
// succeeded (20000000) with the pinned real-transcription payload, and
// terminal idempotence.
func TestMediaVolcengineASRCreateOKScript(t *testing.T) {
	m := New()
	defer m.Close()

	if code, _, raw := submitVolcengineASR(t, m, string(ScenarioMediaVolcengineASRCreateOK)); code != http.StatusOK {
		t.Fatalf("submit status = %d (%s)", code, raw)
	}

	// query #1：20000001 处理中。
	code, header, raw := queryVolcengineASR(t, m)
	if code != http.StatusOK {
		t.Fatalf("query #1 status = %d (%s)", code, raw)
	}
	if got := header.Get("X-Api-Status-Code"); got != volcengineASRStatusRunning {
		t.Fatalf("query #1 X-Api-Status-Code = %q, want %q", got, volcengineASRStatusRunning)
	}
	if trimmed := strings.TrimSpace(string(raw)); trimmed != `{"audio_info":{},"result":{"text":""}}` {
		t.Fatalf("query #1 body = %s, want running 载荷", trimmed)
	}

	// query #2：20000000 终态 + 钉住的实测转写载荷（逐字节一致）。
	code, header, raw = queryVolcengineASR(t, m)
	if code != http.StatusOK {
		t.Fatalf("query #2 status = %d (%s)", code, raw)
	}
	if got := header.Get("X-Api-Status-Code"); got != volcengineASRStatusOK {
		t.Fatalf("query #2 X-Api-Status-Code = %q, want %q (%s)", got, volcengineASRStatusOK, raw)
	}
	if string(raw) != volcengineASRSucceededPayload {
		t.Fatalf("query #2 body 与钉住的实测载荷不一致:\n got %s\nwant %s", raw, volcengineASRSucceededPayload)
	}
	var succeeded parsedVolcengineASRSucceeded
	if err := json.Unmarshal(raw, &succeeded); err != nil {
		t.Fatalf("succeeded body: %v (%s)", err, raw)
	}
	if succeeded.AudioInfo.Duration != 1118 {
		t.Fatalf("audio_info.duration = %d, want 1118 毫秒（网关计量断言锚点）", succeeded.AudioInfo.Duration)
	}
	if succeeded.Result.Text != "Carry an oily rag." || succeeded.Result.Additions.Duration != "1118" {
		t.Fatalf("result text/additions 漂移: %+v", succeeded.Result)
	}
	if len(succeeded.Result.Utterances) != 1 || len(succeeded.Result.Utterances[0].Words) != 5 {
		t.Fatalf("utterances/words 形态漂移: %+v", succeeded.Result.Utterances)
	}

	// 终态幂等：#3 起同载荷。
	_, header, raw = queryVolcengineASR(t, m)
	if header.Get("X-Api-Status-Code") != volcengineASRStatusOK || string(raw) != volcengineASRSucceededPayload {
		t.Fatalf("query #3（终态幂等）漂移: %q %s", header.Get("X-Api-Status-Code"), raw)
	}
}

// TestMediaVolcengineASRPollRunningStuck pins the never-completes script
// and that the query request's own scenario header is ignored (the script
// is frozen at submit, contract §3.2.2).
func TestMediaVolcengineASRPollRunningStuck(t *testing.T) {
	m := New()
	defer m.Close()

	if code, _, raw := submitVolcengineASR(t, m, string(ScenarioMediaVolcengineASRPollRunning)); code != http.StatusOK {
		t.Fatalf("submit status = %d (%s)", code, raw)
	}
	for i := 0; i < 3; i++ {
		code, header, raw := queryVolcengineASR(t, m)
		if code != http.StatusOK || header.Get("X-Api-Status-Code") != volcengineASRStatusRunning {
			t.Fatalf("query #%d = %d/%q, want 200/%q (%s)", i+1, code, header.Get("X-Api-Status-Code"), volcengineASRStatusRunning, raw)
		}
	}
	// 带 succeeded 场景头的 query 仍走冻结的 running 脚本。
	code, header, _ := doVolcengineASR(t, m, volcengineASRQueryPath, volcengineASRTestAPIKey, volcengineASRResourceID, volcengineASRTestRequestID, string(ScenarioMediaVolcengineASRPollSucceededText), []byte("{}"))
	if code != http.StatusOK || header.Get("X-Api-Status-Code") != volcengineASRStatusRunning {
		t.Fatalf("query 带 scenario 头 = %d/%q, want 200/%q（脚本冻结于 submit）", code, header.Get("X-Api-Status-Code"), volcengineASRStatusRunning)
	}
}

// TestMediaVolcengineASRPollSucceededFastPath pins the fast-path script:
// query #1 is already terminal succeeded with the pinned payload.
func TestMediaVolcengineASRPollSucceededFastPath(t *testing.T) {
	m := New()
	defer m.Close()

	if code, _, raw := submitVolcengineASR(t, m, string(ScenarioMediaVolcengineASRPollSucceededText)); code != http.StatusOK {
		t.Fatalf("submit status = %d (%s)", code, raw)
	}
	code, header, raw := queryVolcengineASR(t, m)
	if code != http.StatusOK || header.Get("X-Api-Status-Code") != volcengineASRStatusOK || string(raw) != volcengineASRSucceededPayload {
		t.Fatalf("fast path = %d/%q (%s), want query #1 即 20000000 成功载荷", code, header.Get("X-Api-Status-Code"), raw)
	}
}

// TestMediaVolcengineASRPollFailedScript pins the failed script: query #1
// is terminal failed with the 45000006 download-audio-failed envelope and
// the paired X-Api-Message header; terminal idempotent.
func TestMediaVolcengineASRPollFailedScript(t *testing.T) {
	m := New()
	defer m.Close()

	if code, _, raw := submitVolcengineASR(t, m, string(ScenarioMediaVolcengineASRPollFailed)); code != http.StatusOK {
		t.Fatalf("submit status = %d (%s)", code, raw)
	}
	code, header, raw := queryVolcengineASR(t, m)
	if code != http.StatusOK || header.Get("X-Api-Status-Code") != volcengineASRStatusFetchFailed {
		t.Fatalf("failed script = %d/%q, want 200/%q", code, header.Get("X-Api-Status-Code"), volcengineASRStatusFetchFailed)
	}
	if trimmed := strings.TrimSpace(string(raw)); trimmed != `{"header":{"code":45000006,"message":"download audio failed"}}` {
		t.Fatalf("failed body = %s, want 45000006/download audio failed", trimmed)
	}
	if got := header.Get("X-Api-Message"); got != "download audio failed" {
		t.Fatalf("X-Api-Message = %q, want download audio failed", got)
	}
	_, header, _ = queryVolcengineASR(t, m)
	if header.Get("X-Api-Status-Code") != volcengineASRStatusFetchFailed {
		t.Fatalf("failed 终态幂等漂移: %q", header.Get("X-Api-Status-Code"))
	}
}

// TestMediaVolcengineASRQueryTaskMissAndHeaders pins the query-side task
// table miss (45000001 "task not found", envelope without reqid) and that
// query runs the same header checks as submit.
func TestMediaVolcengineASRQueryTaskMissAndHeaders(t *testing.T) {
	m := New()
	defer m.Close()

	// 任务表未命中。
	code, header, raw := doVolcengineASR(t, m, volcengineASRQueryPath, volcengineASRTestAPIKey, volcengineASRResourceID, "never-submitted", "", []byte("{}"))
	if code != http.StatusOK || header.Get("X-Api-Status-Code") != volcengineASRStatusBadParam {
		t.Fatalf("task miss = %d/%q, want 200/%q (%s)", code, header.Get("X-Api-Status-Code"), volcengineASRStatusBadParam, raw)
	}
	if trimmed := strings.TrimSpace(string(raw)); trimmed != `{"header":{"code":45000001,"message":"task not found"}}` {
		t.Fatalf("task miss body = %s, want 45000001/task not found", trimmed)
	}
	if got := header.Get("X-Api-Message"); got != "task not found" {
		t.Fatalf("task miss X-Api-Message = %q, want task not found", got)
	}

	// query 缺 X-Api-Key：同族 401 平铺。
	code, _, raw = doVolcengineASR(t, m, volcengineASRQueryPath, "", volcengineASRResourceID, volcengineASRTestRequestID, "", []byte("{}"))
	if code != http.StatusUnauthorized {
		t.Fatalf("query 缺 X-Api-Key = %d, want 401 (%s)", code, raw)
	}

	// query 资源未授权：45000030 信封。
	code, header, raw = doVolcengineASR(t, m, volcengineASRQueryPath, volcengineASRTestAPIKey, "volc.bigasr.auc", volcengineASRTestRequestID, "", []byte("{}"))
	if code != http.StatusOK {
		t.Fatalf("query 错资源 ID = %d, want 200 (%s)", code, raw)
	}
	assertVolcengineASREnvelope(t, raw, header, 45000030, "[resource_id=volc.bigasr.auc] requested resource not granted", volcengineASRTestRequestID)

	// query 缺 X-Api-Request-Id：45000001 信封。
	code, header, raw = doVolcengineASR(t, m, volcengineASRQueryPath, volcengineASRTestAPIKey, volcengineASRResourceID, "", "", []byte("{}"))
	if code != http.StatusOK {
		t.Fatalf("query 缺 Request-Id = %d, want 200 (%s)", code, raw)
	}
	assertVolcengineASREnvelope(t, raw, header, 45000001, "X-Api-Request-Id is required", "")
}

// TestMediaVolcengineASRUnknownScenarioDefaultsCreateOK pins the family
// defaulting philosophy: an unknown scenario on the submit endpoint behaves
// like create_ok (running #1 → succeeded #2).
func TestMediaVolcengineASRUnknownScenarioDefaultsCreateOK(t *testing.T) {
	m := New()
	defer m.Close()

	if code, _, raw := submitVolcengineASR(t, m, "media_volcengine_asr_no_such_script"); code != http.StatusOK {
		t.Fatalf("submit status = %d (%s)", code, raw)
	}
	if _, header, _ := queryVolcengineASR(t, m); header.Get("X-Api-Status-Code") != volcengineASRStatusRunning {
		t.Fatalf("未知场景 query #1 = %q, want %q", header.Get("X-Api-Status-Code"), volcengineASRStatusRunning)
	}
	if _, header, _ := queryVolcengineASR(t, m); header.Get("X-Api-Status-Code") != volcengineASRStatusOK {
		t.Fatalf("未知场景 query #2 = %q, want %q", header.Get("X-Api-Status-Code"), volcengineASRStatusOK)
	}
}

// TestMediaVolcengineASRFaceWhitelist pins the method/path acceptance: both
// endpoints are POST-only via the mediaEndpoints map, everything else on
// the /api/v3/auc namespace stays a 404 (no GET, no cancel route — §9.2).
func TestMediaVolcengineASRFaceWhitelist(t *testing.T) {
	m := New()
	defer m.Close()

	if !acceptsMediaEndpoint(http.MethodPost, volcengineASRSubmitPath) || !acceptsMediaEndpoint(http.MethodPost, volcengineASRQueryPath) {
		t.Fatal("submit/query POST 必须在 media 白名单")
	}
	if !acceptsEndpoint(http.MethodPost, volcengineASRSubmitPath) || !acceptsEndpoint(http.MethodPost, volcengineASRQueryPath) {
		t.Fatal("引擎级白名单必须经 acceptsMediaEndpoint 放行")
	}
	if acceptsMediaEndpoint(http.MethodGet, volcengineASRSubmitPath) || acceptsMediaEndpoint(http.MethodGet, volcengineASRQueryPath) {
		t.Fatal("submit/query 是 POST-only 面")
	}
	if code, _, _ := doVolcengineASR(t, m, "/api/v3/auc/bigmodel/cancel", volcengineASRTestAPIKey, volcengineASRResourceID, volcengineASRTestRequestID, "", []byte("{}")); code != http.StatusNotFound {
		t.Fatalf("cancel 路径 = %d, want 404（§9.2 面无取消端点）", code)
	}
	if code, _, _ := doVolcengineASR(t, m, volcengineASRSubmitPath, volcengineASRTestAPIKey, volcengineASRResourceID, volcengineASRTestRequestID, "", []byte("not-json")); code != http.StatusOK {
		t.Fatalf("submit 非 JSON body = %d, want 200（缺 audio.url 的 45000001 拒绝在 200 内）", code)
	}
}
