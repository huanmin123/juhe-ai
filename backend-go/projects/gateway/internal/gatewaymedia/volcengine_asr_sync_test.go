package gatewaymedia

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestVolcASRSyncAudioFormat(t *testing.T) {
	cases := []struct {
		contentType string
		filename    string
		want        string
		wantErr     bool
	}{
		{"audio/mpeg", "", "mp3", false},
		{"audio/mpeg; codecs=mp3", "", "mp3", false},
		{"audio/x-wav", "", "wav", false},
		{"audio/ogg", "", "ogg", false},
		{"audio/L16", "", "pcm", false},
		{"application/octet-stream", "meeting.WAV", "wav", false},
		{"", "voice.mp3", "mp3", false},
		{"application/octet-stream", "voice.m4a", "", true},
		{"audio/mp4", "voice.mp4", "", true},
	}
	for _, tc := range cases {
		got, err := VolcASRSyncAudioFormat(tc.contentType, tc.filename)
		if tc.wantErr {
			if err == nil || !errors.Is(err, ErrParamUnsupported) {
				t.Fatalf("VolcASRSyncAudioFormat(%q,%q) err=%v, want ErrParamUnsupported", tc.contentType, tc.filename, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("VolcASRSyncAudioFormat(%q,%q) = %q,%v want %q", tc.contentType, tc.filename, got, err, tc.want)
		}
	}
}

func TestVolcengineTranscriptionBuildRequest(t *testing.T) {
	adapter := volcengineTranscriptionAdapter{}
	path, body, headers, err := adapter.BuildRequest(TranscriptionRequest{
		Model:       "volc.seedasr.auc",
		Audio:       []byte("audio-bytes"),
		AudioFormat: "mp3",
	})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if path != VolcASRSyncPath {
		t.Fatalf("path = %q, want %q", path, VolcASRSyncPath)
	}
	if headers["X-Api-Resource-Id"] != VolcASRSyncResourceID {
		t.Fatalf("X-Api-Resource-Id = %q", headers["X-Api-Resource-Id"])
	}
	if !strings.HasPrefix(headers["X-Api-Request-Id"], "volcsync-") || len(headers["X-Api-Request-Id"]) <= len("volcsync-") {
		t.Fatalf("X-Api-Request-Id = %q", headers["X-Api-Request-Id"])
	}
	if headers["X-Api-Sequence"] != "-1" {
		t.Fatalf("X-Api-Sequence = %q", headers["X-Api-Sequence"])
	}
	var payload struct {
		User struct {
			UID string `json:"uid"`
		} `json:"user"`
		Audio struct {
			Data   string `json:"data"`
			Format string `json:"format"`
		} `json:"audio"`
		Request struct {
			ModelName string `json:"model_name"`
		} `json:"request"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload.Audio.Data != base64.StdEncoding.EncodeToString([]byte("audio-bytes")) {
		t.Fatalf("audio.data = %q", payload.Audio.Data)
	}
	if payload.Audio.Format != "mp3" || payload.Request.ModelName != volcASRSyncModelName || payload.User.UID == "" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestVolcengineTranscriptionBuildRequestRejectsOversizeAndFormat(t *testing.T) {
	adapter := volcengineTranscriptionAdapter{}
	oversize := make([]byte, VolcASRSyncMaxAudioBytes+1)
	if _, _, _, err := adapter.BuildRequest(TranscriptionRequest{Model: "m", Audio: oversize, AudioFormat: "mp3"}); err == nil {
		t.Fatal("oversize audio must be rejected")
	}
	if _, _, _, err := adapter.BuildRequest(TranscriptionRequest{Model: "m", Audio: []byte("x"), AudioFormat: "mp3", ResponseFormat: "yaml"}); err == nil {
		t.Fatal("unsupported response_format must be rejected")
	}
	if _, _, _, err := adapter.BuildRequest(TranscriptionRequest{Model: "m", Audio: []byte("x")}); err == nil {
		t.Fatal("missing audio format must be rejected")
	}
}

func volcASRSyncTestHeader() http.Header {
	header := http.Header{}
	header.Set("X-Api-Status-Code", volcASRSyncSuccessCode)
	return header
}

func TestVolcengineTranscriptionTransformJSON(t *testing.T) {
	adapter := volcengineTranscriptionAdapter{}
	upstream := []byte(`{"audio_info":{"duration":1698},"result":{"text":"hello world","utterances":[{"start_time":80,"end_time":1400,"text":"hello"},{"start_time":1400,"end_time":1698,"text":"world"}]}}`)
	body, contentType, err := adapter.TransformResponse(200, volcASRSyncTestHeader(), upstream, TranscriptionRequest{ResponseFormat: "json"})
	if err != nil {
		t.Fatalf("TransformResponse: %v", err)
	}
	if contentType != "application/json" {
		t.Fatalf("contentType = %q", contentType)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["text"] != "hello world" || payload["duration"] != 1.698 {
		t.Fatalf("payload = %#v", payload)
	}

	body, contentType, err = adapter.TransformResponse(200, volcASRSyncTestHeader(), upstream, TranscriptionRequest{ResponseFormat: "verbose_json", Language: "en"})
	if err != nil {
		t.Fatalf("verbose TransformResponse: %v", err)
	}
	if contentType != "application/json" {
		t.Fatalf("verbose contentType = %q", contentType)
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode verbose: %v", err)
	}
	segments, ok := payload["segments"].([]any)
	if !ok || len(segments) != 2 || payload["language"] != "en" {
		t.Fatalf("verbose payload = %#v", payload)
	}
}

func TestVolcengineTranscriptionTransformTextAndSubtitles(t *testing.T) {
	adapter := volcengineTranscriptionAdapter{}
	upstream := []byte(`{"audio_info":{"duration":1698},"result":{"text":"hello world","utterances":[{"start_time":80,"end_time":1400,"text":"hello"}]}}`)
	body, contentType, err := adapter.TransformResponse(200, volcASRSyncTestHeader(), upstream, TranscriptionRequest{ResponseFormat: "text"})
	if err != nil || string(body) != "hello world" || contentType != "text/plain; charset=utf-8" {
		t.Fatalf("text transform = %q %q %v", body, contentType, err)
	}
	body, _, err = adapter.TransformResponse(200, volcASRSyncTestHeader(), upstream, TranscriptionRequest{ResponseFormat: "srt"})
	if err != nil || !strings.Contains(string(body), "00:00:00,080 --> 00:00:01,400") || !strings.Contains(string(body), "hello") {
		t.Fatalf("srt transform = %q %v", body, err)
	}
	body, _, err = adapter.TransformResponse(200, volcASRSyncTestHeader(), upstream, TranscriptionRequest{ResponseFormat: "vtt"})
	if err != nil || !strings.HasPrefix(string(body), "WEBVTT") || !strings.Contains(string(body), "00:00:00.080 --> 00:00:01.400") {
		t.Fatalf("vtt transform = %q %v", body, err)
	}
}

func TestVolcengineTranscriptionTransformRejectsBadStatus(t *testing.T) {
	adapter := volcengineTranscriptionAdapter{}
	header := http.Header{}
	header.Set("X-Api-Status-Code", "45000001")
	header.Set("X-Api-Message", "bad request")
	if _, _, err := adapter.TransformResponse(200, header, []byte(`{}`), TranscriptionRequest{ResponseFormat: "json"}); err == nil {
		t.Fatal("non-success status code must fail")
	}
	if _, _, err := adapter.TransformResponse(200, http.Header{}, []byte(`{"result":{"text":"x"}}`), TranscriptionRequest{ResponseFormat: "json"}); err == nil {
		t.Fatal("missing status code must fail")
	}
}
