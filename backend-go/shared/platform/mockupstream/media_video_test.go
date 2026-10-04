package mockupstream

// M2 videos async job face tests, per contract
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension) and
// §4.3 (OpenAI videos wire contract). Every scenario gets at least one
// end-to-end assertion chain (create → poll advance → terminal → content
// where applicable), plus concurrency isolation and the method/path matrix.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// parsedVideoObject mirrors the OpenAI video object fields the contract
// asserts (§4.3): id/object/status/progress/model/prompt/seconds_length/size
// plus the completed content locator array and the failed error object.
type parsedVideoObject struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Status        string `json:"status"`
	Progress      int    `json:"progress"`
	Model         string `json:"model"`
	Prompt        string `json:"prompt"`
	SecondsLength int    `json:"seconds_length"`
	Size          string `json:"size"`
	Content       []struct {
		Status string `json:"status"`
		Type   string `json:"type"`
		URL    string `json:"url"`
	} `json:"content"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

const videoCreateBody = `{"model":"sora-2","prompt":"a mock clip","seconds":"4","size":"1280x720"}`

var videoIDPattern = regexp.MustCompile(`^video_[0-9a-f]{16}$`)

// createVideo posts a creation request under the scenario and asserts the
// acceptance shape (200 + parsable object).
func createVideo(t *testing.T, m *Server, scenario string) parsedVideoObject {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodPost, "/v1/videos", scenario, "application/json", []byte(videoCreateBody))
	if code != http.StatusOK {
		t.Fatalf("create (%s) status = %d, want 200 (%s)", scenario, code, raw)
	}
	var obj parsedVideoObject
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("create (%s) body: %v (%s)", scenario, err, raw)
	}
	return obj
}

// pollVideoGET polls the job id (no scenario header: poll behavior is
// decided by the creating request's script, not the poll request).
func pollVideo(t *testing.T, m *Server, id string) (int, parsedVideoObject, string) {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodGet, "/v1/videos/"+id, "", "", nil)
	var obj parsedVideoObject
	if len(raw) > 0 && code != http.StatusInternalServerError {
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("poll %s body: %v (%s)", id, err, raw)
		}
	}
	return code, obj, string(raw)
}

// assertVideoState checks the status/progress/content/error triplet.
func assertVideoState(t *testing.T, label string, obj parsedVideoObject, status string, progress int, wantContent, wantError bool) {
	t.Helper()
	if obj.Status != status {
		t.Fatalf("%s: status = %q, want %q", label, obj.Status, status)
	}
	if obj.Progress != progress {
		t.Fatalf("%s: progress = %d, want %d", label, obj.Progress, progress)
	}
	if got := len(obj.Content); (got > 0) != wantContent {
		t.Fatalf("%s: content entries = %d, want present=%v", label, got, wantContent)
	}
	if (obj.Error != nil) != wantError {
		t.Fatalf("%s: error object present = %v, want %v", label, obj.Error != nil, wantError)
	}
}

// downloadContent fetches /v1/videos/{id}/content.
func downloadContent(t *testing.T, m *Server, id string) (int, http.Header, []byte) {
	t.Helper()
	return doMedia(t, m.Server, http.MethodGet, "/v1/videos/"+id+"/content", "", "", nil)
}

// listVideos fetches GET /v1/videos and returns the data entries.
func listVideos(t *testing.T, m *Server) []parsedVideoObject {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodGet, "/v1/videos", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (%s)", code, raw)
	}
	var parsed struct {
		Object string              `json:"object"`
		Data   []parsedVideoObject `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("list body: %v (%s)", err, raw)
	}
	if parsed.Object != "list" {
		t.Fatalf("list object = %q, want \"list\"", parsed.Object)
	}
	return parsed.Data
}

// TestMediaVideoOKPoll3FullChain walks the canonical three-poll OK script
// end to end: creation acceptance shape, 33/66/100 progress advance, the
// completed content locator, the binary download, and terminal idempotence.
func TestMediaVideoOKPoll3FullChain(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVideo(t, m, string(ScenarioMediaVideoOKPoll3))
	if !videoIDPattern.MatchString(obj.ID) {
		t.Fatalf("create id = %q, want video_ + 16 hex", obj.ID)
	}
	if obj.Object != "video" {
		t.Fatalf("create object = %q, want video", obj.Object)
	}
	assertVideoState(t, "create", obj, videoStatusQueued, 0, false, false)
	if obj.Model != "sora-2" || obj.Prompt != "a mock clip" || obj.SecondsLength != 4 || obj.Size != "1280x720" {
		t.Fatalf("create echo fields mismatch: %+v", obj)
	}
	id := obj.ID

	code, p1, _ := pollVideo(t, m, id)
	if code != http.StatusOK {
		t.Fatalf("poll #1 status = %d", code)
	}
	assertVideoState(t, "poll #1", p1, videoStatusInProgress, 33, false, false)
	if p1.ID != id || p1.Model != "sora-2" || p1.SecondsLength != 4 {
		t.Fatalf("poll #1 identity/echo drift: %+v", p1)
	}

	_, p2, _ := pollVideo(t, m, id)
	assertVideoState(t, "poll #2", p2, videoStatusInProgress, 66, false, false)

	_, p3, _ := pollVideo(t, m, id)
	assertVideoState(t, "poll #3", p3, videoStatusCompleted, 100, true, false)
	if len(p3.Content) != 1 {
		t.Fatalf("poll #3 content entries = %d, want 1", len(p3.Content))
	}
	locator := p3.Content[0]
	if locator.Status != videoStatusCompleted || locator.Type != "video/mp4" {
		t.Fatalf("poll #3 content locator = %+v", locator)
	}
	if !strings.HasSuffix(locator.URL, "/v1/videos/"+id+"/content") {
		t.Fatalf("poll #3 content url = %q, want the /content download path", locator.URL)
	}

	dcode, dheader, draw := downloadContent(t, m, id)
	if dcode != http.StatusOK {
		t.Fatalf("content status = %d, want 200", dcode)
	}
	if got := dheader.Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("content type = %q, want video/mp4", got)
	}
	if !bytes.Equal(draw[4:8], []byte("ftyp")) {
		t.Fatalf("content payload missing the ftyp box magic at offset 4: % X", draw[:min(8, len(draw))])
	}

	// Terminal states are idempotent: a fourth poll returns the same object.
	_, p4, _ := pollVideo(t, m, id)
	assertVideoState(t, "poll #4 (terminal idempotence)", p4, videoStatusCompleted, 100, true, false)
}

// TestMediaVideoOKPoll1FastPath covers the fast path (first poll is already
// terminal) and proves the poll request's own scenario header cannot change
// the script (a chat_ok header on the poll still returns the job state).
func TestMediaVideoOKPoll1FastPath(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVideo(t, m, string(ScenarioMediaVideoOKPoll1))
	assertVideoState(t, "create", obj, videoStatusQueued, 0, false, false)

	// Poll carrying an unrelated scenario header: the script comes from the
	// creating request only.
	code, _, raw := doMedia(t, m.Server, http.MethodGet, "/v1/videos/"+obj.ID, string(ScenarioChatOK), "", nil)
	if code != http.StatusOK {
		t.Fatalf("poll with chat_ok header status = %d (%s)", code, raw)
	}
	var p1 parsedVideoObject
	if err := json.Unmarshal(raw, &p1); err != nil {
		t.Fatalf("poll body: %v (%s)", err, raw)
	}
	assertVideoState(t, "poll #1", p1, videoStatusCompleted, 100, true, false)

	dcode, dheader, draw := downloadContent(t, m, obj.ID)
	if dcode != http.StatusOK || dheader.Get("Content-Type") != "video/mp4" || !bytes.Equal(draw, mp4Payload()) {
		t.Fatalf("fast-path content = %d/%q, want 200/video/mp4 with the built-in payload", dcode, dheader.Get("Content-Type"))
	}

	// Defaulting: a creation without a videos scenario (chat_ok / none) falls
	// back to the family OK script ok_poll3, mirroring the audio defaulting.
	def := createVideo(t, m, string(ScenarioChatOK))
	assertVideoState(t, "default create", def, videoStatusQueued, 0, false, false)
	_, dp1, _ := pollVideo(t, m, def.ID)
	assertVideoState(t, "default poll #1", dp1, videoStatusInProgress, 33, false, false)
}

// TestMediaVideoFailAfterAccept covers the accepted-then-failed script:
// in_progress(50) on poll #1, failed with an error code/message on poll #2,
// idempotent afterwards, and no downloadable content.
func TestMediaVideoFailAfterAccept(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVideo(t, m, string(ScenarioMediaVideoFailAfterAccept))
	assertVideoState(t, "create", obj, videoStatusQueued, 0, false, false)

	_, p1, _ := pollVideo(t, m, obj.ID)
	assertVideoState(t, "poll #1", p1, videoStatusInProgress, 50, false, false)

	_, p2, _ := pollVideo(t, m, obj.ID)
	assertVideoState(t, "poll #2", p2, videoStatusFailed, 0, false, true)
	if p2.Error == nil || p2.Error.Code == "" || p2.Error.Message == "" {
		t.Fatalf("failed error object must carry code and message: %+v", p2.Error)
	}

	_, p3, _ := pollVideo(t, m, obj.ID)
	assertVideoState(t, "poll #3 (terminal idempotence)", p3, videoStatusFailed, 0, false, true)

	if code, _, _ := downloadContent(t, m, obj.ID); code != http.StatusNotFound {
		t.Fatalf("failed job content status = %d, want 404", code)
	}
}

// TestMediaVideoCreateErrors covers the three pre-accept creation failures;
// none of them may register a job (the list stays empty).
func TestMediaVideoCreateErrors(t *testing.T) {
	m := New()
	defer m.Close()

	cases := []struct {
		scenario  string
		wantCode  int
		wantType  string
		wantCode2 string
	}{
		{string(ScenarioMediaVideo429Create), http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded"},
		{string(ScenarioMediaVideoCreate500), http.StatusInternalServerError, "server_error", ""},
		{string(ScenarioMediaVideoCreate400BadSize), http.StatusBadRequest, "invalid_request_error", "invalid_size"},
	}
	for _, tc := range cases {
		code, header, raw := doMedia(t, m.Server, http.MethodPost, "/v1/videos", tc.scenario, "application/json", []byte(videoCreateBody))
		if code != tc.wantCode {
			t.Fatalf("%s: status = %d, want %d (%s)", tc.scenario, code, tc.wantCode, raw)
		}
		message, errType, errCode := openaiErrorShape(t, raw)
		if message == "" || errType != tc.wantType || errCode != tc.wantCode2 {
			t.Fatalf("%s: error shape = %q/%q/%q", tc.scenario, message, errType, errCode)
		}
		if code == http.StatusTooManyRequests && header.Get("Retry-After") == "" {
			t.Fatalf("%s: missing Retry-After", tc.scenario)
		}
		if got := listVideos(t, m); len(got) != 0 {
			t.Fatalf("%s: job table must stay empty, got %d entries", tc.scenario, len(got))
		}
	}
}

// TestMediaVideoPoll500 covers the after-accept upstream 5xx script: every
// poll answers 500 while the job itself stays queued in the table (no fake
// terminal state — the account-switch / no-switch distinction case).
func TestMediaVideoPoll500(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVideo(t, m, string(ScenarioMediaVideoPoll500))
	assertVideoState(t, "create", obj, videoStatusQueued, 0, false, false)

	for poll := 1; poll <= 3; poll++ {
		code, _, raw := pollVideo(t, m, obj.ID)
		if code != http.StatusInternalServerError {
			t.Fatalf("poll #%d status = %d, want 500", poll, code)
		}
		if !strings.Contains(raw, "server_error") {
			t.Fatalf("poll #%d body missing server_error: %s", poll, raw)
		}
	}

	entries := listVideos(t, m)
	if len(entries) != 1 || entries[0].Status != videoStatusQueued {
		t.Fatalf("poll_500 job must stay queued in the table, got %+v", entries)
	}
	if code, _, _ := downloadContent(t, m, obj.ID); code != http.StatusNotFound {
		t.Fatalf("poll_500 job content status = %d, want 404", code)
	}
}

// TestMediaVideoContentExpired covers the expired-artifact case: the job
// completes normally (content locator included) but the content endpoint
// answers 404.
func TestMediaVideoContentExpired(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVideo(t, m, string(ScenarioMediaVideoContentExpired))
	_, p1, _ := pollVideo(t, m, obj.ID)
	assertVideoState(t, "poll #1", p1, videoStatusCompleted, 100, true, false)

	code, _, raw := downloadContent(t, m, obj.ID)
	if code != http.StatusNotFound {
		t.Fatalf("expired content status = %d, want 404", code)
	}
	if _, errType, errCode := openaiErrorShape(t, raw); errType != "invalid_request_error" || errCode != "content_not_found" {
		t.Fatalf("expired content error shape = %q/%q", errType, errCode)
	}
}

// TestMediaVideoCancelAndNotFound covers the cancel flow (DELETE → 204, then
// GET of the id is 404, and the job disappears from the list) and the 404
// family for unknown ids on every subresource.
func TestMediaVideoCancelAndNotFound(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVideo(t, m, string(ScenarioMediaVideoCancelOK))
	assertVideoState(t, "create", obj, videoStatusQueued, 0, false, false)
	// The cancel scenario's job stays queued while it exists.
	_, p1, _ := pollVideo(t, m, obj.ID)
	assertVideoState(t, "poll before delete", p1, videoStatusQueued, 0, false, false)

	code, _, raw := doMedia(t, m.Server, http.MethodDelete, "/v1/videos/"+obj.ID, "", "", nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 (%s)", code, raw)
	}
	if len(raw) != 0 {
		t.Fatalf("delete body = %q, want empty", raw)
	}
	pcode, _, praw := pollVideo(t, m, obj.ID)
	if pcode != http.StatusNotFound {
		t.Fatalf("poll after delete status = %d, want 404 (%s)", pcode, praw)
	}
	if _, _, errCode := openaiErrorShape(t, []byte(praw)); errCode != "video_not_found" {
		t.Fatalf("poll after delete code = %q, want video_not_found", errCode)
	}
	if code, _, _ := downloadContent(t, m, obj.ID); code != http.StatusNotFound {
		t.Fatalf("content after delete status = %d, want 404", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodDelete, "/v1/videos/"+obj.ID, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", code)
	}
	if got := listVideos(t, m); len(got) != 0 {
		t.Fatalf("cancelled job must leave the list, got %d entries", len(got))
	}

	// Unknown id family: GET, content and DELETE all answer the OpenAI 404.
	unknown := "video_0000000000000000"
	if code, _, raw := pollVideo(t, m, unknown); code != http.StatusNotFound || !strings.Contains(raw, unknown) {
		t.Fatalf("unknown id poll = %d (%s), want 404 echoing the id", code, raw)
	}
	if code, _, _ := downloadContent(t, m, unknown); code != http.StatusNotFound {
		t.Fatalf("unknown id content status = %d, want 404", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodDelete, "/v1/videos/"+unknown, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("unknown id delete status = %d, want 404", code)
	}
}

// TestMediaVideoListAndConcurrentIsolation creates two jobs concurrently on
// one instance and proves the scripts stay isolated: distinct ids, both in
// the list, each advancing on its own script regardless of interleaving.
func TestMediaVideoListAndConcurrentIsolation(t *testing.T) {
	m := New()
	defer m.Close()

	// Plain request helper without t.Fatal: safe to call from goroutines.
	doCreate := func(scenario string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, m.URL+"/v1/videos", strings.NewReader(videoCreateBody))
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Mock-Scenario", scenario)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, nil
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}

	const workers = 2
	codes := make([]int, workers)
	bodies := make([][]byte, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			scenario := string(ScenarioMediaVideoOKPoll3)
			if i == 1 {
				scenario = string(ScenarioMediaVideoOKPoll1)
			}
			codes[i], bodies[i] = doCreate(scenario)
		}(i)
	}
	wg.Wait()

	objs := make([]parsedVideoObject, workers)
	for i := 0; i < workers; i++ {
		if codes[i] != http.StatusOK {
			t.Fatalf("concurrent create #%d status = %d", i, codes[i])
		}
		if err := json.Unmarshal(bodies[i], &objs[i]); err != nil {
			t.Fatalf("concurrent create #%d body: %v (%s)", i, err, bodies[i])
		}
	}
	if objs[0].ID == objs[1].ID {
		t.Fatalf("concurrent jobs share an id: %q", objs[0].ID)
	}

	entries := listVideos(t, m)
	if len(entries) != workers {
		t.Fatalf("list entries = %d, want %d", len(entries), workers)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		seen[entry.ID] = true
	}
	for i := range objs {
		if !seen[objs[i].ID] {
			t.Fatalf("list missing job %q", objs[i].ID)
		}
	}

	// Interleaved polls: the ok_poll1 job reaches completed on its first
	// poll while the ok_poll3 job is still in_progress(33) — no crosstalk.
	_, fast, _ := pollVideo(t, m, objs[1].ID)
	assertVideoState(t, "ok_poll1 job poll #1", fast, videoStatusCompleted, 100, true, false)
	_, slow, _ := pollVideo(t, m, objs[0].ID)
	assertVideoState(t, "ok_poll3 job poll #1", slow, videoStatusInProgress, 33, false, false)
}

// TestMediaVideoPayloadStructure asserts the built-in MP4 container bytes:
// the ISO-BMFF magic (size-prefixed ftyp at offset 0), brand block, and the
// mdat box with size consistency.
func TestMediaVideoPayloadStructure(t *testing.T) {
	payload := mp4Payload()
	if len(payload) != 72 {
		t.Fatalf("payload length = %d, want 72", len(payload))
	}
	if got := binary.BigEndian.Uint32(payload[0:4]); got != mp4FTYPSize {
		t.Fatalf("ftyp box size = %d, want %d", got, mp4FTYPSize)
	}
	for _, want := range []struct {
		at   int
		text string
	}{
		{4, "ftyp"}, {8, "isom"}, {16, "isom"}, {20, "iso2"}, {24, "avc1"}, {28, "mp41"}, {36, "mdat"},
	} {
		if string(payload[want.at:want.at+4]) != want.text {
			t.Fatalf("box marker at %d = %q, want %q", want.at, payload[want.at:want.at+4], want.text)
		}
	}
	if got := binary.BigEndian.Uint32(payload[12:16]); got != 0 {
		t.Fatalf("ftyp minor version = %d, want 0", got)
	}
	if got := binary.BigEndian.Uint32(payload[32:36]); got != 40 {
		t.Fatalf("mdat box size = %d, want 40", got)
	}
	for i, b := range payload[40:] {
		if b != byte(i) {
			t.Fatalf("mdat filler byte %d = 0x%02X, want the deterministic ramp", i, b)
		}
	}
}

// TestMediaVideoWhitelist proves the videos method+path matrix: only the
// five contract pairs are accepted, everything else keeps the 404 behavior.
func TestMediaVideoWhitelist(t *testing.T) {
	m := New()
	defer m.Close()

	bad := []struct {
		method, path string
	}{
		{http.MethodPut, "/v1/videos"},
		{http.MethodPatch, "/v1/videos"},
		{http.MethodDelete, "/v1/videos"},   // delete needs an id
		{http.MethodPost, "/v1/videos/abc"}, // create is the bare path only
		{http.MethodPost, "/v1/videos/abc/content"},
		{http.MethodDelete, "/v1/videos/abc/content"}, // content is GET-only
		{http.MethodPut, "/v1/videos/abc"},
		{http.MethodGet, "/v1/videos/abc/content/extra"},
		{http.MethodGet, "/v1/videos/"},         // empty id
		{http.MethodGet, "/v1/videos//content"}, // empty id before /content
		{http.MethodGet, "/v1/videos2"},
		{http.MethodPost, "/v2/videos"},
	}
	for _, tc := range bad {
		code, _, _ := doMedia(t, m.Server, tc.method, tc.path, string(ScenarioMediaVideoOKPoll1), "application/json", []byte(`{}`))
		if code != http.StatusNotFound {
			t.Fatalf("%s %s: status = %d, want 404", tc.method, tc.path, code)
		}
	}

	// Accepted pairs (unknown job id still answers the endpoint-level 404,
	// not the whitelist 404 — distinguishable by the error code).
	if code, _, raw := doMedia(t, m.Server, http.MethodGet, "/v1/videos/unknown-id", "", "", nil); code != http.StatusNotFound || !strings.Contains(string(raw), "video_not_found") {
		t.Fatalf("GET unknown id = %d (%s), want the endpoint 404", code, raw)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodGet, "/v1/videos", "", "", nil); code != http.StatusOK {
		t.Fatalf("GET empty list status = %d, want 200", code)
	}
}

// TestMediaVideoRequestRecording proves Requests() keeps the full request
// triple across the create → poll → download chain.
func TestMediaVideoRequestRecording(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVideo(t, m, string(ScenarioMediaVideoOKPoll1))
	pollVideo(t, m, obj.ID)
	downloadContent(t, m, obj.ID)

	reqs := m.Requests()
	if len(reqs) != 3 {
		t.Fatalf("recorded %d requests, want 3", len(reqs))
	}
	want := []struct {
		method, path string
	}{
		{http.MethodPost, "/v1/videos"},
		{http.MethodGet, "/v1/videos/" + obj.ID},
		{http.MethodGet, "/v1/videos/" + obj.ID + "/content"},
	}
	for i, w := range want {
		if reqs[i].Method != w.method || reqs[i].Path != w.path {
			t.Fatalf("request #%d = %s %s, want %s %s", i, reqs[i].Method, reqs[i].Path, w.method, w.path)
		}
		if reqs[i].AuthHeader != "Bearer test-key" {
			t.Fatalf("request #%d missing auth header", i)
		}
	}
	if reqs[0].Model != "sora-2" {
		t.Fatalf("create recording model = %q, want sora-2", reqs[0].Model)
	}
	if !strings.Contains(reqs[0].Body, `"prompt":"a mock clip"`) {
		t.Fatalf("create recording missing prompt: %q", reqs[0].Body)
	}
}
