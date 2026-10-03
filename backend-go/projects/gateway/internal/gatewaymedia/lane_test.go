package gatewaymedia

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

func TestRequestLaneForPath(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   gatewayproto.RequestLane
		ok     bool
	}{
		{"speech", "POST", "/v1/audio/speech", gatewayproto.LaneAudio, true},
		{"speech no v1", "POST", "/audio/speech", gatewayproto.LaneAudio, true},
		{"speech query", "POST", "/v1/audio/speech?x=1", gatewayproto.LaneAudio, true},
		{"transcriptions", "POST", "/v1/audio/transcriptions", gatewayproto.LaneAudio, true},
		{"translations", "POST", "/v1/audio/translations", gatewayproto.LaneAudio, true},
		{"transcriptions no v1", "POST", "/audio/transcriptions", gatewayproto.LaneAudio, true},
		{"upper path", "POST", "/V1/AUDIO/SPEECH", gatewayproto.LaneAudio, true},
		{"lowercase method", "post", "/v1/audio/speech", "", false},
		{"GET speech", "GET", "/v1/audio/speech", "", false},
		{"chat completions", "POST", "/v1/chat/completions", "", false},
		{"models", "GET", "/v1/models", "", false},
		{"audio jobs (M2)", "POST", "/v1/audio/jobs", "", false},
		{"audio subpath not family", "POST", "/v1/audio/speech/extra", "", false},
		{"realtime ws", "POST", "/v1/realtime", "", false},
		{"empty", "POST", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RequestLaneForPath(tc.method, tc.path)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("RequestLaneForPath(%q, %q) = (%q, %v), want (%q, %v)",
					tc.method, tc.path, got, ok, tc.want, tc.ok)
			}
		})
	}
}
