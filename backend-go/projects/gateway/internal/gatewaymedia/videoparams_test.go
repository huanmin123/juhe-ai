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
		SupportsSize:           true,
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
		"model", "input_reference", "prompt", "seconds", "size", "seed", "n",
		"negative_prompt", "audio",
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
	// 无 size 能力：直接构造结构时以稳定词 size 回显；width/height 按用户词。
	noSize := noCapsForIgnoredTest()
	if got := strings.Join(ComputeParamsIgnored(NormalizedVideoParams{Size: "1280x720"}, noSize), ","); got != "size" {
		t.Fatalf("size ignored = %q", got)
	}
	if got := strings.Join(ComputeParamsIgnored(NormalizedVideoParams{SizeFields: []string{"width", "height"}}, noSize), ","); got != "width,height" {
		t.Fatalf("width/height ignored = %q", got)
	}
}

// TestParseVideoParamsSizeCapabilityGate 覆盖无 size 入参的 adapter（如
// minimax）：用户传 size 时回显 ignored，但不写入 params.Size，避免计费
// 快照按未生效尺寸冻结分辨率档；与 resolution 的冲突校验仍按用户传入判。
func TestParseVideoParamsSizeCapabilityGate(t *testing.T) {
	caps := allSupportedCaps()
	caps.SupportsSize = false
	body := map[string]any{"model": "MiniMax-Hailuo-02", "prompt": "p", "size": "1280x720"}
	params, err := ParseVideoParams(body, caps)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if params.Size != "" {
		t.Fatalf("不支持 size 时不得写入 Size: %q", params.Size)
	}
	if strings.Join(params.SizeFields, ",") != "size" {
		t.Fatalf("SizeFields = %v", params.SizeFields)
	}
	if strings.Join(params.ParamsIgnored, ",") != "size" {
		t.Fatalf("ignored = %v", params.ParamsIgnored)
	}
	if strings.Contains(strings.Join(params.ParamsApplied, ","), "size") {
		t.Fatalf("不支持 size 时不得进 applied: %v", params.ParamsApplied)
	}

	// width/height 形态同样被忽略，且不能因 Size 为空而放过 size+resolution 冲突。
	body = map[string]any{"model": "m", "prompt": "p", "width": float64(1280), "height": float64(720)}
	params, err = ParseVideoParams(body, caps)
	if err != nil {
		t.Fatalf("width/height: %v", err)
	}
	if params.Size != "" || strings.Join(params.ParamsIgnored, ",") != "width,height" {
		t.Fatalf("width/height 不支持时 = %+v", params)
	}
	body["resolution"] = "720p"
	if _, err := ParseVideoParams(body, caps); err == nil {
		t.Fatal("size 与 resolution 同时给出应 400（即使 adapter 不支持 size）")
	}
}

func noCapsForIgnoredTest() VideoCapabilities { return VideoCapabilities{} }

// openaiVideoCapsForTest 取注册表内 openai adapter 的能力声明（与生产同一
// 事实源，不复制字面量）。
func openaiVideoCapsForTest() VideoCapabilities {
	return videoAdapters["openai"].Capabilities()
}

// TestParseVideoParamsUnifiedFaceFields 覆盖唯一视频面的新增/别名参数
// （设计 §4.2.1）：image/images/input_reference 三词同义、duration/seconds
// 同义、width/height 合成 size、fps/resolution/mode/user 的能力裁决、metadata
// 唯一扩展通道。
func TestParseVideoParamsUnifiedFaceFields(t *testing.T) {
	caps := allSupportedCaps()
	caps.SupportsFPS = true
	base := func() map[string]any {
		return map[string]any{"model": "m", "prompt": "p"}
	}

	// image 单独给出、prompt 可省。
	body := map[string]any{"model": "m", "image": "https://example.test/a.png"}
	params, err := ParseVideoParams(body, caps)
	if err != nil {
		t.Fatalf("image-only: %v", err)
	}
	if params.InputReference == nil || *params.InputReference != "https://example.test/a.png" || params.ReferenceField != "image" {
		t.Fatalf("image 归一 = %+v", params)
	}
	if strings.Contains(strings.Join(params.ParamsApplied, ","), "prompt") {
		t.Fatalf("省 prompt 不应回显 applied: %v", params.ParamsApplied)
	}

	// input_reference 与 image 同值双给可接受；不同值 400。
	body = base()
	body["image"] = "u"
	body["input_reference"] = "u"
	if _, err := ParseVideoParams(body, caps); err != nil {
		t.Fatalf("同值别名: %v", err)
	}
	body["input_reference"] = "v"
	if _, err := ParseVideoParams(body, caps); err == nil {
		t.Fatal("别名取值不一致应 400")
	}

	// images 只接受 1 个。
	body = base()
	body["images"] = []any{"u", "v"}
	if _, err := ParseVideoParams(body, caps); err == nil {
		t.Fatal("images 多值应 400")
	}

	// duration 与 seconds 同义：相等接受、不等 400。
	body = base()
	body["duration"] = float64(4)
	body["seconds"] = "4"
	params, err = ParseVideoParams(body, caps)
	if err != nil || params.Seconds == nil || *params.Seconds != 4 {
		t.Fatalf("duration/seconds 同值 = %+v, %v", params, err)
	}
	if got := strings.Join(params.ParamsApplied, ","); !strings.Contains(got, "duration") || !strings.Contains(got, "seconds") {
		t.Fatalf("同义时长字段都应回显: %v", got)
	}
	body["seconds"] = "5"
	if _, err := ParseVideoParams(body, caps); err == nil {
		t.Fatal("duration/seconds 不一致应 400")
	}

	// width/height 成对合成 size；与 size 不一致 400。
	body = base()
	body["width"] = float64(1280)
	body["height"] = float64(720)
	params, err = ParseVideoParams(body, caps)
	if err != nil || params.Size != "1280x720" {
		t.Fatalf("width/height = %+v, %v", params, err)
	}
	body["size"] = "720x1280"
	if _, err := ParseVideoParams(body, caps); err == nil {
		t.Fatal("size 与 width/height 不一致应 400")
	}

	// size 与 resolution 同时给出 → 400；单独 resolution 按能力 applied/ignored。
	body = base()
	body["resolution"] = "720p"
	params, err = ParseVideoParams(body, caps)
	if err != nil || params.Resolution != "720p" {
		t.Fatalf("resolution = %+v, %v", params, err)
	}
	noResolution := caps
	noResolution.SupportsResolution = false
	params, err = ParseVideoParams(body, noResolution)
	if err != nil || !strings.Contains(strings.Join(params.ParamsIgnored, ","), "resolution") {
		t.Fatalf("resolution 无能力应 ignored: %+v, %v", params, err)
	}
	body["size"] = "1280x720"
	if _, err := ParseVideoParams(body, caps); err == nil {
		t.Fatal("size 与 resolution 同时给出应 400")
	}

	// fps：有消费能力进 applied，无能力进 ignored。
	body = base()
	body["fps"] = float64(30)
	params, err = ParseVideoParams(body, caps)
	if err != nil || params.FPS == nil || *params.FPS != 30 {
		t.Fatalf("fps = %+v, %v", params, err)
	}
	noFPS := caps
	noFPS.SupportsFPS = false
	params, _ = ParseVideoParams(body, noFPS)
	if !strings.Contains(strings.Join(params.ParamsIgnored, ","), "fps") {
		t.Fatalf("fps 无能力应 ignored: %v", params.ParamsIgnored)
	}

	// mode/user 接受但恒进 ignored。
	body = base()
	body["mode"] = "pro"
	body["user"] = "u-1"
	params, err = ParseVideoParams(body, caps)
	if err != nil {
		t.Fatalf("mode/user: %v", err)
	}
	ignored := strings.Join(params.ParamsIgnored, ",")
	if !strings.Contains(ignored, "mode") || !strings.Contains(ignored, "user") {
		t.Fatalf("mode/user 应 ignored: %v", ignored)
	}

	// metadata：对象与 JSON 字符串均可；规范字段冲突 400。
	body = base()
	body["metadata"] = map[string]any{"camera_fixed": true}
	if params, err = ParseVideoParams(body, caps); err != nil || params.Metadata["camera_fixed"] != true {
		t.Fatalf("metadata 对象 = %+v, %v", params, err)
	}
	body["metadata"] = `{"camera_fixed":true}`
	if params, err = ParseVideoParams(body, caps); err != nil || params.Metadata["camera_fixed"] != true {
		t.Fatalf("metadata 字符串 = %+v, %v", params, err)
	}
	body["metadata"] = map[string]any{"prompt": "x"}
	if _, err = ParseVideoParams(body, caps); !errors.Is(err, ErrMetadataReservedField) {
		t.Fatalf("metadata 规范字段冲突应 ErrMetadataReservedField: %v", err)
	}
}
