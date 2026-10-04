package mockupstream

// xAI (Grok Imagine Video) async video task face for the M3 media chain,
// per docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension,
// scenario naming media_<provider>_<behavior>) and §6.1 (Grok Imagine Video
// wire contract, confidence A):
//
//   - POST /v1/videos/generations        create
//     (200 {"request_id":"<uuid>"} — the acceptance credential is the
//     request_id field)
//   - GET  /v1/videos/{request_id}        poll
//     ({"request_id"?,"status":"pending|done|expired|failed",
//     "video":{"url":...,"duration":...} | "error":{code,message}})
//   - GET  /v1/videos/{request_id}/content artifact download
//     (video/mp4 binary channel; the done video.url points here)
//
// Path namespace note: the poll/content shapes share the OpenAI videos
// /v1/videos/{id} namespace (contract §4.3 vs §6.1 — xAI publishes the same
// path form). The openai face owns the dispatch entry; when its task table
// misses the id, video.go serveVideoPoll/serveVideoContent fall through to
// the xai table here (ids never collide: openai uses "video_"+hex, xai uses
// uuid form). xAI has no cancel endpoint on the contract §6.1 face
// (SupportsCancel=false) so this face exposes no cancel route.
//
// The task table mirrors the other async video task tables: creation
// registers the task under a generated uuid with the creating scenario as
// the frozen behavior script; polls advance the "poll #N returns state X"
// script and ignore the poll request's own scenario header (contract
// §3.2.2). Scripts (poll #1 is the first GET after creation):
//
//	media_xai_video_create_request_id:       #1 pending → #2+ done + video{url,duration}
//	media_xai_video_poll_pending:            never completes (stuck case)
//	media_xai_video_poll_done_url_duration:  #1 done + video{url,duration} (fast path)
//	media_xai_video_poll_failed_error:       #1 failed + error{code:invalid_argument}
//	media_xai_video_poll_expired:            #1 status=expired (native terminal)
//
// Unknown scenarios default to the family OK script (create_request_id),
// mirroring the chat_ok / video ok_poll3 defaulting philosophy.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
)

// M3 xai scenario names (contract §3.2.4 M3 list, §6.1 Mock row).
const (
	ScenarioMediaXaiVideoCreateRequestID   Scenario = "media_xai_video_create_request_id"
	ScenarioMediaXaiVideoPollPending       Scenario = "media_xai_video_poll_pending"
	ScenarioMediaXaiVideoPollDoneURLDur    Scenario = "media_xai_video_poll_done_url_duration"
	ScenarioMediaXaiVideoPollFailedError   Scenario = "media_xai_video_poll_failed_error"
	ScenarioMediaXaiVideoPollExpired       Scenario = "media_xai_video_poll_expired"
)

// xaiVideoTask is one registered xai async video task. The request_id is the
// acceptance credential; the terminal video.url renders at response time
// from the server URL so the gateway's credential-free direct download
// points back at this instance.
type xaiVideoTask struct {
	id        string
	scenario  Scenario // behavior script, frozen at creation
	pollCount int
	done      bool
	failed    bool
	expired   bool
}

// acceptsXaiVideoEndpoint whitelists the xAI Grok Imagine Video method+path
// shapes that do NOT collide with the OpenAI videos namespace: the POST
// create endpoint /v1/videos/generations (POST on /v1/videos/{id} is not an
// OpenAI shape, so there is no ambiguity). The GET poll/content shapes share
// the OpenAI namespace and are reached through the openai face's table
// miss fallback (see the file header).
func acceptsXaiVideoEndpoint(method, path string) bool {
	return method == http.MethodPost && path == "/v1/videos/generations"
}

// xaiScriptFor normalizes the creating scenario into the task script.
// Unknown scenarios default to the family OK script (create_request_id).
func xaiScriptFor(scenario Scenario) Scenario {
	switch scenario {
	case ScenarioMediaXaiVideoCreateRequestID, ScenarioMediaXaiVideoPollPending,
		ScenarioMediaXaiVideoPollDoneURLDur, ScenarioMediaXaiVideoPollFailedError,
		ScenarioMediaXaiVideoPollExpired:
		return scenario
	default:
		return ScenarioMediaXaiVideoCreateRequestID
	}
}

// newXaiTaskIDLocked generates an unused <uuid> request_id (the contract
// §6.1 example shape). Callers must hold m.mu.
func (m *Server) newXaiTaskIDLocked() string {
	for {
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			// crypto/rand failure is unrecoverable for id generation; fail
			// loudly instead of silently degrading.
			panic(fmt.Sprintf("mockupstream: xai task id generation failed: %v", err))
		}
		text := hex.EncodeToString(buf[:])
		id := text[:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
		if _, exists := m.xaiVideoTasks[id]; !exists {
			return id
		}
	}
}

// serveXaiVideoCreate implements POST /v1/videos/generations (contract
// §6.1): register the task under a fresh request_id with the scenario as its
// frozen script and answer 200 {"request_id":...} — the acceptance credential
// (the E2E assertion point). Pre-accept rejections reuse the engine-level
// generic fault scenarios (status_429 etc.) per contract §3.2.4.
func (m *Server) serveXaiVideoCreate(w http.ResponseWriter, scenario Scenario) {
	m.mu.Lock()
	id := m.newXaiTaskIDLocked()
	m.xaiVideoTasks[id] = &xaiVideoTask{id: id, scenario: xaiScriptFor(scenario)}
	body, _ := json.Marshal(map[string]string{"request_id": id})
	m.mu.Unlock()
	writeJSONStatus(w, http.StatusOK, string(body))
}

// xaiArtifactURLLocked renders the completed download locator (contract
// §6.1: video.url, temporary absolute URL pointing back at this instance so
// the gateway's credential-free direct GET lands on the content endpoint).
// Callers must hold m.mu (it reads m.URL).
func (m *Server) xaiArtifactURLLocked(task *xaiVideoTask) string {
	return m.URL + "/v1/videos/" + task.id + "/content"
}

// xaiTaskBodyLocked renders the task object for the current state. Callers
// must hold m.mu.
func (m *Server) xaiTaskBodyLocked(task *xaiVideoTask) string {
	switch {
	case task.failed:
		return fmt.Sprintf(`{"request_id":%q,"status":"failed","error":{"code":"invalid_argument","message":"mock video generation failed"}}`, task.id)
	case task.done:
		url, _ := json.Marshal(m.xaiArtifactURLLocked(task))
		return fmt.Sprintf(`{"request_id":%q,"status":"done","video":{"url":%s,"duration":6}}`, task.id, string(url))
	case task.expired:
		return fmt.Sprintf(`{"request_id":%q,"status":"expired"}`, task.id)
	default:
		return fmt.Sprintf(`{"request_id":%q,"status":"pending"}`, task.id)
	}
}

// advanceXaiVideoTaskLocked steps the task script for one poll (contract
// §3.2.2; terminal states are idempotent). Callers must hold m.mu.
func (m *Server) advanceXaiVideoTaskLocked(task *xaiVideoTask) {
	if task.done || task.expired {
		return
	}
	task.pollCount++
	switch task.scenario {
	case ScenarioMediaXaiVideoPollPending:
		// Never completes: every poll keeps pending.
	case ScenarioMediaXaiVideoPollDoneURLDur:
		task.done = true
	case ScenarioMediaXaiVideoPollFailedError:
		task.done = true
		task.failed = true
	case ScenarioMediaXaiVideoPollExpired:
		task.expired = true
	default: // media_xai_video_create_request_id and the normalized default
		if task.pollCount >= 2 {
			task.done = true
		}
	}
}

// serveXaiVideoPoll implements GET /v1/videos/{request_id} (contract §6.1):
// the frozen script advances once and the task object is returned. Unknown
// ids never reach here (the openai face answers its own 404 first).
func (m *Server) serveXaiVideoPoll(w http.ResponseWriter, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.xaiVideoTasks[id]
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, `{"error":{"code":"not_found","message":"video request not found"}}`)
		return
	}
	m.advanceXaiVideoTaskLocked(task)
	writeJSONStatus(w, http.StatusOK, m.xaiTaskBodyLocked(task))
}

// serveXaiVideoContent implements the artifact download endpoint: the mp4
// binary channel is open only for done, non-failed, non-expired tasks (the
// video.url the done poll returned); everything else answers 404.
func (m *Server) serveXaiVideoContent(w http.ResponseWriter, id string) {
	m.mu.Lock()
	task, ok := m.xaiVideoTasks[id]
	downloadable := ok && task.done && !task.failed && !task.expired
	m.mu.Unlock()
	if !downloadable {
		writeJSONStatus(w, http.StatusNotFound, `{"error":{"code":"not_found","message":"video content not found or expired"}}`)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mp4Payload())
}
