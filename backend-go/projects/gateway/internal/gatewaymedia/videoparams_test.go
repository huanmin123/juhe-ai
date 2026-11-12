package gatewaymedia

// 统一参数归一测试（契约 §2.2 视频创建参数表、§2.4 忽略/拒绝/回显规则）：
// 合法归一（含 applied 顺序）、400 分支（L1 必填/形态错误）、能力不支持分支
// （ErrParamUnsupported 与 ignored 回显）。
import (
	"errors"
	"strings"
	"testing"
)

// allSupportedCaps 是全能力假想 adapter（验证归一层不误伤支持面）。
func allSupportedCaps() VideoCapabilities {
	return VideoCapabilities{
		SupportsNegativePrompt: true,
		SupportsSeed:           true,
		SupportsAudio:          true,
		SupportsN:              true,
		SupportsInputReference: true,
		SupportsSeconds:        true,
	}
}

func TestParseVideoParamsValidFull(t *testing.T) {
	params, err := ParseVideoParams(map[string]any{
		"model":           "sora-2",
		"prompt":          "a cat surfing",
		"seconds":         "8", // 契约 §4.3 openai 原生字符串形态
		"size":            "720x1280",
		"n":               float64(2),
		"input_reference": "https://example.com/frame.png",
		"negative_prompt": "blurry",
		"seed":            float64(42),
		"audio":           false,
	}, allSupportedCaps())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if params.Model != "sora-2" || params.Prompt != "a cat surfing" {
		t.Fatalf("L1 字段 = %+v", params)
	}
	if params.Seconds == nil || *params.Seconds != 8 {
		t.Fatalf("seconds = %v", params.Seconds)
	}
	if params.Size != "720x1280" || params.N != 2 {
		t.Fatalf("size/n = %q/%d", params.Size, params.N)
	}
	if params.InputReference == nil || *params.InputReference != "https://example.com/frame.png" {
		t.Fatalf("input_reference = %v", params.InputReference)
	}
	if params.NegativePrompt != "blurry" {
		t.Fatalf("negative_prompt = %q", params.NegativePrompt)
	}
	if params.Seed == nil || *params.Seed != 42 {
		t.Fatalf("seed = %v", params.Seed)
	}
	if params.Audio == nil || *params.Audio {
		t.Fatalf("audio = %v", params.Audio)
	}
	wantApplied := strings.Join([]string{
		"model", "prompt", "seconds", "size", "n", "input_reference",
		"negative_prompt", "seed", "audio",
	}, ",")
	if got := strings.Join(params.ParamsApplied, ","); got != wantApplied {
		t.Fatalf("applied = %q, want %q", got, wantApplied)
	}
	if len(params.ParamsIgnored) != 0 {
		t.Fatalf("全能力下不应有 ignored: %v", params.ParamsIgnored)
	}
}

func TestParseVideoParamsMinimalAndDefaults(t *testing.T) {
	// 数字形态 seconds 与未传 n：n 默认 1 且不进 applied。
	params, err := ParseVideoParams(map[string]any{
		"model":            "sora-2",
		"prompt":           "a cat",
		"seconds":          float64(4),
		"provider_options": map[string]any{"openai": map[string]any{}},
	}, openaiVideoCapsForTest())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if params.N != 1 {
		t.Fatalf("n 默认 = %d, want 1", params.N)
	}
	if got := strings.Join(params.ParamsApplied, ","); got != "model,prompt,seconds" {
		t.Fatalf("applied = %q", got)
	}
	if len(params.ParamsIgnored) != 0 {
		t.Fatalf("ignored = %v", params.ParamsIgnored)
	}
}

func TestParseVideoParamsOpenAIIgnored(t *testing.T) {
	// openai 现状：negative_prompt/seed/audio 均不支持 → 忽略 + 回显
	//（契约 §2.4 规则 1，不静默）；n=1 合法（n>1 才是 400 语义）。
	params, err := ParseVideoParams(map[string]any{
		"model":           "sora-2",
		"prompt":          "a cat",
		"n":               float64(1),
		"negative_prompt": "blurry",
		"seed":            float64(7),
		"audio":           true,
	}, openaiVideoCapsForTest())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	wantIgnored := "negative_prompt,seed,audio"
	if got := strings.Join(params.ParamsIgnored, ","); got != wantIgnored {
		t.Fatalf("ignored = %q, want %q", got, wantIgnored)
	}
	for _, key := range []string{"negative_prompt", "seed", "audio"} {
		for _, applied := range params.ParamsApplied {
			if applied == key {
				t.Fatalf("被忽略的键 %s 不应出现在 applied", key)
			}
		}
	}
	// 参数本体保留在结构上（M3 支持厂商复用），仅裁决为 ignored。
	if params.NegativePrompt != "blurry" || params.Seed == nil || params.Audio == nil {
		t.Fatalf("被忽略参数仍应保留在结构: %+v", params)
	}
}

func TestParseVideoParamsValidation(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want string // 错误消息包含片段
	}{
		{"缺 model", map[string]any{"prompt": "p"}, "model"},
		{"缺 prompt", map[string]any{"model": "sora-2"}, "prompt"},
		{"空 prompt", map[string]any{"model": "sora-2", "prompt": "  "}, "prompt"},
		{"seconds 非数字字符串", map[string]any{"model": "m", "prompt": "p", "seconds": "four"}, "seconds"},
		{"seconds 零", map[string]any{"model": "m", "prompt": "p", "seconds": float64(0)}, "seconds"},
		{"seconds 负数", map[string]any{"model": "m", "prompt": "p", "seconds": float64(-4)}, "seconds"},
		{"seconds 类型错误", map[string]any{"model": "m", "prompt": "p", "seconds": true}, "seconds"},
		{"size 形态错误", map[string]any{"model": "m", "prompt": "p", "size": "720p"}, "size"},
		{"size 大写 X", map[string]any{"model": "m", "prompt": "p", "size": "1280X720"}, "size"},
		{"size 非数字", map[string]any{"model": "m", "prompt": "p", "size": "axb"}, "size"},
		{"size 前导零", map[string]any{"model": "m", "prompt": "p", "size": "01280x720"}, "size"},
		{"n 零", map[string]any{"model": "m", "prompt": "p", "n": float64(0)}, "n"},
		{"n 非整数", map[string]any{"model": "m", "prompt": "p", "n": 1.5}, "n"},
		{"n 类型错误", map[string]any{"model": "m", "prompt": "p", "n": "2"}, "n"},
		{"seed 非整数", map[string]any{"model": "m", "prompt": "p", "seed": 1.5}, "seed"},
		{"audio 类型错误", map[string]any{"model": "m", "prompt": "p", "audio": "yes"}, "audio"},
		{"negative_prompt 类型错误", map[string]any{"model": "m", "prompt": "p", "negative_prompt": 1}, "negative_prompt"},
		{"input_reference 类型错误", map[string]any{"model": "m", "prompt": "p", "input_reference": 42}, "input_reference"},
	}
	for _, c := range cases {
		_, err := ParseVideoParams(c.body, allSupportedCaps())
		if err == nil {
			t.Fatalf("%s: 应返回错误", c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: 错误 %q 应包含 %q", c.name, err.Error(), c.want)
		}
		if errors.Is(err, ErrParamUnsupported) {
			t.Fatalf("%s: 形态错误不应是能力错误", c.name)
		}
	}
	if _, err := ParseVideoParams(nil, allSupportedCaps()); err == nil {
		t.Fatal("nil body 必须报错")
	}
}

func TestParseVideoParamsUnsupported(t *testing.T) {
	// 契约 §2.4 规则 2：语义无法表达 → 400（ErrParamUnsupported），不降级。
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			"n>1 且不支持多段",
			map[string]any{"model": "m", "prompt": "p", "n": float64(3)},
			"n>1",
		},
		{
			"input_reference 且不支持图生视频",
			map[string]any{"model": "m", "prompt": "p", "input_reference": "https://e.com/f.png"},
			"input_reference",
		},
	}
	noCaps := VideoCapabilities{} // 全不支持
	for _, c := range cases {
		_, err := ParseVideoParams(c.body, noCaps)
		if err == nil {
			t.Fatalf("%s: 应返回错误", c.name)
		}
		if !errors.Is(err, ErrParamUnsupported) {
			t.Fatalf("%s: 错误应命中 ErrParamUnsupported, got %v", c.name, err)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: 错误 %q 应包含 %q", c.name, err.Error(), c.want)
		}
	}
}

func TestComputeParamsIgnored(t *testing.T) {
	seconds := 4.0
	seed := int64(7)
	audio := true
	full := NormalizedVideoParams{
		Model: "m", Prompt: "p",
		Seconds:        &seconds,
		NegativePrompt: "blurry",
		Seed:           &seed,
		Audio:          &audio,
	}
	if got := ComputeParamsIgnored(full, allSupportedCaps()); len(got) != 0 {
		t.Fatalf("全能力 ignored = %v", got)
	}
	// openai 现状：negative_prompt/seed/audio 忽略；seconds 支持。
	want := "negative_prompt,seed,audio"
	if got := strings.Join(ComputeParamsIgnored(full, openaiVideoCapsForTest()), ","); got != want {
		t.Fatalf("openai ignored = %q, want %q", got, want)
	}
	// 无 seconds 能力：seconds 进 ignored（换算类参数不支持时不伪造 applied）。
	noSeconds := openaiVideoCapsForTest()
	noSeconds.SupportsSeconds = false
	want = "seconds,negative_prompt,seed,audio"
	if got := strings.Join(ComputeParamsIgnored(full, noSeconds), ","); got != want {
		t.Fatalf("no-seconds ignored = %q, want %q", got, want)
	}
	// 空参数：无回显。
	if got := ComputeParamsIgnored(NormalizedVideoParams{}, noCapsForIgnoredTest()); len(got) != 0 {
		t.Fatalf("空参数 ignored = %v", got)
	}
}

func noCapsForIgnoredTest() VideoCapabilities { return VideoCapabilities{} }

// openaiVideoCapsForTest 取注册表内 openai adapter 的能力声明（与生产同一
// 事实源，不复制字面量）。
func openaiVideoCapsForTest() VideoCapabilities {
	return videoAdapters["openai"].Capabilities()
}

// TestBillingSeconds 钉住按秒计价的计量基源裁决（契约 §2.8.2）：请求
// seconds 生效时用请求值；否则用厂商默认输出时长；两者都无 → nil。
func TestBillingSeconds(t *testing.T) {
	secondsPtr := func(v float64) *float64 { return &v }
	ignored := float64(4)
	cases := []struct {
		name    string
		seconds *float64
		caps    VideoCapabilities
		want    *float64
	}{
		{
			name:    "veo ignored seconds 用厂商默认 8",
			seconds: &ignored,
			caps:    videoAdapters["gemini"].Capabilities(),
			want:    secondsPtr(8),
		},
		{
			name: "veo 未传 seconds 用厂商默认 8",
			caps: videoAdapters["gemini"].Capabilities(),
			want: secondsPtr(8),
		},
		{
			name: "minimax 未传 seconds 用厂商默认 6",
			caps: videoAdapters["minimax"].Capabilities(),
			want: secondsPtr(6),
		},
		{
			name:    "minimax 显式 seconds 生效用请求值",
			seconds: secondsPtr(10),
			caps:    videoAdapters["minimax"].Capabilities(),
			want:    secondsPtr(10),
		},
		{
			name: "无默认值时未传 seconds 无计量基源",
			caps: VideoCapabilities{SupportsSeconds: true},
			want: nil,
		},
		{
			name:    "无默认值时显式 seconds 用请求值",
			seconds: secondsPtr(5),
			caps:    VideoCapabilities{SupportsSeconds: true},
			want:    secondsPtr(5),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := NormalizedVideoParams{Seconds: tc.seconds}
			got := params.BillingSeconds(tc.caps)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("BillingSeconds = %v, want %v", got, tc.want)
			}
			if got != nil && *got != *tc.want {
				t.Fatalf("BillingSeconds = %v, want %v", *got, *tc.want)
			}
		})
	}
}

// TestBillingResolution 钉住按分辨率分档计费的创建档位（契约 §2.8.3）：
// xAI 未传 size 时官方默认 480p；Volcengine/Qwen 未传 size 时返回空串
// （结算回落主档）；三家的 size→档换算与出站 resolution 同源（短边
// ≥900→1080p、≥600→720p、其余 480p）。
func TestBillingResolution(t *testing.T) {
	cases := []struct {
		provider string
		size     string
		want     string
	}{
		{"xai", "", "480p"},
		{"xai", "1920x1080", "1080p"},
		{"xai", "1280x720", "720p"},
		{"xai", "640x480", "480p"},
		{"volcengine", "", ""},
		{"volcengine", "1920x1080", "1080p"},
		{"volcengine", "1280x720", "720p"},
		{"qwen", "", ""},
		{"qwen", "1280x720", "720p"},
	}
	for _, tc := range cases {
		adapter := videoAdapters[tc.provider]
		provider, ok := adapter.(VideoBillingResolutionProvider)
		if !ok {
			t.Fatalf("%s adapter does not implement VideoBillingResolutionProvider", tc.provider)
		}
		if got := provider.BillingResolution(NormalizedVideoParams{Size: tc.size}); got != tc.want {
			t.Fatalf("%s BillingResolution(%q) = %q want %q", tc.provider, tc.size, got, tc.want)
		}
	}
	// 平档厂商不实现该能力（Gemini/OpenAI/GLM/minimax 目录无分辨率档）。
	if _, ok := videoAdapters["gemini"].(VideoBillingResolutionProvider); ok {
		t.Fatal("gemini must not implement VideoBillingResolutionProvider")
	}
}
