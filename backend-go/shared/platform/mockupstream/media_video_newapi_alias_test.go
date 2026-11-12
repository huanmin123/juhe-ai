package mockupstream

// New API video-face alias (2026-11-12 gateway migration): the public gateway
// path is POST/GET /v1/video/generations (+/{id}/content), while the mock's
// internal job handlers stay on the OpenAI /v1/videos vocabulary. These tests
// prove the alias accepts the New API paths with identical create/poll/content
// semantics, and that list/cancel stay outside the New API surface (404).

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestMediaVideoNewAPIFaceAlias mirrors TestMediaAudioNewAPITaskAlias for the
// video face: create/poll/content flow through the alias onto the same job
// table, list/cancel stay 404.
func TestMediaVideoNewAPIFaceAlias(t *testing.T) {
	m := New()
	defer m.Close()

	code, _, raw := doMedia(t, m.Server, http.MethodPost, "/v1/video/generations",
		string(ScenarioMediaVideoOKPoll3), "application/json", []byte(videoCreateBody))
	if code != http.StatusOK {
		t.Fatalf("new API video create status = %d, want 200 (%s)", code, raw)
	}
	var created parsedVideoObject
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("new API video create body: %v (%s)", err, raw)
	}
	if !videoIDPattern.MatchString(created.ID) {
		t.Fatalf("new API video create id = %q, want video_ hex", created.ID)
	}

	code, _, pollRaw := doMedia(t, m.Server, http.MethodGet, "/v1/video/generations/"+created.ID, "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("new API video poll status = %d, want 200 (%s)", code, pollRaw)
	}
	var polled parsedVideoObject
	if err := json.Unmarshal(pollRaw, &polled); err != nil {
		t.Fatalf("new API video poll body: %v (%s)", err, pollRaw)
	}
	assertVideoState(t, "new API video poll #1", polled, videoStatusInProgress, 33, false, false)

	// content before completion is not a 200 download.
	if code, header, body := doMedia(t, m.Server, http.MethodGet, "/v1/video/generations/"+created.ID+"/content", "", "", nil); code == http.StatusOK {
		t.Fatalf("video content before completed must not be 200 (content-type %q, %d bytes)", header.Get("Content-Type"), len(body))
	}

	// List/cancel are not part of the New API task surface.
	if code, _, _ := doMedia(t, m.Server, http.MethodGet, "/v1/video/generations", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("new API video list status = %d, want 404", code)
	}
	if code, _, _ := doMedia(t, m.Server, http.MethodDelete, "/v1/video/generations/"+created.ID, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("new API video delete status = %d, want 404", code)
	}
}

// TestMediaVideoNewAPIFaceWhitelist pins the exact method+path matrix of the
// New API video face: POST on the bare path, GET on /{id} and /{id}/content
// only; everything else keeps the 404 behavior.
func TestMediaVideoNewAPIFaceWhitelist(t *testing.T) {
	m := New()
	defer m.Close()

	bad := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/video/generations"},
		{http.MethodPut, "/v1/video/generations"},
		{http.MethodDelete, "/v1/video/generations"},
		{http.MethodPost, "/v1/video/generations/video_1"},
		{http.MethodDelete, "/v1/video/generations/video_1"},
		{http.MethodGet, "/v1/video/generations/video_1/extra"},
		{http.MethodPost, "/v1/video/generations/video_1/content"},
		{http.MethodGet, "/v1/video/generationsx"},
	}
	for _, tc := range bad {
		if code, _, _ := doMedia(t, m.Server, tc.method, tc.path, "", "", nil); code != http.StatusNotFound {
			t.Fatalf("%s %s: status = %d, want 404", tc.method, tc.path, code)
		}
	}
}
