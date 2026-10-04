package mockupstream

// Qwen (Alibaba Bailian DashScope / Wan) async video task face for the M3
// media chain, per docs/functions/媒体上游协议契约与Mock上游规格.md §3.2
// (engine extension, scenario naming media_<provider>_<behavior>) and §10.1
// (Wan video-synthesis wire contract, confidence B):
//
//   - POST /api/v1/services/aigc/video-generation/video-synthesis   create
//     (requires the DashScope async header X-DashScope-Async: enable — its
//     absence answers 400 "current user api does not support synchronous
//     calls", mirroring the real upstream; 200
//     {"output":{"task_id":"<uuid>","task_status":"PENDING"},
//     "request_id":"..."} — the acceptance credential is output.task_id)
//   - GET  /api/v1/tasks/{task_id}                                  poll
//     ({"output":{"task_status":"PENDING|RUNNING|SUCCEEDED|FAILED",
//     "video_url"} ,"usage":"<JSON string form>","code","message"} —
//     DashScope renders usage as a JSON *string*, per contract §10.1)
//   - GET  /api/v1/tasks/{task_id}/content                          artifact
//     download (video/mp4 binary channel; the succeeded video_url points
//     here)
//
// The task table mirrors the OpenAI videos / gemini veo / glm cogvideo /
// minimax hailuo / volcengine seedance task tables: creation registers the
// task under a generated uuid with the creating scenario as the frozen
// behavior script; polls advance the "poll #N returns state X" script and
// ignore the poll request's own scenario header (contract §3.2.2). Qwen has
// no cancel endpoint on the contract §10.1 face (M3 adjudication:
// SupportsCancel=false) so the face exposes no cancel route. Scripts (poll
// #1 is the first GET after creation):
//
//	media_qwen_create_pending:           #1 PENDING → #2 RUNNING → #3+ SUCCEEDED + video_url + usage string
//	media_qwen_poll_running:             never completes (stuck case)
//	media_qwen_poll_succeeded_video_url: #1 SUCCEEDED + video_url + usage string (fast path)
//	media_qwen_poll_failed_error:        #1 FAILED + top-level code/message
//
// Unknown scenarios default to the family OK script (create_pending),
// mirroring the chat_ok / video ok_poll3 defaulting philosophy.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// M3 qwen scenario names (contract §10.2 Mock row; poll_failed_error follows
// the §3.2.4 media_<provider>_* family template).
const (
	ScenarioMediaQwenCreatePending    Scenario = "media_qwen_create_pending"
	ScenarioMediaQwenPollRunning      Scenario = "media_qwen_poll_running"
	ScenarioMediaQwenPollSucceededURL Scenario = "media_qwen_poll_succeeded_video_url"
	ScenarioMediaQwenPollFailedError  Scenario = "media_qwen_poll_failed_error"
)

// qwenVideoTask is one registered qwen async video task. output.task_id is
// the acceptance credential; the terminal video_url renders at response time
// from the server URL so the gateway's direct download points back at this
// instance.
type qwenVideoTask struct {
	id        string
	scenario  Scenario // behavior script, frozen at creation
	pollCount int
	done      bool
	failed    bool
}

// qwenVideoSynthesisPath is the create collection path (contract §10.1).
const qwenVideoSynthesisPath = "/api/v1/services/aigc/video-generation/video-synthesis"

// qwenTaskPathID returns the {task_id} segment when the path matches the
// qwen task form /api/v1/tasks/{task_id} (non-empty single segment, optionally
// with the /content tail), "" otherwise. hasContent reports the artifact
// download tail.
func qwenTaskPathID(path string) (id string, hasContent bool) {
	const prefix = "/api/v1/tasks"
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

// acceptsQwenVideoEndpoint whitelists the qwen Wan method+path shapes (POST
// create on the video-synthesis path — the DashScope async header is asserted
// in the handler, GET poll/content on the /api/v1/tasks/{id} paths).
func acceptsQwenVideoEndpoint(method, path string) bool {
	if method == http.MethodPost && path == qwenVideoSynthesisPath {
		return true
	}
	if method != http.MethodGet {
		return false
	}
	id, _ := qwenTaskPathID(path)
	return id != ""
}

// isQwenVideoPath reports whether the path belongs to the qwen face. It only
// runs after the method-aware whitelist accepted the request.
func isQwenVideoPath(path string) bool {
	if path == qwenVideoSynthesisPath {
		return true
	}
	id, _ := qwenTaskPathID(path)
	return id != ""
}

// serveQwenVideo dispatches an accepted qwen request to its endpoint handler.
func (m *Server) serveQwenVideo(w http.ResponseWriter, r *http.Request, scenario Scenario) {
	if r.URL.Path == qwenVideoSynthesisPath && r.Method == http.MethodPost {
		m.serveQwenVideoCreate(w, r, scenario)
		return
	}
	id, hasContent := qwenTaskPathID(r.URL.Path)
	switch {
	case hasContent:
		m.serveQwenVideoContent(w, id)
	default:
		m.serveQwenVideoPoll(w, id)
	}
}

// qwenScriptFor normalizes the creating scenario into the task script.
// Unknown scenarios default to the family OK script (create_pending).
func qwenScriptFor(scenario Scenario) Scenario {
	switch scenario {
	case ScenarioMediaQwenCreatePending, ScenarioMediaQwenPollRunning,
		ScenarioMediaQwenPollSucceededURL, ScenarioMediaQwenPollFailedError:
		return scenario
	default:
		return ScenarioMediaQwenCreatePending
	}
}

// newQwenTaskIDLocked generates an unused <uuid> task id (the contract §10.1
// example shape, e.g. 0385dc79-5ff8-4d82-bcb6-...; canonical 8-4-4-4-12 hex
// layout). Callers must hold m.mu.
func (m *Server) newQwenTaskIDLocked() string {
	for {
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			// crypto/rand failure is unrecoverable for id generation; fail
			// loudly instead of silently degrading.
			panic(fmt.Sprintf("mockupstream: qwen task id generation failed: %v", err))
		}
		text := hex.EncodeToString(buf[:])
		id := text[:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
		if _, exists := m.qwenVideoTasks[id]; !exists {
			return id
		}
	}
}

// serveQwenVideoCreate implements POST /api/v1/services/aigc/video-generation/
// video-synthesis (contract §10.1): the DashScope async header
// X-DashScope-Async: enable is mandatory (its absence answers 400 with the
// upstream's synchronous-call rejection — the header is the E2E assertion
// point); otherwise the task is registered under a fresh task_id with the
// scenario as its frozen script and the creation envelope carries the
// acceptance fields {"output":{"task_id","task_status":"PENDING"},
// "request_id"}. Pre-accept rejections reuse the engine-level generic fault
// scenarios (status_429 etc.) per contract §3.2.4.
func (m *Server) serveQwenVideoCreate(w http.ResponseWriter, r *http.Request, scenario Scenario) {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("X-DashScope-Async")), "enable") {
		writeJSONStatus(w, http.StatusBadRequest, `{"code":"InvalidParameter","message":"current user api does not support synchronous calls","request_id":"mock-sync-rejected"}`)
		return
	}
	m.mu.Lock()
	id := m.newQwenTaskIDLocked()
	m.qwenVideoTasks[id] = &qwenVideoTask{id: id, scenario: qwenScriptFor(scenario)}
	body, _ := json.Marshal(map[string]any{
		"output":     map[string]string{"task_id": id, "task_status": "PENDING"},
		"request_id": "req-" + id[:8],
	})
	m.mu.Unlock()
	writeJSONStatus(w, http.StatusOK, string(body))
}

// qwenArtifactURLLocked renders the completed download locator (contract
// §10.1: output.video_url, absolute URL pointing back at this instance so the
// gateway's credential-free direct GET lands on the content endpoint). The
// real upstream serves an OSS signed URL with ~24h validity; the mock serves
// its own /content channel. Callers must hold m.mu (it reads m.URL).
func (m *Server) qwenArtifactURLLocked(task *qwenVideoTask) string {
	return m.URL + "/api/v1/tasks/" + task.id + "/content"
}

// qwenUsageStringLocked renders the contract §10.1 usage field in its
// DashScope-native JSON *string* form (string-in-JSON, double-encoded): the
// wan2.2-line verifiable shape carries video_duration/video_count (the
// wan2.5-and-below usage caliber per the official legacy API reference).
// Callers must hold m.mu.
func (m *Server) qwenUsageStringLocked() string {
	inner, _ := json.Marshal(map[string]any{"video_duration": 5, "video_count": 1})
	encoded, _ := json.Marshal(string(inner))
	return string(encoded)
}

// qwenTaskBodyLocked renders the task object for the current state. Callers
// must hold m.mu.
func (m *Server) qwenTaskBodyLocked(task *qwenVideoTask) string {
	requestID := "req-" + task.id[:8]
	switch {
	case task.failed:
		return fmt.Sprintf(`{"output":{"task_id":%q,"task_status":"FAILED"},"code":"InternalError","message":"mock wanx video generation failed","request_id":%q}`, task.id, requestID)
	case task.done:
		return fmt.Sprintf(`{"output":{"task_id":%q,"task_status":"SUCCEEDED","video_url":%s},"usage":%s,"request_id":%q}`,
			task.id, m.qwenArtifactJSONLocked(task), m.qwenUsageStringLocked(), requestID)
	case task.pollCount <= 1:
		return fmt.Sprintf(`{"output":{"task_id":%q,"task_status":"PENDING"},"request_id":%q}`, task.id, requestID)
	default:
		return fmt.Sprintf(`{"output":{"task_id":%q,"task_status":"RUNNING"},"request_id":%q}`, task.id, requestID)
	}
}

// qwenArtifactJSONLocked marshals the artifact URL for body rendering (keeps
// fmt verbs tidy). Callers must hold m.mu.
func (m *Server) qwenArtifactJSONLocked(task *qwenVideoTask) string {
	encoded, _ := json.Marshal(m.qwenArtifactURLLocked(task))
	return string(encoded)
}

// advanceQwenVideoTaskLocked steps the task script for one poll (contract
// §3.2.2; terminal states are idempotent). Callers must hold m.mu.
func (m *Server) advanceQwenVideoTaskLocked(task *qwenVideoTask) {
	if task.done {
		return
	}
	task.pollCount++
	switch task.scenario {
	case ScenarioMediaQwenPollRunning:
		// Never completes: every poll keeps PENDING/RUNNING.
	case ScenarioMediaQwenPollSucceededURL:
		task.done = true
	case ScenarioMediaQwenPollFailedError:
		task.done = true
		task.failed = true
	default: // media_qwen_create_pending and the normalized default script
		if task.pollCount >= 3 {
			task.done = true
		}
	}
}

// serveQwenVideoPoll implements GET /api/v1/tasks/{task_id}: unknown ids
// answer 404 (DashScope error envelope); otherwise the frozen script advances
// once and the task object is returned. The task interface is shared with the
// paraformer ASR face (contract §10.2) — the video table is consulted first,
// then the ASR table (ids are independent random uuids, no collisions).
func (m *Server) serveQwenVideoPoll(w http.ResponseWriter, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if task, ok := m.qwenVideoTasks[id]; ok {
		m.advanceQwenVideoTaskLocked(task)
		writeJSONStatus(w, http.StatusOK, m.qwenTaskBodyLocked(task))
		return
	}
	if asr, ok := m.qwenASRTasks[id]; ok {
		m.advanceQwenASRTaskLocked(asr)
		writeJSONStatus(w, http.StatusOK, m.qwenASRTaskBodyLocked(asr))
		return
	}
	writeJSONStatus(w, http.StatusNotFound, `{"code":"NotFound","message":"task not found"}`)
}

// serveQwenVideoContent implements the artifact download endpoint: the mp4
// binary channel is open only for done, non-failed video tasks (the video_url
// the succeeded poll returned); ASR tasks (shared /api/v1/tasks/{id}/content
// channel) serve their transcription-result JSON; everything else (unknown id,
// PENDING/RUNNING, failed) answers 404.
func (m *Server) serveQwenVideoContent(w http.ResponseWriter, id string) {
	m.mu.Lock()
	task, ok := m.qwenVideoTasks[id]
	downloadable := ok && task.done && !task.failed
	var asrResult []byte
	if !ok {
		if asr, asrOK := m.qwenASRTasks[id]; asrOK && asr.done && !asr.failed {
			asrResult = qwenASRResultJSON(asr.fileURL)
			downloadable = true
		}
	}
	m.mu.Unlock()
	if !downloadable {
		writeJSONStatus(w, http.StatusNotFound, `{"code":"NotFound","message":"video content not found or expired"}`)
		return
	}
	if asrResult != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(asrResult)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mp4Payload())
}
