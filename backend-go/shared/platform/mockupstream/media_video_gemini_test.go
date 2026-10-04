package mockupstream

// M3 gemini veo async operation face tests, per contract
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension)
// and §5.2 (Veo wire contract). Every scenario gets an end-to-end assertion
// chain (create → poll advance → terminal → content download where
// applicable), plus the cancel flow and the method/path whitelist.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// parsedVeoOperation mirrors the operation object fields the contract
// asserts (§5.2): name (acceptance credential), done, the completed
// generatedSamples uri and the failed error triplet.
type parsedVeoOperation struct {
	Name  string `json:"name"`
	Done  bool   `json:"done"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
	Response *struct {
		GenerateVideoResponse *struct {
			GeneratedSamples []struct {
				Video struct {
					URI string `json:"uri"`
				} `json:"video"`
			} `json:"generatedSamples"`
		} `json:"generateVideoResponse"`
	} `json:"response"`
}

const veoCreateBody = `{"instances":[{"prompt":"a mock clip"}],"parameters":{"aspectRatio":"16:9","resolution":"720p"}}`

const veoModel = "veo-3.0-generate-preview"

var veoNamePattern = regexp.MustCompile(`^models/veo-3\.0-generate-preview/operations/[0-9a-f]{12}$`)

// createVeo posts a creation request under the scenario and asserts the
// acceptance shape (200 + parsable operation with a well-formed name).
func createVeo(t *testing.T, m *Server, scenario string) parsedVeoOperation {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodPost,
		"/v1beta/models/"+veoModel+":predictLongRunning", scenario, "application/json", []byte(veoCreateBody))
	if code != http.StatusOK {
		t.Fatalf("veo create (%s) status = %d, want 200 (%s)", scenario, code, raw)
	}
	var obj parsedVeoOperation
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("veo create (%s) body: %v (%s)", scenario, err, raw)
	}
	if !veoNamePattern.MatchString(obj.Name) {
		t.Fatalf("veo create name = %q, want models/<model>/operations/<12 hex>", obj.Name)
	}
	if obj.Done {
		t.Fatalf("veo create (%s) done = true, want absent/false", scenario)
	}
	return obj
}

// pollVeo GETs the operation name (no scenario header: poll behavior is
// decided by the creating request's script).
func pollVeo(t *testing.T, m *Server, name string) (int, parsedVeoOperation, string) {
	t.Helper()
	code, _, raw := doMedia(t, m.Server, http.MethodGet, "/v1beta/"+name, "", "", nil)
	var obj parsedVeoOperation
	if len(raw) > 0 && code != http.StatusInternalServerError {
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("veo poll %s body: %v (%s)", name, err, raw)
		}
	}
	return code, obj, string(raw)
}

// downloadVeoArtifact fetches the /content tail of the operation path.
func downloadVeoArtifact(t *testing.T, m *Server, name string) (int, http.Header, []byte) {
	t.Helper()
	return doMedia(t, m.Server, http.MethodGet, "/v1beta/"+name+"/content", "", "", nil)
}

// TestMediaGeminiVeoCreateOKFullChain walks the family OK script end to end:
// create acceptance shape, poll #1 running, poll #2 completed with the uri
// locator, the credential-free binary download, and terminal idempotence.
func TestMediaGeminiVeoCreateOKFullChain(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVeo(t, m, string(ScenarioMediaGeminiVideoCreateOK))
	name := obj.Name

	code, p1, _ := pollVeo(t, m, name)
	if code != http.StatusOK {
		t.Fatalf("veo poll #1 status = %d", code)
	}
	if p1.Done {
		t.Fatalf("veo poll #1 done = true, want false (running)")
	}

	code, p2, raw2 := pollVeo(t, m, name)
	if code != http.StatusOK {
		t.Fatalf("veo poll #2 status = %d", code)
	}
	if !p2.Done || p2.Error != nil {
		t.Fatalf("veo poll #2 = done:%v error:%v, want done:true no error (%s)", p2.Done, p2.Error, raw2)
	}
	if p2.Response == nil || p2.Response.GenerateVideoResponse == nil || len(p2.Response.GenerateVideoResponse.GeneratedSamples) != 1 {
		t.Fatalf("veo poll #2 missing generateVideoResponse.generatedSamples: %s", raw2)
	}
	uri := p2.Response.GenerateVideoResponse.GeneratedSamples[0].Video.URI
	if !strings.HasPrefix(uri, m.URL+"/v1beta/models/"+veoModel+"/operations/") || !strings.HasSuffix(uri, "/content") {
		t.Fatalf("veo uri = %q, want the instance /content download path", uri)
	}

	// The uri downloads without credentials: a bare GET (no auth header) is
	// the gateway's artifact path (contract §5.2 GCS signed URL stand-in).
	request, err := http.NewRequest(http.MethodGet, uri, nil)
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

	// Terminal idempotence: a third poll returns the same done:true object.
	_, p3, _ := pollVeo(t, m, name)
	if !p3.Done || p3.Response == nil {
		t.Fatalf("veo poll #3 (terminal idempotence) drifted: done=%v", p3.Done)
	}
}

// TestMediaGeminiVeoScenarioScripts covers the remaining three scripts:
// poll_running never completes; poll_done_uri completes on the first poll
// (and the poll request's own scenario header cannot change the script);
// poll_error fails on the first poll with the error triplet and no
// downloadable artifact. Defaulting: an unknown creation scenario falls
// back to the family OK script.
func TestMediaGeminiVeoScenarioScripts(t *testing.T) {
	m := New()
	defer m.Close()

	// poll_running: stuck in queue forever.
	running := createVeo(t, m, string(ScenarioMediaGeminiVideoPollRunning))
	for poll := 1; poll <= 3; poll++ {
		_, obj, raw := pollVeo(t, m, running.Name)
		if obj.Done {
			t.Fatalf("poll_running poll #%d done = true (%s)", poll, raw)
		}
	}
	if code, _, _ := downloadVeoArtifact(t, m, running.Name); code != http.StatusNotFound {
		t.Fatalf("poll_running artifact status = %d, want 404", code)
	}

	// poll_done_uri: fast path; an unrelated scenario header on the poll
	// cannot change the frozen script.
	fast := createVeo(t, m, string(ScenarioMediaGeminiVideoPollDoneURI))
	code, _, raw := doMedia(t, m.Server, http.MethodGet, "/v1beta/"+fast.Name, string(ScenarioChatOK), "", nil)
	if code != http.StatusOK {
		t.Fatalf("poll with chat_ok header status = %d (%s)", code, raw)
	}
	var p1 parsedVeoOperation
	if err := json.Unmarshal(raw, &p1); err != nil {
		t.Fatalf("poll_done_uri poll body: %v (%s)", err, raw)
	}
	if !p1.Done || p1.Response == nil || p1.Response.GenerateVideoResponse == nil {
		t.Fatalf("poll_done_uri poll #1 = %s, want done:true + uri", raw)
	}
	if dcode, dheader, draw := downloadVeoArtifact(t, m, fast.Name); dcode != http.StatusOK || dheader.Get("Content-Type") != "video/mp4" {
		t.Fatalf("poll_done_uri artifact = %d/%q (%s)", dcode, dheader.Get("Content-Type"), draw)
	}

	// poll_error: first poll is the failed terminal state.
	failed := createVeo(t, m, string(ScenarioMediaGeminiVideoPollError))
	_, f1, fraw := pollVeo(t, m, failed.Name)
	if !f1.Done || f1.Error == nil {
		t.Fatalf("poll_error poll #1 = %s, want done:true + error", fraw)
	}
	if f1.Error.Message == "" || f1.Error.Status == "" {
		t.Fatalf("poll_error error triplet incomplete: %+v", f1.Error)
	}
	if code, _, _ := downloadVeoArtifact(t, m, failed.Name); code != http.StatusNotFound {
		t.Fatalf("poll_error artifact status = %d, want 404", code)
	}

	// Defaulting: an unknown scenario (chat_ok) falls back to create_ok.
	def := createVeo(t, m, string(ScenarioChatOK))
	if _, d, draw := pollVeo(t, m, def.Name); d.Done {
		t.Fatalf("default script poll #1 = %s, want running (create_ok #1 stays done:false)", draw)
	}
}

// TestMediaGeminiVeoCancelAndNotFound covers the cancel flow (POST :cancel
// → 200 {}, then GET of the name is the Gemini 404 and the artifact 404s)
// and the 404 family for unknown names on every subresource.
func TestMediaGeminiVeoCancelAndNotFound(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVeo(t, m, string(ScenarioMediaGeminiVideoPollDoneURI))
	// Cancel before completion: the operation is removed regardless of state.
	code, _, raw := doMedia(t, m.Server, http.MethodPost, "/v1beta/"+obj.Name+":cancel", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("veo cancel status = %d, want 200 (%s)", code, raw)
	}
	if strings.TrimSpace(string(raw)) != "{}" {
		t.Fatalf("veo cancel body = %q, want {}", raw)
	}
	if pcode, _, praw := pollVeo(t, m, obj.Name); pcode != http.StatusNotFound || !strings.Contains(praw, "NOT_FOUND") {
		t.Fatalf("poll after cancel = %d (%s), want the Gemini 404", pcode, praw)
	}
	if code, _, _ := downloadVeoArtifact(t, m, obj.Name); code != http.StatusNotFound {
		t.Fatalf("artifact after cancel status = %d, want 404", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodPost, "/v1beta/"+obj.Name+":cancel", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("second cancel status = %d, want 404", code)
	}

	// Unknown name family: poll, cancel and artifact all answer 404.
	unknown := "models/" + veoModel + "/operations/000000000000"
	if code, _, _ := pollVeo(t, m, unknown); code != http.StatusNotFound {
		t.Fatalf("unknown poll status = %d, want 404", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodPost, "/v1beta/"+unknown+":cancel", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("unknown cancel status = %d, want 404", code)
	}
	if code, _, _ := downloadVeoArtifact(t, m, unknown); code != http.StatusNotFound {
		t.Fatalf("unknown artifact status = %d, want 404", code)
	}
}

// TestMediaGeminiVeoWhitelist proves the veo method+path matrix: only the
// four contract shapes are accepted, everything else keeps the 404 behavior.
func TestMediaGeminiVeoWhitelist(t *testing.T) {
	m := New()
	defer m.Close()

	bad := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1beta/models/" + veoModel + ":predictLongRunning"},      // create is POST-only
		{http.MethodPost, "/v1beta/models/" + veoModel + "/operations/abc"},         // poll is GET-only
		{http.MethodDelete, "/v1beta/models/" + veoModel + "/operations/abc"},       // openai-style delete is not the veo cancel
		{http.MethodGet, "/v1beta/models/" + veoModel + "/operations/abc:cancel"},   // cancel is POST-only
		{http.MethodPost, "/v1beta/models/" + veoModel + "/operations/abc/content"}, // artifact is GET-only
		{http.MethodGet, "/v1beta/models/" + veoModel + "/operations/"},             // empty op id
		{http.MethodGet, "/v1beta/models/" + veoModel + "/operations/a/b"},          // multi-segment op id
		{http.MethodPost, "/v1beta/models/:predictLongRunning"},                     // empty model
		{http.MethodPost, "/v1beta/models/a/b:predictLongRunning"},                  // multi-segment model
		{http.MethodGet, "/v2beta/models/" + veoModel + "/operations/abc"},
	}
	for _, tc := range bad {
		code, _, _ := doMedia(t, m.Server, tc.method, tc.path, string(ScenarioMediaGeminiVideoPollDoneURI), "application/json", []byte(`{}`))
		if code != http.StatusNotFound {
			t.Fatalf("%s %s: status = %d, want 404", tc.method, tc.path, code)
		}
	}
}

// TestMediaGeminiVeoRequestRecording proves Requests() keeps the full
// request triple across the create → poll → download chain.
func TestMediaGeminiVeoRequestRecording(t *testing.T) {
	m := New()
	defer m.Close()

	obj := createVeo(t, m, string(ScenarioMediaGeminiVideoPollDoneURI))
	pollVeo(t, m, obj.Name)
	code, _, _ := downloadVeoArtifact(t, m, obj.Name)
	if code != http.StatusOK {
		t.Fatalf("artifact status = %d, want 200", code)
	}

	reqs := m.Requests()
	if len(reqs) != 3 {
		t.Fatalf("recorded %d requests, want 3", len(reqs))
	}
	want := []struct {
		method, path string
	}{
		{http.MethodPost, "/v1beta/models/" + veoModel + ":predictLongRunning"},
		{http.MethodGet, "/v1beta/" + obj.Name},
		{http.MethodGet, "/v1beta/" + obj.Name + "/content"},
	}
	for i, w := range want {
		if reqs[i].Method != w.method || reqs[i].Path != w.path {
			t.Fatalf("request #%d = %s %s, want %s %s", i, reqs[i].Method, reqs[i].Path, w.method, w.path)
		}
	}
	if reqs[0].AuthHeader != "Bearer test-key" {
		t.Fatalf("request #0 missing auth header: %q", reqs[0].AuthHeader)
	}
	if !strings.Contains(reqs[0].Body, `"prompt":"a mock clip"`) {
		t.Fatalf("create recording missing prompt: %q", reqs[0].Body)
	}
}
