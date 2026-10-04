package mockupstream

// M3 minimax video/TTS face tests, per contract
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension)
// and §8 (MiniMax wire contract). Every scenario gets an end-to-end assertion
// chain (create → poll advance → terminal → artifact download where
// applicable), plus the TTS hex-payload face and the method/path whitelist.

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// parsedMinimaxQuery mirrors the query/video_generation object fields the
// contract asserts (§8.1): task_id (acceptance credential), status, the
// Success file_id/file_download_url locator, and the Fail base_resp.
type parsedMinimaxQuery struct {
	TaskID          string `json:"task_id"`
	Status          string `json:"status"`
	FileID          string `json:"file_id"`
	FileDownloadURL string `json:"file_download_url"`
	BaseResp        *struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

// parsedMinimaxTTS mirrors the t2a_v2 success object (§8.2): data.audio hex
// payload + extra_info character metering.
type parsedMinimaxTTS struct {
	Data struct {
		Audio  string `json:"audio"`
		Status int    `json:"status"`
	} `json:"data"`
	ExtraInfo *struct {
		UsageCharacters float64 `json:"usage_characters"`
	} `json:"extra_info"`
}

const minimaxCreateBody = `{"model":"MiniMax-Hailuo-2.3","prompt":"a mock clip","duration":6}`

var minimaxIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// createMinimax posts a creation request under the scenario and asserts the
// acceptance shape (200 + parsable object with a well-formed task_id +
// status_code 0).
func createMinimax(t *testing.T, m *Server, scenario string) parsedMinimaxQuery {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodPost,
		"/v1/video_generation", scenario, "application/json", []byte(minimaxCreateBody))
	if code != http.StatusOK {
		t.Fatalf("minimax create (%s) status = %d, want 200 (%s)", scenario, code, raw)
	}
	var obj struct {
		TaskID string `json:"task_id"`
		BaseResp *struct {
			StatusCode int `json:"status_code"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("minimax create (%s) body: %v (%s)", scenario, err, raw)
	}
	if !minimaxIDPattern.MatchString(obj.TaskID) {
		t.Fatalf("minimax create task_id = %q, want 16 hex", obj.TaskID)
	}
	if obj.BaseResp == nil || obj.BaseResp.StatusCode != 0 {
		t.Fatalf("minimax create base_resp = %#v, want status_code 0", obj.BaseResp)
	}
	return parsedMinimaxQuery{TaskID: obj.TaskID}
}

// pollMinimax GETs the query endpoint (no scenario header: poll behavior is
// decided by the creating request's script).
func pollMinimax(t *testing.T, m *Server, taskID string) (int, parsedMinimaxQuery, string) {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodGet,
		"/v1/query/video_generation?task_id="+taskID, "", "", nil)
	var obj parsedMinimaxQuery
	if len(raw) > 0 && code != http.StatusInternalServerError {
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("minimax poll %s body: %v (%s)", taskID, err, raw)
		}
	}
	return code, obj, string(raw)
}

// TestMediaMinimaxCreateOKFullChain walks the family OK script end to end:
// create acceptance shape, poll #1 Preparing (queued), poll #2 Queueing,
// poll #3 Success with the file_download_url locator, the credential-free
// binary download, and terminal idempotence.
func TestMediaMinimaxCreateOKFullChain(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createMinimax(t, m, string(ScenarioMediaMinimaxCreateOK))

	code, p1, _ := pollMinimax(t, m, obj.TaskID)
	if code != http.StatusOK || p1.Status != "Preparing" {
		t.Fatalf("minimax poll #1 = %d/%s, want 200/Preparing", code, p1.Status)
	}

	code, p2, _ := pollMinimax(t, m, obj.TaskID)
	if code != http.StatusOK || p2.Status != "Queueing" {
		t.Fatalf("minimax poll #2 = %d/%s, want 200/Queueing", code, p2.Status)
	}

	code, p3, raw3 := pollMinimax(t, m, obj.TaskID)
	if code != http.StatusOK || p3.Status != "Success" {
		t.Fatalf("minimax poll #3 = %d/%s (%s), want 200/Success", code, p3.Status, raw3)
	}
	if !strings.HasPrefix(p3.FileDownloadURL, m.URL+"/v1/files/retrieve?file_id=") {
		t.Fatalf("minimax file_download_url = %q, want the instance files/retrieve locator", p3.FileDownloadURL)
	}

	// The file_download_url downloads without credentials: a bare GET (no
	// auth header) is the gateway's artifact path (contract §8.1).
	request, err := http.NewRequest(http.MethodGet, p3.FileDownloadURL, nil)
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

	// Terminal idempotence: a fourth poll returns the same Success object.
	_, p4, _ := pollMinimax(t, m, obj.TaskID)
	if p4.Status != "Success" || p4.FileDownloadURL == "" {
		t.Fatalf("minimax poll #4 (terminal idempotence) drifted: %s", p4.Status)
	}
}

// TestMediaMinimaxPollRunningStuck pins the never-completes script: every
// poll keeps a running status and the artifact channel stays closed.
func TestMediaMinimaxPollRunningStuck(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createMinimax(t, m, string(ScenarioMediaMinimaxPollRunning))
	for i := 0; i < 3; i++ {
		code, polled, _ := pollMinimax(t, m, obj.TaskID)
		if code != http.StatusOK || (polled.Status != "Preparing" && polled.Status != "Queueing" && polled.Status != "Processing") {
			t.Fatalf("minimax poll #%d = %d/%s, want 200/running", i+1, code, polled.Status)
		}
	}
	code, _, _ := doMedia(t, m.Server, http.MethodGet, "/v1/files/retrieve?file_id="+obj.TaskID, "", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("running task artifact = %d, want 404", code)
	}
}

// TestMediaMinimaxPollSuccessFastPath pins the fast-path script: poll #1 is
// already Success with the file_download_url locator.
func TestMediaMinimaxPollSuccessFastPath(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createMinimax(t, m, string(ScenarioMediaMinimaxPollSuccess))
	code, polled, raw := pollMinimax(t, m, obj.TaskID)
	if code != http.StatusOK || polled.Status != "Success" || polled.FileDownloadURL == "" {
		t.Fatalf("minimax fast path = %d/%s (%s), want 200/Success with file_download_url", code, polled.Status, raw)
	}
}

// TestMediaMinimaxPollFail pins the Fail script: poll #1 is terminal Fail
// with a non-zero base_resp; no artifact channel.
func TestMediaMinimaxPollFail(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createMinimax(t, m, string(ScenarioMediaMinimaxPollFail))
	code, polled, _ := pollMinimax(t, m, obj.TaskID)
	if code != http.StatusOK || polled.Status != "Fail" {
		t.Fatalf("minimax fail script = %d/%s, want 200/Fail", code, polled.Status)
	}
	if polled.BaseResp == nil || polled.BaseResp.StatusCode == 0 {
		t.Fatalf("minimax Fail base_resp = %#v, want non-zero status_code", polled.BaseResp)
	}
	code, _, _ = doMedia(t, m.Server, http.MethodGet, "/v1/files/retrieve?file_id="+obj.TaskID, "", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("failed task artifact = %d, want 404", code)
	}
}

// TestMediaMinimaxTTSOK pins the t2a_v2 face (§8.2): data.audio is a hex
// string whose decoded bytes carry the negotiated format's magic bytes, and
// extra_info.usage_characters reflects the request text rune count (the
// reported billing character source).
func TestMediaMinimaxTTSOK(t *testing.T) {
	m := New()
	defer m.Close()

	body := `{"model":"speech-02-turbo","text":"你好 minimax","voice_setting":{"voice_id":"male-qn-qingse"},"audio_setting":{"format":"mp3"}}`
	code, _, raw := doMedia(t, m.Server, http.MethodPost, "/v1/t2a_v2",
		string(ScenarioMediaMinimaxTTSOK), "application/json", []byte(body))
	if code != http.StatusOK {
		t.Fatalf("minimax tts status = %d (%s), want 200", code, raw)
	}
	var obj parsedMinimaxTTS
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("minimax tts body: %v (%s)", err, raw)
	}
	decoded, err := hex.DecodeString(obj.Data.Audio)
	if err != nil {
		t.Fatalf("data.audio must be valid hex: %v", err)
	}
	// mp3 magic bytes: MPEG frame sync 0xFF 0xFB on every frame head.
	if len(decoded) < 4 || decoded[0] != 0xFF || decoded[1] != 0xFB {
		t.Fatalf("decoded audio 不是 mp3 载荷（frame sync 缺失）: % x", decoded[:min(4, len(decoded))])
	}
	// usage_characters deliberately deviates from the request rune count (+1,
	// see serveMinimaxTTS) so gateway metering can prove the upstream
	// reported value wins over the request-input self count.
	wantChars := float64(len([]rune("你好 minimax")) + 1)
	if obj.ExtraInfo == nil || obj.ExtraInfo.UsageCharacters != wantChars {
		t.Fatalf("extra_info.usage_characters = %#v, want %v", obj.ExtraInfo, wantChars)
	}
}

// TestMediaMinimaxFaceWhitelist pins the method/path acceptance: create and
// tts are POST-only, query/files are GET-only, unknown ids answer 404, and
// unrelated methods under the root stay off the face (no cancel endpoint —
// contract §8.1).
func TestMediaMinimaxFaceWhitelist(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createMinimax(t, m, string(ScenarioMediaMinimaxCreateOK))

	if code, _, _ := doMedia(t, m.Server, http.MethodGet, "/v1/video_generation", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("GET create path = %d, want 404 (POST-only)", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodPost, "/v1/query/video_generation?task_id="+obj.TaskID, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("POST query path = %d, want 404 (GET-only)", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodGet, "/v1/query/video_generation?task_id=deadbeefdeadbeef", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("unknown id query = %d, want 404", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodDelete, "/v1/video_generation", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("DELETE create path = %d, want 404 (minimax 无取消端点，契约 §8.1)", code)
	}
	if !acceptsMinimaxVideoEndpoint(http.MethodPost, "/v1/video_generation") || !acceptsMinimaxVideoEndpoint(http.MethodPost, "/v1/t2a_v2") {
		t.Fatal("create/tts POST 必须在白名单")
	}
	if !acceptsMinimaxVideoEndpoint(http.MethodGet, "/v1/query/video_generation") || !acceptsMinimaxVideoEndpoint(http.MethodGet, "/v1/files/retrieve") {
		t.Fatal("query/files GET 必须在白名单")
	}
	if acceptsMinimaxVideoEndpoint(http.MethodPost, "/v1/video_generation/extra") {
		t.Fatal("create path 带尾段不得进白名单")
	}
}
