package mockupstream

// GLM CogVideoX async task face for the M3 video chain, per
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension,
// scenario naming) and §7.1 (CogVideoX wire contract, confidence B):
//
//   - POST /api/paas/v4/videos/generations        create
//     (200 {"id":"<12hex>","request_id":"...","task_status":"PROCESSING"} —
//     the acceptance credential is the id field)
//   - GET  /api/paas/v4/async-result/{id}          poll
//     ({"task_status":"PROCESSING"} | SUCCESS + video_result.url | "FAIL")
//   - GET  /api/paas/v4/async-result/{id}/content  artifact download
//     (video/mp4 binary channel; the SUCCESS url points here)
//
// The task table mirrors the OpenAI videos job table and the gemini veo
// operation table: creation registers the task under a generated id with the
// creating scenario as the frozen behavior script; polls advance the
// "poll #N returns state X" script and ignore the poll request's own scenario
// header (contract §3.2.2). glm has no cancel API (contract §7.1) so the face
// exposes no cancel endpoint. Scripts (poll #1 is the first GET after
// creation):
//
//	media_glm_video_create_ok:      #1 PROCESSING → #2+ SUCCESS + url
//	media_glm_video_poll_processing: never completes (stuck case)
//	media_glm_video_poll_success_url: #1 SUCCESS + url (fast path)
//	media_glm_video_poll_fail:      #1 FAIL
//
// Unknown scenarios default to the family OK script (create_ok), mirroring
// the chat_ok / video ok_poll3 defaulting philosophy. The engine-level
// generic fault scenarios (status_500 etc.) keep applying before the media
// dispatch for pre-accept fault injection.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// M3 glm cogvideo scenario names (contract §3.2.4 M3 list, §7.1 Mock row).
const (
	ScenarioMediaGlmVideoCreateOK       Scenario = "media_glm_video_create_ok"
	ScenarioMediaGlmVideoPollProcessing Scenario = "media_glm_video_poll_processing"
	ScenarioMediaGlmVideoPollSuccessURL Scenario = "media_glm_video_poll_success_url"
	ScenarioMediaGlmVideoPollFail       Scenario = "media_glm_video_poll_fail"
)

// glmVideoTask is one registered glm async video task. The id is the
// acceptance credential; the terminal url renders at response time from the
// server URL so the gateway's direct download points back at this instance.
type glmVideoTask struct {
	id        string
	model     string
	scenario  Scenario // behavior script, frozen at creation
	pollCount int
	done      bool
	failed    bool
}

// glmAsyncResultID returns the {id} segment when the path matches the glm
// async-result form /api/paas/v4/async-result/{id} (non-empty single segment,
// optionally with the /content tail), "" otherwise. hasContent reports the
// artifact download tail.
func glmAsyncResultID(path string) (id string, hasContent bool) {
	const prefix = "/api/paas/v4/async-result/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(path, prefix)
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

// acceptsGlmVideoEndpoint whitelists the glm CogVideoX method+path shapes.
func acceptsGlmVideoEndpoint(method, path string) bool {
	if method == http.MethodPost && path == "/api/paas/v4/videos/generations" {
		return true
	}
	if method != http.MethodGet {
		return false
	}
	id, _ := glmAsyncResultID(path)
	return id != ""
}

// isGlmVideoPath reports whether the path belongs to the glm face. It only
// runs after the method-aware whitelist accepted the request.
func isGlmVideoPath(path string) bool {
	if path == "/api/paas/v4/videos/generations" {
		return true
	}
	id, _ := glmAsyncResultID(path)
	return id != ""
}

// serveGlmVideo dispatches an accepted glm request to its endpoint handler.
func (m *Server) serveGlmVideo(w http.ResponseWriter, r *http.Request, scenario Scenario) {
	if r.URL.Path == "/api/paas/v4/videos/generations" && r.Method == http.MethodPost {
		m.serveGlmVideoCreate(w, scenario)
		return
	}
	id, hasContent := glmAsyncResultID(r.URL.Path)
	switch {
	case hasContent:
		m.serveGlmVideoContent(w, id)
	default:
		m.serveGlmVideoPoll(w, id)
	}
}

// glmScriptFor normalizes the creating scenario into the task script.
// Unknown scenarios default to the family OK script (create_ok).
func glmScriptFor(scenario Scenario) Scenario {
	switch scenario {
	case ScenarioMediaGlmVideoCreateOK, ScenarioMediaGlmVideoPollProcessing,
		ScenarioMediaGlmVideoPollSuccessURL, ScenarioMediaGlmVideoPollFail:
		return scenario
	default:
		return ScenarioMediaGlmVideoCreateOK
	}
}

// newGlmTaskIDLocked generates an unused 12-hex task id. Callers must hold
// m.mu.
func (m *Server) newGlmTaskIDLocked() string {
	for {
		var buf [6]byte
		if _, err := rand.Read(buf[:]); err != nil {
			// crypto/rand failure is unrecoverable for id generation; fail
			// loudly instead of silently degrading.
			panic(fmt.Sprintf("mockupstream: glm task id generation failed: %v", err))
		}
		id := hex.EncodeToString(buf[:])
		if _, exists := m.glmVideoTasks[id]; !exists {
			return id
		}
	}
}

// serveGlmVideoCreate implements POST /api/paas/v4/videos/generations
// (contract §7.1): register the task under a fresh id with the scenario as
// its frozen script and answer 200 {id,request_id,task_status:"PROCESSING"} —
// the acceptance credential is the id field (the E2E assertion point).
func (m *Server) serveGlmVideoCreate(w http.ResponseWriter, scenario Scenario) {
	m.mu.Lock()
	id := m.newGlmTaskIDLocked()
	m.glmVideoTasks[id] = &glmVideoTask{id: id, scenario: glmScriptFor(scenario)}
	body, _ := json.Marshal(map[string]string{
		"id": id, "request_id": "req-" + id, "task_status": "PROCESSING",
	})
	m.mu.Unlock()
	writeJSONStatus(w, http.StatusOK, string(body))
}

// glmArtifactURLLocked renders the completed download locator (contract §7.1:
// video_result.url, absolute URL pointing back at this instance so the
// gateway's credential-free direct GET lands on the content endpoint).
// Callers must hold m.mu (it reads m.URL).
func (m *Server) glmArtifactURLLocked(task *glmVideoTask) string {
	return m.URL + "/api/paas/v4/async-result/" + task.id + "/content"
}

// glmAsyncResultBodyLocked renders the async-result object for the current
// state. Callers must hold m.mu.
func (m *Server) glmAsyncResultBodyLocked(task *glmVideoTask) string {
	switch {
	case task.failed:
		return fmt.Sprintf(`{"id":%q,"request_id":"req-%s","task_status":"FAIL"}`, task.id, task.id)
	case task.done:
		url, _ := json.Marshal(m.glmArtifactURLLocked(task))
		return fmt.Sprintf(`{"id":%q,"request_id":"req-%s","task_status":"SUCCESS","video_result":{"url":%s,"cover_image_url":""}}`,
			task.id, task.id, string(url))
	default:
		return fmt.Sprintf(`{"id":%q,"request_id":"req-%s","task_status":"PROCESSING"}`, task.id, task.id)
	}
}

// advanceGlmVideoTaskLocked steps the task script for one poll (contract
// §3.2.2; terminal states are idempotent). Callers must hold m.mu.
func (m *Server) advanceGlmVideoTaskLocked(task *glmVideoTask) {
	if task.done {
		return
	}
	task.pollCount++
	switch task.scenario {
	case ScenarioMediaGlmVideoPollProcessing:
		// Never completes: every poll keeps PROCESSING.
	case ScenarioMediaGlmVideoPollSuccessURL:
		task.done = true
	case ScenarioMediaGlmVideoPollFail:
		task.done = true
		task.failed = true
	default: // media_glm_video_create_ok and the normalized default script
		if task.pollCount >= 2 {
			task.done = true
		}
	}
}

// serveGlmVideoPoll implements GET /api/paas/v4/async-result/{id}: unknown
// ids answer 404 (glm error envelope); otherwise the frozen script advances
// once and the async-result object is returned.
func (m *Server) serveGlmVideoPoll(w http.ResponseWriter, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.glmVideoTasks[id]
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, `{"error":{"code":"404","message":"task not exist"}}`)
		return
	}
	m.advanceGlmVideoTaskLocked(task)
	writeJSONStatus(w, http.StatusOK, m.glmAsyncResultBodyLocked(task))
}

// serveGlmVideoContent implements the artifact download endpoint: the mp4
// binary channel is open only for done, non-failed tasks (the url the SUCCESS
// poll returned); everything else (unknown id, processing, failed) answers
// 404.
func (m *Server) serveGlmVideoContent(w http.ResponseWriter, id string) {
	m.mu.Lock()
	task, ok := m.glmVideoTasks[id]
	downloadable := ok && task.done && !task.failed
	m.mu.Unlock()
	if !downloadable {
		writeJSONStatus(w, http.StatusNotFound, `{"error":{"code":"404","message":"video result not found or expired"}}`)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mp4Payload())
}
