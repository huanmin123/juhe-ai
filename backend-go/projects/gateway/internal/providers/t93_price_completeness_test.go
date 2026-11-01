package providers

// T93-2（C3-H2）价格完整性校验单元测试：启用态（active）按能力域要求必填
// 价格维度，缺维度返回 400 文案「启用的自定义模型必须配置完整当前价格」；
// draft/disabled 不校验。裁决 C（2026-11-01）：本轮只收紧 text + image，
// audio/video 维持旧口径。

import "testing"

func TestT93TextPriceCompleteness(t *testing.T) {
	textMode := "text"
	cases := []struct {
		name  string
		input customProviderModelUpsertInput
		want  string
	}{
		{
			name:  "只填 input 拒绝",
			input: customProviderModelUpsertInput{Mode: &textMode, Status: "active", InputUsdPer1M: ptrFloat64(1)},
			want:  "启用的自定义模型必须配置完整当前价格",
		},
		{
			name:  "只填 output 拒绝",
			input: customProviderModelUpsertInput{Mode: &textMode, Status: "active", OutputUsdPer1M: ptrFloat64(1)},
			want:  "启用的自定义模型必须配置完整当前价格",
		},
		{
			name:  "只填缓存价拒绝",
			input: customProviderModelUpsertInput{Mode: &textMode, Status: "active", CachedInputUsdPer1M: ptrFloat64(1)},
			want:  "启用的自定义模型必须配置完整当前价格",
		},
		{
			name:  "input+output 通过",
			input: customProviderModelUpsertInput{Mode: &textMode, Status: "active", InputUsdPer1M: ptrFloat64(1), OutputUsdPer1M: ptrFloat64(2)},
			want:  "",
		},
		{
			name:  "input+output+缓存价通过",
			input: customProviderModelUpsertInput{Mode: &textMode, Status: "active", InputUsdPer1M: ptrFloat64(1), OutputUsdPer1M: ptrFloat64(2), CachedInputUsdPer1M: ptrFloat64(0.5), ImageInputUsdPer1M: ptrFloat64(0.3)},
			want:  "",
		},
		{
			name: "声明档位价集缺 output 拒绝",
			input: customProviderModelUpsertInput{
				Mode: &textMode, Status: "active",
				InputUsdPer1M: ptrFloat64(1), OutputUsdPer1M: ptrFloat64(2),
				SupportedServiceTiers: []string{"priority"},
				ServiceTierPrices:     map[string]ModelPriceSet{"priority": {InputUsdPer1M: ptrFloat64(3)}},
			},
			want: "启用的自定义模型必须配置完整当前价格",
		},
		{
			name: "声明档位价集齐全通过",
			input: customProviderModelUpsertInput{
				Mode: &textMode, Status: "active",
				InputUsdPer1M: ptrFloat64(1), OutputUsdPer1M: ptrFloat64(2),
				SupportedServiceTiers: []string{"priority"},
				ServiceTierPrices:     map[string]ModelPriceSet{"priority": {InputUsdPer1M: ptrFloat64(3), OutputUsdPer1M: ptrFloat64(4)}},
			},
			want: "",
		},
		{
			name:  "草稿缺 output 不校验",
			input: customProviderModelUpsertInput{Mode: &textMode, Status: "draft", InputUsdPer1M: ptrFloat64(1)},
			want:  "",
		},
		{
			name:  "停用缺价不校验",
			input: customProviderModelUpsertInput{Mode: &textMode, Status: "disabled"},
			want:  "",
		},
		{
			name: "纯 embedding 协议只需 input",
			input: customProviderModelUpsertInput{
				Mode: &textMode, Status: "active", InputUsdPer1M: ptrFloat64(1),
				SupportedAPIProtocols: []string{"embed_content"},
			},
			want: "",
		},
		{
			name: "embedding 协议缺 input 拒绝",
			input: customProviderModelUpsertInput{
				Mode: &textMode, Status: "active", OutputUsdPer1M: ptrFloat64(1),
				SupportedAPIProtocols: []string{"embed_content", "count_tokens"},
			},
			want: "启用的自定义模型必须配置完整当前价格",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validateCustomModelPricing("gpt", testCase.input); got != testCase.want {
				t.Fatalf("got %q want %q", got, testCase.want)
			}
		})
	}
}

func TestT93ImagePriceCompleteness(t *testing.T) {
	imageMode := "image"
	cases := []struct {
		name  string
		input customProviderModelUpsertInput
		want  string
	}{
		{
			name:  "只有图片输入 token 价拒绝（缺按张价）",
			input: customProviderModelUpsertInput{Mode: &imageMode, Status: "active", ImageInputUsdPer1M: ptrFloat64(1)},
			want:  "启用的自定义模型必须配置完整当前价格",
		},
		{
			name:  "只有图片输出 token 价拒绝（缺按张价）",
			input: customProviderModelUpsertInput{Mode: &imageMode, Status: "active", ImageOutputUsdPer1M: ptrFloat64(1)},
			want:  "启用的自定义模型必须配置完整当前价格",
		},
		{
			name:  "按张价通过",
			input: customProviderModelUpsertInput{Mode: &imageMode, Status: "active", OutputUsdPerImage: ptrFloat64(0.04)},
			want:  "",
		},
		{
			name:  "按张价 + 图片 token 价通过",
			input: customProviderModelUpsertInput{Mode: &imageMode, Status: "active", OutputUsdPerImage: ptrFloat64(0.04), ImageInputUsdPer1M: ptrFloat64(1)},
			want:  "",
		},
		{
			name:  "草稿缺按张价不校验",
			input: customProviderModelUpsertInput{Mode: &imageMode, Status: "draft", ImageOutputUsdPer1M: ptrFloat64(1)},
			want:  "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validateCustomModelPricing("gpt", testCase.input); got != testCase.want {
				t.Fatalf("got %q want %q", got, testCase.want)
			}
		})
	}
}

// TestT93AudioVideoPriceCompletenessUnchanged 钉住裁决 C：本轮 audio/video
// 维持旧口径（audio 任一音频 token 价；video 无通道 → 启用态无直接价）。
func TestT93AudioVideoPriceCompletenessUnchanged(t *testing.T) {
	audioMode := "audio"
	if got := validateCustomModelPricing("gpt", customProviderModelUpsertInput{
		Mode: &audioMode, Status: "active", AudioInputUsdPer1M: ptrFloat64(1),
	}); got != "" {
		t.Fatalf("audio single token price: got %q", got)
	}
	if got := validateCustomModelPricing("gpt", customProviderModelUpsertInput{
		Mode: &audioMode, Status: "active",
	}); got != "启用的自定义模型必须配置完整当前价格" {
		t.Fatalf("audio without price: got %q", got)
	}
	videoMode := "video"
	if got := validateCustomModelPricing("gpt", customProviderModelUpsertInput{
		Mode: &videoMode, Status: "active",
	}); got != "启用的自定义模型必须配置完整当前价格" {
		t.Fatalf("video active without channel: got %q", got)
	}
	if got := validateCustomModelPricing("gpt", customProviderModelUpsertInput{
		Mode: &videoMode, Status: "draft",
	}); got != "" {
		t.Fatalf("video draft: got %q", got)
	}
}
