// oauthmgmt 纯函数补测：传输编解码（transport.go）、加解密（crypto.go）、
// 错误映射（errors.go）、provider 计划适配器（providers.go）、路由体解析
// （routes.go）、各 provider 回调解析与凭据构建（openai/anthropic/gemini/
// grok.go）、会话存储（sessionstore.go）与轮换失效端口（invalidation.go）。
// 这些函数是 Node 迁移镜像，断言锁定错误语义与字段投影。
package oauthmgmt

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// ---------------------------------------------------------------------------
// transport.go
// ---------------------------------------------------------------------------

// TestWCParseTokenPayload：共享 JSON 容错解析。
func TestWCParseTokenPayload(t *testing.T) {
	if payload := parseTokenPayload(""); len(payload) != 0 {
		t.Fatalf("空 body 必须为空: %v", payload)
	}
	payload := parseTokenPayload("not json")
	if payload["raw"] != "not json" {
		t.Fatalf("非法 JSON 必须落 raw: %v", payload)
	}
	if payload := parseTokenPayload("null"); payload["raw"] != "null" {
		t.Fatalf("null JSON 必须落 raw: %v", payload)
	}
	if payload := parseTokenPayload(`{"a":1}`); payload["a"] != float64(1) {
		t.Fatalf("合法 JSON: %v", payload)
	}
}

// TestWCTransportHelpers：文本/数值/时间/JWT 工具。
func TestWCTransportHelpers(t *testing.T) {
	if normalizeText(" x ") != "x" || normalizeText(1.5) != "" {
		t.Fatalf("normalizeText")
	}
	if _, ok := finitePositiveInt("x"); ok {
		t.Fatalf("非数值必须失败")
	}
	if _, ok := finitePositiveInt(0.0); ok {
		t.Fatalf("零必须失败")
	}
	if value, ok := finitePositiveInt(3.0); !ok || value != 3 {
		t.Fatalf("正整数: %d %v", value, ok)
	}
	if got := isoFromMillis(1767323045678); got != "2026-01-02T03:04:05.678Z" {
		t.Fatalf("isoFromMillis: %q", got)
	}
	if claims := decodeJWTClaims(""); len(claims) != 0 {
		t.Fatalf("空 token: %v", claims)
	}
	if claims := decodeJWTClaims("a.b"); len(claims) != 0 {
		t.Fatalf("坏 base64: %v", claims)
	}
	if claims := decodeJWTClaims("a.Gg.signature"); len(claims) != 0 {
		t.Fatalf("非 JSON payload: %v", claims)
	}
	if claims := decodeJWTClaims(fakeJWT(map[string]any{"sub": "u1"})); claims["sub"] != "u1" {
		t.Fatalf("合法 claims: %v", claims)
	}
	if got := encodeForm(map[string]string{"b": "2", "a": "1"}); got != "a=1&b=2" {
		t.Fatalf("encodeForm 排序: %q", got)
	}
	request := formRequest("https://token", map[string]string{"a": "b"})
	if request.Headers["content-type"] != "application/x-www-form-urlencoded" || request.Body != "a=b" {
		t.Fatalf("formRequest: %+v", request)
	}
	jsonReq := jsonRequest("https://token", map[string]string{"k": "v"})
	if jsonReq.Headers["content-type"] != "application/json" || jsonReq.Headers["user-agent"] != "axios/1.13.6" {
		t.Fatalf("jsonRequest: %+v", jsonReq)
	}
	if ensureContext(nil) == nil {
		t.Fatalf("ensureContext nil 兜底")
	}
	upstream := upstreamError("OpenAI", 400, "bad request")
	if upstream.Error() != "OpenAI OAuth 令牌请求失败：HTTP 400，bad request" || upstream.StatusCode != 502 {
		t.Fatalf("upstreamError: %+v", upstream)
	}
	if bare := upstreamError("X", 400, ""); strings.Contains(bare.Error(), "，") {
		t.Fatalf("空 detail 不得追加: %q", bare.Error())
	}
	if status, ok := upstreamStatus(upstream); !ok || status != 502 {
		t.Fatalf("upstreamStatus: %d %v", status, ok)
	}
	if _, ok := upstreamStatus(errors.New("plain")); ok {
		t.Fatalf("普通错误必须无 upstream 状态")
	}
	called := false
	exchanger := ExchangerFunc(func(context.Context, TokenHTTPRequest) (TokenHTTPResponse, error) {
		called = true
		return TokenHTTPResponse{StatusCode: 200}, nil
	})
	if _, err := exchanger.Do(context.Background(), TokenHTTPRequest{}); err != nil || !called {
		t.Fatalf("ExchangerFunc.Do: %v", err)
	}
}

// ---------------------------------------------------------------------------
// crypto.go
// ---------------------------------------------------------------------------

// TestWCCryptoRoundTrip：v1 信封加解密与失败分支。
func TestWCCryptoRoundTrip(t *testing.T) {
	sealed, err := encryptJSON(testSecret, map[string]any{"k": "v"})
	if err != nil || !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("encryptJSON: %s %v", sealed, err)
	}
	var out map[string]any
	if err := decryptJSON(testSecret, sealed, &out); err != nil || out["k"] != "v" {
		t.Fatalf("decryptJSON: %v %v", out, err)
	}
	failures := []string{
		"",
		"v1:only:three",
		"v2:a:b:c",
		"v1:!!!:!!!:!!!",
	}
	for _, envelope := range failures {
		if err := decryptJSON(testSecret, envelope, &out); err == nil {
			t.Fatalf("坏信封必须失败: %q", envelope)
		}
	}
	// 正确格式 + 错误密钥 → GCM 校验失败。
	if err := decryptJSON("other-secret", sealed, &out); err == nil {
		t.Fatalf("错误密钥必须失败")
	}
	if got := hashSecret("x"); len(got) != 64 {
		t.Fatalf("hashSecret 长度: %d", len(got))
	}
	if maskSecret("") != "" {
		t.Fatalf("空值掩码必须为空")
	}
	if got := maskSecret("short"); got != "sh***rt" {
		t.Fatalf("短值掩码: %q", got)
	}
	if got := maskSecret("0123456789abcdef"); got != "012345***cdef" {
		t.Fatalf("长值掩码: %q", got)
	}
}

// ---------------------------------------------------------------------------
// errors.go
// ---------------------------------------------------------------------------

// TestWCErrorWriters：错误到 HTTP 状态/文案的映射矩阵。
func TestWCTestErrorWriters(t *testing.T) {
	deps := &Deps{}
	recorder := httptest.NewRecorder()
	writeCreated(recorder, map[string]any{"id": "x"})
	if recorder.Code != http.StatusCreated || !strings.Contains(recorder.Body.String(), `"data"`) {
		t.Fatalf("writeCreated: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	writeRotationReceipt(recorder, &RotationResult{ID: "a", ConfigRevision: 2, UpdatedAt: "u"})
	if !strings.Contains(recorder.Body.String(), `"configRevision":2`) {
		t.Fatalf("writeRotationReceipt: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.writeProfileError(recorder, &ValidationError{Message: "档案无效"})
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "档案无效") {
		t.Fatalf("writeProfileError validation: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.writeProfileError(recorder, errors.New("db down"))
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "服务器内部错误") {
		t.Fatalf("writeProfileError 其他: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.writeStoreError(recorder, errors.New("db down"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("writeStoreError: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.writeCreateError(recorder, &ConflictError{Message: "账号已存在"}, "fallback")
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "账号已存在") {
		t.Fatalf("writeCreateError conflict: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.writeCreateError(recorder, errors.New("mystery"), "创建失败")
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "创建失败") {
		t.Fatalf("writeCreateError 默认: %d %s", recorder.Code, recorder.Body.String())
	}

	cases := []struct {
		name     string
		err      error
		fallback string
		revision string
		status   int
		message  string
	}{
		{"upstream 502", &UpstreamError{Message: "上游失败", StatusCode: 502}, "fb", "", http.StatusBadGateway, "上游失败"},
		{"upstream 403", &UpstreamError{Message: "拒绝", StatusCode: 403}, "fb", "", http.StatusForbidden, "拒绝"},
		{"grok 400", &grokOAuthError{Message: "Grok 失败", StatusCode: 400}, "Grok 授权失败", "", http.StatusBadRequest, "Grok 授权失败"},
		{"conflict", &ConflictError{Message: "重复"}, "fb", "", http.StatusConflict, "重复"},
		{"revision 专用文案", &RevisionConflictError{Message: "冲突"}, "fb", "专用文案", http.StatusConflict, "专用文案"},
		{"revision 回退", &RevisionConflictError{Message: "冲突"}, "路由冲突回退", "", http.StatusConflict, "路由冲突回退"},
		{"默认", errors.New("mystery"), "兜底", "", http.StatusBadGateway, "兜底"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			deps.writeOAuthError(recorder, testCase.err, testCase.fallback, testCase.revision)
			if recorder.Code != testCase.status || !strings.Contains(recorder.Body.String(), testCase.message) {
				t.Fatalf("%d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if got := oauthErrorText(&UpstreamError{Message: "上游"}, "fb"); got != "上游" {
		t.Fatalf("oauthErrorText 上游: %q", got)
	}
	if got := oauthErrorText(errors.New("x"), "fb"); got != "fb" {
		t.Fatalf("oauthErrorText 回退: %q", got)
	}
}

// ---------------------------------------------------------------------------
// store.go 静态与分支
// ---------------------------------------------------------------------------

// wcStrPtr 返回字符串指针。
func wcStrPtr(value string) *string { return &value }

// TestWCNewStoreValidation：NewStore 的参数校验分支。
func TestWCNewStoreValidation(t *testing.T) {
	if _, err := NewStore(nil, false, "s", nil, nil, nil, nil); err == nil {
		t.Fatalf("缺 db 必须报错")
	}
}

// TestWCStoreStatics：itoa/isoMillis/canonicalRFC3339/scope 投影。
func TestWCStoreStatics(t *testing.T) {
	if itoa(0) != "0" || itoa(123) != "123" || itoa64(45) != "45" {
		t.Fatalf("itoa/itoa64")
	}
	if got := isoMillis(time.Date(2026, 9, 4, 12, 0, 0, 500_000_000, time.UTC)); got != "2026-09-04T12:00:00.500Z" {
		t.Fatalf("isoMillis: %q", got)
	}
	if got, ok := canonicalRFC3339(" 2026-01-02T03:04:05Z "); !ok || got != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("canonicalRFC3339: %q %v", got, ok)
	}
	if _, ok := canonicalRFC3339("bad"); ok {
		t.Fatalf("非法时间必须失败")
	}
	admin := AccessScope{ViewerID: "v", IsAdmin: true, FilterID: "f"}
	if admin.manageableID() != "f" || !admin.canAccessAll() {
		t.Fatalf("admin scope: %+v", admin)
	}
	user := AccessScope{ViewerID: "v"}
	if user.manageableID() != "v" || user.canAccessAll() {
		t.Fatalf("user scope: %+v", user)
	}
	if user.accountsScope().ViewerID != "v" {
		t.Fatalf("accountsScope: %+v", user.accountsScope())
	}
	pg := &Store{pg: true}
	if got := pg.table("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("pg table: %q", got)
	}
	if got := pg.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("pg bind: %q", got)
	}
}

// TestWCStoreErrorTypes：错误类型的 Error() 实现。
func TestWCStoreErrorTypes(t *testing.T) {
	if (&ConflictError{Message: "a"}).Error() != "a" {
		t.Fatalf("ConflictError")
	}
	if (&ValidationError{Message: "b"}).Error() != "b" {
		t.Fatalf("ValidationError")
	}
	if (&RevisionConflictError{Message: "c"}).Error() != "c" {
		t.Fatalf("RevisionConflictError")
	}
	if (&CredentialsUnavailableError{}).Error() != "OAuth 凭据读取失败" {
		t.Fatalf("CredentialsUnavailableError")
	}
	if requiredProfileForProvider(ProviderXAI) != ProfileXAIOpenAIV1 || requiredProfileForProvider(ProviderGPT) != "" {
		t.Fatalf("requiredProfileForProvider")
	}
}

// TestWCCredentialsHelpers：凭据比较与来源提取。
func TestWCCredentialsHelpers(t *testing.T) {
	if !credentialsEqual(map[string]any{"a": 1.0}, map[string]any{"a": 1.0}) {
		t.Fatalf("相同凭据必须相等")
	}
	if credentialsEqual(map[string]any{"a": 1.0}, map[string]any{"a": 2.0}) {
		t.Fatalf("不同凭据不得相等")
	}
	if stringCredential(map[string]any{"k": " v "}, "k") != "v" || stringCredential(map[string]any{}, "k") != "" {
		t.Fatalf("stringCredential")
	}
	if source, err := requiredCredentialSource("oauth", map[string]any{"refresh_token": " r "}); err != nil || source != "r" {
		t.Fatalf("oauth refresh 优先: %q %v", source, err)
	}
	if source, err := requiredCredentialSource("oauth", map[string]any{"access_token": "a"}); err != nil || source != "a" {
		t.Fatalf("oauth access 回退: %q %v", source, err)
	}
	if _, err := requiredCredentialSource("oauth", map[string]any{}); err == nil {
		t.Fatalf("空 oauth 凭据必须报错")
	}
	if source, err := requiredCredentialSource("api_key", map[string]any{"api_key": "k"}); err != nil || source != "k" {
		t.Fatalf("api_key: %q %v", source, err)
	}
	if _, err := requiredCredentialSource("api_key", map[string]any{}); err == nil || err.Error() != "API Key 不能为空" {
		t.Fatalf("空 api_key: %v", err)
	}
	if _, err := requiredCredentialSource("google_oauth", map[string]any{}); err == nil {
		t.Fatalf("空 google_oauth 必须报错")
	}
	if source, err := requiredCredentialSource("unknown", map[string]any{"refresh_token": "r"}); err != nil || source != "r" {
		t.Fatalf("unknown pick: %q %v", source, err)
	}
	if _, err := requiredCredentialSource("unknown", map[string]any{}); err == nil {
		t.Fatalf("unknown 空凭据必须报错")
	}
}

// ---------------------------------------------------------------------------
// providers.go
// ---------------------------------------------------------------------------

// TestWCProviderPlanAdapters：plan 的 rotatable/revisionMessage/safePatch。
func TestWCProviderPlanAdapters(t *testing.T) {
	plan := providerPlan{providerCode: "gpt", accountType: "oauth", revisionConflictMessage: "专用"}
	if !plan.rotatable(&rotationAccount{ProviderCode: "gpt", Type: "oauth", ProtocolCode: "openai", ProtocolVersion: "v1"}) {
		t.Fatalf("rotatable 通过")
	}
	if plan.rotatable(&rotationAccount{ProviderCode: "x", Type: "oauth", ProtocolCode: "openai", ProtocolVersion: "v1"}) {
		t.Fatalf("供应商不符必须拒绝")
	}
	if plan.rotatable(&rotationAccount{ProviderCode: "gpt", Type: "google_oauth"}) {
		t.Fatalf("类型不符必须拒绝")
	}
	if plan.rotatable(&rotationAccount{ProviderCode: "gpt", Type: "oauth", ProtocolCode: "gemini", ProtocolVersion: "v1"}) {
		t.Fatalf("协议不符必须拒绝")
	}
	grok := grokPlan()
	if !grok.rotatable(&rotationAccount{ProviderCode: "xai", Type: "oauth", ProviderProtocolProfileID: ProfileXAIOpenAIV1}) {
		t.Fatalf("grok pin 通过")
	}
	if grok.rotatable(&rotationAccount{ProviderCode: "xai", Type: "oauth", ProviderProtocolProfileID: "other"}) {
		t.Fatalf("grok pin 不符必须拒绝")
	}
	if plan.revisionMessage("fallback") != "专用" || grok.revisionMessage("fallback") != "fallback" {
		t.Fatalf("revisionMessage")
	}
	if _, ok := safePatch(plan, map[string]any{}); !ok {
		t.Fatalf("缺 patch 放行")
	}
	if _, ok := safePatch(plan, map[string]any{"credentialsPatch": "x"}); ok {
		t.Fatalf("非对象 patch 必须拒绝")
	}
	merged := mergePatchOpenAI(map[string]any{"a": 1.0, "b": 1.0}, map[string]any{"b": 2.0})
	if merged["a"] != 1.0 || merged["b"] != 2.0 {
		t.Fatalf("mergePatchOpenAI 凭据优先: %v", merged)
	}
	merged = mergePatchLast(map[string]any{"a": 1.0, "b": 1.0}, map[string]any{"b": 2.0})
	if merged["a"] != 1.0 || merged["b"] != 2.0 {
		t.Fatalf("mergePatchLast patch 优先: %v", merged)
	}
	merged = mergeRotationCredentials(map[string]any{"base_url": "https://keep", "k": "v"}, map[string]any{"n": 1.0}, true)
	if merged["base_url"] != "https://keep" || merged["n"] != 1.0 {
		t.Fatalf("mergeRotationCredentials preserve: %v", merged)
	}
	merged = mergeRotationCredentials(map[string]any{"base_url": "https://old"}, map[string]any{"base_url": "https://new"}, false)
	if merged["base_url"] != "https://new" {
		t.Fatalf("mergeRotationCredentials 不保留: %v", merged)
	}
	blocked := &rotationAccount{Status: "error", LastErrorCode: "unexpected"}
	if !isOpenAIBlockedErrorAccount(blocked) {
		t.Fatalf("异常账户必须命中")
	}
	if isOpenAIBlockedErrorAccount(&rotationAccount{Status: "error", LastErrorCode: "oauth_token_refresh_failed"}) {
		t.Fatalf("刷新失败错误不视为 blocked")
	}
	if isOpenAIBlockedErrorAccount(&rotationAccount{Status: "active"}) {
		t.Fatalf("非 error 状态不命中")
	}
	if got := textFrom(map[string]any{"k": "v"}, "k"); got != "v" || textFrom(nil, "k") != "" || textFrom(map[string]any{"k": 1.5}, "k") != "" {
		t.Fatalf("textFrom")
	}
	if got := trim(" x "); got != "x" {
		t.Fatalf("trim: %q", got)
	}
	if !isHTTPURL("https://x.example/a") || isHTTPURL("ftp://x") || isHTTPURL("https://") {
		t.Fatalf("isHTTPURL")
	}
}

// TestWCCredentialsPatchParsers：四家 credentialsPatch schema。
func TestWCCredentialsPatchParsers(t *testing.T) {
	if modes, ok := endpointModes(nil); !ok || modes != nil {
		t.Fatalf("endpointModes nil 放行: %v %v", modes, ok)
	}
	if _, ok := endpointModes("x"); ok {
		t.Fatalf("endpointModes 非数组拒绝")
	}
	if _, ok := endpointModes([]any{1.5}); ok {
		t.Fatalf("endpointModes 非字符串拒绝")
	}
	if modes, ok := endpointModes([]any{" chat_json ", "chat_json"}); !ok || len(modes) != 2 || modes[0] != "chat_json" {
		t.Fatalf("endpointModes: %v %v", modes, ok)
	}
	// openai。
	patch, ok := parseOpenAICredentialsPatch(map[string]any{
		"supported_endpoint_modes": []any{"chat_json"}, "service_tier_override": " priority ",
		"reasoning_effort_override": "high", "error_handling_rules": map[string]any{},
	})
	if !ok || patch["service_tier_override"] != "priority" || patch["reasoning_effort_override"] != "high" {
		t.Fatalf("openai patch: %v %v", patch, ok)
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"supported_endpoint_modes": []any{}}); ok {
		t.Fatalf("空 modes 必须拒绝")
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"service_tier_override": "bogus"}); ok {
		t.Fatalf("tier 枚举必须拒绝")
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"service_tier_override": "  "}); ok {
		t.Fatalf("空 tier 必须拒绝")
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"reasoning_effort_override": "bogus"}); ok {
		t.Fatalf("effort 枚举必须拒绝")
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"unknown": 1}); ok {
		t.Fatalf("openai 未知键必须拒绝")
	}
	// anthropic。
	patch, ok = parseAnthropicCredentialsPatch(map[string]any{"base_url": " https://x ", "quota_recovery_policy": map[string]any{}})
	if !ok || patch["base_url"] != "https://x" {
		t.Fatalf("anthropic patch: %v %v", patch, ok)
	}
	if _, ok := parseAnthropicCredentialsPatch(map[string]any{"base_url": ""}); ok {
		t.Fatalf("anthropic 空 base_url 必须拒绝")
	}
	if _, ok := parseAnthropicCredentialsPatch(map[string]any{"unknown": 1}); ok {
		t.Fatalf("anthropic 未知键必须拒绝")
	}
	// gemini。
	patch, ok = parseGeminiCredentialsPatch(map[string]any{"base_url": "https://g.example", "quota_project_id": " p "})
	if !ok || patch["quota_project_id"] != "p" {
		t.Fatalf("gemini patch: %v %v", patch, ok)
	}
	if _, ok := parseGeminiCredentialsPatch(map[string]any{"base_url": "not-url"}); ok {
		t.Fatalf("gemini base_url 必须 URL")
	}
	if _, ok := parseGeminiCredentialsPatch(map[string]any{"unknown": 1}); ok {
		t.Fatalf("gemini 未知键必须拒绝")
	}
	// grok。
	patch, ok = parseGrokCredentialsPatch(map[string]any{"base_url": " https://g "})
	if !ok || patch["base_url"] != "https://g" {
		t.Fatalf("grok patch: %v %v", patch, ok)
	}
	if _, ok := parseGrokCredentialsPatch(map[string]any{"service_tier_override": "x"}); ok {
		t.Fatalf("grok 不接受 tier 键")
	}
}

// ---------------------------------------------------------------------------
// routes.go 体解析
// ---------------------------------------------------------------------------

// TestWCRouteBodyHelpers：路由层 body 工具的全分支。
func TestWCRouteBodyHelpers(t *testing.T) {
	if !strictBody(map[string]any{"a": 1.0}, []string{"a"}) || strictBody(map[string]any{"b": 1.0}, []string{"a"}) {
		t.Fatalf("strictBody")
	}
	if _, ok := bodyString(map[string]any{}, "k"); ok {
		t.Fatalf("bodyString 缺失")
	}
	if text, ok := requiredTrimmedString(map[string]any{"k": " v "}, "k"); !ok || text != "v" {
		t.Fatalf("requiredTrimmedString: %q %v", text, ok)
	}
	if _, ok := requiredTrimmedString(map[string]any{"k": "  "}, "k"); ok {
		t.Fatalf("requiredTrimmedString 空白拒绝")
	}
	if got := optionalTrimmedText(map[string]any{"k": " v "}, "k"); got != "v" {
		t.Fatalf("optionalTrimmedText: %q", got)
	}
	if value, ok := bodyInt(map[string]any{}, "k", 1); value != nil || !ok {
		t.Fatalf("bodyInt 缺省: %v %v", value, ok)
	}
	if value, ok := bodyInt(map[string]any{"k": "x"}, "k", 1); value != nil || ok {
		t.Fatalf("bodyInt 非数: %v %v", value, ok)
	}
	if value, ok := bodyInt(map[string]any{"k": 0.0}, "k", 1); value != nil || ok {
		t.Fatalf("bodyInt 下限: %v %v", value, ok)
	}
	if value, ok := bodyInt(map[string]any{"k": 5.0}, "k", 1); !ok || *value != 5 {
		t.Fatalf("bodyInt 正常: %v %v", value, ok)
	}
	if _, ok := requiredRevision(map[string]any{}, "k"); ok {
		t.Fatalf("requiredRevision 缺失")
	}
	if _, ok := requiredRevision(map[string]any{"k": 0.0}, "k"); ok {
		t.Fatalf("requiredRevision 下限")
	}
	if revision, ok := requiredRevision(map[string]any{"k": 3.0}, "k"); !ok || revision != 3 {
		t.Fatalf("requiredRevision 正常: %d %v", revision, ok)
	}
	if got := creationStatusValue(map[string]any{"status": " disabled "}, "status"); got != "disabled" {
		t.Fatalf("creationStatusValue: %q", got)
	}
	if got := creationStatusValue(map[string]any{}, "status"); got != "pending_test" {
		t.Fatalf("creationStatusValue 缺省: %q", got)
	}
	if got := creationStatusText(" active "); got != "active" {
		t.Fatalf("creationStatusText: %q", got)
	}
	if got := creationStatusText(1.5); got != "pending_test" {
		t.Fatalf("creationStatusText 非字符串: %q", got)
	}
	if got := creationStatusText("bogus"); got != "pending_test" {
		t.Fatalf("creationStatusText 非法: %q", got)
	}
}

// TestWCParseManagedFields：托管字段 schema 的全分支矩阵。
func TestWCParseManagedFields(t *testing.T) {
	bad := []map[string]any{
		{},
		{"providerProtocolProfileId": "  "},
		{"providerProtocolProfileId": "p", "name": "  "},
		{"providerProtocolProfileId": "p", "concurrencyLimit": 0.0},
		{"providerProtocolProfileId": "p", "concurrencyLimit": "x"},
		{"providerProtocolProfileId": "p", "priority": -1.0},
		{"providerProtocolProfileId": "p", "status": "bogus"},
		{"providerProtocolProfileId": "p", "status": 1.5},
		{"providerProtocolProfileId": "p", "superPriorityEnabled": "x"},
		{"providerProtocolProfileId": "p", "fallbackEnabled": "x"},
		{"providerProtocolProfileId": "p", "supportedModels": []any{}},
		{"providerProtocolProfileId": "p", "supportedModels": []any{""}},
		{"providerProtocolProfileId": "p", "supportedModels": "x"},
		{"providerProtocolProfileId": "p", "healthCheckModel": "  "},
		{"providerProtocolProfileId": "p", "healthCheckEndpointMode": "bogus"},
		{"providerProtocolProfileId": "p", "temporaryUnavailableContinuousProbeEnabled": "x"},
		{"providerProtocolProfileId": "p", "modelMappings": "x"},
		{"providerProtocolProfileId": "p", "modelMappings": []any{"x"}},
		{"providerProtocolProfileId": "p", "modelMappings": []any{map[string]any{"sourceModel": "a"}}},
		{"providerProtocolProfileId": "p", "tags": []any{""}},
		{"providerProtocolProfileId": "p", "tags": "x"},
		{"providerProtocolProfileId": "p", "accountExpiresAt": 1.5},
	}
	for index, body := range bad {
		if _, ok := parseManagedFields(body); ok {
			t.Fatalf("case %d 必须拒绝: %v", index, body)
		}
	}
	good, ok := parseManagedFields(map[string]any{
		"providerProtocolProfileId": " p ", "name": " n ", "groupId": " g ",
		"concurrencyLimit": 3.0, "priority": 1.0, "status": "disabled",
		"superPriorityEnabled": true, "fallbackEnabled": false,
		"supportedModels":                            []any{" m1 "},
		"healthCheckModel":                           " hm ",
		"healthCheckEndpointMode":                    "chat_json",
		"temporaryUnavailableContinuousProbeEnabled": true,
		"modelMappings":                              []any{map[string]any{"sourceModel": " a ", "sourceEndpointFamily": " chat ", "upstreamModel": " b ", "upstreamEndpointFamily": " chat ", "enabled": false}},
		"tags":                                       []any{" t "},
		"proxyProfileId":                             " pp ",
		"accountExpiresAt":                           "2030-01-01T00:00:00Z",
		"availabilitySchedule":                       map[string]any{"enabled": true},
		"notes":                                      " note ",
	})
	if !ok || good.ProviderProtocolProfileID != "p" || good.Name == nil || good.GroupID == nil ||
		good.ConcurrencyLimit == nil || good.Priority == nil || good.Status != "disabled" ||
		good.SuperPriorityEnabled == nil || good.FallbackEnabled == nil || len(good.SupportedModels) != 1 ||
		good.HealthCheckModel == nil || good.HealthCheckEndpointMode == nil ||
		len(good.ModelMappings) != 1 || good.ModelMappings[0].SourceModel != "a" || good.ModelMappings[0].Enabled == nil ||
		len(good.Tags) != 1 || good.ProxyProfileID == nil || good.AccountExpiresAt == nil ||
		good.AvailabilitySchedule == nil || good.Notes == nil {
		t.Fatalf("全字段解析: %+v %v", good, ok)
	}
}

// TestWCRoutesMisc：accountName/createLogContext/actorResolver。
func TestWCRoutesMisc(t *testing.T) {
	if got := accountName(wcStrPtr(" 自定义 "), &tokenOutcome{Name: "a@b.c"}, openAIPlan()); got != "自定义" {
		t.Fatalf("显式名优先: %q", got)
	}
	if got := accountName(nil, &tokenOutcome{Name: " a@b.c "}, openAIPlan()); got != "a@b.c" {
		t.Fatalf("email 回退: %q", got)
	}
	if got := accountName(nil, nil, openAIPlan()); got != "OpenAI OAuth Account" {
		t.Fatalf("默认名: %q", got)
	}
	if got := accountName(nil, &tokenOutcome{Name: "x"}, geminiPlan()); got != "Gemini OAuth Account" {
		t.Fatalf("gemini 不走 email 回退: %q", got)
	}
	operationKey, summaryPrefix := openAIPlan().createLogContext("授权码")
	if operationKey != "openai_oauth.create_from_code" || summaryPrefix != "通过授权码创建 OpenAI OAuth 账户" {
		t.Fatalf("createLogContext code: %q %q", operationKey, summaryPrefix)
	}
	operationKey, _ = grokPlan().createLogContext("刷新令牌")
	if operationKey != "grok_oauth.create_from_refresh_token" {
		t.Fatalf("createLogContext refresh: %q", operationKey)
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	if got := actorResolver(request); got != "anonymous" {
		t.Fatalf("匿名 actor: %q", got)
	}
	// scopeFor 的 admin 过滤与 all 丢弃（需要注入 auth context）。
	withAuth := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/?systemAccountId=alice", nil)
		return request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: "viewer"}))
	}
	if scope := scopeFor(withAuth(), false); scope.FilterID != "alice" || !scope.IsAdmin || scope.ViewerID != "viewer" {
		t.Fatalf("admin scope: %+v", scope)
	}
	allRequest := httptest.NewRequest(http.MethodPost, "/?systemAccountId=all", nil)
	allRequest = allRequest.WithContext(authsys.WithAuthContext(allRequest.Context(), &authsys.AuthContext{SystemAccountID: "viewer"}))
	if scope := scopeFor(allRequest, false); scope.FilterID != "" {
		t.Fatalf("all 过滤丢弃: %+v", scope)
	}
	selfRequest := httptest.NewRequest(http.MethodPost, "/", nil)
	selfRequest = selfRequest.WithContext(authsys.WithAuthContext(selfRequest.Context(), &authsys.AuthContext{SystemAccountID: "self"}))
	if scope := scopeFor(selfRequest, true); scope.ViewerID != "self" || scope.IsAdmin {
		t.Fatalf("selfOnly scope: %+v", scope)
	}
	// actorResolver：有 auth 时返回账号 ID。
	if got := actorResolver(selfRequest); got != "self" {
		t.Fatalf("auth actor: %q", got)
	}
	// scopeQueryOK：空白过滤拒绝。
	request = httptest.NewRequest(http.MethodPost, "/?systemAccountId=%20", nil)
	if scopeQueryOK(request) {
		t.Fatalf("空白 systemAccountId 必须拒绝")
	}
	request = httptest.NewRequest(http.MethodPost, "/", nil)
	if !scopeQueryOK(request) {
		t.Fatalf("无过滤放行")
	}
}

// TestWCGrokSSOImportHelpers：SSO 导入名称/到期分支。
func TestWCGrokSSOImportHelpers(t *testing.T) {
	if got := grokSSOImportAccountName(nil, &tokenOutcome{Name: " a@x "}, 1, 1); got != "a@x" {
		t.Fatalf("email 回退: %q", got)
	}
	if got := grokSSOImportAccountName(wcStrPtr(" 名 "), nil, 1, 1); got != "名" {
		t.Fatalf("显式名: %q", got)
	}
	if got := grokSSOImportAccountName(nil, nil, 1, 1); got != "Grok OAuth Account" {
		t.Fatalf("默认名: %q", got)
	}
	if got := grokSSOImportAccountName(wcStrPtr("名"), nil, 2, 3); got != "名 #2" {
		t.Fatalf("多 token 后缀: %q", got)
	}
	// 无 refresh_token：到期钳制到 access token expires_at。
	outcome := &tokenOutcome{Credentials: map[string]any{"expires_at": "2030-01-02T00:00:00Z"}}
	expires, err := grokSSOImportAccountExpiresAt(nil, outcome)
	if err != nil || expires == nil || *expires != "2030-01-02T00:00:00Z" {
		t.Fatalf("到期钳制: %v %v", expires, err)
	}
	expires, err = grokSSOImportAccountExpiresAt(wcStrPtr("2031-01-01T00:00:00Z"), outcome)
	if err != nil || *expires != "2030-01-02T00:00:00Z" {
		t.Fatalf("请求到期晚于 token 时钳制: %v %v", expires, err)
	}
	expires, err = grokSSOImportAccountExpiresAt(wcStrPtr("2029-01-01T00:00:00Z"), outcome)
	if err != nil || *expires != "2029-01-01T00:00:00Z" {
		t.Fatalf("请求到期早于 token 时保留: %v %v", expires, err)
	}
	if _, err := grokSSOImportAccountExpiresAt(wcStrPtr("bad"), outcome); err == nil {
		t.Fatalf("非法请求到期必须报错")
	}
	bad := &tokenOutcome{Credentials: map[string]any{"expires_at": "bad"}}
	if _, err := grokSSOImportAccountExpiresAt(nil, bad); err == nil {
		t.Fatalf("非法 token 到期必须报错")
	}
	// 有 refresh_token：直接透传请求值。
	withRefresh := &tokenOutcome{Credentials: map[string]any{"refresh_token": "r"}}
	expires, err = grokSSOImportAccountExpiresAt(nil, withRefresh)
	if err != nil || expires != nil {
		t.Fatalf("有 refresh 不钳制: %v %v", expires, err)
	}
	if _, ok := rfc3339Millis("bad"); ok {
		t.Fatalf("rfc3339Millis 非法")
	}
	if millis, ok := rfc3339Millis("2026-01-02T03:04:05.678Z"); !ok || millis != 1767323045678 {
		t.Fatalf("rfc3339Millis: %d %v", millis, ok)
	}
}

// ---------------------------------------------------------------------------
// sessionstore.go
// ---------------------------------------------------------------------------

// TestWCSessionStore：set/get 过期与 compareDelete 单次消费语义。
func TestWCSessionStore(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	store := newSessionStore(func() time.Time { return now })
	type payload struct {
		State string
	}
	store.set("ns", "s1", payload{State: "a"}, oauthSessionTTL)
	if store.get("ns", "s1") == nil {
		t.Fatalf("未过期必须可读")
	}
	if store.get("ns", "missing") != nil {
		t.Fatalf("缺失必须为 nil")
	}
	// ttl<=0 钳制为 1ms → 立即过期。
	store.set("ns", "s2", payload{State: "b"}, 0)
	now = now.Add(time.Second)
	if store.get("ns", "s2") != nil {
		t.Fatalf("过期必须为 nil")
	}
	// compareDelete：值匹配才删除。
	store.set("ns", "s3", payload{State: "c"}, oauthSessionTTL)
	if !store.compareDelete("ns", "s3", payload{State: "c"}) {
		t.Fatalf("匹配必须删除成功")
	}
	if store.get("ns", "s3") != nil {
		t.Fatalf("删除后必须不可读")
	}
	// 不匹配 → 保留。
	store.set("ns", "s4", payload{State: "d"}, oauthSessionTTL)
	if store.compareDelete("ns", "s4", payload{State: "x"}) {
		t.Fatalf("不匹配必须失败")
	}
	if store.get("ns", "s4") == nil {
		t.Fatalf("不匹配必须保留")
	}
	// 过期条目 compareDelete 清理并失败。
	store.set("ns", "s5", payload{State: "e"}, oauthSessionTTL)
	now = now.Add(time.Hour)
	if store.compareDelete("ns", "s5", payload{State: "e"}) {
		t.Fatalf("过期 compareDelete 必须失败")
	}
}

// ---------------------------------------------------------------------------
// invalidation.go
// ---------------------------------------------------------------------------

// wcRecordingInvalidator 记录调用并可注入错误。
type wcRecordingInvalidator struct {
	lookupErr, runtimeErr, validationErr error
	lookups, runtime, validation         []string
}

func (w *wcRecordingInvalidator) InvalidateAccountLookup(id string) error {
	w.lookups = append(w.lookups, id)
	return w.lookupErr
}
func (w *wcRecordingInvalidator) InvalidateRuntime(reason string) error {
	w.runtime = append(w.runtime, reason)
	return w.runtimeErr
}
func (w *wcRecordingInvalidator) InvalidateAPIKeyValidation(reason string) error {
	w.validation = append(w.validation, reason)
	return w.validationErr
}

// TestWCCircuitIdentity：凭据身份子集提取与变更判定。
func TestWCCircuitIdentity(t *testing.T) {
	if identity := circuitCredentialOwnerIdentity(nil); len(identity) != 0 {
		t.Fatalf("nil 凭据身份为空")
	}
	credentials := map[string]any{"access_token": "a", "notes": "非身份字段"}
	identity := circuitCredentialOwnerIdentity(credentials)
	if _, hasToken := identity["access_token"]; !hasToken {
		t.Fatalf("身份必须包含 access_token")
	}
	if _, hasNotes := identity["notes"]; hasNotes {
		t.Fatalf("身份不得包含非身份字段")
	}
	if circuitCredentialIdentityChanged(map[string]any{"access_token": "a"}, map[string]any{"access_token": "b"}) != true {
		t.Fatalf("token 变化必须判变")
	}
	if circuitCredentialIdentityChanged(map[string]any{"access_token": "a", "notes": "x"}, map[string]any{"access_token": "a", "notes": "y"}) != false {
		t.Fatalf("非身份字段变化不判变")
	}
}

// TestWCFinishRotationSideEffects：三通道通知与错误吞噬。
func TestWCFinishRotationSideEffects(t *testing.T) {
	store := &Store{}
	store.finishRotationSideEffects(nil) // nil 容错
	store.finishRotationSideEffects(&RotationResult{Changed: false})

	invalidator := &wcRecordingInvalidator{lookupErr: errors.New("lookup down"), runtimeErr: errors.New("runtime down")}
	store.invalidator = invalidator
	store.finishRotationSideEffects(&RotationResult{ID: "acc1", Changed: true})
	if len(invalidator.lookups) != 1 || invalidator.lookups[0] != "acc1" {
		t.Fatalf("lookup 通道: %v", invalidator.lookups)
	}
	if len(invalidator.runtime) != 1 || invalidator.runtime[0] != RotationRuntimeInvalidationReason {
		t.Fatalf("runtime 通道: %v", invalidator.runtime)
	}
	if len(invalidator.validation) != 1 || invalidator.validation[0] != RotationRuntimeInvalidationReason {
		t.Fatalf("validation 通道: %v", invalidator.validation)
	}
}

// TestWCOptionNilSafety：With* 选项对 nil 协作者保持静默。
func TestWCOptionNilSafety(t *testing.T) {
	store := &Store{}
	WithCacheInvalidator(nil)(store)
	WithDispatchRevisionAdvancer(nil)(store)
	WithSSODeviceTransport(nil)(store)
	WithSSOSleep(nil)(store)
	if store.invalidator != nil || store.revisionAdvancer != nil {
		t.Fatalf("nil 选项不得写入")
	}
}

// ---------------------------------------------------------------------------
// openai.go / anthropic.go 回调解析
// ---------------------------------------------------------------------------

// TestWCExtractOpenAICodeAndState：回调解析全分支。
func TestWCExtractOpenAICodeAndState(t *testing.T) {
	if _, _, err := extractOpenAICodeAndState(""); err == nil || err.Error() != "回调 URL 不能为空" {
		t.Fatalf("空回调: %v", err)
	}
	if _, _, err := extractOpenAICodeAndState("https://cb?error=access_denied&error_description=denied"); err == nil || err.Error() != "denied" {
		t.Fatalf("error 转发: %v", err)
	}
	code, state, err := extractOpenAICodeAndState("https://cb?code=c1&state=s1")
	if err != nil || code != "c1" || state != "s1" {
		t.Fatalf("URL 形式: %q %q %v", code, state, err)
	}
	// 行为存疑：fragment 形式解析恒失败——normalizeText 只接受 string，
	// 而 fragment["code"] 取出的是 url.Values 切片（疑似应使用 Get），
	// Node 侧 new URLSearchParams(fragment).get('code') 可解析。按当前实际
	// 行为断言，待主代理裁定。
	code, state, err = extractOpenAICodeAndState("https://cb#code=c2&state=s2")
	if err == nil || code != "" || state != "" {
		t.Fatalf("fragment 形式（行为存疑）应按当前实现报错: %q %q %v", code, state, err)
	}
	code, state, err = extractOpenAICodeAndState("c3#s3")
	if err != nil || code != "c3" || state != "s3" {
		t.Fatalf("code#state 形式: %q %q %v", code, state, err)
	}
	// 行为存疑：裸 query 形式与 fragment 同因（normalizeText(query["code"])
	// 取到 []string）恒失败，Node 的 URLSearchParams 形式可解析。按当前实际
	// 行为断言。
	code, state, err = extractOpenAICodeAndState("?code=c4&state=s4")
	if err == nil || code != "" || state != "" {
		t.Fatalf("裸 query 形式（行为存疑）应按当前实现报错: %q %q %v", code, state, err)
	}
	if _, _, err := extractOpenAICodeAndState("https://cb?code=c5"); err == nil {
		t.Fatalf("缺 state 必须报错")
	}
	if _, _, err := extractOpenAICodeAndState("only-code"); err == nil {
		t.Fatalf("无 state 裸码必须报错")
	}
}

// TestWCExtractAnthropicCodeAndState：anthropic 回调全分支。
func TestWCExtractAnthropicCodeAndState(t *testing.T) {
	if _, err := extractAnthropicCodeAndState(""); err == nil {
		t.Fatalf("空回调必须报错")
	}
	if _, err := extractAnthropicCodeAndState("https://cb?error=denied"); err == nil {
		t.Fatalf("error 转发必须报错")
	}
	authorization, err := extractAnthropicCodeAndState("https://cb?code=c1&state=s1")
	if err != nil || authorization.code != "c1" || authorization.state != "s1" || !authorization.requiresState {
		t.Fatalf("URL 形式: %+v %v", authorization, err)
	}
	authorization, err = extractAnthropicCodeAndState("bare-code")
	if err != nil || authorization.code != "bare-code" || authorization.requiresState {
		t.Fatalf("裸码无需 state: %+v %v", authorization, err)
	}
	authorization, err = extractAnthropicCodeAndState("c2#s2")
	if err != nil || authorization.code != "c2" || authorization.state != "s2" || !authorization.requiresState {
		t.Fatalf("code#state 形式: %+v %v", authorization, err)
	}
	authorization, err = extractAnthropicCodeAndState("code=c3&state=s3")
	if err != nil || authorization.code != "c3" || authorization.state != "s3" {
		t.Fatalf("query 形式: %+v %v", authorization, err)
	}
	if _, err := extractAnthropicCodeAndState("https://cb?code=c4"); err == nil {
		t.Fatalf("URL 缺 state 必须报错")
	}
}

// TestWCBuildCredentialsFallback：四家凭据构建的回退分支。
func TestWCBuildCredentialsFallback(t *testing.T) {
	info := &openAITokenInfo{AccessToken: "a", ExpiresAt: "e", ClientID: "c", Email: "x@y", AccountID: "acc", ChatGPTUserID: "u", PlanType: "plus"}
	credentials := buildOpenAIOAuthCredentials(info, "")
	if credentials["email"] != "x@y" || credentials["plan_type"] != "plus" {
		t.Fatalf("openai credentials: %v", credentials)
	}
	if credentials := buildOpenAIOAuthCredentials(&openAITokenInfo{AccessToken: "a"}, "fb-refresh"); credentials["refresh_token"] != "fb-refresh" {
		t.Fatalf("openai refresh 回退: %v", credentials)
	}
	anthroInfo := &anthropicTokenInfo{AccessToken: "a", ClientID: "c", Email: "e", AccountID: "acc", OrganizationID: "org", Scope: "s", TokenType: "bearer"}
	credentials = buildAnthropicOAuthCredentials(anthroInfo, "")
	if credentials["organization_id"] != "org" || credentials["token_type"] != "bearer" {
		t.Fatalf("anthropic credentials: %v", credentials)
	}
	credentials = buildAnthropicOAuthCredentials(&anthropicTokenInfo{AccessToken: "a"}, "fb")
	if credentials["refresh_token"] != "fb" {
		t.Fatalf("anthropic refresh 回退: %v", credentials)
	}
	grokInfo := &grokTokenInfo{AccessToken: "a", ExpiresAt: "e", ClientID: "c", IDToken: "id", Scope: "s", Email: "e", Subject: "sub", TeamID: "team", SubscriptionTier: "tier", EntitlementStatus: "active"}
	credentials = buildGrokOAuthCredentials(grokInfo, "")
	if credentials["sub"] != "sub" || credentials["team_id"] != "team" || credentials["id_token"] != "id" {
		t.Fatalf("grok credentials: %v", credentials)
	}
	credentials = buildGrokOAuthCredentials(&grokTokenInfo{AccessToken: "a"}, "fb")
	if credentials["refresh_token"] != "fb" {
		t.Fatalf("grok refresh 回退: %v", credentials)
	}
}

// TestWCGrokTokenHelpers：JWT 合并/常量比较/entitlement 判定。
func TestWCGrokTokenHelpers(t *testing.T) {
	idToken := fakeJWT(map[string]any{"email": "id@x", "sub": ""})
	accessToken := fakeJWT(map[string]any{"email": "access@x", "sub": "sub1"})
	claims := mergeJWTClaims(idToken, accessToken)
	if claims["email"] != "id@x" {
		t.Fatalf("先到 token 优先: %v", claims)
	}
	if claims["sub"] != "sub1" {
		t.Fatalf("空值让位: %v", claims)
	}
	if !constantTimeEqual("abc", "abc") || constantTimeEqual("abc", "abd") || constantTimeEqual("abc", "ab") {
		t.Fatalf("constantTimeEqual")
	}
	if !hasExplicitEntitlementDenial(map[string]any{"error": "access_denied"}, "") {
		t.Fatalf("error 字段判定")
	}
	if !hasExplicitEntitlementDenial(map[string]any{}, "No Active Grok Subscription") {
		t.Fatalf("body 文案判定")
	}
	if hasExplicitEntitlementDenial(map[string]any{"error": "other"}, "fine") {
		t.Fatalf("无否认不得命中")
	}
	info := toGrokTokenInfo(grokRawToken{AccessToken: "a", ExpiresIn: -1}, "", func() int64 { return 1000 })
	if info.ExpiresIn != grokDefaultTokenTTL || info.TokenType != "Bearer" || info.ClientID != GrokOAuthClientID || info.ExpiresAt == "" {
		t.Fatalf("toGrokTokenInfo 缺省: %+v", info)
	}
}
