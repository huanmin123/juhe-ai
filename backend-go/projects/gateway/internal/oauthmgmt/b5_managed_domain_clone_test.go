// B5 托管 OAuth 建号 UI 400 修复的永久固化测试：
//   - 全键载荷回归（B5 的门）：create-from-* 三端点 strict-body 收
//     capabilityDomain / cloneSourceId；不帷新键的载荷行为与旧形状逐项一致；
//   - 媒体域建号：gemini google_oauth × video/audio → 201，账户行域、派生
//     连接 modes、健康检查形态（video_create/audio_speech）与凭据声明集
//     （写面按域种子）全链路落库正确；
//   - 拒绝面：非法域值 400「能力域无效」（写面统一校验透传）、组合缺席
//     （gpt oauth × video、gemini google_oauth × image）400；
//   - 克隆语义：同域克隆 201（连接按注册表派生，不做连接事实拷贝）、跨域
//     克隆 400「克隆账户能力域必须与源账户一致」、源不存在 404。
//
// 媒体域夹具：seedOAuthCatalog 的 models 行只有 chat 形态，媒体用例就地补带
// 媒体 endpoint modes 的声明行（firstSupportedModelDeclaringMode 与写面
// finalizeAccountHealthCheckTrio 都要求检查模型声明检查形态）。
package oauthmgmt

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// b5SeedGeminiMediaModels 补 gemini 媒体域 models 声明行（vendor 行由
// seedOAuthCatalog 预置）。
func b5SeedGeminiMediaModels(t *testing.T, env *testEnv) {
	t.Helper()
	for _, model := range []struct {
		name, modes string
	}{
		{"gemini-media-video", `["video_create","video_get","video_content","video_cancel"]`},
		{"gemini-media-audio", `["audio_speech"]`},
	} {
		env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope,
			status, supported_endpoint_modes_json, created_at, updated_at)
			VALUES (?, 'gemini', ?, ?, 'builtin', 'active', ?, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
			"mod-gemini-"+model.name, model.name, model.name, model.modes)
	}
}

// b5AccountRow 读账户行的域/健康检查三件套断言素材。
type b5AccountRow struct {
	domain      string
	healthMode  string
	healthModel string
}

func b5ReadAccountRow(t *testing.T, env *testEnv, accountID string) b5AccountRow {
	t.Helper()
	var row b5AccountRow
	if err := env.db.QueryRow(fixtureBind(`SELECT capability_domain, health_check_endpoint_mode, health_check_model
		FROM accounts WHERE id = ?`), accountID).Scan(&row.domain, &row.healthMode, &row.healthModel); err != nil {
		t.Fatal(err)
	}
	return row
}

// b5ConnectionModes 读账户单连接的 endpoint mode 全集（ASCII 升序）。
func b5ConnectionModes(t *testing.T, env *testEnv, accountID string) []string {
	t.Helper()
	rows, err := env.db.Query(fixtureBind(`SELECT account_endpoint_modes.endpoint_mode
		FROM account_endpoint_modes
		INNER JOIN account_connections ON account_connections.id = account_endpoint_modes.connection_id
		WHERE account_connections.account_id = ?
		ORDER BY account_endpoint_modes.endpoint_mode ASC`), accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	modes := []string{}
	for rows.Next() {
		var mode string
		if err := rows.Scan(&mode); err != nil {
			t.Fatal(err)
		}
		modes = append(modes, mode)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return modes
}

// b5WantModes 渲染期望 mode 集的 SQL 断言用切片比较文案。
func b5JoinModes(modes []string) string {
	out := ""
	for index, mode := range modes {
		if index > 0 {
			out += ","
		}
		out += mode
	}
	return out
}

// TestB5ManagedOAuthCapabilityDomainKeys 是 B5 的门：托管建户三端点 strict-body
// 收 capabilityDomain / cloneSourceId，帷新键载荷 201 且账户行 capability_domain
// 正确；不帷新键载荷 201 且派生形状（连接 preset/modes、健康检查形态）与旧形状
// 完全一致。
func TestB5ManagedOAuthCapabilityDomainKeys(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = staticToken(openAITokenPayload("b5-access"))

	// 带全量新键（capabilityDomain=chat + cloneSourceId 空串=普通新建）→ 201。
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"b5-refresh-full","name":"B5 Full Keys","capabilityDomain":"chat","cloneSourceId":""}`)
	if code != http.StatusCreated {
		t.Fatalf("full-keys create: %d %v", code, created)
	}
	fullKeysID := dataMap(t, created)["id"].(string)
	if row := b5ReadAccountRow(t, env, fullKeysID); row.domain != "chat" {
		t.Fatalf("full-keys capability_domain: %+v", row)
	}
	// cloneSourceId 空串不得触发写面克隆连接拷贝：连接行仍是注册表派生单行。
	if modes := b5ConnectionModes(t, env, fullKeysID); b5JoinModes(modes) != "responses_json,responses_sse" {
		t.Fatalf("full-keys derived modes: %v", modes)
	}

	// 不帷新键 → 201 且派生形状与旧形状一致（chat 缺省语义不变）。
	code, legacy := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"b5-refresh-legacy","name":"B5 Legacy Shape"}`)
	if code != http.StatusCreated {
		t.Fatalf("legacy create: %d %v", code, legacy)
	}
	legacyID := dataMap(t, legacy)["id"].(string)
	if row := b5ReadAccountRow(t, env, legacyID); row.domain != "chat" || row.healthMode != "responses_sse" {
		t.Fatalf("legacy shape: %+v", row)
	}
	if modes := b5ConnectionModes(t, env, legacyID); b5JoinModes(modes) != "responses_json,responses_sse" {
		t.Fatalf("legacy derived modes: %v", modes)
	}
}

// TestB5ManagedOAuthMediaDomains：gemini google_oauth × video/audio 托管建号
// 全链路——账户行域、派生连接 modes、健康检查形态（域缺省 ∈ 派生启用 modes）
// 与凭据声明集（媒体域不继承 chat 形态声明，写面按域种子）。
func TestB5ManagedOAuthMediaDomains(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	b5SeedGeminiMediaModels(t, env)
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != GeminiOAuthTokenURL {
			return http.StatusNotFound, `{}`
		}
		return http.StatusOK, `{"access_token":"b5-gem-access","refresh_token":"b5-gem-refresh","expires_in":3600,"scope":"https://www.googleapis.com/auth/cloud-platform","token_type":"Bearer"}`
	}

	// video 域：201，域/连接 modes/健康检查形态/凭据声明全链路 video。
	code, video := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"b5-gem-video","projectId":"proj-b5","name":"B5 Gemini Video","capabilityDomain":"video","supportedModels":["gemini-media-video"]}`)
	if code != http.StatusCreated {
		t.Fatalf("gemini video create: %d %v", code, video)
	}
	videoID := dataMap(t, video)["id"].(string)
	if row := b5ReadAccountRow(t, env, videoID); row.domain != "video" || row.healthMode != "video_create" || row.healthModel != "gemini-media-video" {
		t.Fatalf("video row: %+v", row)
	}
	if modes := b5ConnectionModes(t, env, videoID); b5JoinModes(modes) != "video_cancel,video_content,video_create,video_get" {
		t.Fatalf("video derived modes: %v", modes)
	}
	videoCredentials := env.accountCredentials(t, videoID)
	videoDeclared, ok := videoCredentials["supported_endpoint_modes"].([]any)
	if !ok || len(videoDeclared) != 4 {
		t.Fatalf("video declared modes: %v", videoCredentials["supported_endpoint_modes"])
	}
	for _, mode := range []string{"video_create", "video_get", "video_content", "video_cancel"} {
		found := false
		for _, declared := range videoDeclared {
			if declared == mode {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("video declared modes missing %s: %v", mode, videoDeclared)
		}
	}

	// audio 域：201，健康检查形态 audio_speech（域缺省值 ∈ 派生启用 modes）。
	code, audio := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"b5-gem-audio","projectId":"proj-b5","name":"B5 Gemini Audio","capabilityDomain":"audio","supportedModels":["gemini-media-audio"]}`)
	if code != http.StatusCreated {
		t.Fatalf("gemini audio create: %d %v", code, audio)
	}
	audioID := dataMap(t, audio)["id"].(string)
	if row := b5ReadAccountRow(t, env, audioID); row.domain != "audio" || row.healthMode != "audio_speech" || row.healthModel != "gemini-media-audio" {
		t.Fatalf("audio row: %+v", row)
	}
	if modes := b5ConnectionModes(t, env, audioID); b5JoinModes(modes) != "audio_speech" {
		t.Fatalf("audio derived modes: %v", modes)
	}
	audioDeclared, ok := env.accountCredentials(t, audioID)["supported_endpoint_modes"].([]any)
	if !ok || len(audioDeclared) != 1 || audioDeclared[0] != "audio_speech" {
		t.Fatalf("audio declared modes: %v", audioDeclared)
	}
}

// TestB5ManagedOAuthRejections：非法 capabilityDomain 值（写面「能力域无效」
// 透传 400）、非字符串类型（路由层参数无效 400）与组合缺席（gpt oauth ×
// video、gemini google_oauth × image，注册表组合门拒绝 400）。
func TestB5ManagedOAuthRejections(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	b5SeedGeminiMediaModels(t, env)
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL == OpenAIOAuthTokenURL {
			return http.StatusOK, openAITokenPayload("b5-reject-access")
		}
		return http.StatusOK, `{"access_token":"b5-gem-reject","refresh_token":"b5-gem-reject-refresh","expires_in":3600,"token_type":"Bearer"}`
	}

	// 非法域值：路由层透传（契约 §2 不重复实现校验）。派生门先于写面域校验
	// 执行，非法值按组合拒绝 400（connection_capability_unsupported ...），
	// 与写面「能力域无效」同为确定性 400 面。
	code, invalid := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"b5-reject-domain","name":"B5 Invalid Domain","capabilityDomain":"bogus"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("invalid domain: %d %v", code, invalid)
	}
	if message, _ := invalid["message"].(string); message != "能力域无效" && !strings.Contains(message, "connection_capability_unsupported") {
		t.Fatalf("invalid domain message: %v", invalid)
	}

	// 非字符串域值：managed 解析类型门 400。
	code, mistyped := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"b5-reject-type","capabilityDomain":7}`)
	if code != http.StatusBadRequest || mistyped["message"] != "OpenAI 刷新令牌参数无效" {
		t.Fatalf("mistyped domain: %d %v", code, mistyped)
	}

	// 组合缺席：gpt oauth × video（组合行只有 chat）→ 400 组合拒绝。
	code, gptVideo := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"b5-reject-gpt-video","name":"B5 GPT Video","capabilityDomain":"video"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("gpt oauth video: %d %v", code, gptVideo)
	}
	if message, _ := gptVideo["message"].(string); !strings.Contains(message, "connection_capability_unsupported") {
		t.Fatalf("gpt oauth video message: %v", gptVideo)
	}

	// 组合缺席：gemini google_oauth × image（组合行 chat/video/audio，无 image）
	// → 400 组合拒绝。
	code, geminiImage := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"b5-reject-gem-image","projectId":"proj-b5","name":"B5 Gemini Image","capabilityDomain":"image"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("gemini image: %d %v", code, geminiImage)
	}
	if message, _ := geminiImage["message"].(string); !strings.Contains(message, "connection_capability_unsupported") {
		t.Fatalf("gemini image message: %v", geminiImage)
	}
}

// TestB5ManagedOAuthCloneSource：cloneSourceId 克隆语义——同域克隆 201 且连接
// 按注册表派生（不做克隆连接事实拷贝）、跨域克隆 400「克隆账户能力域必须与
// 源账户一致」（克隆预检先于组合派生）、源不存在 404「账户不存在」。
func TestB5ManagedOAuthCloneSource(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = staticToken(openAITokenPayload("b5-clone-access"))

	// chat 源账户。
	code, sourceCreated := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"b5-clone-source","name":"B5 Clone Source"}`)
	if code != http.StatusCreated {
		t.Fatalf("clone source create: %d %v", code, sourceCreated)
	}
	sourceID := dataMap(t, sourceCreated)["id"].(string)

	// 同域克隆（chat 源 + chat 目标）→ 201，派生连接与源一致（同 provider/
	// type/域 时注册表派生结果与源一致，无需连接事实拷贝）。
	code, sameClone := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		fmt.Sprintf(`{"refreshToken":"b5-clone-same","name":"B5 Clone Same","cloneSourceId":%q,"capabilityDomain":"chat"}`, sourceID))
	if code != http.StatusCreated {
		t.Fatalf("same-domain clone: %d %v", code, sameClone)
	}
	sameID := dataMap(t, sameClone)["id"].(string)
	if row := b5ReadAccountRow(t, env, sameID); row.domain != "chat" || row.healthMode != "responses_sse" {
		t.Fatalf("same-domain clone row: %+v", row)
	}
	if modes := b5ConnectionModes(t, env, sameID); b5JoinModes(modes) != "responses_json,responses_sse" {
		t.Fatalf("same-domain clone derived modes: %v", modes)
	}

	// 跨域克隆（chat 源 + video 目标）→ 400 克隆域断言（先于组合派生触发，
	// 不落到 connection_capability_unsupported）。
	code, crossClone := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		fmt.Sprintf(`{"refreshToken":"b5-clone-cross","name":"B5 Clone Cross","cloneSourceId":%q,"capabilityDomain":"video"}`, sourceID))
	if code != http.StatusBadRequest || crossClone["message"] != "克隆账户能力域必须与源账户一致" {
		t.Fatalf("cross-domain clone: %d %v", code, crossClone)
	}

	// 源不存在 → 404（FindCloneContext 缺行语义，照搬 accounts/routes.go）。
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"b5-clone-missing","name":"B5 Clone Missing","cloneSourceId":"acc-b5-nope"}`)
	if code != http.StatusNotFound || missing["message"] != "账户不存在" {
		t.Fatalf("missing clone source: %d %v", code, missing)
	}

	// 授权实例禁克隆 → 403「授权实例不能克隆」（CloneInteractionForbiddenError
	// 经 errors.As 类型判别映射 CloneSourceError，不再按错误文案 Contains）。
	if _, err := env.db.Exec(fixtureBind(`UPDATE accounts SET authorization_instance_authorization_id = 'auth-b5-forbidden' WHERE id = ?`), sourceID); err != nil {
		t.Fatalf("mark authorization instance: %v", err)
	}
	code, forbidden := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		fmt.Sprintf(`{"refreshToken":"b5-clone-forbidden","name":"B5 Clone Forbidden","cloneSourceId":%q}`, sourceID))
	if code != http.StatusForbidden || forbidden["message"] != "授权实例不能克隆" {
		t.Fatalf("forbidden clone source: %d %v", code, forbidden)
	}
}

// TestB5SsoToOAuthCloneValidationErrorSurfacesVerbatim：sso-to-oauth 批量条目的
// 确定性业务失败直写具体文案——SSO 转换成功、建户在克隆预检域断言处失败时，
// 失败项 error 为 400 面「克隆账户能力域必须与源账户一致」，不再折叠进
// 「Grok SSO Cookie 转换失败」兜底。
func TestB5SsoToOAuthCloneValidationErrorSurfacesVerbatim(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	// chat 源账户（openai create-from-refresh-token，域缺省 chat）。
	env.exchanger.respond = staticToken(openAITokenPayload("b5-sso-clone-source"))
	code, sourceCreated := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"b5-sso-clone-source","name":"B5 SSO Clone Source"}`)
	if code != http.StatusCreated {
		t.Fatalf("clone source create: %d %v", code, sourceCreated)
	}
	sourceID := dataMap(t, sourceCreated)["id"].(string)

	// SSO 设备流脚本走到 token 成功步，转换本身不失败。
	tokenBody := fmt.Sprintf(`{"access_token":%q,"id_token":%q,"token_type":"Bearer","expires_in":21600}`,
		fakeJWT(map[string]any{"email": "sso-clone@example.com"}), fakeJWT(map[string]any{"sub": "s"}))
	env.sso.steps = []SSODeviceResponse{
		ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
		ssoStep(http.StatusOK, nil, `{"device_code":"dc","user_code":"UC","verification_uri_complete":"https://auth.x.ai/device?user_code=UC","interval":1,"expires_in":600}`),
		ssoStep(http.StatusOK, nil, "<html>device</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/consent"}, ""),
		ssoStep(http.StatusOK, nil, "<html>consent</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/done"}, ""),
		ssoStep(http.StatusOK, nil, "<html>done</html>"),
		ssoStep(http.StatusOK, nil, tokenBody),
	}

	// chat 源 + video 目标 → 克隆预检 ValidationError，失败项直写具体文案。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		fmt.Sprintf(`{"ssoTokens":["sso=clonecase"],"cloneSourceId":%q,"capabilityDomain":"video"}`, sourceID))
	if code != http.StatusOK {
		t.Fatalf("sso-to-oauth: %d %v", code, payload)
	}
	outcome := dataMap(t, payload)
	failed, ok := outcome["failed"].([]any)
	if !ok || len(failed) != 1 {
		t.Fatalf("必须收集失败项: %v", outcome)
	}
	item := failed[0].(map[string]any)
	if item["error"] != "克隆账户能力域必须与源账户一致" {
		t.Fatalf("失败项必须直写 400 具体文案而非兜底: %v", item)
	}
}
