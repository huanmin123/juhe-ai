package mockupstream

// Qwen (Alibaba Bailian DashScope / paraformer) async long-audio ASR task
// face for the M3f /v1/audio/jobs chain, per
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (scenario naming
// media_<provider>_<behavior>) and §10.2 (paraformer transcription wire
// contract, confidence B, backfilled 2026-10-04):
//
//   - POST /api/v1/services/audio/asr/transcription   create
//     (NO X-DashScope-Async header required — the service is natively async,
//     unlike the wanx video face; body must carry model + input.file_urls as
//     a non-empty string array, mirroring the upstream's parameter rejection;
//     200 {"output":{"task_id":"<uuid>","task_status":"PENDING"},
//     "request_id":"..."} — the acceptance credential is output.task_id)
//   - GET  /api/v1/tasks/{task_id}                     poll (SHARED with the
//     wanx video face — same DashScope task interface; this table is consulted
//     after the video table misses):
//     {"output":{"task_status":"PENDING|RUNNING|SUCCEEDED|FAILED",
//     "results":[{"transcription_url":"..."}],"message"},
//     "usage":"<JSON string form>","code","request_id"}; SUCCEEDED freezes the
//     transcription_url (a transcription-result JSON file link), FAILED carries
//     output.message, usage renders the DashScope JSON-string duration metering
//   - GET  /api/v1/tasks/{task_id}/content             artifact download
//     (application/json transcription-result channel; the succeeded
//     transcription_url points here)
//
// The task table mirrors the wanx video task table (scenario frozen at
// creation, polls advance the script, terminal states idempotent). Qwen has no
// cancel endpoint on the contract §10.2 face (M3f adjudication:
// SupportsCancel=false) so the face exposes no cancel route. Scripts (poll #1
// is the first GET after creation):
//
//	media_qwen_asr_create_pending:              #1 PENDING → #2 RUNNING → #3+ SUCCEEDED + transcription_url + usage string
//	media_qwen_asr_poll_running:                never completes (stuck case)
//	media_qwen_asr_poll_succeeded_transcription_url: #1 SUCCEEDED + transcription_url + usage string (fast path)
//	media_qwen_asr_poll_failed:                 #1 FAILED + output.message
//
// Unknown scenarios default to the family OK script (create_pending),
// mirroring the video face defaulting philosophy.

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// M3f qwen ASR scenario names (contract §10.2 Mock row).
const (
	ScenarioMediaQwenASRCreatePending Scenario = "media_qwen_asr_create_pending"
	ScenarioMediaQwenASRPollRunning   Scenario = "media_qwen_asr_poll_running"
	ScenarioMediaQwenASRPollSucceeded Scenario = "media_qwen_asr_poll_succeeded_transcription_url"
	ScenarioMediaQwenASRPollFailed    Scenario = "media_qwen_asr_poll_failed"
)

// qwenASRTask is one registered qwen paraformer async ASR task.
// output.task_id is the acceptance credential; fileURL echoes the creating
// request's input.file_urls[0] into the transcription result payload (the
// result JSON's file_url field, mirroring the real upstream); the terminal
// transcription_url renders at response time from the server URL so the
// gateway's direct download points back at this instance.
type qwenASRTask struct {
	id        string
	scenario  Scenario // behavior script, frozen at creation
	fileURL   string   // creating request's input.file_urls[0] (result echo)
	pollCount int
	done      bool
	failed    bool
}

// qwenASRTranscriptionPath is the create collection path (contract §10.2).
const qwenASRTranscriptionPath = "/api/v1/services/audio/asr/transcription"

// acceptsQwenASREndpoint whitelists the qwen paraformer create shape (POST on
// the transcription path; the /api/v1/tasks/{id} poll/content family is already
// whitelisted by the shared wanx video face).
func acceptsQwenASREndpoint(method, path string) bool {
	return method == http.MethodPost && path == qwenASRTranscriptionPath
}

// isQwenASRPath reports whether the path belongs to the qwen ASR create face.
func isQwenASRPath(path string) bool {
	return path == qwenASRTranscriptionPath
}

// qwenASRScriptFor normalizes the creating scenario into the task script.
// Unknown scenarios default to the family OK script (create_pending).
func qwenASRScriptFor(scenario Scenario) Scenario {
	switch scenario {
	case ScenarioMediaQwenASRCreatePending, ScenarioMediaQwenASRPollRunning,
		ScenarioMediaQwenASRPollSucceeded, ScenarioMediaQwenASRPollFailed:
		return scenario
	default:
		return ScenarioMediaQwenASRCreatePending
	}
}

// qwenASRCreateBody is the create request consumption face (contract §10.2):
// {model, input:{file_urls:[...]}, parameters:{language_hints?...}}.
type qwenASRCreateBody struct {
	Model string `json:"model"`
	Input struct {
		FileURLs []string `json:"file_urls"`
	} `json:"input"`
}

// serveQwenASRCreate implements POST /api/v1/services/audio/asr/transcription
// (contract §10.2): the body must be a JSON object carrying a non-empty
// model + input.file_urls string array (the only accepted input form — public
// URL array, no file upload; a missing/invalid field answers 400 mirroring the
// upstream's parameter rejection, which the gateway passes through as a
// deterministic parameter error); the task is registered under a fresh uuid
// task_id with the scenario as its frozen script and the creation envelope
// carries the acceptance fields {"output":{"task_id","task_status":"PENDING"},
// "request_id"}. The X-DashScope-Async header is NOT required (natively async
// service, the §10.1 wanx difference); its presence is ignored like the real
// upstream.
func (m *Server) serveQwenASRCreate(w http.ResponseWriter, r *http.Request, scenario Scenario) {
	var body qwenASRCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model == "" || len(body.Input.FileURLs) == 0 {
		writeJSONStatus(w, http.StatusBadRequest, `{"code":"InvalidParameter","message":"model and input.file_urls are required","request_id":"mock-asr-bad-request"}`)
		return
	}
	m.mu.Lock()
	id := m.newQwenTaskIDLocked()
	fileURL := body.Input.FileURLs[0]
	m.qwenASRTasks[id] = &qwenASRTask{id: id, scenario: qwenASRScriptFor(scenario), fileURL: fileURL}
	payload, _ := json.Marshal(map[string]any{
		"output":     map[string]string{"task_id": id, "task_status": "PENDING"},
		"request_id": "req-" + id[:8],
	})
	m.mu.Unlock()
	writeJSONStatus(w, http.StatusOK, string(payload))
}

// qwenASRArtifactURLLocked renders the completed download locator (contract
// §10.2: output.results[].transcription_url, absolute URL pointing back at
// this instance's shared /api/v1/tasks/{id}/content channel — the real
// upstream serves a time-limited OSS link; the artifact is a transcription
// result JSON file, not audio). Callers must hold m.mu.
func (m *Server) qwenASRArtifactURLLocked(task *qwenASRTask) string {
	return m.URL + "/api/v1/tasks/" + task.id + "/content"
}

// qwenASRUsageStringLocked renders the contract §10.2 usage field in its
// DashScope-native JSON *string* form (double-encoded): duration is the
// metering caliber (input audio seconds). Callers must hold m.mu.
func (m *Server) qwenASRUsageStringLocked() string {
	inner, _ := json.Marshal(map[string]any{"duration": 62.5})
	encoded, _ := json.Marshal(string(inner))
	return string(encoded)
}

// qwenASRTaskBodyLocked renders the task object for the current state.
// Callers must hold m.mu.
func (m *Server) qwenASRTaskBodyLocked(task *qwenASRTask) string {
	requestID := "req-" + task.id[:8]
	switch {
	case task.failed:
		return fmt.Sprintf(`{"output":{"task_id":%q,"task_status":"FAILED","message":"mock asr transcription failed"},"code":"InternalError","request_id":%q}`, task.id, requestID)
	case task.done:
		url, _ := json.Marshal(m.qwenASRArtifactURLLocked(task))
		return fmt.Sprintf(`{"output":{"task_id":%q,"task_status":"SUCCEEDED","results":[{"transcription_url":%s}]},"usage":%s,"request_id":%q}`,
			task.id, string(url), m.qwenASRUsageStringLocked(), requestID)
	case task.pollCount <= 1:
		return fmt.Sprintf(`{"output":{"task_id":%q,"task_status":"PENDING"},"request_id":%q}`, task.id, requestID)
	default:
		return fmt.Sprintf(`{"output":{"task_id":%q,"task_status":"RUNNING"},"request_id":%q}`, task.id, requestID)
	}
}

// advanceQwenASRTaskLocked steps the task script for one poll (contract
// §3.2.2; terminal states are idempotent). Callers must hold m.mu.
func (m *Server) advanceQwenASRTaskLocked(task *qwenASRTask) {
	if task.done || task.failed {
		return
	}
	task.pollCount++
	switch task.scenario {
	case ScenarioMediaQwenASRPollRunning:
		// Never completes: every poll keeps PENDING/RUNNING.
	case ScenarioMediaQwenASRPollSucceeded:
		task.done = true
	case ScenarioMediaQwenASRPollFailed:
		task.done = true
		task.failed = true
	default: // media_qwen_asr_create_pending and the normalized default script
		if task.pollCount >= 3 {
			task.done = true
		}
	}
}

// qwenASRResultJSON renders the transcription-result JSON document served on
// the artifact channel (contract §10.2: content includes transcripts[].text /
// sentence timestamps / speaker channels — mock keeps one sentence).
func qwenASRResultJSON(fileURL string) []byte {
	payload, _ := json.Marshal(map[string]any{
		"file_url": fileURL,
		"transcripts": []map[string]any{{
			"channel_id": 0,
			"content": []map[string]any{{
				"text":       "mock transcription text",
				"begin_time": 0,
				"end_time":   1500,
				"words":      []map[string]any{},
			}},
		}},
	})
	return payload
}
