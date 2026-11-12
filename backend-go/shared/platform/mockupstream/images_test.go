package mockupstream

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeImagesScenarios(t *testing.T) {
	server := New()
	cases := []struct {
		scenario Scenario
		want     string
	}{
		{ScenarioMediaImagesOK, `"url":"https://mock-images.example/generated/mock-0001.png"`},
		{ScenarioMediaImagesB64OK, `"b64_json":"bW9jay1iNjQ="`},
		{ScenarioMediaImagesNoImage, `"data":[]`},
		{ScenarioMediaImagesRelayModelMismatch, `"model":"grok-imagine-image-2.0"`},
		{ScenarioMediaImagesEditOK, `"url":"https://mock-images.example/generated/edit-0001.png"`},
		{ScenarioMediaImagesEditNoImage, `"data":[]`},
		{ScenarioMediaXaiImageOK, `"mime_type":"image/png"`},
		{ScenarioMediaXaiImageNoImage, `"data":[]`},
		{ScenarioMediaVolcengineImageOK, `"generated_images":1`},
		{ScenarioMediaVolcengineImageNoImage, `"generated_images":0`},
	}
	for _, tc := range cases {
		t.Run(string(tc.scenario), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			server.serveImages(recorder, 0, tc.scenario)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d", recorder.Code)
			}
			if !strings.Contains(recorder.Body.String(), tc.want) {
				t.Fatalf("body = %s, want contains %s", recorder.Body.String(), tc.want)
			}
		})
	}
}

func TestImageMediaEndpointWhitelist(t *testing.T) {
	for _, path := range []string{
		"/v1/images/generations",
		"/v1/images/edits",
		"/api/v3/images/generations",
		"/api/v1/services/aigc/multimodal-generation/generation",
	} {
		if !acceptsMediaEndpoint(http.MethodPost, path) {
			t.Errorf("image endpoint %s not accepted", path)
		}
	}
	if acceptsMediaEndpoint(http.MethodGet, "/v1/images/edits") {
		t.Error("GET /v1/images/edits must not be accepted")
	}
}

// TestServeImagesEditEndpointDispatch 验证 /v1/images/edits 经引擎分派到
// serveImages 并返回编辑 OK 载荷（2026-11-12 T216 mock 契约）。
func TestServeImagesEditEndpointDispatch(t *testing.T) {
	server := New()
	defer server.Close()
	code, _, body := doMedia(t, server.Server, http.MethodPost, "/v1/images/edits",
		string(ScenarioMediaImagesEditOK), "multipart/form-data; boundary=BOUNDARY",
		[]byte("--BOUNDARY\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\ngpt-image-2\r\n--BOUNDARY--\r\n"))
	if code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", code, body)
	}
	if !strings.Contains(string(body), "edit-0001.png") {
		t.Fatalf("body = %s, want edit-0001.png", body)
	}
}

func TestServeQwenImageScenarios(t *testing.T) {
	server := New()
	cases := []struct {
		scenario Scenario
		want     string
	}{
		{ScenarioMediaQwenImageOK, `"image_count":1`},
		{ScenarioMediaQwenImageV3OK, `"output_image_count":1`},
		{ScenarioMediaQwenImageNoImage, `"content":[]`},
	}
	for _, tc := range cases {
		t.Run(string(tc.scenario), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			server.serveQwenImage(recorder, tc.scenario)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d", recorder.Code)
			}
			if !strings.Contains(recorder.Body.String(), tc.want) {
				t.Fatalf("body = %s, want contains %s", recorder.Body.String(), tc.want)
			}
		})
	}
}
