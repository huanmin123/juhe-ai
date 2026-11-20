// videoparams.go 是对外唯一视频面 POST /v1/video/generations 请求体的统一
// 参数归一层（设计《对外协议面对齐 new-api 设计》§4.2.1；契约 §2.2 视频创建
// 参数表、§2.4 忽略/拒绝/回显规则、§2.7 映射表）。产出 NormalizedVideoParams
// 供 adapter 构造上游报文与链上回显 params_applied/params_ignored；与 adapter
// 能力相关的裁决（n>1、input_reference、fps/resolution）在此按 Capabilities
// 做出 400 或 ignored 语义（ErrParamUnsupported）。
package gatewaymedia

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ErrParamUnsupported 标记"语义无法表达"类参数错误（契约 §2.4 规则 2：n>1
// 且该模型/厂商不支持多段、input_reference 无对应能力等）。链上层以
// errors.Is 命中映射 400（参数类 4xx 不换账户，媒体设计 §7）。
var ErrParamUnsupported = errors.New("参数超出该模型/厂商能力边界")

// VideoCapabilities 是视频 adapter 的能力声明（契约 §2.2 表"厂商不支持时"
// 列的裁决输入）。M3 各厂商 adapter 按各自请求面声明。
type VideoCapabilities struct {
	SupportsNegativePrompt bool
	SupportsSeed           bool
	SupportsAudio          bool
	SupportsN              bool
	SupportsInputReference bool
	SupportsSeconds        bool
	SupportsSize           bool
	SupportsFPS            bool
	SupportsResolution     bool
}

// NormalizedVideoParams 是归一后的视频创建参数（设计 §4.2.1 参数表）。指针
// 字段区分"未传"（nil）与"显式传入"；ParamsApplied 是用户显式传入且实际
// 生效的键名列表，ParamsIgnored 是传入但被忽略的键名列表（固定顺序见
// ParseVideoParams 注释）。
type NormalizedVideoParams struct {
	Model  string
	Prompt string
	// Seconds 承接 duration/seconds 两个同义字段；SecondsFields 记录用户
	// 实际使用的字段名（ignored 回显按用户词表，不伪造）。
	Seconds       *float64
	SecondsFields []string
	// Size 仅在 adapter 声明 SupportsSize 时写入；不支持的 adapter 保留
	// SizeFields 供 ignored 回显，Size 留空，避免计费按未生效尺寸冻结档位。
	Size string
	// SizeFields 记录 size / width / height 中用户实际使用的字段名。
	SizeFields     []string
	N              int
	NegativePrompt string
	Seed           *int64
	Audio          *bool
	FPS            *int
	Resolution     string
	Mode           string
	User           string
	// Metadata 是唯一扩展通道的厂商参数包（设计 §5.1）；不参与
	// params_applied，回显走 metadata_applied。
	Metadata map[string]any
	// InputReference 承接图生视频首帧/参考图（设计 §4.2.1：image / images /
	// input_reference 三词同义）。网关只透传不解析存储（零存储）。
	InputReference *string
	ReferenceField string
	ParamsApplied  []string
	ParamsIgnored  []string
}

// ParseVideoParams 从已解析的 POST /v1/video/generations 请求 JSON 对象提取
// 归一参数。校验面（设计 §4.2.1）：
//   - L1：model 必填；prompt 在有图（image/images/input_reference）时可省；
//   - L2 形态：duration/seconds 数字或数字字符串且 >0、size 为 WxH 像素串、
//     width/height 成对正整数、fps 正整数、n 为 ≥1 整数、seed 为整数、
//     audio 为 bool、response_format 仅 url、resolution 为非空串；
//   - 同义字段冲突：duration 与 seconds 不等、size 与 width/height 不一致、
//     image/images/input_reference 值不一致 → 400；size 与 resolution 同时
//     指定 → 400（两者都表达分辨率，不做猜测性合并）；
//   - 能力裁决：n>1 且 adapter 不支持多段、input_reference 已传但 adapter
//     无图生视频能力 → ErrParamUnsupported；size/fps/resolution 不支持 →
//     ignored（size 同时不进入快照计费档）；
//   - 扩展通道：metadata 键不得与规范字段同名（400，设计 §5.1）。
//
// ParamsApplied 固定顺序：model、图引用词、prompt、时长词、尺寸词、fps、
// seed、n、negative_prompt、audio、response_format、resolution。size 仅在
// adapter 声明 SupportsSize 时进入 applied；user/mode 接受但恒进 ignored
// （无 adapter 映射）。
func ParseVideoParams(body map[string]any, caps VideoCapabilities) (NormalizedVideoParams, error) {
	if body == nil {
		return NormalizedVideoParams{}, fmt.Errorf("视频生成请求体必须是 JSON 对象")
	}
	model, _ := body["model"].(string)
	prompt, _ := body["prompt"].(string)
	params := NormalizedVideoParams{
		Model:  strings.TrimSpace(model),
		Prompt: prompt,
	}
	if params.Model == "" {
		return NormalizedVideoParams{}, fmt.Errorf("视频生成请求缺少 model")
	}
	params.ParamsApplied = append(params.ParamsApplied, "model")

	reference, referenceField, err := parseVideoReference(body, caps)
	if err != nil {
		return NormalizedVideoParams{}, err
	}
	if reference != nil {
		params.InputReference = reference
		params.ReferenceField = referenceField
		params.ParamsApplied = append(params.ParamsApplied, referenceField)
	}
	if strings.TrimSpace(prompt) == "" {
		if reference == nil {
			return NormalizedVideoParams{}, fmt.Errorf("视频生成请求缺少 prompt（无图引用时必填）")
		}
	} else {
		params.ParamsApplied = append(params.ParamsApplied, "prompt")
	}

	seconds, secondsFields, err := parseVideoSecondsFields(body)
	if err != nil {
		return NormalizedVideoParams{}, err
	}
	if seconds != nil {
		params.Seconds = seconds
		params.SecondsFields = secondsFields
		if caps.SupportsSeconds {
			params.ParamsApplied = append(params.ParamsApplied, secondsFields...)
		}
	}

	size, sizeFields, err := parseVideoSizeFields(body)
	if err != nil {
		return NormalizedVideoParams{}, err
	}
	if size != "" {
		params.SizeFields = sizeFields
		if caps.SupportsSize {
			params.Size = size
			params.ParamsApplied = append(params.ParamsApplied, sizeFields...)
		}
	}

	if raw, present := body["fps"]; present && raw != nil {
		fps, err := normalizeVideoPositiveInt("fps", raw)
		if err != nil {
			return NormalizedVideoParams{}, err
		}
		params.FPS = &fps
		if caps.SupportsFPS {
			params.ParamsApplied = append(params.ParamsApplied, "fps")
		}
	}

	if raw, present := body["seed"]; present && raw != nil {
		seed, err := normalizeVideoSeed(raw)
		if err != nil {
			return NormalizedVideoParams{}, err
		}
		params.Seed = seed
		if caps.SupportsSeed {
			params.ParamsApplied = append(params.ParamsApplied, "seed")
		}
	}

	if raw, present := body["n"]; present && raw != nil {
		n, err := normalizeVideoCount(raw)
		if err != nil {
			return NormalizedVideoParams{}, err
		}
		if n > 1 && !caps.SupportsN {
			return NormalizedVideoParams{}, fmt.Errorf("%w: 该模型不支持 n>1 的多段生成", ErrParamUnsupported)
		}
		params.N = n
		params.ParamsApplied = append(params.ParamsApplied, "n")
	} else {
		params.N = 1 // 契约 §2.2：默认 1（未显式传入不进 applied 回显）
	}

	if raw, present := body["negative_prompt"]; present && raw != nil {
		text, ok := raw.(string)
		if !ok {
			return NormalizedVideoParams{}, fmt.Errorf("视频生成请求 negative_prompt 必须是字符串")
		}
		params.NegativePrompt = text
		if caps.SupportsNegativePrompt {
			params.ParamsApplied = append(params.ParamsApplied, "negative_prompt")
		}
	}

	if raw, present := body["audio"]; present && raw != nil {
		flag, ok := raw.(bool)
		if !ok {
			return NormalizedVideoParams{}, fmt.Errorf("视频生成请求 audio 必须是布尔值")
		}
		params.Audio = &flag
		if caps.SupportsAudio {
			params.ParamsApplied = append(params.ParamsApplied, "audio")
		}
	}

	if raw, present := body["response_format"]; present && raw != nil {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) != "url" {
			return NormalizedVideoParams{}, fmt.Errorf("%w: response_format 仅支持 url（零存储不返回内联 base64）", ErrParamUnsupported)
		}
		params.ParamsApplied = append(params.ParamsApplied, "response_format")
	}

	if raw, present := body["user"]; present && raw != nil {
		text, ok := raw.(string)
		if !ok {
			return NormalizedVideoParams{}, fmt.Errorf("视频生成请求 user 必须是字符串")
		}
		params.User = strings.TrimSpace(text)
	}

	if raw, present := body["resolution"]; present && raw != nil {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return NormalizedVideoParams{}, fmt.Errorf("视频生成请求 resolution 必须是非空字符串")
		}
		if len(params.SizeFields) > 0 {
			return NormalizedVideoParams{}, fmt.Errorf("%w: size 与 resolution 只能指定一个", ErrParamUnsupported)
		}
		params.Resolution = strings.TrimSpace(text)
		if caps.SupportsResolution {
			params.ParamsApplied = append(params.ParamsApplied, "resolution")
		}
	}

	if raw, present := body["mode"]; present && raw != nil {
		text, ok := raw.(string)
		if !ok {
			return NormalizedVideoParams{}, fmt.Errorf("视频生成请求 mode 必须是字符串")
		}
		params.Mode = strings.TrimSpace(text)
	}

	metadata, err := ExtractMetadata(body[MetadataKey], VideoMetadataReserved)
	if err != nil {
		return NormalizedVideoParams{}, err
	}
	params.Metadata = metadata

	params.ParamsIgnored = ComputeParamsIgnored(params, caps)
	return params, nil
}

// parseVideoReference 解析 image/images/input_reference 三词同义图引用。
// 同时给出时要求值完全一致（不一致即语义冲突 400）；images 只接受 1 个
// （多参考图能力未开放，不静默截断）。返回统一引用值与用户使用的词。
func parseVideoReference(body map[string]any, caps VideoCapabilities) (*string, string, error) {
	var (
		value *string
		field string
	)
	set := func(candidate, name string) error {
		if strings.TrimSpace(candidate) == "" {
			return fmt.Errorf("视频生成请求 %s 必须是非空字符串", name)
		}
		if value == nil {
			reference := candidate
			value = &reference
			field = name
			return nil
		}
		if *value != candidate {
			return fmt.Errorf("%w: 图引用字段之间取值不一致（%s 与 %s）", ErrParamUnsupported, field, name)
		}
		return nil
	}
	if raw, present := body["image"]; present && raw != nil {
		text, ok := raw.(string)
		if !ok {
			return nil, "", fmt.Errorf("视频生成请求 image 必须是 image_url 或 base64 data URL 字符串")
		}
		if err := set(text, "image"); err != nil {
			return nil, "", err
		}
	}
	if raw, present := body["input_reference"]; present && raw != nil {
		text, ok := raw.(string)
		if !ok {
			return nil, "", fmt.Errorf("视频生成请求 input_reference 必须是 image_url 或 base64 data URL 字符串")
		}
		if err := set(text, "input_reference"); err != nil {
			return nil, "", err
		}
	}
	if raw, present := body["images"]; present && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return nil, "", fmt.Errorf("视频生成请求 images 必须是字符串数组")
		}
		if len(list) > 1 {
			return nil, "", fmt.Errorf("%w: 该模型暂不支持多参考图（images 只能给 1 个）", ErrParamUnsupported)
		}
		if len(list) == 1 {
			text, ok := list[0].(string)
			if !ok {
				return nil, "", fmt.Errorf("视频生成请求 images[0] 必须是 image_url 或 base64 data URL 字符串")
			}
			if err := set(text, "images"); err != nil {
				return nil, "", err
			}
		}
	}
	if value != nil && !caps.SupportsInputReference {
		return nil, "", fmt.Errorf("%w: 该模型不支持图生视频（%s）", ErrParamUnsupported, field)
	}
	return value, field, nil
}

// parseVideoSecondsFields 解析 duration/seconds 两个同义时长字段。同时给出
// 且数值不等 → 400；相等时两个词都回显。
func parseVideoSecondsFields(body map[string]any) (*float64, []string, error) {
	var (
		value  *float64
		fields []string
	)
	for _, name := range []string{"duration", "seconds"} {
		raw, present := body[name]
		if !present || raw == nil {
			continue
		}
		parsed, err := normalizeVideoSeconds(raw, name)
		if err != nil {
			return nil, nil, err
		}
		if value != nil && *value != *parsed {
			return nil, nil, fmt.Errorf("%w: duration 与 seconds 取值不一致", ErrParamUnsupported)
		}
		if value == nil {
			value = parsed
		}
		fields = append(fields, name)
	}
	return value, fields, nil
}

// parseVideoSizeFields 解析 size 与 width/height。size 优先；width/height 必须
// 成对，且与 size 同时给出时必须与 size 一致（否则 400）。
func parseVideoSizeFields(body map[string]any) (string, []string, error) {
	var (
		size   string
		fields []string
	)
	if raw, present := body["size"]; present && raw != nil {
		text, ok := raw.(string)
		if !ok || !isValidVideoSize(text) {
			return "", nil, fmt.Errorf("视频生成请求 size 必须是 WxH 像素串（如 1280x720），实际为 %v", raw)
		}
		size = text
		fields = append(fields, "size")
	}
	width, widthPresent, err := parseOptionalVideoDimension(body, "width")
	if err != nil {
		return "", nil, err
	}
	height, heightPresent, err := parseOptionalVideoDimension(body, "height")
	if err != nil {
		return "", nil, err
	}
	if widthPresent != heightPresent {
		return "", nil, fmt.Errorf("视频生成请求 width 与 height 必须成对出现")
	}
	if widthPresent {
		combined := fmt.Sprintf("%dx%d", width, height)
		if size != "" && size != combined {
			return "", nil, fmt.Errorf("%w: size 与 width/height 不一致（%s 与 %s）", ErrParamUnsupported, size, combined)
		}
		if size == "" {
			size = combined
		}
		fields = append(fields, "width", "height")
	}
	return size, fields, nil
}

func parseOptionalVideoDimension(body map[string]any, name string) (int, bool, error) {
	raw, present := body[name]
	if !present || raw == nil {
		return 0, false, nil
	}
	value, err := normalizeVideoPositiveInt(name, raw)
	if err != nil {
		return 0, false, err
	}
	return value, true, nil
}

// ComputeParamsIgnored 按 adapter 能力产出被忽略的键名列表（契约 §2.4 规则
// 1：L2 可选语义参数厂商不支持 → 忽略 + 回显，不静默）。顺序固定：
// duration/seconds（按用户词）、size/width/height（按用户词）、fps、
// negative_prompt、seed、audio、resolution。
func ComputeParamsIgnored(params NormalizedVideoParams, caps VideoCapabilities) []string {
	var ignored []string
	if params.Seconds != nil && !caps.SupportsSeconds {
		if len(params.SecondsFields) > 0 {
			ignored = append(ignored, params.SecondsFields...)
		} else {
			// 直接构造结构（非 ParseVideoParams 路径）时的稳定回显词。
			ignored = append(ignored, "seconds")
		}
	}
	if !caps.SupportsSize && (len(params.SizeFields) > 0 || params.Size != "") {
		if len(params.SizeFields) > 0 {
			ignored = append(ignored, params.SizeFields...)
		} else {
			ignored = append(ignored, "size")
		}
	}
	if params.FPS != nil && !caps.SupportsFPS {
		ignored = append(ignored, "fps")
	}
	if params.NegativePrompt != "" && !caps.SupportsNegativePrompt {
		ignored = append(ignored, "negative_prompt")
	}
	if params.Seed != nil && !caps.SupportsSeed {
		ignored = append(ignored, "seed")
	}
	if params.Audio != nil && !caps.SupportsAudio {
		ignored = append(ignored, "audio")
	}
	if params.Resolution != "" && !caps.SupportsResolution {
		ignored = append(ignored, "resolution")
	}
	// user/mode 是 new-api 宿主词表字段：网关接受但当前无 adapter 映射
	//（厂商请求面无等价语义），按契约 §2.4 规则 1 回显 ignored 而非静默丢弃。
	if params.User != "" {
		ignored = append(ignored, "user")
	}
	if params.Mode != "" {
		ignored = append(ignored, "mode")
	}
	return ignored
}

// normalizeVideoSeconds 归一时长（契约 §2.2 number；兼容数字字符串），拒绝
// 非正数与非数字形态。
func normalizeVideoSeconds(raw any, name string) (*float64, error) {
	var value float64
	switch v := raw.(type) {
	case float64:
		value = v
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil, fmt.Errorf("视频生成请求 %s 必须是数字或数字字符串: %w", name, err)
		}
		value = parsed
	default:
		return nil, fmt.Errorf("视频生成请求 %s 必须是数字或数字字符串，实际为 %v", name, raw)
	}
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, fmt.Errorf("视频生成请求 %s 必须是正数，实际为 %v", name, raw)
	}
	return &value, nil
}

// isValidVideoSize 校验 size 为 WxH 像素串形态（两段正整数、小写 x 分隔、
// 无前导零/空格，如 1280x720/720x1280/960x960）。档位合法性由上游裁决
// （契约 §2.2：网关不硬编码档位清单）。
func isValidVideoSize(text string) bool {
	width, height, ok := strings.Cut(strings.TrimSpace(text), "x")
	if !ok {
		return false
	}
	parsedWidth, err := strconv.Atoi(width)
	if err != nil || parsedWidth <= 0 || strconv.Itoa(parsedWidth) != width {
		return false
	}
	parsedHeight, err := strconv.Atoi(height)
	if err != nil || parsedHeight <= 0 || strconv.Itoa(parsedHeight) != height {
		return false
	}
	return true
}

// normalizeVideoPositiveInt 归一正整数（width/height/fps）。
func normalizeVideoPositiveInt(name string, raw any) (int, error) {
	value, ok := raw.(float64)
	if !ok {
		return 0, fmt.Errorf("视频生成请求 %s 必须是整数，实际为 %v", name, raw)
	}
	if value != math.Trunc(value) {
		return 0, fmt.Errorf("视频生成请求 %s 必须是整数，实际为 %v", name, raw)
	}
	if value < 1 {
		return 0, fmt.Errorf("视频生成请求 %s 必须 ≥1，实际为 %v", name, raw)
	}
	return int(value), nil
}

// normalizeVideoCount 归一 n（契约 §2.2：int ≥1，默认 1）。
func normalizeVideoCount(raw any) (int, error) {
	return normalizeVideoPositiveInt("n", raw)
}

// normalizeVideoSeed 归一 seed（契约 §2.2：int）。
func normalizeVideoSeed(raw any) (*int64, error) {
	value, ok := raw.(float64)
	if !ok {
		return nil, fmt.Errorf("视频生成请求 seed 必须是整数，实际为 %v", raw)
	}
	if value != math.Trunc(value) {
		return nil, fmt.Errorf("视频生成请求 seed 必须是整数，实际为 %v", raw)
	}
	seed := int64(value)
	return &seed, nil
}
