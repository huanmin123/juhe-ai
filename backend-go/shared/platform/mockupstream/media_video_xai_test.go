package mockupstream

// M3 xAI (Grok Imagine Video) video face tests, per contract
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension)
// and §6.1 (Grok Imagine Video wire contract). Every scenario gets an
// end-to-end assertion chain (create → poll advance → terminal → artifact
// download where applicable), the method/path whitelist, and the shared
// /v1/videos/{id} namespace fallback through the openai face.

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// parsedXaiTask mirrors the task object fields the contract asserts (§6.1):
// request_id (acceptance credential), status, the done video{url,duration}
// locator, and the failed error envelope.
type parsedXaiTask struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Video     *struct {
		URL      string   `json:"url"`
		Duration *float64 `json:"duration"`
	} `json:"video"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

const xaiCreateBody = `{"model":"grok-imagine-video-1.5","prompt":"a mock clip","duration":6,"aspect_ratio":"16:9","resolution":"720p","generate_audio":true}`

var xaiIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// createXai posts a creation request under the scenario and asserts the
// acceptance shape (200 + parsable object with a well-formed uuid
// request_id; the creation response carries no status field — contract
// §6.1's minimal face).
func createXai(t *testing.T, m *Server, scenario string) parsedXaiTask {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodPost,
		"/v1/videos/generations", scenario, "application/json", []byte(xaiCreateBody))
	if code != http.StatusOK {
		t.Fatalf("xai create (%s) status = %d, want 200 (%s)", scenario, code, raw)
	}
	var obj parsedXaiTask
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("xai create (%s) body: %v (%s)", scenario, err, raw)
	}
	if !xaiIDPattern.MatchString(obj.RequestID) {
		t.Fatalf("xai create request_id = %q, want uuid", obj.RequestID)
	}
	return obj
}

// pollXai GETs the task endpoint through the shared /v1/videos/{id}
// namespace (no scenario header: poll behavior is decided by the creating
// request's script).
func pollXai(t *testing.T, m *Server, id string) (int, parsedXaiTask, string) {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodGet,
		"/v1/videos/"+id, "", "", nil)
	var obj parsedXaiTask
	if len(raw) > 0 && code != http.StatusInternalServerError {
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("xai poll %s body: %v (%s)", id, err, raw)
		}
	}
	return code, obj, string(raw)
}

// TestMediaXaiCreateRequestIDFullChain walks the family OK script end to
// end: create acceptance shape (uuid request_id), poll #1 pending, poll #2
// done with the video{url,duration} locator, the credential-free binary
// download, and terminal idempotence. The polls ride the shared
// /v1/videos/{id} namespace via the openai face's table-miss fallback
// (video.go serveVideoPoll → xai table).
func TestMediaXaiCreateRequestIDFullChain(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createXai(t, m, string(ScenarioMediaXaiVideoCreateRequestID))

	code, p1, _ := pollXai(t, m, obj.RequestID)
	if code != http.StatusOK || p1.Status != "pending" {
		t.Fatalf("xai poll #1 = %d/%s, want 200/pending", code, p1.Status)
	}

	code, p2, raw2 := pollXai(t, m, obj.RequestID)
	if code != http.StatusOK || p2.Status != "done" {
		t.Fatalf("xai poll #2 = %d/%s (%s), want 200/done", code, p2.Status, raw2)
	}
	if p2.Video == nil || !strings.HasPrefix(p2.Video.URL, m.URL+"/v1/videos/") {
		t.Fatalf("xai video.url = %#v, want the instance content locator", p2.Video)
	}
	if p2.Video.Duration == nil || *p2.Video.Duration != 6 {
		t.Fatalf("xai video.duration = %#v, want 6（时长计量基源）", p2.Video)
	}

	// The video.url downloads without credentials: a bare GET (no auth
	// header) is the gateway's artifact path (contract §6.1).
	request, err := http.NewRequest(http.MethodGet, p2.Video.URL, nil)
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

	// Terminal idempotence: a third poll returns the same done object.
	_, p3, _ := pollXai(t, m, obj.RequestID)
	if p3.Status != "done" || p3.Video == nil || p3.Video.URL == "" {
		t.Fatalf("xai poll #3 (terminal idempotence) drifted: %s", p3.Status)
	}
}

// TestMediaXaiPollPendingStuck pins the never-completes script: every poll
// keeps pending and the artifact channel stays closed.
func TestMediaXaiPollPendingStuck(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createXai(t, m, string(ScenarioMediaXaiVideoPollPending))
	for i := 0; i < 3; i++ {
		code, polled, _ := pollXai(t, m, obj.RequestID)
		if code != http.StatusOK || polled.Status != "pending" {
			t.Fatalf("xai stuck poll #%d = %d/%s, want 200/pending", i+1, code, polled.Status)
		}
	}
	request, _ := http.NewRequest(http.MethodGet, m.URL+"/v1/videos/"+obj.RequestID+"/content", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("pending artifact probe: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("pending artifact status = %d, want 404", response.StatusCode)
	}
}

// TestMediaXaiPollDoneFastPath pins the fast-path script: poll #1 is already
// done with the video{url,duration} locator.
func TestMediaXaiPollDoneFastPath(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createXai(t, m, string(ScenarioMediaXaiVideoPollDoneURLDur))
	code, polled, raw := pollXai(t, m, obj.RequestID)
	if code != http.StatusOK || polled.Status != "done" {
		t.Fatalf("xai fast-path poll = %d/%s (%s), want 200/done", code, polled.Status, raw)
	}
	if polled.Video == nil || polled.Video.URL == "" || polled.Video.Duration == nil {
		t.Fatalf("xai fast-path video = %#v, want url+duration", polled.Video)
	}
}

// TestMediaXaiPollFailedError pins the failed script: poll #1 is failed with
// the error{code,message} envelope (code invalid_argument, contract §6.1
// word list).
func TestMediaXaiPollFailedError(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createXai(t, m, string(ScenarioMediaXaiVideoPollFailedError))
	code, polled, _ := pollXai(t, m, obj.RequestID)
	if code != http.StatusOK || polled.Status != "failed" {
		t.Fatalf("xai failed poll = %d/%s, want 200/failed", code, polled.Status)
	}
	if polled.Error == nil || polled.Error.Code != "invalid_argument" {
		t.Fatalf("xai failed error = %#v, want code invalid_argument", polled.Error)
	}
	// failed keeps the artifact channel closed.
	request, _ := http.NewRequest(http.MethodGet, m.URL+"/v1/videos/"+obj.RequestID+"/content", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("failed artifact probe: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("failed artifact status = %d, want 404", response.StatusCode)
	}
}

// TestMediaXaiPollExpired pins the expired script: poll #1 answers the
// native expired terminal (contract §6.1 — the upstream has an expired
// terminal state, no payload face) and the artifact channel stays closed.
func TestMediaXaiPollExpired(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createXai(t, m, string(ScenarioMediaXaiVideoPollExpired))
	code, polled, _ := pollXai(t, m, obj.RequestID)
	if code != http.StatusOK || polled.Status != "expired" {
		t.Fatalf("xai expired poll = %d/%s, want 200/expired", code, polled.Status)
	}
	request, _ := http.NewRequest(http.MethodGet, m.URL+"/v1/videos/"+obj.RequestID+"/content", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("expired artifact probe: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("expired artifact status = %d, want 404", response.StatusCode)
	}
}

// TestMediaXaiFaceWhitelist pins the method/path acceptance and the shared
// namespace routing: the xai create endpoint is POST-only; an unknown
// /v1/videos/{id} still answers the openai face's 404 (the fallback only
// fires for registered xai request_ids); the openai create POST /v1/videos
// keeps its own face.
func TestMediaXaiFaceWhitelist(t *testing.T) {
	if !acceptsXaiVideoEndpoint(http.MethodPost, "/v1/videos/generations") {
		t.Fatal("xai create endpoint (POST /v1/videos/generations) 应在白名单")
	}
	if acceptsXaiVideoEndpoint(http.MethodGet, "/v1/videos/generations") {
		t.Fatal("GET /v1/videos/generations 不是 xai create 形态")
	}

	m := New()
	defer m.Close()

	// Unknown id: the openai face answers its own 404 (no xai table hit).
	code, _, raw := doMedia(t, m.Server, http.MethodGet, "/v1/videos/00000000-0000-0000-0000-000000000000", "", "", nil)
	if code != http.StatusNotFound || !strings.Contains(string(raw), "video_not_found") {
		t.Fatalf("unknown id = %d (%s), want openai 404", code, raw)
	}

	// The openai videos face keeps working (create registers in the openai
	// table with its own response shape).
	code, _, raw = doMedia(t, m.Server, http.MethodPost, "/v1/videos", string(ScenarioMediaVideoOKPoll1), "application/json", []byte(`{"model":"sora-2","prompt":"p"}`))
	if code != http.StatusOK || !strings.Contains(string(raw), `"id":"video_`) {
		t.Fatalf("openai create face = %d (%s), want 200 with video_ id", code, raw)
	}
}
