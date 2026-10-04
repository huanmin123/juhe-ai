package mockupstream

// Gemini Veo async operation face for the M3 video chain, per
// docs/functions/媒体上游协议契约与Mock上游规格.md §3.2 (engine extension,
// scenario naming) and §5.2 (Veo wire contract, confidence B):
//
//   - POST /v1beta/models/{model}:predictLongRunning   create
//     (200 {"name":"models/<model>/operations/<op>"} — the acceptance
//     credential is the name field)
//   - GET  /v1beta/models/{model}/operations/{op}       poll
//     ({"done":false} | {"done":true,"response":...uri} |
//      {"done":true,"error":{code,message,status}})
//   - POST /v1beta/models/{model}/operations/{op}:cancel cancel
//     (200 {}; the operation is removed so later polls answer 404)
//   - GET  /v1beta/models/{model}/operations/{op}/content artifact download
//     (video/mp4 binary channel; the completed operation's uri points here)
//
// The operation table mirrors the OpenAI videos job table (video.go,
// contract §3.2.2): creation registers the operation under a generated op id
// with the creating scenario as the frozen behavior script; polls advance the
// "poll #N returns state X" script and ignore the poll request's own scenario
// header. Scripts (poll #1 is the first GET after creation):
//
//	media_gemini_video_create_ok:     #1 done:false → #2+ done:true + uri
//	media_gemini_video_poll_running:  never done (stuck-in-queue case)
//	media_gemini_video_poll_done_uri: #1 done:true + uri (fast path)
//	media_gemini_video_poll_error:    #1 done:true + error
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

// M3 gemini veo scenario names (contract §3.2.4 M3 list, §5.2 Mock row).
const (
	ScenarioMediaGeminiVideoCreateOK    Scenario = "media_gemini_video_create_ok"
	ScenarioMediaGeminiVideoPollRunning Scenario = "media_gemini_video_poll_running"
	ScenarioMediaGeminiVideoPollDoneURI Scenario = "media_gemini_video_poll_done_uri"
	ScenarioMediaGeminiVideoPollError   Scenario = "media_gemini_video_poll_error"
)

// veoOperation is one registered gemini async video operation. The name is
// the full operation resource path ("models/<model>/operations/<op>"); the
// terminal uri (GCS signed URL stand-in) renders at response time from the
// server URL so the gateway's direct download points back at this instance.
type veoOperation struct {
	name      string
	model     string
	opID      string
	scenario  Scenario // behavior script, frozen at creation
	pollCount int
	done      bool
	failed    bool
	cancelled bool
}

// veoRequestForm enumerates the method+path shapes of the operation face.
type veoRequestForm int

const (
	veoFormNone veoRequestForm = iota
	veoFormCreate
	veoFormPoll
	veoFormCancel
	veoFormContent
)

// geminiVeoCreateModel returns the {model} segment when the path matches the
// Veo create form POST /v1beta/models/{model}:predictLongRunning with a
// non-empty single-segment model, "" otherwise.
func geminiVeoCreateModel(path string) string {
	const prefix, suffix = "/v1beta/models/", ":predictLongRunning"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	model := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if model == "" || strings.Contains(model, "/") {
		return ""
	}
	return model
}

// geminiVeoOperationRequest matches the operation method+path shapes and
// returns the operation name ("models/<model>/operations/<op>") plus the
// matched form. The poll form is the bare /v1beta/{name} GET; :cancel is a
// POST suffix; /content is the artifact GET tail.
func geminiVeoOperationRequest(method, path string) (string, veoRequestForm) {
	const prefix = "/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return "", veoFormNone
	}
	rest := strings.TrimPrefix(path, prefix)
	model, rest, ok := strings.Cut(rest, "/operations/")
	if !ok || model == "" || strings.Contains(model, "/") {
		return "", veoFormNone
	}
	if rest == "" {
		return "", veoFormNone
	}
	if op, ok := strings.CutSuffix(rest, ":cancel"); ok && op != "" && !strings.Contains(op, "/") {
		if method == http.MethodPost {
			return "models/" + model + "/operations/" + op, veoFormCancel
		}
		return "", veoFormNone
	}
	if op, ok := strings.CutSuffix(rest, "/content"); ok && op != "" && !strings.Contains(op, "/") {
		if method == http.MethodGet {
			return "models/" + model + "/operations/" + op, veoFormContent
		}
		return "", veoFormNone
	}
	if !strings.Contains(rest, "/") && method == http.MethodGet {
		return "models/" + model + "/operations/" + rest, veoFormPoll
	}
	return "", veoFormNone
}

// acceptsGeminiVeoEndpoint whitelists the Veo method+path shapes above.
func acceptsGeminiVeoEndpoint(method, path string) bool {
	if method == http.MethodPost && geminiVeoCreateModel(path) != "" {
		return true
	}
	_, form := geminiVeoOperationRequest(method, path)
	return form != veoFormNone
}

// isGeminiVeoPath reports whether the path belongs to the Veo face. It only
// runs after the method-aware whitelist accepted the request.
func isGeminiVeoPath(path string) bool {
	if geminiVeoCreateModel(path) != "" {
		return true
	}
	if _, form := geminiVeoOperationRequest(http.MethodGet, path); form != veoFormNone {
		return true
	}
	_, form := geminiVeoOperationRequest(http.MethodPost, path)
	return form != veoFormNone
}

// serveGeminiVeo dispatches an accepted Veo request to its endpoint handler.
func (m *Server) serveGeminiVeo(w http.ResponseWriter, r *http.Request, scenario Scenario) {
	if model := geminiVeoCreateModel(r.URL.Path); model != "" && r.Method == http.MethodPost {
		m.serveGeminiVeoCreate(w, scenario, model)
		return
	}
	name, form := geminiVeoOperationRequest(r.Method, r.URL.Path)
	switch form {
	case veoFormPoll:
		m.serveGeminiVeoPoll(w, name)
	case veoFormCancel:
		m.serveGeminiVeoCancel(w, name)
	case veoFormContent:
		m.serveGeminiVeoContent(w, name)
	default:
		writeJSONStatus(w, http.StatusNotFound, `{"error":{"message":"Unknown path","type":"invalid_request_error"}}`)
	}
}

// veoScriptFor normalizes the creating scenario into the operation script.
// Unknown scenarios default to the family OK script (create_ok).
func veoScriptFor(scenario Scenario) Scenario {
	switch scenario {
	case ScenarioMediaGeminiVideoCreateOK, ScenarioMediaGeminiVideoPollRunning,
		ScenarioMediaGeminiVideoPollDoneURI, ScenarioMediaGeminiVideoPollError:
		return scenario
	default:
		return ScenarioMediaGeminiVideoCreateOK
	}
}

// newVeoOperationIDLocked generates an unused 12-hex operation id. Callers
// must hold m.mu.
func (m *Server) newVeoOperationIDLocked() string {
	for {
		var buf [6]byte
		if _, err := rand.Read(buf[:]); err != nil {
			// crypto/rand failure is unrecoverable for id generation; fail
			// loudly instead of silently degrading.
			panic(fmt.Sprintf("mockupstream: veo operation id generation failed: %v", err))
		}
		id := hex.EncodeToString(buf[:])
		suffix := "/operations/" + id
		collision := false
		for name := range m.veoOperations {
			if strings.HasSuffix(name, suffix) {
				collision = true
				break
			}
		}
		if !collision {
			return id
		}
	}
}

// serveGeminiVeoCreate implements POST /v1beta/models/{model}:predictLongRunning
// (contract §5.2): register the operation under a fresh op id with the
// scenario as its frozen script and answer 200 {"name":...} — the acceptance
// credential is the name field (the E2E assertion point).
func (m *Server) serveGeminiVeoCreate(w http.ResponseWriter, scenario Scenario, model string) {
	m.mu.Lock()
	opID := m.newVeoOperationIDLocked()
	operation := &veoOperation{
		name:     "models/" + model + "/operations/" + opID,
		model:    model,
		opID:     opID,
		scenario: veoScriptFor(scenario),
	}
	m.veoOperations[operation.name] = operation
	body, _ := json.Marshal(map[string]string{"name": operation.name})
	m.mu.Unlock()
	writeJSONStatus(w, http.StatusOK, string(body))
}

// veoOperationNotFoundBody renders the Gemini error JSON for an unknown
// (or cancelled) operation name, echoing the name for assertion.
func veoOperationNotFoundBody(name string) string {
	message, _ := json.Marshal("Operation not found: " + name)
	return fmt.Sprintf(`{"error":{"code":404,"message":%s,"status":"NOT_FOUND"}}`, message)
}

// veoArtifactURLLocked renders the completed download locator (contract §5.2:
// GCS signed URL stand-in, ~2-day validity; pointing back at this instance so
// the gateway's credential-free direct GET lands on the content endpoint).
// Callers must hold m.mu (it reads m.URL).
func (m *Server) veoArtifactURLLocked(operation *veoOperation) string {
	return m.URL + "/v1beta/models/" + operation.model + "/operations/" + operation.opID + "/content"
}

// veoOperationBodyLocked renders the operation object for the current state.
// Callers must hold m.mu.
func (m *Server) veoOperationBodyLocked(operation *veoOperation) string {
	if !operation.done {
		return `{"done":false}`
	}
	if operation.failed {
		return `{"done":true,"error":{"code":500,"message":"Video generation failed after the request was accepted","status":"INTERNAL"}}`
	}
	uri, _ := json.Marshal(m.veoArtifactURLLocked(operation))
	return `{"done":true,"response":{"generateVideoResponse":{"generatedSamples":[{"video":{"uri":` + string(uri) + `}}]}}}`
}

// advanceVeoOperationLocked steps the operation script for one poll
// (contract §3.2.2; terminal states are idempotent). Callers must hold m.mu.
func (m *Server) advanceVeoOperationLocked(operation *veoOperation) {
	if operation.done {
		return
	}
	operation.pollCount++
	switch operation.scenario {
	case ScenarioMediaGeminiVideoPollRunning:
		// Never completes: every poll keeps done:false.
	case ScenarioMediaGeminiVideoPollDoneURI:
		operation.done = true
	case ScenarioMediaGeminiVideoPollError:
		operation.done = true
		operation.failed = true
	default: // media_gemini_video_create_ok and the normalized default script
		if operation.pollCount >= 2 {
			operation.done = true
		}
	}
}

// serveGeminiVeoPoll implements GET /v1beta/{name}: unknown (or cancelled)
// names answer the Gemini 404 error JSON; otherwise the frozen script
// advances once and the operation object is returned.
func (m *Server) serveGeminiVeoPoll(w http.ResponseWriter, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	operation, ok := m.veoOperations[name]
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, veoOperationNotFoundBody(name))
		return
	}
	m.advanceVeoOperationLocked(operation)
	writeJSONStatus(w, http.StatusOK, m.veoOperationBodyLocked(operation))
}

// serveGeminiVeoCancel implements POST /v1beta/{name}:cancel: an existing
// operation is removed and 200 {} answered (a later GET of the name is 404,
// mirroring the operations.cancel semantics); unknown names answer 404.
func (m *Server) serveGeminiVeoCancel(w http.ResponseWriter, name string) {
	m.mu.Lock()
	_, ok := m.veoOperations[name]
	if ok {
		delete(m.veoOperations, name)
	}
	m.mu.Unlock()
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, veoOperationNotFoundBody(name))
		return
	}
	writeJSONStatus(w, http.StatusOK, `{}`)
}

// serveGeminiVeoContent implements the artifact download endpoint: the mp4
// binary channel is open only for done, non-failed operations (the uri the
// completed poll returned); everything else (unknown name, running, failed,
// cancelled) answers 404.
func (m *Server) serveGeminiVeoContent(w http.ResponseWriter, name string) {
	m.mu.Lock()
	operation, ok := m.veoOperations[name]
	downloadable := ok && operation.done && !operation.failed
	m.mu.Unlock()
	if !downloadable {
		writeJSONStatus(w, http.StatusNotFound, `{"error":{"code":404,"message":"Video content not found or expired","status":"NOT_FOUND"}}`)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mp4Payload())
}
