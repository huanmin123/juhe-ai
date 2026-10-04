package mockupstream

// OpenAI videos async job face for the M2 video chain, per
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension,
// scenario naming) and §4.3 (OpenAI videos wire contract):
//
//   - POST   /v1/videos                create  (contract §4.3)
//   - GET    /v1/videos                list    (OpenAI list shape)
//   - GET    /v1/videos/{id}           poll    (state machine advances per poll)
//   - GET    /v1/videos/{id}/content   download (video/mp4 binary channel)
//   - DELETE /v1/videos/{id}           cancel  (204; OpenAI semantics: after
//                                    deletion a GET of the id is 404)
//
// Async job table (contract §3.2.2, new mechanism): creation registers a job
// in the Server's in-memory table under a generated id ("video_" + 16 hex)
// together with its creation-time scenario as the behavior script; polls
// advance a "poll #N returns state X" script; the table lives and dies with
// the instance and is guarded by the Server mutex so concurrent use cases
// on one instance cannot interfere. The poll request's own scenario header
// is ignored — the script is decided by the creating request only (the
// engine-level generic fault scenarios such as status_500 keep applying
// before the media dispatch, for pre-accept fault injection).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// M2 video scenario names (contract §3.2.4 M2 list plus the fast-path and
// bad-size cases).
const (
	ScenarioMediaVideoOKPoll3          Scenario = "media_video_ok_poll3"
	ScenarioMediaVideoOKPoll1          Scenario = "media_video_ok_poll1"
	ScenarioMediaVideoFailAfterAccept  Scenario = "media_video_fail_after_accept"
	ScenarioMediaVideo429Create        Scenario = "media_video_429_create"
	ScenarioMediaVideoCreate500        Scenario = "media_video_create_500"
	ScenarioMediaVideoPoll500          Scenario = "media_video_poll_500"
	ScenarioMediaVideoContentExpired   Scenario = "media_video_content_expired"
	ScenarioMediaVideoCancelOK         Scenario = "media_video_cancel_ok"
	ScenarioMediaVideoCreate400BadSize Scenario = "media_video_create_400_bad_size"
)

// OpenAI video object status word list (contract §4.3: queued|in_progress|
// completed|failed; cancelled/expired collapse locally after upstream
// deletion — here deletion simply removes the job so polls turn 404).
const (
	videoStatusQueued     = "queued"
	videoStatusInProgress = "in_progress"
	videoStatusCompleted  = "completed"
	videoStatusFailed     = "failed"
)

// videoTask is one registered async video job. Fields mirror the contract
// §4.3 creation request echo plus the state machine bookkeeping.
type videoTask struct {
	id        string
	scenario  Scenario // behavior script, fixed at creation
	status    string
	progress  int
	pollCount int
	model     string
	prompt    string
	seconds   int
	size      string
	createdAt time.Time
}

// videoContentPart is the completed download locator (contract §4.3:
// "completed 时 content 数组提供下载定位").
type videoContentPart struct {
	Status string `json:"status"`
	Type   string `json:"type"`
	URL    string `json:"url"`
}

// videoErrorObject is the failed-state error payload (code/message pair).
type videoErrorObject struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// videoObject is the wire shape of every create/poll/list entry (contract
// §4.3: id/object/status/progress/model/prompt/seconds_length/size, plus
// content once completed and error once failed).
type videoObject struct {
	ID            string             `json:"id"`
	Object        string             `json:"object"`
	Status        string             `json:"status"`
	Progress      int                `json:"progress"`
	Model         string             `json:"model"`
	Prompt        string             `json:"prompt"`
	SecondsLength int                `json:"seconds_length"`
	Size          string             `json:"size"`
	Content       []videoContentPart `json:"content,omitempty"`
	Error         *videoErrorObject  `json:"error,omitempty"`
}

// acceptsVideoEndpoint whitelists the exact method+path pairs of the videos
// face: POST/GET on the bare path, GET/DELETE on /{id}, GET on /{id}/content
// with a non-empty single-segment id.
func acceptsVideoEndpoint(method, path string) bool {
	if path == "/v1/videos" {
		return method == http.MethodPost || method == http.MethodGet
	}
	rest, ok := strings.CutPrefix(path, "/v1/videos/")
	if !ok || rest == "" {
		return false
	}
	id, tail, hasTail := strings.Cut(rest, "/")
	if id == "" {
		return false
	}
	if !hasTail {
		return method == http.MethodGet || method == http.MethodDelete
	}
	return tail == "content" && method == http.MethodGet
}

// isVideoPath reports whether the path belongs to the videos family. It only
// runs after the method-aware whitelist accepted the request.
func isVideoPath(path string) bool {
	return path == "/v1/videos" || strings.HasPrefix(path, "/v1/videos/")
}

// serveVideo dispatches an accepted videos request to its endpoint handler.
func (m *Server) serveVideo(w http.ResponseWriter, r *http.Request, idx int, scenario Scenario) {
	if r.URL.Path == "/v1/videos" {
		if r.Method == http.MethodPost {
			m.serveVideoCreate(w, idx, scenario)
			return
		}
		m.serveVideoList(w) // GET list
		return
	}
	id, tail, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/videos/"), "/")
	if tail == "content" {
		m.serveVideoContent(w, id)
		return
	}
	if r.Method == http.MethodDelete {
		m.serveVideoDelete(w, id)
		return
	}
	m.serveVideoPoll(w, id)
}

// serveVideoCreate implements POST /v1/videos (contract §4.3): the family
// error scenarios fail before a job is registered; otherwise the job is
// registered as queued with the scenario as its script and the creation
// response carries the acceptance fields (id + status "queued" are the E2E
// assertion points).
func (m *Server) serveVideoCreate(w http.ResponseWriter, idx int, scenario Scenario) {
	switch scenario {
	case ScenarioMediaVideo429Create:
		// Pre-accept rate limit: account-switch retry case (contract §3.2.4).
		w.Header().Set("Retry-After", FormatRetryAfter(30))
		writeJSONStatus(w, http.StatusTooManyRequests, `{"error":{"message":"Rate limit reached before accepting request","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
		return
	case ScenarioMediaVideoCreate500:
		writeJSONStatus(w, http.StatusInternalServerError, `{"error":{"message":"Internal server error","type":"server_error"}}`)
		return
	case ScenarioMediaVideoCreate400BadSize:
		// Parameter error, no account switch (contract §2.4 rule 2).
		writeJSONStatus(w, http.StatusBadRequest, `{"error":{"message":"Invalid size: value must be one of 1280x720, 720x1280, 960x960","type":"invalid_request_error","code":"invalid_size"}}`)
		return
	}

	// Read the recorded request body under the Server mutex: concurrent
	// requests append to m.seenReqs, so an unlocked read races the writer.
	m.mu.Lock()
	requestBody := m.seenReqs[idx].Body
	m.mu.Unlock()
	var req struct {
		Model   string          `json:"model"`
		Prompt  string          `json:"prompt"`
		Seconds json.RawMessage `json:"seconds"`
		Size    string          `json:"size"`
	}
	_ = json.Unmarshal([]byte(requestBody), &req)
	task := &videoTask{
		scenario:  videoScriptFor(scenario),
		status:    videoStatusQueued,
		progress:  0,
		model:     req.Model,
		prompt:    req.Prompt,
		seconds:   parseVideoSeconds(req.Seconds),
		size:      req.Size,
		createdAt: m.now(),
	}
	if task.model == "" {
		task.model = "sora-2" // contract §4.3 example model
	}
	if task.size == "" {
		task.size = "1280x720"
	}

	m.mu.Lock()
	task.id = m.newVideoIDLocked()
	m.videos[task.id] = task
	body := m.videoObjectBodyLocked(task)
	m.mu.Unlock()
	writeJSONStatus(w, http.StatusOK, body)
}

// videoScriptFor normalizes the creation scenario into a job script. Unknown
// scenarios default to the family OK script (ok_poll3), mirroring the
// chat_ok / audio OK defaulting philosophy.
func videoScriptFor(scenario Scenario) Scenario {
	switch scenario {
	case ScenarioMediaVideoOKPoll3, ScenarioMediaVideoOKPoll1, ScenarioMediaVideoFailAfterAccept,
		ScenarioMediaVideoPoll500, ScenarioMediaVideoContentExpired, ScenarioMediaVideoCancelOK:
		return scenario
	default:
		return ScenarioMediaVideoOKPoll3
	}
}

// parseVideoSeconds accepts the contract §4.3 forms — string ("4") or number
// (4) — and defaults to 4 when absent or unparsable (the response always
// echoes a numeric seconds_length).
func parseVideoSeconds(raw json.RawMessage) int {
	if len(raw) > 0 {
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			if n, err := strconv.Atoi(text); err == nil && n > 0 {
				return n
			}
		} else {
			var n int
			if err := json.Unmarshal(raw, &n); err == nil && n > 0 {
				return n
			}
		}
	}
	return 4
}

// newVideoIDLocked generates an unused "video_" + 16 hex id. Callers must
// hold m.mu.
func (m *Server) newVideoIDLocked() string {
	for {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err != nil {
			// crypto/rand failure is unrecoverable for id generation; fail
			// loudly instead of silently degrading.
			panic(fmt.Sprintf("mockupstream: video id generation failed: %v", err))
		}
		id := "video_" + hex.EncodeToString(buf[:])
		if _, exists := m.videos[id]; !exists {
			return id
		}
	}
}

// videoObjectBodyLocked renders the video object for the job's current
// state: completed adds the content locator array, failed adds the error
// object. Callers must hold m.mu (it reads m.URL for the content locator).
func (m *Server) videoObjectBodyLocked(task *videoTask) string {
	obj := videoObject{
		ID:            task.id,
		Object:        "video",
		Status:        task.status,
		Progress:      task.progress,
		Model:         task.model,
		Prompt:        task.prompt,
		SecondsLength: task.seconds,
		Size:          task.size,
	}
	if task.status == videoStatusCompleted {
		obj.Content = []videoContentPart{{
			Status: videoStatusCompleted,
			Type:   "video/mp4",
			URL:    m.URL + "/v1/videos/" + task.id + "/content",
		}}
	}
	if task.status == videoStatusFailed {
		obj.Error = &videoErrorObject{
			Code:    "video_generation_failed",
			Message: "Video generation failed after the request was accepted",
		}
	}
	body, _ := json.Marshal(obj)
	return string(body)
}

// advanceVideoTaskLocked steps the "poll #N returns state X" script for one
// poll (contract §3.2.2). Terminal states are idempotent: later polls keep
// returning the terminal object. Callers must hold m.mu.
//
// Script semantics (poll #1 is the first GET after creation):
//
//	media_video_ok_poll3:         #1 in_progress(33) → #2 in_progress(66) → #3+ completed(100)
//	media_video_ok_poll1:         #1 completed(100) — fast path
//	media_video_fail_after_accept:#1 in_progress(50) → #2+ failed (error code/message)
//	media_video_content_expired:  #1 completed(100), but the content endpoint 404s
//	media_video_cancel_ok:        stays queued (the delete flow is the scenario's purpose)
//	media_video_poll_500:         never advanced — serveVideoPoll answers 500 first
func (m *Server) advanceVideoTaskLocked(task *videoTask) {
	if task.status == videoStatusCompleted || task.status == videoStatusFailed {
		return
	}
	task.pollCount++
	switch task.scenario {
	case ScenarioMediaVideoOKPoll1, ScenarioMediaVideoContentExpired:
		task.status = videoStatusCompleted
		task.progress = 100
	case ScenarioMediaVideoFailAfterAccept:
		if task.pollCount >= 2 {
			task.status = videoStatusFailed
			task.progress = 0
		} else {
			task.status = videoStatusInProgress
			task.progress = 50
		}
	case ScenarioMediaVideoCancelOK:
		// No advance: queued until the DELETE removes the job.
	default: // media_video_ok_poll3 and the normalized default script
		switch task.pollCount {
		case 1:
			task.status = videoStatusInProgress
			task.progress = 33
		case 2:
			task.status = videoStatusInProgress
			task.progress = 66
		default:
			task.status = videoStatusCompleted
			task.progress = 100
		}
	}
}

// serveVideoPoll implements GET /v1/videos/{id}: unknown (or deleted) ids
// answer the OpenAI 404 error JSON; poll_500 scripts answer 500 on every
// poll (after-accept upstream fault, job keeps its pre-fault state — the
// engine never fakes a terminal state); otherwise the script advances once.
func (m *Server) serveVideoPoll(w http.ResponseWriter, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.videos[id]
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, videoNotFoundBody(id))
		return
	}
	if task.scenario == ScenarioMediaVideoPoll500 {
		writeJSONStatus(w, http.StatusInternalServerError, `{"error":{"message":"Internal server error","type":"server_error"}}`)
		return
	}
	m.advanceVideoTaskLocked(task)
	writeJSONStatus(w, http.StatusOK, m.videoObjectBodyLocked(task))
}

// serveVideoContent implements GET /v1/videos/{id}/content: the video/mp4
// binary channel is open only for completed, non-expired jobs; everything
// else (unknown id, not completed yet, failed, expired artifact) answers 404.
func (m *Server) serveVideoContent(w http.ResponseWriter, id string) {
	m.mu.Lock()
	task, ok := m.videos[id]
	downloadable := ok && task.status == videoStatusCompleted && task.scenario != ScenarioMediaVideoContentExpired
	m.mu.Unlock()
	if !downloadable {
		writeJSONStatus(w, http.StatusNotFound, `{"error":{"message":"Video content not found or expired","type":"invalid_request_error","code":"content_not_found"}}`)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mp4Payload())
}

// serveVideoDelete implements DELETE /v1/videos/{id}: an existing job is
// removed and 204 answered (OpenAI semantics — a later GET of the id is
// 404); unknown ids answer the OpenAI 404 error JSON.
func (m *Server) serveVideoDelete(w http.ResponseWriter, id string) {
	m.mu.Lock()
	_, ok := m.videos[id]
	if ok {
		delete(m.videos, id)
	}
	m.mu.Unlock()
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, videoNotFoundBody(id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveVideoList implements GET /v1/videos: every registered job as its
// current video object, in (createdAt, id) order for stable golden diffs.
func (m *Server) serveVideoList(w http.ResponseWriter) {
	m.mu.Lock()
	tasks := make([]*videoTask, 0, len(m.videos))
	for _, task := range m.videos {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if !tasks[i].createdAt.Equal(tasks[j].createdAt) {
			return tasks[i].createdAt.Before(tasks[j].createdAt)
		}
		return tasks[i].id < tasks[j].id
	})
	bodies := make([]string, len(tasks))
	for i, task := range tasks {
		bodies[i] = m.videoObjectBodyLocked(task)
	}
	m.mu.Unlock()
	writeJSONStatus(w, http.StatusOK, `{"object":"list","data":[`+strings.Join(bodies, ",")+`]}`)
}

// videoNotFoundBody renders the OpenAI error JSON for an unknown video id,
// echoing the id for assertion.
func videoNotFoundBody(id string) string {
	message, _ := json.Marshal("Video not found: " + id)
	return fmt.Sprintf(`{"error":{"message":%s,"type":"invalid_request_error","code":"video_not_found"}}`, message)
}
