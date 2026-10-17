package mockupstream

// Volcengine (SeedASR AUC, 录音文件识别 2.0) async ASR task face for the
// M3f /v1/audio/jobs chain, per docs/functions/媒体上游协议契约与Mock上游规格.md
// §3.2 (scenario naming media_<provider>_<behavior>) and §9.2 (seedasr
// long-transcription wire contract, confidence A, adjudicated 2026-10-17):
//
//   - POST /api/v3/auc/bigmodel/submit               create
//     (HEADER-driven acceptance, unlike every body-driven task face: HTTP
//     200 + EMPTY body {} and the success flag lives in the response
//     headers X-Api-Status-Code: 20000000 + X-Api-Message: OK; the task id
//     is the caller-defined X-Api-Request-Id request header — NOT an
//     upstream-returned field; submit body must carry audio.url)
//   - POST /api/v3/auc/bigmodel/query                 poll
//     (the status again travels ONLY in X-Api-Status-Code: 20000001
//     processing / 20000002 queued → both in_progress, 20000000 terminal
//     succeeded with the transcription payload, 450000xx failure; the
//     query body is {})
//
// Rejection shapes mirror the measured real forms (HTTP 200 unless noted —
// the transport status stays 200 on this face, the protocol status is the
// header):
//
//	missing X-Api-Key      → 401 flat {"code":45000010,"message":"Invalid X-Api-Key"}
//	                         (TTS mock family precedent, video_volcengine.go)
//	resource not granted   → 200 X-Api-Status-Code: 45000030 +
//	                         {"header":{"reqid":...,"code":45000030,"message":"[resource_id=<submitted>] requested resource not granted"}}
//	missing X-Api-Request-Id / missing audio.url
//	                       → 200 X-Api-Status-Code: 45000001 +
//	                         {"header":{"code":45000001,"message":"..."}}
//	query task table miss  → 200 X-Api-Status-Code: 45000001 +
//	                         {"header":{"code":45000001,"message":"task not found"}}
//
// Every non-401 response pairs X-Api-Status-Code with X-Api-Message. The
// task table mirrors the qwen ASR task table (asr_qwen.go): scenario frozen
// at creation, polls advance the script and ignore the poll request's own
// scenario header, terminal states idempotent — keyed by the submitted
// X-Api-Request-Id. Volcengine has no cancel endpoint on the contract §9.2
// face (SupportsCancel=false) so the face exposes no cancel route. Scripts
// (query #1 is the first POST after submit):
//
//	media_volcengine_asr_create_ok:           #1 20000001 → #2+ 20000000 + duration:1118 payload
//	media_volcengine_asr_poll_running:        never completes (stuck case)
//	media_volcengine_asr_poll_succeeded_text: #1 20000000 + payload (fast path)
//	media_volcengine_asr_poll_failed:         #1 45000006 download audio failed
//
// Unknown scenarios default to the family OK script (create_ok), mirroring
// the qwen ASR / volcengine video defaulting philosophy.

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// M3f volcengine ASR scenario names (contract §9.2 Mock row).
const (
	ScenarioMediaVolcengineASRCreateOK          Scenario = "media_volcengine_asr_create_ok"
	ScenarioMediaVolcengineASRPollRunning       Scenario = "media_volcengine_asr_poll_running"
	ScenarioMediaVolcengineASRPollSucceededText Scenario = "media_volcengine_asr_poll_succeeded_text"
	ScenarioMediaVolcengineASRPollFailed        Scenario = "media_volcengine_asr_poll_failed"
)

// Seedasr AUC wire constants (contract §9.2): the two POST endpoints, the
// only granted resource id, and the X-Api-Status-Code protocol statuses.
const (
	volcengineASRSubmitPath = "/api/v3/auc/bigmodel/submit"
	volcengineASRQueryPath  = "/api/v3/auc/bigmodel/query"
	volcengineASRResourceID = "volc.seedasr.auc"

	volcengineASRStatusOK          = "20000000" // terminal succeeded / submit accepted
	volcengineASRStatusRunning     = "20000001" // processing (20000002 queued is same in_progress arm)
	volcengineASRStatusBadParam    = "45000001" // invalid parameter / task not found
	volcengineASRStatusFetchFailed = "45000006" // measured: audio URL download failure
	volcengineASRStatusNotGranted  = "45000030" // resource not granted for this account
	volcengineASRStatusNoAuth      = "45000010" // invalid/missing X-Api-Key (flat 401 face)
)

// volcengineASRTask is one registered seedasr AUC task. The id IS the
// creating request's X-Api-Request-Id (the caller-defined acceptance
// credential — nothing is returned upstream); audioURL/format echo the
// submitted audio.url/audio.format for request-side assertions.
type volcengineASRTask struct {
	id        string
	scenario  Scenario // behavior script, frozen at creation
	audioURL  string   // submitted audio.url
	format    string   // submitted audio.format
	pollCount int
	done      bool
	failed    bool
}

// isVolcengineASRPath reports whether the path belongs to the volcengine
// seedasr ASR face (both endpoints are POST-only — no GET, no cancel route
// on the §9.2 face; the whitelist entries themselves live in the
// mediaEndpoints map, media.go).
func isVolcengineASRPath(path string) bool {
	return path == volcengineASRSubmitPath || path == volcengineASRQueryPath
}

// volcengineASRScriptFor normalizes the creating scenario into the task
// script. Unknown scenarios default to the family OK script (create_ok).
func volcengineASRScriptFor(scenario Scenario) Scenario {
	switch scenario {
	case ScenarioMediaVolcengineASRCreateOK, ScenarioMediaVolcengineASRPollRunning,
		ScenarioMediaVolcengineASRPollSucceededText, ScenarioMediaVolcengineASRPollFailed:
		return scenario
	default:
		return ScenarioMediaVolcengineASRCreateOK
	}
}

// volcengineASRSubmitBody is the create request consumption face (contract
// §9.2): {user:{uid}, audio:{url, format}, request:{model_name}} — only
// audio.url is validated (non-empty, the sole accepted input form).
type volcengineASRSubmitBody struct {
	Audio struct {
		URL    string `json:"url"`
		Format string `json:"format"`
	} `json:"audio"`
}

// volcengineASRSucceededPayload is the terminal transcription body, pinned
// byte-for-byte from the 2026-10-17 real-upstream measurement (contract
// §9.2). audio_info.duration is MILLISECONDS (1118) — the gateway metering
// arm divides it by 1000 into audio_input_seconds, so chain assertions
// depend on this exact value.
const volcengineASRSucceededPayload = `{"audio_info":{"duration":1118},"result":{"additions":{"duration":"1118"},"text":"Carry an oily rag.","utterances":[{"end_time":1040,"start_time":0,"text":"Carry an oily rag.","words":[{"confidence":0,"end_time":240,"start_time":0,"text":"Carry"},{"confidence":0,"end_time":-1,"start_time":-1,"text":" "},{"confidence":0,"end_time":480,"start_time":360,"text":"an"},{"confidence":0,"end_time":720,"start_time":560,"text":"oily"},{"confidence":0,"end_time":1040,"start_time":880,"text":"rag"}]}]}}`

// writeVolcengineASRResponse writes one header-driven protocol response:
// the transport status stays 200 with the protocol status carried in
// X-Api-Status-Code, paired with X-Api-Message on every response of the
// face (contract §9.2).
func writeVolcengineASRResponse(w http.ResponseWriter, statusCode, message, body string) {
	w.Header().Set("X-Api-Status-Code", statusCode)
	w.Header().Set("X-Api-Message", message)
	writeJSONStatus(w, http.StatusOK, body)
}

// writeVolcengineASRResponseEchoID 在 writeVolcengineASRResponse 之上回显
// 请求的 X-Api-Request-Id 响应头——真实上游原样回显该头（2026-10-17 实测
// submit/query 响应均带），网关从头驱动协议的响应头取任务 ID。
func writeVolcengineASRResponseEchoID(w http.ResponseWriter, r *http.Request, statusCode, message, body string) {
	w.Header().Set("X-Api-Request-Id", r.Header.Get("X-Api-Request-Id"))
	writeVolcengineASRResponse(w, statusCode, message, body)
}

// volcengineASREnvelope renders one header-envelope error body (contract
// §9.2). The measured resource-not-granted form echoes the submitted
// X-Api-Request-Id as header.reqid; the parameter / task-not-found forms
// (450000xx) are pinned WITHOUT reqid — pass "" as reqID there.
func volcengineASREnvelope(reqID string, code int, message string) string {
	if reqID == "" {
		return fmt.Sprintf(`{"header":{"code":%d,"message":%q}}`, code, message)
	}
	return fmt.Sprintf(`{"header":{"reqid":%q,"code":%d,"message":%q}}`, reqID, code, message)
}

// volcengineASRHeaderCheck validates the header trio shared by submit and
// query (contract §9.2: X-Api-Key auth first, then resource scope, then the
// caller-defined request id) and writes the matching rejection itself when
// one fails; ok reports acceptance. X-Api-Key is checked by presence only —
// the mock cannot know which keys the real account granted.
func volcengineASRHeaderCheck(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Api-Key") == "" {
		w.Header().Set("X-Api-Status-Code", volcengineASRStatusNoAuth)
		w.Header().Set("X-Api-Message", "Invalid X-Api-Key")
		writeJSONStatus(w, http.StatusUnauthorized, `{"code":45000010,"message":"Invalid X-Api-Key"}`)
		return false
	}
	resourceID := r.Header.Get("X-Api-Resource-Id")
	requestID := r.Header.Get("X-Api-Request-Id")
	if resourceID != volcengineASRResourceID {
		message := fmt.Sprintf("[resource_id=%s] requested resource not granted", resourceID)
		writeVolcengineASRResponseEchoID(w, r, volcengineASRStatusNotGranted, message, volcengineASREnvelope(requestID, 45000030, message))
		return false
	}
	if requestID == "" {
		const message = "X-Api-Request-Id is required"
		writeVolcengineASRResponseEchoID(w, r, volcengineASRStatusBadParam, message, volcengineASREnvelope("", 45000001, message))
		return false
	}
	return true
}

// serveVolcengineASR dispatches an accepted seedasr AUC request to its
// endpoint handler.
func (m *Server) serveVolcengineASR(w http.ResponseWriter, r *http.Request, scenario Scenario) {
	if r.URL.Path == volcengineASRSubmitPath {
		m.serveVolcengineASRSubmit(w, r, scenario)
		return
	}
	m.serveVolcengineASRQuery(w, r)
}

// serveVolcengineASRSubmit implements POST /api/v3/auc/bigmodel/submit
// (contract §9.2): the header-driven acceptance answers HTTP 200 + EMPTY
// body {} with X-Api-Status-Code: 20000000 + X-Api-Message: OK, and
// registers the task under the caller-defined X-Api-Request-Id with the
// creating scenario as its frozen script. Pre-accept rejections reuse the
// engine-level generic fault scenarios (status_429 etc.) per contract
// §3.2.4. A duplicate X-Api-Request-Id re-registers (mock keeps the last
// script — the real upstream does not document the duplicate form).
func (m *Server) serveVolcengineASRSubmit(w http.ResponseWriter, r *http.Request, scenario Scenario) {
	if !volcengineASRHeaderCheck(w, r) {
		return
	}
	requestID := r.Header.Get("X-Api-Request-Id")
	var body volcengineASRSubmitBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Audio.URL == "" {
		const message = "audio.url is required"
		writeVolcengineASRResponseEchoID(w, r, volcengineASRStatusBadParam, message, volcengineASREnvelope("", 45000001, message))
		return
	}
	m.mu.Lock()
	m.volcengineASRTasks[requestID] = &volcengineASRTask{
		id:       requestID,
		scenario: volcengineASRScriptFor(scenario),
		audioURL: body.Audio.URL,
		format:   body.Audio.Format,
	}
	m.mu.Unlock()
	writeVolcengineASRResponseEchoID(w, r, volcengineASRStatusOK, "OK", "{}")
}

// advanceVolcengineASRTaskLocked steps the task script for one query poll
// (contract §3.2.2; terminal states are idempotent). Callers must hold
// m.mu.
func (m *Server) advanceVolcengineASRTaskLocked(task *volcengineASRTask) {
	if task.done {
		return
	}
	task.pollCount++
	switch task.scenario {
	case ScenarioMediaVolcengineASRPollRunning:
		// Never completes: every poll stays processing.
	case ScenarioMediaVolcengineASRPollSucceededText:
		task.done = true
	case ScenarioMediaVolcengineASRPollFailed:
		task.done = true
		task.failed = true
	default: // media_volcengine_asr_create_ok and the normalized default script
		if task.pollCount >= 2 {
			task.done = true
		}
	}
}

// volcengineASRQueryResponse renders one query response for the task's
// current script state (contract §9.2): running 20000001 with the empty
// processing body, succeeded 20000000 with the pinned real-transcription
// payload, or failed 45000006 (measured: audio URL download failure).
// Callers must hold m.mu.
func volcengineASRQueryResponse(task *volcengineASRTask) (statusCode, message, body string) {
	switch {
	case task.failed:
		return volcengineASRStatusFetchFailed, "download audio failed", `{"header":{"code":45000006,"message":"download audio failed"}}`
	case task.done:
		return volcengineASRStatusOK, "OK", volcengineASRSucceededPayload
	default:
		return volcengineASRStatusRunning, "OK", `{"audio_info":{},"result":{"text":""}}`
	}
}

// serveVolcengineASRQuery implements POST /api/v3/auc/bigmodel/query
// (contract §9.2): the same header check as submit, then the task table
// keyed by X-Api-Request-Id — a miss answers 45000001 "task not found",
// a hit advances the frozen script once and returns the state-specific
// status header + body. The query request's own scenario header is
// ignored, like every task face in this package.
func (m *Server) serveVolcengineASRQuery(w http.ResponseWriter, r *http.Request) {
	if !volcengineASRHeaderCheck(w, r) {
		return
	}
	m.mu.Lock()
	task, ok := m.volcengineASRTasks[r.Header.Get("X-Api-Request-Id")]
	if !ok {
		m.mu.Unlock()
		writeVolcengineASRResponseEchoID(w, r, volcengineASRStatusBadParam, "task not found", `{"header":{"code":45000001,"message":"task not found"}}`)
		return
	}
	m.advanceVolcengineASRTaskLocked(task)
	statusCode, message, body := volcengineASRQueryResponse(task)
	m.mu.Unlock()
	writeVolcengineASRResponseEchoID(w, r, statusCode, message, body)
}
