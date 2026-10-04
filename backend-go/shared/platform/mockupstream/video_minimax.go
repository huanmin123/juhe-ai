package mockupstream

// MiniMax async video task face and synchronous TTS face for the M3 media
// chain, per docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine
// extension, scenario naming media_<provider>_<behavior>) and §8 (MiniMax
// wire contract, confidence B):
//
//   - POST /v1/video_generation                  create
//     (200 {"task_id":"<hex>","base_resp":{"status_code":0,"status_msg":""}} —
//     the acceptance credential is the task_id field)
//   - GET  /v1/query/video_generation?task_id=   poll
//     ({"task_id","status":"Preparing|Queueing|Processing|Success|Fail",
//     "file_id","file_download_url"}; Fail carries a non-zero base_resp)
//   - GET  /v1/files/retrieve?file_id=           artifact download
//     (video/mp4 binary channel; Success renders file_download_url pointing
//     here so the gateway's credential-free direct GET lands on this instance)
//   - POST /v1/t2a_v2                            synchronous TTS
//     ({"data":{"audio":"<hex mp4-ish payload>","status":2},
//     "extra_info":{"usage_characters":N,...}} — the hex payload must be
//     decoded by the adapter, asserted via magic bytes)
//
// The task table mirrors the OpenAI videos / gemini veo / glm cogvideo task
// tables: creation registers the task under a generated id with the creating
// scenario as the frozen behavior script; polls advance the "poll #N returns
// state X" script and ignore the poll request's own scenario header
// (contract §3.2.2). MiniMax has no cancel API (contract §8.1 has no cancel
// endpoint) so the face exposes no cancel route. Scripts (poll #1 is the
// first GET after creation):
//
//	media_minimax_create_ok:      #1 Preparing → #2 Processing → #3+ Success + file_download_url
//	media_minimax_poll_running:   never completes (stuck case)
//	media_minimax_poll_success:   #1 Success + file_download_url (fast path)
//	media_minimax_poll_fail:      #1 Fail + non-zero base_resp
//
// Unknown scenarios default to the family OK script (create_ok), mirroring
// the chat_ok / video ok_poll3 defaulting philosophy.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// M3 minimax scenario names (contract §3.2.4 M3 list, §8.1/§8.2 Mock rows).
const (
	ScenarioMediaMinimaxCreateOK    Scenario = "media_minimax_create_ok"
	ScenarioMediaMinimaxPollRunning Scenario = "media_minimax_poll_running"
	ScenarioMediaMinimaxPollSuccess Scenario = "media_minimax_poll_success"
	ScenarioMediaMinimaxPollFail    Scenario = "media_minimax_poll_fail"
	ScenarioMediaMinimaxTTSOK       Scenario = "media_minimax_tts_ok"
)

// minimaxVideoTask is one registered minimax async video task. The task_id is
// the acceptance credential; the terminal file_download_url renders at
// response time from the server URL so the gateway's direct download points
// back at this instance.
type minimaxVideoTask struct {
	id        string
	scenario  Scenario // behavior script, frozen at creation
	pollCount int
	done      bool
	failed    bool
}

// minimaxVideoTaskID extracts the task_id query parameter from the query
// string ("" when absent).
func minimaxVideoTaskID(query string) string {
	for _, pair := range strings.Split(query, "&") {
		key, value, _ := strings.Cut(pair, "=")
		if key == "task_id" && value != "" {
			return value
		}
	}
	return ""
}

// minimaxFileID extracts the file_id query parameter from the query string
// ("" when absent).
func minimaxFileID(query string) string {
	for _, pair := range strings.Split(query, "&") {
		key, value, _ := strings.Cut(pair, "=")
		if key == "file_id" && value != "" {
			return value
		}
	}
	return ""
}

// acceptsMinimaxVideoEndpoint whitelists the minimax method+path shapes
// (path only; the task_id/file_id query discrimination happens inside the
// handlers — a missing query parameter falls through to the same 404 as an
// unknown id).
func acceptsMinimaxVideoEndpoint(method, path string) bool {
	if method == http.MethodPost && (path == "/v1/video_generation" || path == "/v1/t2a_v2") {
		return true
	}
	if method != http.MethodGet {
		return false
	}
	return path == "/v1/query/video_generation" || path == "/v1/files/retrieve"
}

// isMinimaxVideoPath reports whether the path belongs to the minimax face.
// It only runs after the method-aware whitelist accepted the request.
func isMinimaxVideoPath(path string) bool {
	return path == "/v1/video_generation" || path == "/v1/query/video_generation" ||
		path == "/v1/files/retrieve" || path == "/v1/t2a_v2"
}

// serveMinimax dispatches an accepted minimax request to its endpoint
// handler.
func (m *Server) serveMinimax(w http.ResponseWriter, r *http.Request, idx int, scenario Scenario) {
	switch r.URL.Path {
	case "/v1/video_generation":
		m.serveMinimaxVideoCreate(w, scenario)
	case "/v1/query/video_generation":
		m.serveMinimaxVideoPoll(w, minimaxVideoTaskID(r.URL.RawQuery))
	case "/v1/files/retrieve":
		m.serveMinimaxFileRetrieve(w, minimaxFileID(r.URL.RawQuery))
	default: // /v1/t2a_v2
		m.serveMinimaxTTS(w, idx, scenario)
	}
}

// minimaxScriptFor normalizes the creating scenario into the task script.
// Unknown scenarios default to the family OK script (create_ok).
func minimaxScriptFor(scenario Scenario) Scenario {
	switch scenario {
	case ScenarioMediaMinimaxCreateOK, ScenarioMediaMinimaxPollRunning,
		ScenarioMediaMinimaxPollSuccess, ScenarioMediaMinimaxPollFail:
		return scenario
	default:
		return ScenarioMediaMinimaxCreateOK
	}
}

// newMinimaxTaskIDLocked generates an unused hex task id. Callers must hold
// m.mu.
func (m *Server) newMinimaxTaskIDLocked() string {
	for {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err != nil {
			// crypto/rand failure is unrecoverable for id generation; fail
			// loudly instead of silently degrading.
			panic(fmt.Sprintf("mockupstream: minimax task id generation failed: %v", err))
		}
		id := hex.EncodeToString(buf[:])
		if _, exists := m.minimaxVideoTasks[id]; !exists {
			return id
		}
	}
}

// serveMinimaxVideoCreate implements POST /v1/video_generation (contract
// §8.1): register the task under a fresh task_id with the scenario as its
// frozen script and answer 200 {task_id, base_resp:{status_code:0}} — the
// acceptance credential is the task_id field (the E2E assertion point).
// Pre-accept rejections reuse the engine-level generic fault scenarios
// (status_429 etc.) per contract §3.2.4; the OK face never answers a
// non-zero base_resp on creation.
func (m *Server) serveMinimaxVideoCreate(w http.ResponseWriter, scenario Scenario) {
	m.mu.Lock()
	id := m.newMinimaxTaskIDLocked()
	m.minimaxVideoTasks[id] = &minimaxVideoTask{id: id, scenario: minimaxScriptFor(scenario)}
	body, _ := json.Marshal(map[string]any{
		"task_id":   id,
		"base_resp": map[string]any{"status_code": 0, "status_msg": ""},
	})
	m.mu.Unlock()
	writeJSONStatus(w, http.StatusOK, string(body))
}

// minimaxArtifactURLLocked renders the completed download locator (contract
// §8.1: file_download_url, absolute URL pointing back at this instance so
// the gateway's credential-free direct GET lands on the files/retrieve
// endpoint). Callers must hold m.mu (it reads m.URL).
func (m *Server) minimaxArtifactURLLocked(task *minimaxVideoTask) string {
	return m.URL + "/v1/files/retrieve?file_id=" + task.id
}

// minimaxQueryBodyLocked renders the query/video_generation object for the
// current state. Callers must hold m.mu.
func (m *Server) minimaxQueryBodyLocked(task *minimaxVideoTask) string {
	switch {
	case task.failed:
		return fmt.Sprintf(`{"task_id":%q,"status":"Fail","base_resp":{"status_code":1004,"status_msg":"video generation failed"}}`, task.id)
	case task.done:
		url, _ := json.Marshal(m.minimaxArtifactURLLocked(task))
		return fmt.Sprintf(`{"task_id":%q,"status":"Success","file_id":%q,"file_download_url":%s}`,
			task.id, task.id, string(url))
	default:
		return fmt.Sprintf(`{"task_id":%q,"status":"%s"}`, task.id, minimaxRunningStatusOf(task.pollCount))
	}
}

// minimaxRunningStatusOf maps the poll count onto the not-yet-terminal
// status vocabulary (Preparing → Queueing → Processing; Processing from
// poll #3 on).
func minimaxRunningStatusOf(pollCount int) string {
	switch {
	case pollCount <= 1:
		return "Preparing"
	case pollCount == 2:
		return "Queueing"
	default:
		return "Processing"
	}
}

// advanceMinimaxVideoTaskLocked steps the task script for one poll (contract
// §3.2.2; terminal states are idempotent). Callers must hold m.mu.
func (m *Server) advanceMinimaxVideoTaskLocked(task *minimaxVideoTask) {
	if task.done {
		return
	}
	task.pollCount++
	switch task.scenario {
	case ScenarioMediaMinimaxPollRunning:
		// Never completes: every poll keeps a running status.
	case ScenarioMediaMinimaxPollSuccess:
		task.done = true
	case ScenarioMediaMinimaxPollFail:
		task.done = true
		task.failed = true
	default: // media_minimax_create_ok and the normalized default script
		if task.pollCount >= 3 {
			task.done = true
		}
	}
}

// serveMinimaxVideoPoll implements GET /v1/query/video_generation?task_id=:
// unknown ids answer 404 (minimax error envelope); otherwise the frozen
// script advances once and the query object is returned.
func (m *Server) serveMinimaxVideoPoll(w http.ResponseWriter, taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.minimaxVideoTasks[taskID]
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, `{"base_resp":{"status_code":1005,"status_msg":"task not exist"}}`)
		return
	}
	m.advanceMinimaxVideoTaskLocked(task)
	writeJSONStatus(w, http.StatusOK, m.minimaxQueryBodyLocked(task))
}

// serveMinimaxFileRetrieve implements the artifact download endpoint
// (contract §8.1): the mp4 binary channel is open only for done, non-failed
// tasks (the file_download_url the Success poll returned); everything else
// (unknown id, running, failed) answers 404.
func (m *Server) serveMinimaxFileRetrieve(w http.ResponseWriter, fileID string) {
	m.mu.Lock()
	task, ok := m.minimaxVideoTasks[fileID]
	downloadable := ok && task.done && !task.failed
	m.mu.Unlock()
	if !downloadable {
		writeJSONStatus(w, http.StatusNotFound, `{"base_resp":{"status_code":1006,"status_msg":"file not exist or expired"}}`)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mp4Payload())
}

// serveMinimaxTTS implements POST /v1/t2a_v2 (contract §8.2): the success
// scenario answers data.audio as a hex-encoded payload selected by the
// request's audio_setting.format (mp3/pcm/flac/wav — the adapter MUST
// hex-decode before forwarding; the E2E asserts the decoded magic bytes)
// plus the extra_info character metering (usage_characters is the reported
// TTS billing character count). Out-of-word-list formats 400 like the real
// upstream.
func (m *Server) serveMinimaxTTS(w http.ResponseWriter, idx int, scenario Scenario) {
	// Read the recorded body under the Server mutex: concurrent requests
	// append to m.seenReqs and an unlocked read races the writer.
	m.mu.Lock()
	requestBody := m.seenReqs[idx].Body
	m.mu.Unlock()
	var req struct {
		AudioSetting struct {
			Format string `json:"format"`
		} `json:"audio_setting"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(requestBody), &req)

	// media_minimax_tts_ok (unknown scenarios default OK, mirroring the
	// chat/tts default philosophy): hex(audio payload) + extra_info. The
	// usage_characters value deliberately deviates from the request text rune
	// count (+1) so gateway-side assertions can prove the reported-character
	// metering takes the upstream extra_info over the request-input self
	// count (contract §8.2/§2.8). Format negotiation stays inside the §8.2
	// word list (mp3 default / pcm / flac / wav).
	var payload []byte
	switch req.AudioSetting.Format {
	case "", "mp3":
		payload = mp3Payload()
	case "wav":
		payload = wavPayload()
	case "pcm":
		payload = silencePCM()
	case "flac":
		payload = flacPayload()
	default:
		writeJSONStatus(w, http.StatusBadRequest, `{"base_resp":{"status_code":1004,"status_msg":"audio_setting.format not supported"}}`)
		return
	}
	encoded := hex.EncodeToString(payload)
	usageCharacters := len([]rune(req.Text)) + 1
	body, _ := json.Marshal(map[string]any{
		"data": map[string]any{"audio": encoded, "status": 2},
		"extra_info": map[string]any{
			"audio_length":        3056,
			"usage_characters":    usageCharacters,
			"word_size":           usageCharacters,
			"audio_sampling_rate": 32000,
		},
		"trace_id": "mock-minimax-trace",
	})
	writeJSONStatus(w, http.StatusOK, string(body))
}
