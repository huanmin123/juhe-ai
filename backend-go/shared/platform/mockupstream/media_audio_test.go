package mockupstream

// New API generic task face for long audio (2026-11-12 gateway migration):
// the mock accepts POST /v1/tasks/audio, GET /v1/tasks/audio/{id} and
// .../{id}/content and aliases them onto the existing async job handler. The
// mock has no dedicated long-audio upstream face, so the alias reuses the
// OpenAI videos job table (same create/poll/content semantics). List/cancel
// stay outside the New API task surface (404), matching the gateway's own
// public contract.

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestMediaAudioNewAPITaskAlias proves the New API generic task-face audio
// paths are accepted and dispatched onto the existing async job handler, and
// that the unsupported list/cancel shapes stay 404.
func TestMediaAudioNewAPITaskAlias(t *testing.T) {
	m := New()
	defer m.Close()

	code, _, raw := doMedia(t, m.Server, http.MethodPost, "/v1/tasks/audio",
		string(ScenarioMediaVideoOKPoll3), "application/json", []byte(videoCreateBody))
	if code != http.StatusOK {
		t.Fatalf("new API audio create status = %d, want 200 (%s)", code, raw)
	}
	var created parsedVideoObject
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("new API audio create body: %v (%s)", err, raw)
	}
	if !videoIDPattern.MatchString(created.ID) {
		t.Fatalf("new API audio create id = %q, want video_ hex (shared job table)", created.ID)
	}

	code, _, pollRaw := doMedia(t, m.Server, http.MethodGet, "/v1/tasks/audio/"+created.ID, "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("new API audio poll status = %d, want 200 (%s)", code, pollRaw)
	}
	var polled parsedVideoObject
	if err := json.Unmarshal(pollRaw, &polled); err != nil {
		t.Fatalf("new API audio poll body: %v (%s)", err, pollRaw)
	}
	assertVideoState(t, "new API audio poll #1", polled, videoStatusInProgress, 33, false, false)

	// content before completion is not a 200 download.
	if code, header, body := doMedia(t, m.Server, http.MethodGet, "/v1/tasks/audio/"+created.ID+"/content", "", "", nil); code == http.StatusOK {
		t.Fatalf("audio content before completed must not be 200 (content-type %q, %d bytes)", header.Get("Content-Type"), len(body))
	}

	// List/cancel are not part of the New API task surface.
	if code, _, _ := doMedia(t, m.Server, http.MethodGet, "/v1/tasks/audio", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("new API audio list status = %d, want 404", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodDelete, "/v1/tasks/audio/"+created.ID, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("new API audio delete status = %d, want 404", code)
	}
}

// TestMediaAudioNewAPITaskWhitelist proves the exact method+path matrix of the
// New API audio task alias: POST on the bare path, GET on /{id} and
// /{id}/content only; everything else keeps the 404 behavior.
func TestMediaAudioNewAPITaskWhitelist(t *testing.T) {
	m := New()
	defer m.Close()

	bad := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/tasks/audio"},
		{http.MethodPut, "/v1/tasks/audio"},
		{http.MethodDelete, "/v1/tasks/audio"},
		{http.MethodPost, "/v1/tasks/audio/audiojob_1"},
		{http.MethodDelete, "/v1/tasks/audio/audiojob_1"},
		{http.MethodGet, "/v1/tasks/audio/audiojob_1/extra"},
		{http.MethodPost, "/v1/tasks/audio/audiojob_1/content"},
		{http.MethodGet, "/v1/tasks/audio2"},
	}
	for _, tc := range bad {
		if code, _, _ := doMedia(t, m.Server, tc.method, tc.path, "", "", nil); code != http.StatusNotFound {
			t.Fatalf("%s %s: status = %d, want 404", tc.method, tc.path, code)
		}
	}
}
