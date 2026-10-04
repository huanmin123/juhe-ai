package mockupstream

// M3 volcengine (Seedance) video face tests, per contract
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension)
// and §9.1 (Seedance wire contract). Every scenario gets an end-to-end
// assertion chain (create → poll advance → terminal → artifact download
// where applicable), plus the method/path whitelist.

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// parsedVolcengineTask mirrors the task object fields the contract asserts
// (§9.1): id (acceptance credential), status, the succeeded
// content.video_url locator, and the failed error envelope.
type parsedVolcengineTask struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Content *struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

const volcengineCreateBody = `{"model":"doubao-seedance-1-0-pro-250528","content":[{"type":"text","text":"a mock clip"}],"resolution":"720p","ratio":"16:9","duration":5}`

var volcengineIDPattern = regexp.MustCompile(`^cgt-[0-9a-f]{16}$`)

// createVolcengine posts a creation request under the scenario and asserts
// the acceptance shape (200 + parsable object with a well-formed cgt- id +
// status queued).
func createVolcengine(t *testing.T, m *Server, scenario string) parsedVolcengineTask {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodPost,
		"/api/v3/contents/generations/tasks", scenario, "application/json", []byte(volcengineCreateBody))
	if code != http.StatusOK {
		t.Fatalf("volcengine create (%s) status = %d, want 200 (%s)", scenario, code, raw)
	}
	var obj parsedVolcengineTask
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("volcengine create (%s) body: %v (%s)", scenario, err, raw)
	}
	if !volcengineIDPattern.MatchString(obj.ID) {
		t.Fatalf("volcengine create id = %q, want cgt-<16hex>", obj.ID)
	}
	if obj.Status != "queued" {
		t.Fatalf("volcengine create status = %q, want queued", obj.Status)
	}
	return obj
}

// pollVolcengine GETs the task endpoint (no scenario header: poll behavior is
// decided by the creating request's script).
func pollVolcengine(t *testing.T, m *Server, id string) (int, parsedVolcengineTask, string) {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodGet,
		"/api/v3/contents/generations/tasks/"+id, "", "", nil)
	var obj parsedVolcengineTask
	if len(raw) > 0 && code != http.StatusInternalServerError {
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("volcengine poll %s body: %v (%s)", id, err, raw)
		}
	}
	return code, obj, string(raw)
}

// TestMediaVolcengineCreateOKFullChain walks the family OK script end to end:
// create acceptance shape, poll #1 queued, poll #2 running, poll #3
// succeeded with the content.video_url locator, the credential-free binary
// download, and terminal idempotence.
func TestMediaVolcengineCreateOKFullChain(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVolcengine(t, m, string(ScenarioMediaVolcengineCreateOK))

	code, p1, _ := pollVolcengine(t, m, obj.ID)
	if code != http.StatusOK || p1.Status != "queued" {
		t.Fatalf("volcengine poll #1 = %d/%s, want 200/queued", code, p1.Status)
	}

	code, p2, _ := pollVolcengine(t, m, obj.ID)
	if code != http.StatusOK || p2.Status != "running" {
		t.Fatalf("volcengine poll #2 = %d/%s, want 200/running", code, p2.Status)
	}

	code, p3, raw3 := pollVolcengine(t, m, obj.ID)
	if code != http.StatusOK || p3.Status != "succeeded" {
		t.Fatalf("volcengine poll #3 = %d/%s (%s), want 200/succeeded", code, p3.Status, raw3)
	}
	if p3.Content == nil || !strings.HasPrefix(p3.Content.VideoURL, m.URL+"/api/v3/contents/generations/tasks/") {
		t.Fatalf("volcengine video_url = %#v, want the instance content locator", p3.Content)
	}

	// The video_url downloads without credentials: a bare GET (no auth
	// header) is the gateway's artifact path (contract §9.1).
	request, err := http.NewRequest(http.MethodGet, p3.Content.VideoURL, nil)
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

	// Terminal idempotence: a fourth poll returns the same succeeded object.
	_, p4, _ := pollVolcengine(t, m, obj.ID)
	if p4.Status != "succeeded" || p4.Content == nil || p4.Content.VideoURL == "" {
		t.Fatalf("volcengine poll #4 (terminal idempotence) drifted: %s", p4.Status)
	}
}

// TestMediaVolcenginePollRunningStuck pins the never-completes script: every
// poll keeps queued/running and the artifact channel stays closed.
func TestMediaVolcenginePollRunningStuck(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVolcengine(t, m, string(ScenarioMediaVolcenginePollRunning))
	for i := 0; i < 3; i++ {
		code, polled, _ := pollVolcengine(t, m, obj.ID)
		if code != http.StatusOK || (polled.Status != "queued" && polled.Status != "running") {
			t.Fatalf("volcengine poll #%d = %d/%s, want 200/running", i+1, code, polled.Status)
		}
	}
	code, _, _ := doMedia(t, m.Server, http.MethodGet,
		"/api/v3/contents/generations/tasks/"+obj.ID+"/content", "", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("running task artifact = %d, want 404", code)
	}
}

// TestMediaVolcenginePollSucceededFastPath pins the fast-path script: poll #1
// is already succeeded with the video_url locator.
func TestMediaVolcenginePollSucceededFastPath(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVolcengine(t, m, string(ScenarioMediaVolcenginePollSucceededURL))
	code, polled, raw := pollVolcengine(t, m, obj.ID)
	if code != http.StatusOK || polled.Status != "succeeded" ||
		polled.Content == nil || polled.Content.VideoURL == "" {
		t.Fatalf("volcengine fast path = %d/%s (%s), want 200/succeeded with video_url", code, polled.Status, raw)
	}
}

// TestMediaVolcenginePollFailedError pins the failed script: poll #1 is
// terminal failed with an error{code,message} envelope; no artifact channel.
func TestMediaVolcenginePollFailedError(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVolcengine(t, m, string(ScenarioMediaVolcenginePollFailedError))
	code, polled, _ := pollVolcengine(t, m, obj.ID)
	if code != http.StatusOK || polled.Status != "failed" {
		t.Fatalf("volcengine fail script = %d/%s, want 200/failed", code, polled.Status)
	}
	if polled.Error == nil || polled.Error.Code == "" || polled.Error.Message == "" {
		t.Fatalf("volcengine failed error = %#v, want non-empty code/message", polled.Error)
	}
	code, _, _ = doMedia(t, m.Server, http.MethodGet,
		"/api/v3/contents/generations/tasks/"+obj.ID+"/content", "", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("failed task artifact = %d, want 404", code)
	}
}

// TestMediaVolcengineFaceWhitelist pins the method/path acceptance: create is
// POST-only on the collection path, poll/content are GET-only on the {id}
// paths, unknown ids answer 404, and the collection path is not a GET list
// endpoint (no cancel endpoint — contract §9.1).
func TestMediaVolcengineFaceWhitelist(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVolcengine(t, m, string(ScenarioMediaVolcengineCreateOK))

	if code, _, _ := doMedia(t, m.Server, http.MethodGet, "/api/v3/contents/generations/tasks", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("GET collection path = %d, want 404 (POST-only)", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodPost, "/api/v3/contents/generations/tasks/"+obj.ID, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("POST task path = %d, want 404 (GET-only)", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodGet, "/api/v3/contents/generations/tasks/cgt-deadbeefdeadbeef", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("unknown id poll = %d, want 404", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodDelete, "/api/v3/contents/generations/tasks/"+obj.ID, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("DELETE task path = %d, want 404 (volcengine §9.1 面无取消端点)", code)
	}
	if !acceptsVolcengineVideoEndpoint(http.MethodPost, "/api/v3/contents/generations/tasks") {
		t.Fatal("create POST 必须在白名单")
	}
	if !acceptsVolcengineVideoEndpoint(http.MethodGet, "/api/v3/contents/generations/tasks/"+obj.ID) ||
		!acceptsVolcengineVideoEndpoint(http.MethodGet, "/api/v3/contents/generations/tasks/"+obj.ID+"/content") {
		t.Fatal("poll/content GET 必须在白名单")
	}
	if acceptsVolcengineVideoEndpoint(http.MethodPost, "/api/v3/contents/generations/tasks/x/extra") {
		t.Fatal("未知尾段路径不得进白名单")
	}
}
