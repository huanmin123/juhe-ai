package gatewaymedia

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestVolcengineTTSStreamModelDispatch 钉住 2.0 面分派判定：精确匹配
// seed-tts-2.0，1.0 系模型名（含占位名）不进入流式面。
func TestVolcengineTTSStreamModelDispatch(t *testing.T) {
	if !IsVolcengineTTSStreamModel("seed-tts-2.0") || !IsVolcengineTTSStreamModel(" seed-tts-2.0 ") {
		t.Fatalf("seed-tts-2.0 应命中流式面")
	}
	for _, name := range []string{"seed-audio-1.0", "doubao-tts", "seed-tts-1.0", ""} {
		if IsVolcengineTTSStreamModel(name) {
			t.Fatalf("%q 不应命中流式面", name)
		}
	}
}

// TestVolcengineTTSStreamBuildRequest 钉住 unidirectional 请求构造（V1a
// 报文契约）：路径 /api/v3/tts/unidirectional，req_params 三件套 +
// audio_params.format，speed → speech_rate 换算 (speed-1)×100（2026-10-23
// 官方字段表回填）；2.0 词表与流式面区间 [0.5, 2.0] 校验。
func TestVolcengineTTSStreamBuildRequest(t *testing.T) {
	adapter := volcengineSpeechAdapter{}
	path, body, err := adapter.BuildRequest(SpeechRequest{
		Model: VolcengineTTSStreamModel, Input: "流式探测。", Voice: "zh_female_demo",
		ResponseFormat: "mp3",
	})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if path != "/api/v3/tts/unidirectional" {
		t.Fatalf("path = %q", path)
	}
	var decoded struct {
		User      map[string]any `json:"user"`
		ReqParams map[string]any `json:"req_params"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body 非法 JSON: %v", err)
	}
	req := decoded.ReqParams
	if req["text"] != "流式探测。" || req["speaker"] != "zh_female_demo" {
		t.Fatalf("req_params = %v", req)
	}
	params, _ := req["audio_params"].(map[string]any)
	if params == nil || params["format"] != "mp3" {
		t.Fatalf("audio_params = %v", params)
	}

	// speed → audio_params.speech_rate（2026-10-23 官方字段名回填），
	// 倍率换算 (speed-1)×100：1.5 倍速 → 50，非倍率直传。
	speedValue := 1.5
	_, body, err = adapter.BuildRequest(SpeechRequest{
		Model: VolcengineTTSStreamModel, Input: "x", Voice: "v", Speed: &speedValue,
	})
	if err != nil {
		t.Fatalf("speed BuildRequest: %v", err)
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("speed body 非法 JSON: %v", err)
	}
	params, _ = decoded.ReqParams["audio_params"].(map[string]any)
	if params == nil || params["speech_rate"] != float64(50) {
		t.Fatalf("speed=1.5 应换算 speech_rate=50，audio_params = %v", params)
	}

	// 流式面官方可表达倍速边界 [0.5, 2.0]（speech_rate 恰落 ±端点）。
	for _, tc := range []struct{ speed, want float64 }{{0.5, -50}, {2.0, 100}} {
		speed := tc.speed
		if _, body, err := adapter.BuildRequest(SpeechRequest{
			Model: VolcengineTTSStreamModel, Input: "x", Voice: "v", Speed: &speed,
		}); err != nil {
			t.Fatalf("speed=%v 边界应通过: %v", speed, err)
		} else {
			var decoded struct {
				ReqParams struct {
					AudioParams struct {
						SpeechRate int `json:"speech_rate"`
					} `json:"audio_params"`
				} `json:"req_params"`
			}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("speed=%v body 非法 JSON: %v", speed, err)
			}
			if got := decoded.ReqParams.AudioParams.SpeechRate; got != int(tc.want) {
				t.Fatalf("speed=%v 应换算 speech_rate=%d，got %d", speed, int(tc.want), got)
			}
		}
	}

	// 越界本地 400（§2.4 规则 2）：0.2/3.0 是同步面 [0.2,3.0] 合法值，
	// 但流式面 speech_rate [-50,100] 无法表达，本地拒绝不静默外发。
	for _, speed := range []float64{0.2, 3.0, 5.0} {
		speed := speed
		if _, _, err := adapter.BuildRequest(SpeechRequest{
			Model: VolcengineTTSStreamModel, Input: "x", Voice: "v", Speed: &speed,
		}); err == nil || !strings.Contains(err.Error(), "[0.5, 2.0]") {
			t.Fatalf("speed=%v 越界应 400: %v", speed, err)
		}
	}
	if _, _, err := adapter.BuildRequest(SpeechRequest{
		Model: VolcengineTTSStreamModel, Input: "x", Voice: "v", ResponseFormat: "opus",
	}); err == nil || !strings.Contains(err.Error(), "mp3/pcm/ogg_opus/wav") {
		t.Fatalf("opus 应被 2.0 词表拒绝: %v", err)
	}
	// 2.0 正式支持 pcm（与 1.0 挂起不采纳不同）。
	if _, _, err := adapter.BuildRequest(SpeechRequest{
		Model: VolcengineTTSStreamModel, Input: "x", Voice: "v", ResponseFormat: "pcm",
	}); err != nil {
		t.Fatalf("pcm 应被 2.0 词表接受: %v", err)
	}
}

// TestVolcengineTTSStreamSpeedRate 钉住倍率→官方 speech_rate 换算样本
// （2026-10-23 官方文档字段表：默认 0、[-50, 100]、100=2.0 倍速、-50=0.5
// 倍速；math.Round 半数远离零——1.15 的 float64 乘积 14.999999999999998
// 取整后仍命中 15，此样本防实现改用截断/浮点比较退化）。
func TestVolcengineTTSStreamSpeedRate(t *testing.T) {
	cases := []struct {
		speed float64
		want  int
	}{
		{0.5, -50},
		{0.75, -25},
		{1.0, 0},
		{1.15, 15},
		{1.5, 50},
		{2.0, 100},
	}
	for _, tc := range cases {
		if got := volcengineTTSStreamSpeedRate(tc.speed); got != tc.want {
			t.Fatalf("volcengineTTSStreamSpeedRate(%v) = %d, want %d", tc.speed, got, tc.want)
		}
	}
}

// TestVolcengineTTSStreamTransformResponse 钉住 NDJSON 转换（A 级帧形态，
// 2026-10-18 真实上游 2.0 出音实测）：音频帧 data 为 base64 字符串、句级帧
// 丢弃、终止帧 code 20000000、多分块按序拼接、错误行上抛、content-type 按词表推导。
func TestVolcengineTTSStreamTransformResponse(t *testing.T) {
	adapter := volcengineSpeechAdapter{}
	chunkA := base64.StdEncoding.EncodeToString([]byte{0xFF, 0xFB, 0x90, 0x00})
	chunkB := base64.StdEncoding.EncodeToString([]byte{0x01, 0x02})
	ndjson := strings.Join([]string{
		`{"code":0,"message":"","data":"` + chunkA + `"}`,
		`{"code":0,"message":"","data":"` + chunkB + `"}`,
		`{"code":0,"message":"","data":null,"sentence":{"phonemes":[],"text":"x","words":[]}}`,
		`{"code":20000000,"message":"OK","data":null}`,
	}, "\n")
	audio, contentType, err := adapter.TransformResponse([]byte(ndjson), SpeechRequest{
		Model: VolcengineTTSStreamModel, ResponseFormat: "mp3"})
	if err != nil {
		t.Fatalf("TransformResponse: %v", err)
	}
	want := []byte{0xFF, 0xFB, 0x90, 0x00, 0x01, 0x02}
	if string(audio) != string(want) {
		t.Fatalf("audio = %v, want %v", audio, want)
	}
	if contentType != "audio/mpeg" {
		t.Fatalf("contentType = %q", contentType)
	}

	// 错误行（55000000 实测形态）→ 上抛。
	errBody := `{"reqid":"","code":55000000,"message":"resource ID is mismatched with speaker related resource"}`
	if _, _, err := adapter.TransformResponse([]byte(errBody), SpeechRequest{Model: VolcengineTTSStreamModel}); err == nil ||
		!strings.Contains(err.Error(), "speaker related resource") {
		t.Fatalf("错误行应上抛: %v", err)
	}

	// 无音频分块且无错误 → 协议违约。
	if _, _, err := adapter.TransformResponse([]byte(`{"code":0,"data":null}`), SpeechRequest{Model: VolcengineTTSStreamModel}); err == nil {
		t.Fatalf("空音频应违约上抛")
	}

	// pcm → audio/pcm。
	_, contentType, err = adapter.TransformResponse([]byte(`{"code":0,"data":"`+base64.StdEncoding.EncodeToString([]byte{1})+`"}`),
		SpeechRequest{Model: VolcengineTTSStreamModel, ResponseFormat: "pcm"})
	if err != nil || contentType != "audio/pcm" {
		t.Fatalf("pcm contentType = %q err=%v", contentType, err)
	}
}
