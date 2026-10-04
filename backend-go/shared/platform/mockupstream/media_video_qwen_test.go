package mockupstream

// M3 qwen (Wan / DashScope) video face tests, per contract
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension)
// and §10.1 (Wan video-synthesis wire contract). Every scenario gets an
// end-to-end assertion chain (create → poll advance → terminal → artifact
// download where applicable), plus the method/path whitelist and the
// X-DashScope-Async async-header gate.

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// parsedQwenTask mirrors the task object fields the contract asserts (§10.1):
// output.task_id (acceptance credential), output.task_status, the succeeded
// output.video_url locator, the usage JSON string, and the failed top-level
// code/message pair.
type parsedQwenTask struct {
	Output struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
		VideoURL   string `json:"video_url"`
	} `json:"output"`
	Usage json.RawMessage `json:"usage"`
	Code  string          `json:"code"`
	// Message needs to be explicitly declared (the failed-shape top-level error message).
	Message string `json:"message"`
}

const qwenCreateBody = `{"model":"wan2.2-t2v-plus","input":{"prompt":"a mock clip"},"parameters":{"size":"1920*1080","duration":5}}`

var qwenIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// createQwen posts a creation request under the scenario (carrying the
// DashScope async header) and asserts the acceptance shape (200 + parsable
// object with a well-formed uuid task_id + output.task_status PENDING).
func createQwen(t *testing.T, m *Server, scenario string) parsedQwenTask {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost,
		m.URL+"/api/v1/services/aigc/video-generation/video-synthesis", strings.NewReader(qwenCreateBody))
	if err != nil {
		t.Fatalf("build create request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-DashScope-Async", "enable")
	if scenario != "" {
		request.Header.Set("X-Mock-Scenario", scenario)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("qwen create (%s): %v", scenario, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("qwen create (%s) status = %d, want 200 (%s)", scenario, response.StatusCode, raw)
	}
	var obj parsedQwenTask
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("qwen create (%s) body: %v (%s)", scenario, err, raw)
	}
	if !qwenIDPattern.MatchString(obj.Output.TaskID) {
		t.Fatalf("qwen create task_id = %q, want uuid 形态", obj.Output.TaskID)
	}
	if obj.Output.TaskStatus != "PENDING" {
		t.Fatalf("qwen create task_status = %q, want PENDING", obj.Output.TaskStatus)
	}
	return obj
}

// pollQwen GETs the task endpoint (no scenario header: poll behavior is
// decided by the creating request's script).
func pollQwen(t *testing.T, m *Server, id string) (int, parsedQwenTask, string) {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodGet, "/api/v1/tasks/"+id, "", "", nil)
	var obj parsedQwenTask
	if len(raw) > 0 && code != http.StatusInternalServerError {
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("qwen poll %s body: %v (%s)", id, err, raw)
		}
	}
	return code, obj, string(raw)
}

// TestMediaQwenCreatePendingFullChain walks the family OK script end to end:
// create acceptance shape, poll #1 PENDING, poll #2 RUNNING, poll #3
// SUCCEEDED with the output.video_url locator and the usage JSON string, the
// credential-free binary download, and terminal idempotence.
func TestMediaQwenCreatePendingFullChain(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createQwen(t, m, string(ScenarioMediaQwenCreatePending))

	code, p1, _ := pollQwen(t, m, obj.Output.TaskID)
	if code != http.StatusOK || p1.Output.TaskStatus != "PENDING" {
		t.Fatalf("qwen poll #1 = %d/%s, want 200/PENDING", code, p1.Output.TaskStatus)
	}

	code, p2, _ := pollQwen(t, m, obj.Output.TaskID)
	if code != http.StatusOK || p2.Output.TaskStatus != "RUNNING" {
		t.Fatalf("qwen poll #2 = %d/%s, want 200/RUNNING", code, p2.Output.TaskStatus)
	}

	code, p3, raw3 := pollQwen(t, m, obj.Output.TaskID)
	if code != http.StatusOK || p3.Output.TaskStatus != "SUCCEEDED" {
		t.Fatalf("qwen poll #3 = %d/%s (%s), want 200/SUCCEEDED", code, p3.Output.TaskStatus, raw3)
	}
	if !strings.HasPrefix(p3.Output.VideoURL, m.URL+"/api/v1/tasks/") {
		t.Fatalf("qwen video_url = %q, want the instance content locator", p3.Output.VideoURL)
	}
	// usage 是 DashScope 的 JSON 字符串形态（契约 §10.1）：字符串内再嵌一层
	// JSON，解析出 video_duration=5。
	var usageText string
	if err := json.Unmarshal(p3.Usage, &usageText); err != nil {
		t.Fatalf("usage 应为 JSON 字符串形态: %s", p3.Usage)
	}
	var usage struct {
		VideoDuration float64 `json:"video_duration"`
		VideoCount    int     `json:"video_count"`
	}
	if err := json.Unmarshal([]byte(usageText), &usage); err != nil {
		t.Fatalf("usage 内层 JSON: %v (%s)", err, usageText)
	}
	if usage.VideoDuration != 5 || usage.VideoCount != 1 {
		t.Fatalf("usage = %s, want video_duration 5 / video_count 1", usageText)
	}

	// The video_url downloads without credentials: a bare GET (no auth
	// header) is the gateway's artifact path (contract §10.1).
	request, err := http.NewRequest(http.MethodGet, p3.Output.VideoURL, nil)
	if err != nil {
		t.Fatalf("build artifact request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("artifact download: %v", err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("artifact status = %d, want 200", response.StatusCode)
	}
	if len(payload) < 12 || string(payload[4:8]) != "ftyp" {
		t.Fatalf("artifact payload 不是 mp4 载荷（ftyp magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}

	// Terminal idempotence: a fourth poll returns the same SUCCEEDED object.
	_, p4, _ := pollQwen(t, m, obj.Output.TaskID)
	if p4.Output.TaskStatus != "SUCCEEDED" || p4.Output.VideoURL == "" {
		t.Fatalf("qwen poll #4 (terminal idempotence) drifted: %s", p4.Output.TaskStatus)
	}
}

// TestMediaQwenCreateRequiresAsyncHeader pins the DashScope async-header gate:
// a creation request without X-DashScope-Async: enable answers 400 with the
// upstream's synchronous-call rejection (contract §10.1 — the header is the
// E2E assertion point).
func TestMediaQwenCreateRequiresAsyncHeader(t *testing.T) {
	m := New()
	defer m.Close()

	request, err := http.NewRequest(http.MethodPost,
		m.URL+"/api/v1/services/aigc/video-generation/video-synthesis", strings.NewReader(qwenCreateBody))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("create without async header: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("create without X-DashScope-Async status = %d, want 400 (%s)", response.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "does not support synchronous calls") {
		t.Fatalf("rejection body 应携带上游同步调用拒绝消息: %s", raw)
	}
}

// TestMediaQwenPollRunningStuck pins the never-completes script: every poll
// keeps PENDING/RUNNING and the artifact channel stays closed.
func TestMediaQwenPollRunningStuck(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createQwen(t, m, string(ScenarioMediaQwenPollRunning))
	for i := 0; i < 3; i++ {
		code, polled, _ := pollQwen(t, m, obj.Output.TaskID)
		if code != http.StatusOK || (polled.Output.TaskStatus != "PENDING" && polled.Output.TaskStatus != "RUNNING") {
			t.Fatalf("qwen poll #%d = %d/%s, want 200/RUNNING", i+1, code, polled.Output.TaskStatus)
		}
	}
	code, _, _ := doMedia(t, m.Server, http.MethodGet, "/api/v1/tasks/"+obj.Output.TaskID+"/content", "", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("running task artifact = %d, want 404", code)
	}
}

// TestMediaQwenPollSucceededFastPath pins the fast-path script: poll #1 is
// already SUCCEEDED with the video_url locator and the usage string.
func TestMediaQwenPollSucceededFastPath(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createQwen(t, m, string(ScenarioMediaQwenPollSucceededURL))
	code, polled, raw := pollQwen(t, m, obj.Output.TaskID)
	if code != http.StatusOK || polled.Output.TaskStatus != "SUCCEEDED" || polled.Output.VideoURL == "" {
		t.Fatalf("qwen fast path = %d/%s (%s), want 200/SUCCEEDED with video_url", code, polled.Output.TaskStatus, raw)
	}
	if len(polled.Usage) == 0 {
		t.Fatalf("qwen succeeded 应携带 usage JSON 字符串: %s", raw)
	}
}

// TestMediaQwenPollFailedError pins the failed script: poll #1 is terminal
// FAILED with the top-level code/message pair; no artifact channel.
func TestMediaQwenPollFailedError(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createQwen(t, m, string(ScenarioMediaQwenPollFailedError))
	code, polled, _ := pollQwen(t, m, obj.Output.TaskID)
	if code != http.StatusOK || polled.Output.TaskStatus != "FAILED" {
		t.Fatalf("qwen fail script = %d/%s, want 200/FAILED", code, polled.Output.TaskStatus)
	}
	if polled.Code == "" || polled.Message == "" {
		t.Fatalf("qwen failed error = %q/%q, want non-empty code/message", polled.Code, polled.Message)
	}
	code, _, _ = doMedia(t, m.Server, http.MethodGet, "/api/v1/tasks/"+obj.Output.TaskID+"/content", "", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("failed task artifact = %d, want 404", code)
	}
}

// TestMediaQwenFaceWhitelist pins the method/path acceptance: create is
// POST-only on the video-synthesis path, poll/content are GET-only on the
// /api/v1/tasks/{id} paths, unknown ids answer 404, and the collection path
// is not a GET list endpoint (no cancel endpoint — contract §10.1).
func TestMediaQwenFaceWhitelist(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createQwen(t, m, string(ScenarioMediaQwenCreatePending))

	if code, _, _ := doMedia(t, m.Server, http.MethodGet, "/api/v1/services/aigc/video-generation/video-synthesis", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("GET collection path = %d, want 404 (POST-only)", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodPost, "/api/v1/tasks/"+obj.Output.TaskID, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("POST task path = %d, want 404 (GET-only)", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodGet, "/api/v1/tasks/00000000-0000-0000-0000-000000000000", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("unknown id poll = %d, want 404", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodDelete, "/api/v1/tasks/"+obj.Output.TaskID, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("DELETE task path = %d, want 404 (qwen §10.1 面无取消端点)", code)
	}
	if !acceptsQwenVideoEndpoint(http.MethodPost, "/api/v1/services/aigc/video-generation/video-synthesis") {
		t.Fatal("create POST 必须在白名单")
	}
	if !acceptsQwenVideoEndpoint(http.MethodGet, "/api/v1/tasks/"+obj.Output.TaskID) ||
		!acceptsQwenVideoEndpoint(http.MethodGet, "/api/v1/tasks/"+obj.Output.TaskID+"/content") {
		t.Fatal("poll/content GET 必须在白名单")
	}
	if acceptsQwenVideoEndpoint(http.MethodPost, "/api/v1/tasks/x/extra") {
		t.Fatal("未知尾段路径不得进白名单")
	}
}
