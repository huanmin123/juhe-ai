package mockupstream

// Volcengine (Doubao Seedance) async video task face for the M3 media chain,
// per docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension,
// scenario naming media_<provider>_<behavior>) and §9.1 (Seedance wire
// contract, confidence B):
//
//   - POST /api/v3/contents/generations/tasks              create
//     (200 {"id":"cgt-<16hex>","status":"queued"} — the acceptance
//     credential is the id field)
//   - GET  /api/v3/contents/generations/tasks/{id}          poll
//     ({"id","status":"queued|running|succeeded|failed",
//     "content":{"video_url":...} | "error":{code,message}})
//   - GET  /api/v3/contents/generations/tasks/{id}/content  artifact download
//     (video/mp4 binary channel; the succeeded video_url points here)
//
// The task table mirrors the OpenAI videos / gemini veo / glm cogvideo /
// minimax hailuo task tables: creation registers the task under a generated
// id with the creating scenario as the frozen behavior script; polls advance
// the "poll #N returns state X" script and ignore the poll request's own
// scenario header (contract §3.2.2). Volcengine has no cancel endpoint on the
// contract §9.1 face (M3 adjudication: SupportsCancel=false) so the face
// exposes no cancel route. Scripts (poll #1 is the first GET after creation):
//
//	media_volcengine_create_ok:            #1 queued → #2 running → #3+ succeeded + video_url
//	media_volcengine_poll_running:         never completes (stuck case)
//	media_volcengine_poll_succeeded_video_url: #1 succeeded + video_url (fast path)
//	media_volcengine_poll_failed_error:    #1 failed + error{code,message}
//
// Unknown scenarios default to the family OK script (create_ok), mirroring
// the chat_ok / video ok_poll3 defaulting philosophy.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// M3 volcengine scenario names (contract §3.2.4 M3 list, §9.1 Mock row); M6
// adds the sync TTS pair (contract §9.2 Mock rows).
const (
	ScenarioMediaVolcengineCreateOK         Scenario = "media_volcengine_create_ok"
	ScenarioMediaVolcenginePollRunning      Scenario = "media_volcengine_poll_running"
	ScenarioMediaVolcenginePollSucceededURL Scenario = "media_volcengine_poll_succeeded_video_url"
	ScenarioMediaVolcenginePollFailedError  Scenario = "media_volcengine_poll_failed_error"
	ScenarioMediaVolcengineTTSOK            Scenario = "media_volcengine_tts_ok"
	ScenarioMediaVolcengineTTSFailed        Scenario = "media_volcengine_tts_failed"
)

// volcengineVideoTask is one registered volcengine async video task. The id
// is the acceptance credential; the terminal video_url renders at response
// time from the server URL so the gateway's direct download points back at
// this instance.
type volcengineVideoTask struct {
	id        string
	scenario  Scenario // behavior script, frozen at creation
	pollCount int
	done      bool
	failed    bool
}

// volcengineTaskPathID returns the {id} segment when the path matches the
// volcengine task form /api/v3/contents/generations/tasks/{id} (non-empty
// single segment, optionally with the /content tail), "" otherwise.
// hasContent reports the artifact download tail.
func volcengineTaskPathID(path string) (id string, hasContent bool) {
	const prefix = "/api/v3/contents/generations/tasks"
	if !strings.HasPrefix(path, prefix+"/") {
		return "", false
	}
	rest := strings.TrimPrefix(path, prefix+"/")
	if rest == "" {
		return "", false
	}
	if taskID, ok := strings.CutSuffix(rest, "/content"); ok && taskID != "" && !strings.Contains(taskID, "/") {
		return taskID, true
	}
	if strings.Contains(rest, "/") {
		return "", false
	}
	return rest, false
}

// acceptsVolcengineVideoEndpoint whitelists the volcengine Seedance
// method+path shapes (POST create on the collection path, GET poll/content on
// the {id} paths; the collection path itself is not a GET list endpoint).
func acceptsVolcengineVideoEndpoint(method, path string) bool {
	if method == http.MethodPost && path == "/api/v3/contents/generations/tasks" {
		return true
	}
	if method != http.MethodGet {
		return false
	}
	id, _ := volcengineTaskPathID(path)
	return id != ""
}

// isVolcengineVideoPath reports whether the path belongs to the volcengine
// face. It only runs after the method-aware whitelist accepted the request.
func isVolcengineVideoPath(path string) bool {
	if path == "/api/v3/contents/generations/tasks" {
		return true
	}
	id, _ := volcengineTaskPathID(path)
	return id != ""
}

// serveVolcengineVideo dispatches an accepted volcengine request to its
// endpoint handler.
func (m *Server) serveVolcengineVideo(w http.ResponseWriter, r *http.Request, scenario Scenario) {
	if r.URL.Path == "/api/v3/contents/generations/tasks" && r.Method == http.MethodPost {
		m.serveVolcengineVideoCreate(w, scenario)
		return
	}
	id, hasContent := volcengineTaskPathID(r.URL.Path)
	switch {
	case hasContent:
		m.serveVolcengineVideoContent(w, id)
	default:
		m.serveVolcengineVideoPoll(w, id)
	}
}

// volcengineScriptFor normalizes the creating scenario into the task script.
// Unknown scenarios default to the family OK script (create_ok).
func volcengineScriptFor(scenario Scenario) Scenario {
	switch scenario {
	case ScenarioMediaVolcengineCreateOK, ScenarioMediaVolcenginePollRunning,
		ScenarioMediaVolcenginePollSucceededURL, ScenarioMediaVolcenginePollFailedError:
		return scenario
	default:
		return ScenarioMediaVolcengineCreateOK
	}
}

// newVolcengineTaskIDLocked generates an unused cgt-<16hex> task id (the
// contract §9.1 example shape). Callers must hold m.mu.
func (m *Server) newVolcengineTaskIDLocked() string {
	for {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err != nil {
			// crypto/rand failure is unrecoverable for id generation; fail
			// loudly instead of silently degrading.
			panic(fmt.Sprintf("mockupstream: volcengine task id generation failed: %v", err))
		}
		id := "cgt-" + hex.EncodeToString(buf[:])
		if _, exists := m.volcengineVideoTasks[id]; !exists {
			return id
		}
	}
}

// serveVolcengineVideoCreate implements POST /api/v3/contents/generations/tasks
// (contract §9.1): register the task under a fresh id with the scenario as
// its frozen script and answer 200 {id,status:"queued"} — the acceptance
// credential is the id field (the E2E assertion point). Pre-accept
// rejections reuse the engine-level generic fault scenarios (status_429
// etc.) per contract §3.2.4.
func (m *Server) serveVolcengineVideoCreate(w http.ResponseWriter, scenario Scenario) {
	m.mu.Lock()
	id := m.newVolcengineTaskIDLocked()
	m.volcengineVideoTasks[id] = &volcengineVideoTask{id: id, scenario: volcengineScriptFor(scenario)}
	body, _ := json.Marshal(map[string]string{"id": id, "status": "queued"})
	m.mu.Unlock()
	writeJSONStatus(w, http.StatusOK, string(body))
}

// volcengineArtifactURLLocked renders the completed download locator
// (contract §9.1: content.video_url, absolute URL pointing back at this
// instance so the gateway's credential-free direct GET lands on the content
// endpoint). Callers must hold m.mu (it reads m.URL).
func (m *Server) volcengineArtifactURLLocked(task *volcengineVideoTask) string {
	return m.URL + "/api/v3/contents/generations/tasks/" + task.id + "/content"
}

// volcengineTaskBodyLocked renders the task object for the current state.
// Callers must hold m.mu.
func (m *Server) volcengineTaskBodyLocked(task *volcengineVideoTask) string {
	switch {
	case task.failed:
		return fmt.Sprintf(`{"id":%q,"status":"failed","error":{"code":"InternalServiceError","message":"mock video generation failed"}}`, task.id)
	case task.done:
		url, _ := json.Marshal(m.volcengineArtifactURLLocked(task))
		return fmt.Sprintf(`{"id":%q,"status":"succeeded","content":{"video_url":%s}}`, task.id, string(url))
	case task.pollCount <= 1:
		return fmt.Sprintf(`{"id":%q,"status":"queued"}`, task.id)
	default:
		return fmt.Sprintf(`{"id":%q,"status":"running"}`, task.id)
	}
}

// advanceVolcengineVideoTaskLocked steps the task script for one poll
// (contract §3.2.2; terminal states are idempotent). Callers must hold m.mu.
func (m *Server) advanceVolcengineVideoTaskLocked(task *volcengineVideoTask) {
	if task.done {
		return
	}
	task.pollCount++
	switch task.scenario {
	case ScenarioMediaVolcenginePollRunning:
		// Never completes: every poll keeps running.
	case ScenarioMediaVolcenginePollSucceededURL:
		task.done = true
	case ScenarioMediaVolcenginePollFailedError:
		task.done = true
		task.failed = true
	default: // media_volcengine_create_ok and the normalized default script
		if task.pollCount >= 3 {
			task.done = true
		}
	}
}

// serveVolcengineVideoPoll implements GET /api/v3/contents/generations/
// tasks/{id}: unknown ids answer 404 (volcengine error envelope); otherwise
// the frozen script advances once and the task object is returned.
func (m *Server) serveVolcengineVideoPoll(w http.ResponseWriter, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.volcengineVideoTasks[id]
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, `{"error":{"code":"NotFound","message":"task not found"}}`)
		return
	}
	m.advanceVolcengineVideoTaskLocked(task)
	writeJSONStatus(w, http.StatusOK, m.volcengineTaskBodyLocked(task))
}

// serveVolcengineVideoContent implements the artifact download endpoint: the
// mp4 binary channel is open only for done, non-failed tasks (the video_url
// the succeeded poll returned); everything else (unknown id, queued/running,
// failed) answers 404.
func (m *Server) serveVolcengineVideoContent(w http.ResponseWriter, id string) {
	m.mu.Lock()
	task, ok := m.volcengineVideoTasks[id]
	downloadable := ok && task.done && !task.failed
	m.mu.Unlock()
	if !downloadable {
		writeJSONStatus(w, http.StatusNotFound, `{"error":{"code":"NotFound","message":"video content not found or expired"}}`)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mp4Payload())
}

// serveVolcengineTTS implements POST /api/v3/tts (contract §9.2, M6): the OK
// scenario answers HTTP 200 {"reqid","code":3000,"message","data":<base64>} —
// data carries a payload selected by the request's audio_params.format
// (mp3 default / pcm / ogg_opus / wav — the adapter MUST base64-decode before
// forwarding; the chain tests assert the decoded magic bytes). The failure
// scenario answers 200 with code 3001 (contract: code!=3000 即失败——the
// gateway surfaces it as an upstream protocol error, not a silent audio
// passthrough). Out-of-word-list formats 400 like the real upstream.
func (m *Server) serveVolcengineTTS(w http.ResponseWriter, idx int, scenario Scenario) {
	// Read the recorded body under the Server mutex: concurrent requests
	// append to m.seenReqs and an unlocked read races the writer.
	m.mu.Lock()
	requestBody := m.seenReqs[idx].Body
	m.mu.Unlock()
	var req struct {
		ReqParams struct {
			Text    string `json:"text"`
			Speaker string `json:"speaker"`
			AudioParams struct {
				Format string `json:"format"`
			} `json:"audio_params"`
		} `json:"req_params"`
	}
	_ = json.Unmarshal([]byte(requestBody), &req)

	if scenario == ScenarioMediaVolcengineTTSFailed {
		writeJSONStatus(w, http.StatusOK, `{"reqid":"mock-volc-tts-failed","code":3001,"message":"mock tts synthesis failed","data":""}`)
		return
	}
	// media_volcengine_tts_ok (unknown scenarios default OK): base64(audio
	// payload) + code 3000. Format negotiation stays inside the §9.2 word
	// list (mp3 default / pcm / ogg_opus / wav).
	var payload []byte
	switch req.ReqParams.AudioParams.Format {
	case "", "mp3":
		payload = mp3Payload()
	case "wav":
		payload = wavPayload()
	case "pcm":
		payload = silencePCM()
	case "ogg_opus":
		payload = oggOpusPayload()
	default:
		writeJSONStatus(w, http.StatusBadRequest, `{"reqid":"mock-volc-tts-400","code":3002,"message":"audio_params.format not supported","data":""}`)
		return
	}
	body, _ := json.Marshal(map[string]any{
		"reqid":   "mock-volc-tts-ok",
		"code":    3000,
		"message": "success",
		"data":    base64.StdEncoding.EncodeToString(payload),
	})
	writeJSONStatus(w, http.StatusOK, string(body))
}
