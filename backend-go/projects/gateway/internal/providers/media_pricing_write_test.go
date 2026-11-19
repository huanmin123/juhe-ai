package providers

// 非对话媒体统一计费写入路径单测（设计 §5.2/§5.3）：严格 schema、双口径
// 键判定、mode/协议匹配、0 价有效性与启用态完整性。

import (
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
