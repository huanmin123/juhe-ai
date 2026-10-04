// videoparams.go 是对外 POST /v1/videos 请求体的统一参数归一层（媒体设计
// §6 自建规范 L1/L2/L3；契约 §2.2 视频创建参数表、§2.4 忽略/拒绝/回显规则、
// §2.7 映射表）。产出 NormalizedVideoParams 供 adapter 构造上游报文与链上
// 回显 params_applied/params_ignored；与 adapter 能力相关的裁决（n>1、
// input_reference）在此按 Capabilities 做出 400 语义裁决（ErrParamUnsupported）。
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
// 列的裁决输入，openai 按 sora 现状填写——见 openaiVideoAdapter。
// Capabilities 注释）。M3 各厂商 adapter 按各自请求面声明。
type VideoCapabilities struct {
	SupportsNegativePrompt bool
	SupportsSeed           bool
	SupportsAudio          bool
	SupportsN              bool
	SupportsInputReference bool
	SupportsSeconds        bool
}

// NormalizedVideoParams 是归一后的视频创建参数（契约 §2.2 全表）。指针字段
// 区分"未传"（nil）与"显式传入"；ParamsApplied 是用户显式传入且实际生效的
// 键名列表（固定顺序 model/prompt/seconds/size/n/input_reference/
// negative_prompt/seed/audio），ParamsIgnored 是传入但被忽略的键名列表。
type NormalizedVideoParams struct {
	Model          string
	Prompt         string
	Seconds        *float64
	Size           string
	N              int
	NegativePrompt string
	Seed           *int64
	Audio          *bool
	// InputReference 承接图生视频首帧引用（契约 §2.2：image_url 或 base64
	// data URL）。网关只透传不解析存储（契约 §2.7 input_reference 行）。
	InputReference *string
	ParamsApplied  []string
	ParamsIgnored  []string
}

// ParseVideoParams 从已解析的对外 POST /v1/videos 请求 JSON 对象提取归一
// 参数。校验面：
//   - L1 必填：model、prompt 非空；
//   - L2 形态：seconds 数字/数字字符串兼容且 >0（契约 §4.3 openai 原生
//     seconds 为字符串形态）、size 为 WxH 像素串形态（档位由上游裁决，网关
//     不硬编码）、n 为 ≥1 的整数、seed 为整数、audio 为 bool；
//   - 能力裁决：n>1 且 adapter 不支持多段、input_reference 已传但 adapter
//     无图生视频能力 → ErrParamUnsupported（契约 §2.4 规则 2，不降级不猜测）；
//   - 忽略语义：negative_prompt/seed/audio 不支持、seconds 无对应字段时进
//     ParamsIgnored（契约 §2.4 规则 1，回显不静默）。
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
	if strings.TrimSpace(prompt) == "" {
		return NormalizedVideoParams{}, fmt.Errorf("视频生成请求缺少 prompt")
	}
	params.ParamsApplied = append(params.ParamsApplied, "model", "prompt")

	if raw, present := body["seconds"]; present && raw != nil {
		seconds, err := normalizeVideoSeconds(raw)
		if err != nil {
			return NormalizedVideoParams{}, err
		}
		params.Seconds = seconds
		if caps.SupportsSeconds {
			params.ParamsApplied = append(params.ParamsApplied, "seconds")
		}
	}

	if size, present := body["size"]; present && size != nil {
		text, ok := size.(string)
		if !ok || !isValidVideoSize(text) {
			return NormalizedVideoParams{}, fmt.Errorf("视频生成请求 size 必须是 WxH 像素串（如 1280x720），实际为 %v", size)
		}
		params.Size = text
		params.ParamsApplied = append(params.ParamsApplied, "size")
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

	if raw, present := body["input_reference"]; present && raw != nil {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return NormalizedVideoParams{}, fmt.Errorf("视频生成请求 input_reference 必须是 image_url 或 base64 data URL 字符串")
		}
		if !caps.SupportsInputReference {
			return NormalizedVideoParams{}, fmt.Errorf("%w: 该模型不支持图生视频（input_reference）", ErrParamUnsupported)
		}
		reference := text
		params.InputReference = &reference
		params.ParamsApplied = append(params.ParamsApplied, "input_reference")
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

	params.ParamsIgnored = ComputeParamsIgnored(params, caps)
	return params, nil
}

// ComputeParamsIgnored 按 adapter 能力产出被忽略的键名列表（契约 §2.4 规则
// 1：L2 可选语义参数厂商不支持 → 忽略 + 回显，不静默）。顺序固定为契约
// §2.2 表序：seconds、negative_prompt、seed、audio。seconds 属换算类参数
// （规则 3），不支持时该模型用厂商默认时长、用户值不生效——网关不猜测厂商
// 默认值，故进 ignored 而非伪造 applied 值（M3 档位型 adapter 在 Create 内
// 换算时可改写 applied 回显实际档位）。
func ComputeParamsIgnored(params NormalizedVideoParams, caps VideoCapabilities) []string {
	var ignored []string
	if params.Seconds != nil && !caps.SupportsSeconds {
		ignored = append(ignored, "seconds")
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
	return ignored
}

// normalizeVideoSeconds 归一 seconds（契约 §2.2 number；契约 §4.3 openai
// 原生请求为字符串形态 "4"——字符串/数字双形态兼容），拒绝非正数与非数字
// 形态。
func normalizeVideoSeconds(raw any) (*float64, error) {
	var value float64
	switch v := raw.(type) {
	case float64:
		value = v
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil, fmt.Errorf("视频生成请求 seconds 必须是数字或数字字符串: %w", err)
		}
		value = parsed
	default:
		return nil, fmt.Errorf("视频生成请求 seconds 必须是数字或数字字符串，实际为 %v", raw)
	}
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, fmt.Errorf("视频生成请求 seconds 必须是正数，实际为 %v", raw)
	}
	return &value, nil
}

// isValidVideoSize 校验 size 为 WxH 像素串形态（两段正整数、小写 x 分隔、
// 无前导零/空格，如 1280x720/720x1280/960x960）。档位合法性由上游裁决
// （契约 §2.2：sora 三档之外上游 400，如 mockupstream
// media_video_create_400_bad_size），网关不硬编码档位清单。
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

// normalizeVideoCount 归一 n（契约 §2.2：int ≥1，默认 1）。
func normalizeVideoCount(raw any) (int, error) {
	value, ok := raw.(float64)
	if !ok {
		return 0, fmt.Errorf("视频生成请求 n 必须是整数，实际为 %v", raw)
	}
	if value != math.Trunc(value) {
		return 0, fmt.Errorf("视频生成请求 n 必须是整数，实际为 %v", raw)
	}
	if value < 1 {
		return 0, fmt.Errorf("视频生成请求 n 必须 ≥1，实际为 %v", raw)
	}
	return int(value), nil
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
