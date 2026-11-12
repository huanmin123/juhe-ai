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
		{"videos create", "POST", "/v1/videos", gatewayproto.LaneVideo, true},
		{"videos create no v1", "POST", "/videos", gatewayproto.LaneVideo, true},
		{"videos create query", "POST", "/v1/videos?x=1", gatewayproto.LaneVideo, true},
		{"new api video create", "POST", "/v1/video/generations", gatewayproto.LaneVideo, true},
		{"new api video create no v1", "POST", "/video/generations", gatewayproto.LaneVideo, true},
		{"new api video create query", "POST", "/v1/video/generations?x=1", gatewayproto.LaneVideo, true},
		{"new api video item get not dispatch lane", "GET", "/v1/video/generations/video_1", "", false},
		{"new api video content not dispatch lane", "GET", "/v1/video/generations/video_1/content", "", false},
		{"new api video delete not dispatch lane", "DELETE", "/v1/video/generations/video_1", "", false},
		{"new api video subpath not family", "POST", "/v1/video/generations/video_1", "", false},
		{"videos item get not dispatch lane", "GET", "/v1/videos/video_1", "", false},
		{"videos content not dispatch lane", "GET", "/v1/videos/video_1/content", "", false},
		{"videos delete not dispatch lane", "DELETE", "/v1/videos/video_1", "", false},
		{"videos subpath not family", "POST", "/v1/videos/video_1", "", false},
		{"upper path", "POST", "/V1/AUDIO/SPEECH", gatewayproto.LaneAudio, true},
		{"lowercase method", "post", "/v1/audio/speech", "", false},
		{"GET speech", "GET", "/v1/audio/speech", "", false},
		{"chat completions", "POST", "/v1/chat/completions", "", false},
		{"models", "GET", "/v1/models", "", false},
		{"embeddings utility", "POST", "/v1/embeddings", gatewayproto.LaneUtility, true},
		{"embeddings no v1", "POST", "/embeddings", gatewayproto.LaneUtility, true},
		{"embeddings subpath utility", "POST", "/v1/embeddings/multimodal", gatewayproto.LaneUtility, true},
		{"rerank utility", "POST", "/v1/rerank", gatewayproto.LaneUtility, true},
		{"rerank query utility", "POST", "/v1/rerank?x=1", gatewayproto.LaneUtility, true},
		{"moderations utility", "POST", "/v1/moderations", gatewayproto.LaneUtility, true},
		{"moderations subpath utility", "POST", "/moderations/extra", gatewayproto.LaneUtility, true},
		{"GET embeddings not dispatch lane", "GET", "/v1/embeddings", "", false},
		{"audio jobs create (M3f, video lane)", "POST", "/v1/audio/jobs", gatewayproto.LaneVideo, true},
		{"new api audio task create (video lane)", "POST", "/v1/tasks/audio", gatewayproto.LaneVideo, true},
		{"new api audio task create no v1", "POST", "/tasks/audio", gatewayproto.LaneVideo, true},
		{"new api audio task create query", "POST", "/v1/tasks/audio?x=1", gatewayproto.LaneVideo, true},
		{"new api audio task item get not dispatch lane", "GET", "/v1/tasks/audio/audiojob_1", "", false},
		{"new api audio task content not dispatch lane", "GET", "/v1/tasks/audio/audiojob_1/content", "", false},
		{"new api audio task delete not dispatch lane", "DELETE", "/v1/tasks/audio/audiojob_1", "", false},
		{"new api audio task subpath not family", "POST", "/v1/tasks/audio/audiojob_1", "", false},
		{"audio subpath not family", "POST", "/v1/audio/speech/extra", "", false},
		{"realtime ws upgrade (M5b)", "GET", "/v1/realtime", gatewayproto.LaneRealtime, true},
		{"realtime ws upgrade no v1", "GET", "/realtime", gatewayproto.LaneRealtime, true},
		{"realtime ws upgrade query", "GET", "/v1/realtime?model=gpt-realtime", gatewayproto.LaneRealtime, true},
		{"realtime POST is not the upgrade shape", "POST", "/v1/realtime", "", false},
		{"realtime subpath not family", "GET", "/v1/realtime/extra", "", false},
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
