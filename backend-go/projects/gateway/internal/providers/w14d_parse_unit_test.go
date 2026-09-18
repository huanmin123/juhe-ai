// w14d_parse_unit_test.go pins the previously uncovered pure branches of the
// write-family parsers and validators: every zod-equivalent failure fork of
// parseCustomModelBody / parseServiceTierPrices, decodeModelBody body edges,
// the capability/pricing validators and the small mutation helpers.
package providers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// w14dDecodeBody decodes a JSON literal through decodeModelBody.
func w14dDecodeBody(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	request := httptest.NewRequest("POST", "/", strings.NewReader(body))
	decoded, ok := decodeModelBody(httptest.NewRecorder(), request)
	if !ok {
		t.Fatalf("decodeModelBody rejected %s", body)
	}
	return decoded
}

func TestW14dDecodeModelBodyEdges(t *testing.T) {
	// Empty body decodes to an empty object (the route sees zero keys).
	emptyRequest := httptest.NewRequest("POST", "/", strings.NewReader("   "))
	emptyBody, ok := decodeModelBody(httptest.NewRecorder(), emptyRequest)
	if !ok || len(emptyBody) != 0 {
		t.Fatalf("empty body: ok=%v body=%v", ok, emptyBody)
	}
	// Invalid JSON is a 400.
	badRecorder := httptest.NewRecorder()
	badRequest := httptest.NewRequest("POST", "/", strings.NewReader("{nope"))
	if _, ok := decodeModelBody(badRecorder, badRequest); ok {
		t.Fatal("invalid JSON must fail")
	}
	if badRecorder.Code != 400 {
		t.Fatalf("invalid JSON status: %d", badRecorder.Code)
	}
	// A non-object JSON value (an array) collapses to an empty object.
	arrayRequest := httptest.NewRequest("POST", "/", strings.NewReader(`[1,2]`))
	arrayBody, ok := decodeModelBody(httptest.NewRecorder(), arrayRequest)
	if !ok || len(arrayBody) != 0 {
		t.Fatalf("array body: ok=%v body=%v", ok, arrayBody)
	}
	// A failing body reader renders the same bad request.
	failingRecorder := httptest.NewRecorder()
	failingRequest := httptest.NewRequest("POST", "/", io.MultiReader(errReader{}, errReader{}))
	if _, ok := decodeModelBody(failingRecorder, failingRequest); ok {
		t.Fatal("read failure must fail")
	}
	if failingRecorder.Code != 400 {
		t.Fatalf("read failure status: %d", failingRecorder.Code)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("w14d read failure") }

func TestW14dParseCustomModelBodyFieldForks(t *testing.T) {
	postOptions := customModelParseOptions{
		requireModel: true, allowTemplateID: true, allowScope: true, allowNotes: true,
		statusValues: []string{"draft", "active", "disabled"},
	}
	patchOptions := customModelParseOptions{
		allowNotes:               true,
		requireExpectedUpdatedAt: true,
		statusValues:             []string{"draft", "active", "disabled"},
	}
	builtInOptions := customModelParseOptions{
		allowCatalogVisible:      true,
		requireExpectedUpdatedAt: true,
		statusValues:             []string{"active", "disabled"},
	}
	cases := []struct {
		name    string
		body    string
		options customModelParseOptions
	}{
		{"template id not allowed", `{"model":"a","configurationTemplateId":"t"}`, customModelParseOptions{requireModel: true, statusValues: []string{"active"}}},
		{"template id non-string", `{"model":"a","configurationTemplateId":7}`, postOptions},
		{"template id empty", `{"model":"a","configurationTemplateId":" "}`, postOptions},
		{"scope not allowed", `{"model":"a","scope":"global"}`, customModelParseOptions{requireModel: true, statusValues: []string{"active"}}},
		{"scope invalid value", `{"model":"a","scope":"team"}`, postOptions},
		{"model not allowed on patch", `{"model":"a","expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`, patchOptions},
		{"status invalid", `{"model":"a","status":"live"}`, postOptions},
		{"catalogVisible not allowed", `{"model":"a","catalogVisible":true}`, postOptions},
		{"catalogVisible non-bool", `{"catalogVisible":"yes","expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`, builtInOptions},
		{"mode non-string", `{"model":"a","mode":3}`, postOptions},
		{"mode invalid", `{"model":"a","mode":"video"}`, postOptions},
		{"protocols non-array", `{"model":"a","supportedApiProtocols":"chat"}`, postOptions},
		{"protocols bad entry", `{"model":"a","supportedApiProtocols":[7]}`, postOptions},
		{"tiers too many", `{"model":"a","supportedServiceTiers":["t1","t2","t3","t4","t5","t6","t7","t8","t9","ta","tb","tc","td","te","tf","tg","th"]}`, postOptions},
		{"tiers bad token", `{"model":"a","supportedServiceTiers":["!!"]}`, postOptions},
		{"efforts bad token", `{"model":"a","supportedReasoningEfforts":["!!"]}`, postOptions},
		{"default effort non-string", `{"model":"a","defaultReasoningEffort":1}`, postOptions},
		{"release date non-string", `{"model":"a","releaseDate":7}`, postOptions},
		{"release date malformed", `{"model":"a","releaseDate":"2024/01/01"}`, postOptions},
		{"shutdown date malformed", `{"model":"a","shutdownDate":"tomorrow"}`, postOptions},
		{"context window float", `{"model":"a","contextWindowTokens":1.5}`, postOptions},
		{"context window negative", `{"model":"a","contextWindowTokens":-1}`, postOptions},
		{"max input string", `{"model":"a","maxInputTokens":"x"}`, postOptions},
		{"max output negative", `{"model":"a","maxOutputTokens":-3}`, postOptions},
		{"input price string", `{"model":"a","inputUsdPer1M":"1"}`, postOptions},
		{"output price negative", `{"model":"a","outputUsdPer1M":-2}`, postOptions},
		{"cached price negative", `{"model":"a","cachedInputUsdPer1M":-2}`, postOptions},
		{"cache write negative", `{"model":"a","cacheWriteUsdPer1M":-2}`, postOptions},
		{"cache write 1h negative", `{"model":"a","cacheWrite1hUsdPer1M":-2}`, postOptions},
		{"cache storage negative", `{"model":"a","cacheStorageUsdPer1MPerHour":-2}`, postOptions},
		{"tier prices non-object", `{"model":"a","serviceTierPrices":[]}`, postOptions},
		{"tier prices empty key", `{"model":"a","inputUsdPer1M":1,"serviceTierPrices":{"  ":{}}}`, postOptions},
		{"tier prices long key", `{"model":"a","inputUsdPer1M":1,"serviceTierPrices":{"` + strings.Repeat("k", 65) + `":{}}}`, postOptions},
		{"tier prices value non-object", `{"model":"a","serviceTierPrices":{"p":3}}`, postOptions},
		{"tier prices unknown key", `{"model":"a","inputUsdPer1M":1,"serviceTierPrices":{"p":{"mystery":1}}}`, postOptions},
		{"tier prices negative price", `{"model":"a","inputUsdPer1M":1,"serviceTierPrices":{"p":{"inputUsdPer1M":-1}}}`, postOptions},
		{"tier prices non-number price", `{"model":"a","inputUsdPer1M":1,"serviceTierPrices":{"p":{"inputUsdPer1M":"1"}}}`, postOptions},
		{"image input negative", `{"model":"a","imageInputUsdPer1M":-1}`, postOptions},
		{"image output string", `{"model":"a","imageOutputUsdPer1M":true}`, postOptions},
		{"audio input negative", `{"model":"a","audioInputUsdPer1M":-1}`, postOptions},
		{"audio output string", `{"model":"a","audioOutputUsdPer1M":false}`, postOptions},
		{"per image negative", `{"model":"a","outputUsdPerImage":-1}`, postOptions},
		{"notes not allowed on built-in", `{"notes":"x","expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`, builtInOptions},
		{"notes non-string", `{"model":"a","notes":5}`, postOptions},
		{"expectedUpdatedAt not allowed on post", `{"model":"a","expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`, postOptions},
		{"expectedUpdatedAt malformed", `{"expectedUpdatedAt":"yesterday","notes":"x"}`, patchOptions},
	}
	for _, testCase := range cases {
		body := w14dDecodeBody(t, testCase.body)
		if _, ok := parseCustomModelBody(body, testCase.options); ok {
			t.Fatalf("%s: parse must fail", testCase.name)
		}
	}

	// The accepted shape: trimmed strings and the canonical instant. The
	// patch schema rejects model/scope/template, so only present-keyed
	// fields ride along.
	body := w14dDecodeBody(t, `{"status":" draft ","notes":" n ","expectedUpdatedAt":"2026-01-02T03:04:05.678Z"}`)
	parsed, ok := parseCustomModelBody(body, patchOptions)
	if !ok {
		t.Fatal("valid patch body must parse")
	}
	if parsed.status != "draft" || parsed.expectedUpdatedAt != "2026-01-02T03:04:05.678Z" {
		t.Fatalf("parsed values: %+v", parsed)
	}
	if !parsed.present["status"] || !parsed.present["notes"] {
		t.Fatalf("present keys: %v", parsed.present)
	}
	// hasContentBeyondExpectedUpdatedAt: model/scope/template alone count as
	// content even though they carry no present key.
	if !parsed.hasContentBeyondExpectedUpdatedAt() {
		t.Fatal("model must count as content")
	}
	// A blank notes value still counts as content (its present key exists).
	contentBody := w14dDecodeBody(t, `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","notes":"  "}`)
	contentParsed, ok := parseCustomModelBody(contentBody, patchOptions)
	if !ok {
		t.Fatal("blank notes body must parse")
	}
	if !contentParsed.hasContentBeyondExpectedUpdatedAt() {
		t.Fatal("blank notes still counts as submitted content")
	}
	// catalogVisible + expectedUpdatedAt only: the bool field is present-keyed.
	visibleBody := w14dDecodeBody(t, `{"catalogVisible":false,"expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`)
	visibleParsed, ok := parseCustomModelBody(visibleBody, builtInOptions)
	if !ok {
		t.Fatal("catalogVisible body must parse")
	}
	if visibleParsed.catalogVisible == nil || *visibleParsed.catalogVisible {
		t.Fatalf("catalogVisible value: %v", visibleParsed.catalogVisible)
	}
	// An instant with a numeric offset canonicalizes to UTC millis.
	offsetBody := w14dDecodeBody(t, `{"notes":"n","expectedUpdatedAt":"2026-01-02T11:04:05.678+08:00"}`)
	offsetParsed, ok := parseCustomModelBody(body, patchOptions)
	if !ok {
		t.Fatal("offset body must parse")
	}
	offsetParsed, ok = parseCustomModelBody(offsetBody, patchOptions)
	if !ok || offsetParsed.expectedUpdatedAt != "2026-01-02T03:04:05.678Z" {
		t.Fatalf("offset canonicalization: %v ok=%v", offsetParsed, ok)
	}
}

func TestW14dParseCustomModelBodyAcceptedEdges(t *testing.T) {
	options := customModelParseOptions{
		requireModel: true, allowTemplateID: true, allowScope: true, allowNotes: true,
		statusValues: []string{"draft", "active", "disabled"},
	}
	// Nullable variants across every supported key.
	body := w14dDecodeBody(t, `{"model":"m","mode":null,"supportedApiProtocols":[],
		"supportedServiceTiers":[],"supportedReasoningEfforts":[],"defaultReasoningEffort":null,
		"releaseDate":null,"shutdownDate":null,"contextWindowTokens":null,"maxInputTokens":null,
		"maxOutputTokens":null,"inputUsdPer1M":null,"outputUsdPer1M":null,"cachedInputUsdPer1M":null,
		"cacheWriteUsdPer1M":null,"cacheWrite1hUsdPer1M":null,"cacheStorageUsdPer1MPerHour":null,
		"serviceTierPrices":{},"imageInputUsdPer1M":null,"imageOutputUsdPer1M":null,
		"audioInputUsdPer1M":null,"audioOutputUsdPer1M":null,"outputUsdPerImage":null,
		"pricingNotes":null,"capabilityNotes":null,"notes":null}`)
	parsed, ok := parseCustomModelBody(body, options)
	if !ok {
		t.Fatal("null-heavy body must parse")
	}
	if len(parsed.present) == 0 {
		t.Fatal("present keys must be recorded")
	}
	// Integer and date acceptance plus zero price.
	body = w14dDecodeBody(t, `{"model":"m","contextWindowTokens":128,"releaseDate":"2024-05-06","inputUsdPer1M":0}`)
	parsed, ok = parseCustomModelBody(body, options)
	if !ok {
		t.Fatal("integer body must parse")
	}
	if parsed.values.ContextWindowTokens == nil || *parsed.values.ContextWindowTokens != 128 {
		t.Fatalf("context window: %v", parsed.values.ContextWindowTokens)
	}
	if parsed.values.ReleaseDate == nil || *parsed.values.ReleaseDate != "2024-05-06" {
		t.Fatalf("release date: %v", parsed.values.ReleaseDate)
	}
	if parsed.values.InputUsdPer1M == nil || *parsed.values.InputUsdPer1M != 0 {
		t.Fatalf("input price: %v", parsed.values.InputUsdPer1M)
	}
}

func TestW14dParseServiceTierPricesForks(t *testing.T) {
	if _, _, ok := parseServiceTierPrices(json.RawMessage(`nope`)); ok {
		t.Fatal("non-JSON must fail")
	}
	if _, _, ok := parseServiceTierPrices(json.RawMessage(`{"p":[]}`)); ok {
		t.Fatal("non-object tier value must fail")
	}
	prices, tiers, ok := parseServiceTierPrices(json.RawMessage(`{
		" p ": {"inputUsdPer1M":1,"outputUsdPer1M":null},
		"empty": {},
		"nulls": {"inputUsdPer1M":null}
	}`))
	if !ok {
		t.Fatal("valid tier prices must parse")
	}
	if len(prices) != 3 {
		t.Fatalf("prices: %v", prices)
	}
	// Only tiers carrying at least one defined price are reported.
	if len(tiers) != 1 || tiers[0] != "p" {
		t.Fatalf("defined tiers: %v", tiers)
	}
}

func TestW14dCanonicalInstantAndKeys(t *testing.T) {
	if _, ok := canonicalRfc3339Instant("not-a-time"); ok {
		t.Fatal("malformed instant must fail")
	}
	canonical, ok := canonicalRfc3339Instant("2026-01-02T03:04:05.6789+08:00")
	if !ok {
		t.Fatal("offset instant must parse")
	}
	// 2026-01-02T03:04:05.6789+08:00 == 2026-01-01T19:04:05.678Z.
	if canonical != "2026-01-01T19:04:05.678Z" {
		t.Fatalf("canonical instant: %s", canonical)
	}
	ordered := jsonOrderedKeys(map[string]json.RawMessage{"b": {}, "a": {}, "c": {}})
	if len(ordered) != 3 || ordered[0] != "a" || ordered[1] != "b" || ordered[2] != "c" {
		t.Fatalf("ordered keys: %v", ordered)
	}
}

func TestW14dValidateCustomModelCapabilitiesForks(t *testing.T) {
	imageMode := "image"
	textMode := "text"
	cases := []struct {
		name     string
		provider string
		input    customProviderModelUpsertInput
		want     string
	}{
		{"image with tiers", "gpt", customProviderModelUpsertInput{Mode: &imageMode, SupportedServiceTiers: []string{"priority"}}, "只有文本自定义模型支持服务等级和思考能力配置"},
		{"image with default effort", "gpt", customProviderModelUpsertInput{Mode: &imageMode, DefaultReasoningEffort: stringPtr("high")}, "只有文本自定义模型支持服务等级和思考能力配置"},
		{"gpt too many tiers", "gpt", customProviderModelUpsertInput{SupportedServiceTiers: []string{"priority", "flex", "standard"}}, "自定义模型参数无效"},
		{"gpt too many efforts", "gpt", customProviderModelUpsertInput{SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "max", "xhigh", "minimal", "extra"}}, "自定义模型参数无效"},
		{"gpt foreign tier", "GPT", customProviderModelUpsertInput{SupportedServiceTiers: []string{"fast"}}, "自定义模型参数无效"},
		{"gpt foreign effort", "gpt", customProviderModelUpsertInput{SupportedReasoningEfforts: []string{"ultra"}}, "自定义模型参数无效"},
		{"default effort unknown", "gpt", customProviderModelUpsertInput{DefaultReasoningEffort: stringPtr("high"), SupportedReasoningEfforts: []string{"low"}}, "默认思考级别必须属于支持的思考级别"},
		{"default effort known", "gpt", customProviderModelUpsertInput{DefaultReasoningEffort: stringPtr("high"), SupportedReasoningEfforts: []string{"high"}}, ""},
		{"tier price on image mode", "gpt", customProviderModelUpsertInput{Mode: &imageMode, ServiceTierPrices: map[string]ModelPriceSet{"priority": {InputUsdPer1M: ptrFloat64(1)}}}, "只有文本自定义模型支持服务档位价格"},
		{"tier price unknown tier", "gpt", customProviderModelUpsertInput{ServiceTierPrices: map[string]ModelPriceSet{"flex": {InputUsdPer1M: ptrFloat64(1)}}}, "服务档位价格必须属于模型支持的服务等级"},
	}
	for _, testCase := range cases {
		if got := validateCustomModelCapabilities(testCase.provider, testCase.input); got != testCase.want {
			t.Fatalf("%s: got %q want %q", testCase.name, got, testCase.want)
		}
	}
	// The text mode path accepts a tier-price set over supported tiers.
	textInput := customProviderModelUpsertInput{
		Mode:                  &textMode,
		SupportedServiceTiers: []string{"priority"},
		ServiceTierPrices:     map[string]ModelPriceSet{"priority": {InputUsdPer1M: ptrFloat64(1)}},
	}
	if got := validateCustomModelCapabilities("gpt", textInput); got != "" {
		t.Fatalf("valid tier prices rejected: %q", got)
	}
}

func TestW14dValidateCustomModelPricingForks(t *testing.T) {
	// Disabled status skips the price completeness rule.
	disabled := customProviderModelUpsertInput{Status: "disabled"}
	if got := validateCustomModelPricing("gpt", disabled); got != "" {
		t.Fatalf("disabled without price: %q", got)
	}
	// Empty status defaults to active and demands a direct price.
	empty := customProviderModelUpsertInput{}
	if got := validateCustomModelPricing("gpt", empty); got != "启用的自定义模型必须配置完整当前价格" {
		t.Fatalf("empty status pricing: %q", got)
	}
	// Audio mode direct price satisfies the completeness rule.
	audioMode := "audio"
	audio := customProviderModelUpsertInput{Mode: &audioMode, Status: "active", AudioOutputUsdPer1M: ptrFloat64(2)}
	if got := validateCustomModelPricing("gpt", audio); got != "" {
		t.Fatalf("audio direct price: %q", got)
	}
}

func TestW14dValidateBuiltInModelCompletenessForks(t *testing.T) {
	if got := validateBuiltInModelCompleteness(&ModelCatalogItem{}); got != "内置模型必须配置发布时间" {
		t.Fatalf("missing release: %q", got)
	}
	release := "2024-01-01"
	if got := validateBuiltInModelCompleteness(&ModelCatalogItem{ReleaseDate: &release}); got != "内置模型必须配置接口协议" {
		t.Fatalf("missing protocols: %q", got)
	}
	if got := validateBuiltInModelCompleteness(&ModelCatalogItem{ReleaseDate: &release, SupportedAPIProtocols: []string{"chat_completions"}}); got != "内置模型必须配置当前价格" {
		t.Fatalf("missing price: %q", got)
	}
	zero := int64(0)
	item := &ModelCatalogItem{
		ReleaseDate:           &release,
		SupportedAPIProtocols: []string{"chat_completions"},
		InputUsdPer1M:         ptrFloat64(1),
		ContextWindowTokens:   &zero,
		MaxInputTokens:        &zero,
	}
	if got := validateBuiltInModelCompleteness(item); got != "内置文本模型必须配置上下文或最大输入容量" {
		t.Fatalf("missing context: %q", got)
	}
	item.ContextWindowTokens = ptrInt64(128)
	if got := validateBuiltInModelCompleteness(item); got != "" {
		t.Fatalf("complete text model: %q", got)
	}
	// Image mode skips the context rule.
	imageMode := "image"
	item.Mode = &imageMode
	item.ContextWindowTokens = &zero
	if got := validateBuiltInModelCompleteness(item); got != "" {
		t.Fatalf("image model context rule must be skipped: %q", got)
	}
}

func TestW14dCustomModelInputFromConfigurationTemplate(t *testing.T) {
	imageMode := "image"
	template := &ModelCatalogItem{
		Mode:                      &imageMode,
		SupportedAPIProtocols:     []string{"images", "audio", "realtime", "chat_completions"},
		SupportedServiceTiers:     []string{"priority"},
		SupportedReasoningEfforts: []string{"high"},
		ServiceTierPrices:         map[string]ModelPriceSet{"priority": {InputUsdPer1M: ptrFloat64(1)}},
	}
	inherited := customModelInputFromConfigurationTemplate(template)
	if inherited.Mode == nil || *inherited.Mode != "image" {
		t.Fatalf("image mode inheritance: %v", inherited.Mode)
	}
	// audio/realtime are dropped, images kept.
	if len(inherited.SupportedAPIProtocols) != 2 {
		t.Fatalf("filtered protocols: %v", inherited.SupportedAPIProtocols)
	}
	if inherited.SupportedAPIProtocols[0] != "images" || inherited.SupportedAPIProtocols[1] != "chat_completions" {
		t.Fatalf("protocol order: %v", inherited.SupportedAPIProtocols)
	}
	if len(inherited.ServiceTierPrices) != 1 || inherited.ServiceTierPrices["priority"].InputUsdPer1M == nil {
		t.Fatalf("cloned tier prices: %v", inherited.ServiceTierPrices)
	}
	if len(inherited.SupportedServiceTiers) != 1 || len(inherited.SupportedReasoningEfforts) != 1 {
		t.Fatalf("cloned capability lists: %v %v", inherited.SupportedServiceTiers, inherited.SupportedReasoningEfforts)
	}
	// A text template keeps text mode; empty clone sources stay nil-safe.
	textMode := "text"
	plain := customModelInputFromConfigurationTemplate(&ModelCatalogItem{Mode: &textMode})
	if plain.Mode == nil || *plain.Mode != "text" {
		t.Fatalf("text mode inheritance: %v", plain.Mode)
	}
	if len(plain.SupportedAPIProtocols) != 0 || plain.ServiceTierPrices == nil || len(plain.ServiceTierPrices) != 0 {
		t.Fatalf("empty template clone: %v %v", plain.SupportedAPIProtocols, plain.ServiceTierPrices)
	}
}

// w14dAuth builds an AuthContext with the given role and account id.
func w14dAuth(role, systemAccountID string) *authsys.AuthContext {
	return &authsys.AuthContext{Role: role, SystemAccountID: systemAccountID}
}

func TestW14dSmallMutationHelpers(t *testing.T) {
	userAuth := w14dAuth("user", "u1")
	adminAuth := w14dAuth("admin", "a1")
	if !canMutateCustomModel("global", "", adminAuth) {
		t.Fatal("admin mutates global")
	}
	if canMutateCustomModel("global", "", userAuth) {
		t.Fatal("user cannot mutate global")
	}
	if !canMutateCustomModel("personal", "u1", userAuth) {
		t.Fatal("owner mutates personal")
	}
	if canMutateCustomModel("personal", "u2", userAuth) {
		t.Fatal("non-owner cannot mutate personal")
	}
	if !canMutateCustomModel("personal", "u2", adminAuth) {
		t.Fatal("admin mutates any personal")
	}

	// customModelBoundToAccountMessage.
	emptyMessage := customModelBoundToAccountMessage(&customProviderModelBindingSummary{})
	if emptyMessage != "模型已绑定 AI 账户，不能删除；请先解除账户绑定后再删除" {
		t.Fatalf("empty binding message: %s", emptyMessage)
	}
	detailed := customModelBoundToAccountMessage(&customProviderModelBindingSummary{
		SupportedModelAccountCount: 2, MappingSourceAccountCount: 1, MappingUpstreamAccountCount: 3,
	})
	for _, fragment := range []string{"2 个账户支持模型", "1 个账户映射下游模型", "3 个账户映射上游模型"} {
		if !strings.Contains(detailed, fragment) {
			t.Fatalf("binding message missing %q: %s", fragment, detailed)
		}
	}

	// mutationResultBody with and without cleared provider codes.
	plainBody := mutationResultBody("id", "gpt", "m", "active", "2026-01-01T00:00:00.000Z", nil)
	if _, exists := plainBody["defaultHealthCheckModelCleared"]; exists {
		t.Fatalf("no cleared key expected: %v", plainBody)
	}
	clearedBody := mutationResultBody("id", "gpt", "m", "active", "2026-01-01T00:00:00.000Z", []string{"gpt"})
	if clearedBody["defaultHealthCheckModelCleared"] != true {
		t.Fatalf("cleared key expected: %v", clearedBody)
	}

	// requiresCustomModelPatchValidation.
	statusOnly := &customModelParsedInput{present: map[string]bool{}, status: "active"}
	if !requiresCustomModelPatchValidation(statusOnly) {
		t.Fatal("active status requires validation")
	}
	draftStatus := &customModelParsedInput{present: map[string]bool{}, status: "draft"}
	if requiresCustomModelPatchValidation(draftStatus) {
		t.Fatal("draft without model fields skips validation")
	}
	pricingField := &customModelParsedInput{present: map[string]bool{"inputUsdPer1M": true}}
	if !requiresCustomModelPatchValidation(pricingField) {
		t.Fatal("pricing field requires validation")
	}

	// requiresBuiltInModelPatchValidation.
	if requiresBuiltInModelPatchValidation(nil) {
		t.Fatal("empty patch skips validation")
	}
	if !requiresBuiltInModelPatchValidation([]builtinPatchField{{Name: "status", Value: "active"}}) {
		t.Fatal("active status requires validation")
	}
	if requiresBuiltInModelPatchValidation([]builtinPatchField{{Name: "status", Value: "disabled"}}) {
		t.Fatal("disabled status skips validation")
	}
	if !requiresBuiltInModelPatchValidation([]builtinPatchField{{Name: "releaseDate", Value: "2024-01-01"}}) {
		t.Fatal("releaseDate requires validation")
	}

	// customModelDefaultUsabilityTransitioned / builtinDefaultUsabilityTransitioned.
	current := &customProviderModelRecord{Status: "active", SupportedAPIProtocols: []string{"chat_completions"}}
	disabledNext := customProviderModelUpsertInput{Status: "disabled"}
	if !customModelDefaultUsabilityTransitioned(current, disabledNext) {
		t.Fatal("active->disabled must transition")
	}
	if customModelDefaultUsabilityTransitioned(current, customProviderModelUpsertInput{Status: "active"}) {
		t.Fatal("active->active must not transition")
	}
	before := &ModelCatalogItem{Status: "active", SupportedAPIProtocols: []string{"chat_completions"}}
	after := &ModelCatalogItem{Status: "active", SupportedAPIProtocols: []string{"chat_completions"}, ShutdownDate: stringPtr("2000-01-01")}
	if !builtinDefaultUsabilityTransitioned(before, after) {
		t.Fatal("past shutdown date must transition")
	}
}

func TestW14dProviderModelUsabilityPredicates(t *testing.T) {
	imageMode := "image"
	// isProviderModelUsableForAccountTest.
	if isProviderModelUsableForAccountTest(&ModelCatalogItem{Mode: &imageMode}) {
		t.Fatal("image mode is not usable for account tests")
	}
	if !isProviderModelUsableForAccountTest(&ModelCatalogItem{}) {
		t.Fatal("no protocols defaults to usable")
	}
	if isProviderModelUsableForAccountTest(&ModelCatalogItem{SupportedAPIProtocols: []string{"images"}}) {
		t.Fatal("images-only protocol is not usable")
	}
	if !isProviderModelUsableForAccountTest(&ModelCatalogItem{SupportedAPIProtocols: []string{"images", "chat_completions"}}) {
		t.Fatal("mixed protocols with a text protocol are usable")
	}

	// providerModelIsUsableAsDefault.
	invisible := false
	if providerModelIsUsableAsDefault("active", &invisible, nil, nil, nil) {
		t.Fatal("invisible model is not usable as default")
	}
	if providerModelIsUsableAsDefault("disabled", nil, nil, nil, nil) {
		t.Fatal("disabled model is not usable as default")
	}
	expired := "2000-01-01"
	if providerModelIsUsableAsDefault("active", nil, &expired, nil, nil) {
		t.Fatal("expired shutdown date is not usable")
	}
	future := "2999-01-01"
	if !providerModelIsUsableAsDefault("active", nil, &future, nil, nil) {
		t.Fatal("future shutdown date stays usable")
	}
	blank := " "
	if !providerModelIsUsableAsDefault("active", nil, &blank, nil, nil) {
		t.Fatal("blank shutdown date stays usable")
	}
	if providerModelIsUsableAsDefault("active", nil, nil, &imageMode, nil) {
		t.Fatal("image mode is not usable as default")
	}
	if !providerModelIsUsableAsDefault("active", nil, nil, nil, []string{}) {
		t.Fatal("no protocols is usable as default")
	}
	if providerModelIsUsableAsDefault("active", nil, nil, nil, []string{"images"}) {
		t.Fatal("images-only is not usable as default")
	}
	if !providerModelIsUsableAsDefault("active", nil, nil, nil, []string{"messages"}) {
		t.Fatal("messages protocol is usable as default")
	}
}

func TestW14dBuiltInSubmittedFieldsAndMerge(t *testing.T) {
	mode := "image"
	status := "active"
	visible := true
	protocols := []string{"chat_completions"}
	tiers := []string{"priority"}
	efforts := []string{"high"}
	effort := "high"
	release := "2024-01-01"
	shutdown := "2999-01-01"
	window := int64(128)
	input := int64(64)
	output := int64(32)
	price := 1.5
	parsed := &customModelParsedInput{
		present: map[string]bool{
			"mode": true, "supportedApiProtocols": true, "supportedServiceTiers": true,
			"supportedReasoningEfforts": true, "defaultReasoningEffort": true,
			"releaseDate": true, "shutdownDate": true, "contextWindowTokens": true,
			"maxInputTokens": true, "maxOutputTokens": true, "inputUsdPer1M": true,
			"outputUsdPer1M": true, "serviceTierPrices": true,
		},
		status:            status,
		catalogVisible:    &visible,
		model:             "m",
		scope:             "global",
		expectedUpdatedAt: "2026-01-01T00:00:00.000Z",
	}
	parsed.values = customProviderModelUpsertInput{
		Mode:                      &mode,
		SupportedAPIProtocols:     protocols,
		SupportedServiceTiers:     tiers,
		SupportedReasoningEfforts: efforts,
		DefaultReasoningEffort:    &effort,
		ReleaseDate:               &release,
		ShutdownDate:              &shutdown,
		ContextWindowTokens:       &window,
		MaxInputTokens:            &input,
		MaxOutputTokens:           &output,
		InputUsdPer1M:             &price,
		OutputUsdPer1M:            &price,
		ServiceTierPrices:         map[string]ModelPriceSet{"priority": {InputUsdPer1M: &price}},
	}
	fields := builtInSubmittedFields(parsed)
	names := map[string]bool{}
	for _, field := range fields {
		names[field.Name] = true
	}
	for _, expected := range []string{"status", "catalogVisible", "mode", "supportedApiProtocols",
		"supportedServiceTiers", "supportedReasoningEfforts", "defaultReasoningEffort",
		"releaseDate", "shutdownDate", "contextWindowTokens", "maxInputTokens", "maxOutputTokens",
		"inputUsdPer1M", "outputUsdPer1M", "serviceTierPrices"} {
		if !names[expected] {
			t.Fatalf("submitted field %s missing: %v", expected, names)
		}
	}

	current := &ModelCatalogItem{ID: "cat-1", ProviderCode: "gpt", Model: "m", Status: "active"}
	next := mergedBuiltInItem(current, fields)
	if next.Mode == nil || *next.Mode != "image" {
		t.Fatalf("merged mode: %v", next.Mode)
	}
	if next.CatalogVisible == nil || !*next.CatalogVisible {
		t.Fatalf("merged catalogVisible: %v", next.CatalogVisible)
	}
	if len(next.SupportedServiceTiers) != 1 || len(next.SupportedReasoningEfforts) != 1 {
		t.Fatalf("merged capability lists: %v %v", next.SupportedServiceTiers, next.SupportedReasoningEfforts)
	}
	if next.ContextWindowTokens == nil || *next.ContextWindowTokens != 128 {
		t.Fatalf("merged context window: %v", next.ContextWindowTokens)
	}
	if next.ServiceTierPrices["priority"].InputUsdPer1M == nil {
		t.Fatalf("merged tier prices: %v", next.ServiceTierPrices)
	}
	// mergedBuiltInItem tolerates type-mismatched values (collapses to zero).
	broken := mergedBuiltInItem(current, []builtinPatchField{
		{Name: "mode", Value: 7},
		{Name: "supportedApiProtocols", Value: "not-a-slice"},
		{Name: "supportedServiceTiers", Value: 3},
		{Name: "supportedReasoningEfforts", Value: true},
		{Name: "contextWindowTokens", Value: "big"},
		{Name: "serviceTierPrices", Value: "broken"},
	})
	if broken.Mode != nil || len(broken.SupportedAPIProtocols) != 0 ||
		len(broken.SupportedServiceTiers) != 0 || len(broken.SupportedReasoningEfforts) != 0 ||
		broken.ContextWindowTokens != nil {
		t.Fatalf("type-mismatched patch must collapse to defaults: %+v", broken)
	}
}

func TestW14dParseHealthCheckModelBodyForks(t *testing.T) {
	if _, ok := parseHealthCheckModelBody(map[string]json.RawMessage{
		"model": {}, "extra": {},
	}); ok {
		t.Fatal("extra keys must fail the strict schema")
	}
	if _, ok := parseHealthCheckModelBody(map[string]json.RawMessage{}); ok {
		t.Fatal("missing model key must fail")
	}
	if _, ok := parseHealthCheckModelBody(map[string]json.RawMessage{"model": {}}); ok {
		t.Fatal("non-string model must fail")
	}
	if model, ok := parseHealthCheckModelBody(map[string]json.RawMessage{"model": json.RawMessage(`" m " `)}); !ok || model != "m" {
		t.Fatalf("valid model: %q ok=%v", model, ok)
	}
}
