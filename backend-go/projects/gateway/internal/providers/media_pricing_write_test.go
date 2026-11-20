package providers

// 非对话媒体统一计费写入路径单测（设计 §5.2/§5.3）：严格 schema、双口径
// 键判定、mode/协议匹配、0 价有效性与启用态完整性。

import (
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

func parseMediaPricingForTest(t *testing.T, raw string) *mediapricing.Pricing {
	t.Helper()
	parsed, err := mediapricing.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse media pricing: %v", err)
	}
	return parsed
}

const (
	testVideoMediaPricingSecond = `{"version":1,"unit":"second","baseUsd":0.1,"baseTier":"720p","tierMultipliers":{"480p":0.5,"720p":1,"1080p":2,"1440p":3,"2160p":5}}`
	testVideoMediaPricingCall   = `{"version":1,"unit":"call","baseUsd":0,"baseTier":"","tierMultipliers":{}}`
	testImageMediaPricing       = `{"version":1,"unit":"image","baseUsd":0.04,"baseTier":"1k","tierMultipliers":{"1k":1,"2k":1.6,"4k":3}}`
	testAudioMediaPricing       = `{"version":1,"unit":"second","baseUsd":0.0002,"baseTier":"","tierMultipliers":{}}`
)

func TestApplyCatalogMediaPricingModeAware(t *testing.T) {
	videoMode := "video"
	video := ModelCatalogItem{Mode: &videoMode}
	applyCatalogMediaPricing(&video, sql.NullString{String: testVideoMediaPricingSecond, Valid: true})
	if !video.HasMediaPricing || video.MediaPricingInvalid {
		t.Fatalf("video 行 + second 模板应有效: %#v", video)
	}
	// mode=video 配 image 模板：结构合法但 kind 不匹配，必须按无价处理，
	// 不能显示为有价、终态却 ValidateFor 失败落 0 + usage_missing。
	mismatch := ModelCatalogItem{Mode: &videoMode}
	applyCatalogMediaPricing(&mismatch, sql.NullString{String: testImageMediaPricing, Valid: true})
	if mismatch.HasMediaPricing || !mismatch.MediaPricingInvalid {
		t.Fatalf("video 行 + image 模板必须标记无价/非法: %#v", mismatch)
	}
	// chat 投影没有媒体 kind 信息（Gemini generate_content 图像行），不能
	// 据此把真实图像价误判为非法。
	chatMode := "chat"
	chatImage := ModelCatalogItem{Mode: &chatMode}
	applyCatalogMediaPricing(&chatImage, sql.NullString{String: testImageMediaPricing, Valid: true})
	if !chatImage.HasMediaPricing || chatImage.MediaPricingInvalid {
		t.Fatalf("chat 投影 + image 模板应保留为有效媒体价: %#v", chatImage)
	}
}

func TestParseMediaPricingNullableDistinguishesInvalid(t *testing.T) {
	videoMode := "video"
	if parsed, invalid := parseMediaPricingNullable(sql.NullString{}, &videoMode); parsed != nil || invalid {
		t.Fatalf("NULL 列应为无价且非非法: %#v/%v", parsed, invalid)
	}
	if parsed, invalid := parseMediaPricingNullable(sql.NullString{String: testVideoMediaPricingSecond, Valid: true}, &videoMode); parsed == nil || invalid {
		t.Fatalf("合法 video 模板应解析成功: %#v/%v", parsed, invalid)
	}
	if parsed, invalid := parseMediaPricingNullable(sql.NullString{String: "{", Valid: true}, &videoMode); parsed != nil || !invalid {
		t.Fatalf("坏 JSON 应标记非法: %#v/%v", parsed, invalid)
	}
	// 结构合法但 mode/unit 不匹配与“无价”必须可区分，管理面才能展示
	// mediaPricingInvalid 诊断（设计 §5.2）。
	if parsed, invalid := parseMediaPricingNullable(sql.NullString{String: testImageMediaPricing, Valid: true}, &videoMode); parsed != nil || !invalid {
		t.Fatalf("video 行 + image 模板应标记非法: %#v/%v", parsed, invalid)
	}
	// text/chat 投影无法表达媒体 kind（自定义 generate_content 图像行），
	// 读侧保留模板，不能误判为非法价。
	textMode := "text"
	if parsed, invalid := parseMediaPricingNullable(sql.NullString{String: testImageMediaPricing, Valid: true}, &textMode); parsed == nil || invalid {
		t.Fatalf("text 投影 + image 模板应保留为有效价: %#v/%v", parsed, invalid)
	}
}

func TestParseCustomModelBodyMediaPricing(t *testing.T) {
	body := w14dDecodeBody(t, `{"model":"sora-x","mode":"video","supportedApiProtocols":["video"],"mediaPricing":`+testVideoMediaPricingSecond+`}`)
	parsed, ok := parseCustomModelBody(body, customModelParseOptions{
		requireModel: true,
		statusValues: []string{"draft", "active", "disabled"},
	})
	if !ok {
		t.Fatal("valid mediaPricing rejected")
	}
	if !parsed.present["mediaPricing"] || parsed.values.MediaPricing == nil {
		t.Fatal("mediaPricing not carried through parse")
	}
	if parsed.values.MediaPricing.Unit != mediapricing.UnitSecond {
		t.Fatalf("unit = %q", parsed.values.MediaPricing.Unit)
	}

	nullBody := w14dDecodeBody(t, `{"model":"m","mediaPricing":null}`)
	nullParsed, ok := parseCustomModelBody(nullBody, customModelParseOptions{requireModel: true})
	if !ok || !nullParsed.present["mediaPricing"] || nullParsed.values.MediaPricing != nil {
		t.Fatal("mediaPricing null must be a tracked clear")
	}

	invalidBodies := []string{
		`{"model":"m","mediaPricing":{}}`,
		`{"model":"m","mediaPricing":{"version":1,"unit":"second","baseUsd":0.1,"extra":true}}`,
		`{"model":"m","mediaPricing":{"version":1,"unit":"image","baseUsd":0.1,"baseTier":"1k","tierMultipliers":{"1k":1,"2k":1.6}}}`,
		`{"model":"m","mediaPricing":{"version":1,"unit":"call","baseUsd":-1}}`,
	}
	for _, raw := range invalidBodies {
		if _, ok := parseCustomModelBody(w14dDecodeBody(t, raw), customModelParseOptions{requireModel: true}); ok {
			t.Fatalf("invalid mediaPricing accepted: %s", raw)
		}
	}
}

func TestValidateCustomModelMediaPricing(t *testing.T) {
	videoMode := "video"
	imageMode := "image"
	audioMode := "audio"
	textMode := "text"

	video := parseMediaPricingForTest(t, testVideoMediaPricingSecond)
	if got := validateCustomModelPricing("gpt", customProviderModelUpsertInput{
		Mode: &videoMode, Status: "active", SupportedAPIProtocols: []string{"video"}, MediaPricing: video,
	}); got != "" {
		t.Fatalf("video media pricing rejected: %q", got)
	}

	freeCall := parseMediaPricingForTest(t, testVideoMediaPricingCall)
	if got := validateCustomModelPricing("gpt", customProviderModelUpsertInput{
		Mode: &videoMode, Status: "active", SupportedAPIProtocols: []string{"video"}, MediaPricing: freeCall,
	}); got != "" {
		t.Fatalf("free call pricing rejected: %q", got)
	}
	if !customInputHasCompletePrice(customProviderModelUpsertInput{Mode: &videoMode, MediaPricing: freeCall}) {
		t.Fatal("free call pricing must count as complete price")
	}

	image := parseMediaPricingForTest(t, testImageMediaPricing)
	if got := validateCustomModelPricing("gpt", customProviderModelUpsertInput{
		Mode: &imageMode, Status: "active", SupportedAPIProtocols: []string{"images"}, MediaPricing: image,
	}); got != "" {
		t.Fatalf("image media pricing rejected: %q", got)
	}

	audio := parseMediaPricingForTest(t, testAudioMediaPricing)
	if got := validateCustomModelPricing("gpt", customProviderModelUpsertInput{
		Mode: &audioMode, Status: "active", SupportedAPIProtocols: []string{"audio_speech"}, MediaPricing: audio,
	}); got != "" {
		t.Fatalf("audio media pricing rejected: %q", got)
	}
	if got := validateCustomModelPricing("gpt", customProviderModelUpsertInput{
		Mode: &audioMode, Status: "active", SupportedAPIProtocols: []string{"realtime"}, MediaPricing: audio,
	}); got == "" {
		t.Fatal("audio media pricing accepted without media audio protocol")
	}

	if got := validateCustomModelPricing("gpt", customProviderModelUpsertInput{
		Mode: &textMode, Status: "active", MediaPricing: video,
	}); got == "" {
		t.Fatal("text mode accepted media pricing")
	}

	imageUnitOnVideo := parseMediaPricingForTest(t, testImageMediaPricing)
	if got := validateCustomModelPricing("gpt", customProviderModelUpsertInput{
		Mode: &videoMode, Status: "active", MediaPricing: imageUnitOnVideo,
	}); got == "" {
		t.Fatal("video mode accepted image unit")
	}
}

func TestExistingMediaPricingAllowsUnknownProjectedMode(t *testing.T) {
	image := parseMediaPricingForTest(t, testImageMediaPricing)
	chatMode := "chat"
	textMode := "text"
	// 新建（严格）：chat/空投影不得新配置 mediaPricing。
	for _, mode := range []*string{&chatMode, &textMode, nil} {
		if got := validateCustomModelPricing("gpt", customProviderModelUpsertInput{
			Mode: mode, Status: "active", MediaPricing: image,
		}); got == "" {
			t.Fatalf("mode %v 新配置 image 模板必须拒绝", mode)
		}
	}
	// 已存在媒体模板的 PATCH：Gemini generate_content 图像行/空 endpoint
	// modes 迁移行用 chat/text/空投影，编辑价格与日期必须放行。
	for _, mode := range []*string{&chatMode, &textMode, nil} {
		if got := validateCustomModelPricingAllowUnknown("gpt", customProviderModelUpsertInput{
			Mode: mode, Status: "active", MediaPricing: image,
		}, true); got != "" {
			t.Fatalf("mode %v 已有模板的 PATCH 被拒绝: %s", mode, got)
		}
		if !customInputHasCompletePriceAllowUnknown(customProviderModelUpsertInput{
			Mode: mode, MediaPricing: image,
		}, true) {
			t.Fatalf("mode %v 已有模板应视为已定价", mode)
		}
	}
	// 可映射的媒体 mode 即使走 PATCH 入口仍严格校验 kind。
	videoMode := "video"
	if got := validateCustomModelPricingAllowUnknown("gpt", customProviderModelUpsertInput{
		Mode: &videoMode, Status: "active", MediaPricing: image,
	}, true); got == "" {
		t.Fatal("video mode + image 模板即使 PATCH 也必须拒绝")
	}
}

func TestBuiltInMediaPricingAllowsUnknownProjectedMode(t *testing.T) {
	image := parseMediaPricingForTest(t, testImageMediaPricing)
	canonical, err := image.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	chatMode := "chat"
	item := &ModelCatalogItem{
		Mode:                  &chatMode,
		MediaPricing:          canonical,
		HasMediaPricing:       true,
		SupportedAPIProtocols: []string{"generate_content"},
	}
	// 新建/严格：chat 投影不得配置媒体模板。
	if got := validateBuiltInMediaPricing(item, false); got == "" {
		t.Fatal("chat 投影新配置 mediaPricing 必须拒绝")
	}
	// 已有模板的 PATCH：放行 chat 投影。
	if got := validateBuiltInMediaPricing(item, true); got != "" {
		t.Fatalf("chat 投影已有模板的 PATCH 被拒绝: %s", got)
	}
	// 完整性校验不得再把该行当文本模型索要上下文容量（否则只改发布日期
	// 也会 400）。
	release := "2025-10-01"
	complete := &ModelCatalogItem{
		Mode:                  &chatMode,
		MediaPricing:          canonical,
		HasMediaPricing:       true,
		ReleaseDate:           &release,
		SupportedAPIProtocols: []string{"generate_content"},
	}
	if got := validateBuiltInModelCompletenessAllowUnknown(complete, true); got != "" {
		t.Fatalf("chat 投影媒体模板行被文本完整性拦截: %s", got)
	}
	// 空投影（无 endpoint modes 历史/迁移行）同理。
	empty := &ModelCatalogItem{MediaPricing: canonical, HasMediaPricing: true}
	if got := validateBuiltInMediaPricing(empty, false); got == "" {
		t.Fatal("空 mode 新配置 mediaPricing 必须拒绝")
	}
	if got := validateBuiltInMediaPricing(empty, true); got != "" {
		t.Fatalf("空 mode 已有模板的 PATCH 被拒绝: %s", got)
	}
	// 可映射 mode 仍严格。
	videoMode := "video"
	if got := validateBuiltInMediaPricing(&ModelCatalogItem{
		Mode: &videoMode, MediaPricing: canonical, HasMediaPricing: true,
	}, true); got == "" {
		t.Fatal("video 行 + image 模板即使 PATCH 也必须拒绝")
	}
}

func TestCustomModelMediaPricingPriceKeys(t *testing.T) {
	parsed := &customModelParsedInput{present: map[string]bool{"mediaPricing": true}}
	if !parsed.hasPriceKeys() || parsed.hasLegacyPriceKeys() {
		t.Fatal("mediaPricing price-key classification drifted")
	}
	if parsed.hasMediaPricingConflict() {
		t.Fatal("mediaPricing alone is not a dual-field conflict")
	}
	parsed.values.MediaPricing = parseMediaPricingForTest(t, testVideoMediaPricingSecond)
	parsed.present["outputUsdPerImage"] = true
	if !parsed.hasMediaPricingConflict() {
		t.Fatal("mediaPricing + legacy price key must conflict")
	}
	parsed.values.MediaPricing = nil
	if parsed.hasMediaPricingConflict() {
		t.Fatal("null mediaPricing must not conflict with legacy price keys")
	}
}

func TestMediaPricingJSONValue(t *testing.T) {
	if mediaPricingJSONValue(nil) != nil {
		t.Fatal("nil pricing must write SQL NULL")
	}
	pricing := parseMediaPricingForTest(t, testVideoMediaPricingCall)
	value, ok := mediaPricingJSONValue(pricing).(string)
	if !ok || value == "" {
		t.Fatalf("media pricing JSON value = %#v", mediaPricingJSONValue(pricing))
	}
	reparsed, err := mediapricing.Parse([]byte(value))
	if err != nil || reparsed.BaseUsd != 0 || !reparsed.Unit.Valid() {
		t.Fatalf("round trip pricing = %+v, %v", reparsed, err)
	}
}

func TestBuiltinMediaPricingPatch(t *testing.T) {
	pricing := parseMediaPricingForTest(t, testImageMediaPricing)
	canonical, err := pricing.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	imageMode := "image"
	visible := true
	current := &ModelCatalogItem{
		Mode:              &imageMode,
		MediaPricing:      canonical,
		HasMediaPricing:   true,
		CatalogVisible:    &visible,
		OutputUsdPerImage: ptrFloat64(0.04),
	}
	if got := builtInCurrentValue(current, "mediaPricing"); got != string(canonical) {
		t.Fatalf("current mediaPricing = %#v", got)
	}
	if !hasDirectPrice(*current) {
		t.Fatal("HasMediaPricing row must count as directly priced")
	}
	next := mergedBuiltInItem(current, []builtinPatchField{{
		Name:  "mediaPricing",
		Value: string(canonical),
	}})
	if !next.HasMediaPricing || string(next.MediaPricing) != string(canonical) {
		t.Fatalf("merged mediaPricing = %#v", next.MediaPricing)
	}
	assignments, params := (&Store{}).builtinPatchAssignments(current, []builtinPatchField{{
		Name:  "mediaPricing",
		Value: string(canonical),
	}})
	if len(assignments) != 1 || assignments[0] != "media_pricing_json = ?" || params[0] != string(canonical) {
		t.Fatalf("assignments = %v, params = %v", assignments, params)
	}
}

func TestProjectedMediaKindForTemplateCandidates(t *testing.T) {
	cases := []struct {
		name      string
		mode      string
		protocols []string
		outputs   []string
		unit      mediapricing.Unit
		want      mediapricing.Kind
		ok        bool
	}{
		{"direct mode", "video", nil, nil, mediapricing.UnitSecond, mediapricing.KindVideo, true},
		{"gemini image projection", "chat", []string{"generate_content"}, []string{"text", "image"},
			mediapricing.UnitImage, mediapricing.KindImage, true},
		{"empty mode call", "", nil, nil, mediapricing.UnitCall, mediapricing.KindVideo, true},
		{"audio output", "chat", []string{"generate_content"}, []string{"audio"},
			mediapricing.UnitSecond, mediapricing.KindAudio, true},
		{"audio protocol fallback", "chat", []string{"audio_transcription"}, nil,
			mediapricing.UnitSecond, mediapricing.KindAudio, true},
		{"ambiguous second", "chat", []string{"generate_content"}, []string{"text"},
			mediapricing.UnitSecond, "", false},
	}
	for _, tc := range cases {
		kind, ok := projectedMediaKind(tc.mode, tc.protocols, tc.outputs, tc.unit)
		if kind != tc.want || ok != tc.ok {
			t.Fatalf("%s: kind=%q ok=%v, want %q/%v", tc.name, kind, ok, tc.want, tc.ok)
		}
	}
}

func TestMediaPricingProtocolFamily(t *testing.T) {
	if got := mediaPricingProtocolFamily(mediapricing.KindAudio, []string{"audio_speech"}); got != "audio_speech" {
		t.Fatalf("speech family = %q", got)
	}
	if got := mediaPricingProtocolFamily(mediapricing.KindAudio, []string{"audio_transcription"}); got != "audio_transcription" {
		t.Fatalf("transcription family = %q", got)
	}
	if got := mediaPricingProtocolFamily(mediapricing.KindAudio, []string{"audio_speech", "audio_transcription"}); got != "audio_transcription" {
		t.Fatalf("mixed family = %q", got)
	}
	if got := mediaPricingProtocolFamily(mediapricing.KindVideo, []string{"video"}); got != "video" {
		t.Fatalf("video family = %q", got)
	}
}
