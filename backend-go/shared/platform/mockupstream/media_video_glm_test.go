package mockupstream

// M3 glm cogvideo async task face tests, per contract
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension)
// and §7.1 (CogVideoX wire contract). Every scenario gets an end-to-end
// assertion chain (create → poll advance → terminal → content download where
// applicable), plus the method/path whitelist and the no-cancel face.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// parsedGlmAsyncResult mirrors the async-result object fields the contract
// asserts (§7.1): id (acceptance credential), task_status, the SUCCESS
// video_result.url locator.
type parsedGlmAsyncResult struct {
	ID          string `json:"id"`
	RequestID   string `json:"request_id"`
	TaskStatus  string `json:"task_status"`
	VideoResult *struct {
		URL           string `json:"url"`
		CoverImageURL string `json:"cover_image_url"`
	} `json:"video_result"`
}

const glmCreateBody = `{"model":"cogvideox-3","prompt":"a mock clip","size":"1440x720","duration":"5s"}`

var glmIDPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// createGlm posts a creation request under the scenario and asserts the
// acceptance shape (200 + parsable object with a well-formed id + PROCESSING).
func createGlm(t *testing.T, m *Server, scenario string) parsedGlmAsyncResult {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodPost,
		"/api/paas/v4/videos/generations", scenario, "application/json", []byte(glmCreateBody))
	if code != http.StatusOK {
		t.Fatalf("glm create (%s) status = %d, want 200 (%s)", scenario, code, raw)
	}
	var obj parsedGlmAsyncResult
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("glm create (%s) body: %v (%s)", scenario, err, raw)
	}
	if !glmIDPattern.MatchString(obj.ID) {
		t.Fatalf("glm create id = %q, want 12 hex", obj.ID)
	}
	if obj.TaskStatus != "PROCESSING" {
		t.Fatalf("glm create task_status = %q, want PROCESSING", obj.TaskStatus)
	}
	return obj
}

// pollGlm GETs the async-result endpoint (no scenario header: poll behavior
// is decided by the creating request's script).
func pollGlm(t *testing.T, m *Server, id string) (int, parsedGlmAsyncResult, string) {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodGet, "/api/paas/v4/async-result/"+id, "", "", nil)
	var obj parsedGlmAsyncResult
	if len(raw) > 0 && code != http.StatusInternalServerError {
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("glm poll %s body: %v (%s)", id, err, raw)
		}
	}
	return code, obj, string(raw)
}

// TestMediaGlmVideoCreateOKFullChain walks the family OK script end to end:
// create acceptance shape, poll #1 PROCESSING, poll #2 SUCCESS with the url
// locator, the credential-free binary download, and terminal idempotence.
func TestMediaGlmVideoCreateOKFullChain(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createGlm(t, m, string(ScenarioMediaGlmVideoCreateOK))

	code, p1, _ := pollGlm(t, m, obj.ID)
	if code != http.StatusOK || p1.TaskStatus != "PROCESSING" {
		t.Fatalf("glm poll #1 = %d/%s, want 200/PROCESSING", code, p1.TaskStatus)
	}

	code, p2, raw2 := pollGlm(t, m, obj.ID)
	if code != http.StatusOK || p2.TaskStatus != "SUCCESS" {
		t.Fatalf("glm poll #2 = %d/%s (%s), want 200/SUCCESS", code, p2.TaskStatus, raw2)
	}
	if p2.VideoResult == nil || !strings.HasPrefix(p2.VideoResult.URL, m.URL+"/api/paas/v4/async-result/") || !strings.HasSuffix(p2.VideoResult.URL, "/content") {
		t.Fatalf("glm video_result.url = %#v, want the instance /content download path", p2.VideoResult)
	}

	// The url downloads without credentials: a bare GET (no auth header) is
	// the gateway's artifact path (contract §7.1).
	request, err := http.NewRequest(http.MethodGet, p2.VideoResult.URL, nil)
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
	if got := response.Header.Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("artifact content-type = %q, want video/mp4", got)
	}
	if !bytes.Equal(payload, mp4Payload()) {
		t.Fatalf("artifact payload must equal the built-in mp4 channel payload (%d bytes)", len(payload))
	}

	// Terminal idempotence: a third poll returns the same SUCCESS object.
	_, p3, _ := pollGlm(t, m, obj.ID)
	if p3.TaskStatus != "SUCCESS" || p3.VideoResult == nil {
		t.Fatalf("glm poll #3 (terminal idempotence) drifted: %s", p3.TaskStatus)
	}
}

// TestMediaGlmVideoPollProcessingStuck pins the never-completes script: every
// poll keeps PROCESSING and the content channel stays closed.
func TestMediaGlmVideoPollProcessingStuck(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createGlm(t, m, string(ScenarioMediaGlmVideoPollProcessing))
	for i := 0; i < 3; i++ {
		code, polled, _ := pollGlm(t, m, obj.ID)
		if code != http.StatusOK || polled.TaskStatus != "PROCESSING" {
			t.Fatalf("glm poll #%d = %d/%s, want 200/PROCESSING", i+1, code, polled.TaskStatus)
		}
	}
	code, _, _ := doMedia(t, m.Server, http.MethodGet, "/api/paas/v4/async-result/"+obj.ID+"/content", "", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("processing task content = %d, want 404", code)
	}
}

// TestMediaGlmVideoPollSuccessFastPath pins the fast-path script: poll #1 is
// already SUCCESS with the url locator.
func TestMediaGlmVideoPollSuccessFastPath(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createGlm(t, m, string(ScenarioMediaGlmVideoPollSuccessURL))
	code, polled, raw := pollGlm(t, m, obj.ID)
	if code != http.StatusOK || polled.TaskStatus != "SUCCESS" || polled.VideoResult == nil {
		t.Fatalf("glm fast path = %d/%s (%s), want 200/SUCCESS with url", code, polled.TaskStatus, raw)
	}
}

// TestMediaGlmVideoPollFail pins the FAIL script: poll #1 is terminal FAIL.
func TestMediaGlmVideoPollFail(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createGlm(t, m, string(ScenarioMediaGlmVideoPollFail))
	code, polled, _ := pollGlm(t, m, obj.ID)
	if code != http.StatusOK || polled.TaskStatus != "FAIL" {
		t.Fatalf("glm fail script = %d/%s, want 200/FAIL", code, polled.TaskStatus)
	}
	// Failed task: no artifact channel.
	code, _, _ = doMedia(t, m.Server, http.MethodGet, "/api/paas/v4/async-result/"+obj.ID+"/content", "", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("failed task content = %d, want 404", code)
	}
}

// TestMediaGlmVideoFaceWhitelist pins the method/path acceptance: create is
// POST-only, async-result is GET-only, unknown ids answer 404, and unrelated
// methods under the root stay off the face.
func TestMediaGlmVideoFaceWhitelist(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createGlm(t, m, string(ScenarioMediaGlmVideoCreateOK))

	if code, _, _ := doMedia(t, m.Server, http.MethodGet, "/api/paas/v4/videos/generations", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("GET create path = %d, want 404 (POST-only)", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodPost, "/api/paas/v4/async-result/"+obj.ID, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("POST poll path = %d, want 404 (GET-only)", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodGet, "/api/paas/v4/async-result/deadbeefdead", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("unknown id poll = %d, want 404", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodDelete, "/api/paas/v4/async-result/"+obj.ID, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("DELETE poll path = %d, want 404 (glm 无取消端点，契约 §7.1)", code)
	}
	if acceptsGlmVideoEndpoint(http.MethodPost, "/api/paas/v4/videos/generations/extra") {
		t.Fatal("create path 带尾段不得进白名单")
	}
	if acceptsGlmVideoEndpoint(http.MethodGet, "/api/paas/v4/async-result/") {
		t.Fatal("空 id 不得进白名单")
	}
}
