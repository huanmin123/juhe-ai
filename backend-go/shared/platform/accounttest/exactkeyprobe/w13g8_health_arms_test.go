// 本文件由 jobs internal/accounthealth/w13g8_health_arms_test.go 拆分而来（成对关系见 probe.go 包注释）：
// 承载随探针执行器闭包下沉的被移函数测试；同源文件的留守测试仍在 jobs 包内，
// 两侧不重复、不丢失。
package exactkeyprobe

import (
	"strings"
	"testing"
	"time"
)

// TestW13G8DirectProbeTargetArms 驱动 directProbeTarget 的 hybrid 解析失败臂。
func TestW13G8DirectProbeTargetArms(t *testing.T) {
	// hybrid profile 缺模型映射 → ResolveHybridProbeTarget 失败。
	if _, _, err := directProbeTarget(DirectAccount{ProtocolProfileID: "profile_hybrid_openai_chat_v1", EndpointMode: "chat_json", MappedUpstreamModel: "", MappedUpstreamEndpointFamily: ""}); err == nil || !strings.Contains(err.Error(), "PG direct input") {
		t.Fatalf("hybrid 缺模型映射必须报错: %v", err)
	}
	// 合法 profile → 成功路径。
	mode, model, err := directProbeTarget(DirectAccount{ProtocolProfileID: "profile_gpt_openai_v1", EndpointMode: "responses_json", HealthModel: "gpt-w13g8"})
	if err != nil || mode == "" || model == "" {
		t.Fatalf("合法 profile 必须解析成功: %q %q %v", mode, model, err)
	}
}

// TestW13G8DecryptJSONObjectArms 驱动 decryptJSONObject 的解封与解析失败臂。
func TestW13G8DecryptJSONObjectArms(t *testing.T) {
	if _, err := decryptJSONObject("w13g8-secret", "w13g8-not-an-envelope", "proxy "); err == nil || !strings.Contains(err.Error(), "解封 PG direct input") {
		t.Fatalf("坏 envelope 必须报解封错误: %v", err)
	}
	envelope, err := EncryptV1Envelope("w13g8-secret", []byte("w13g8-not-json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decryptJSONObject("w13g8-secret", envelope, "proxy "); err == nil || !strings.Contains(err.Error(), "解析 PG direct input") {
		t.Fatalf("非 JSON 明文必须报解析错误: %v", err)
	}
}

// TestW13G8ValidateDirectAuthorizationArms 驱动 validateDirectAuthorization
// 的全部校验失败臂。
func TestW13G8ValidateDirectAuthorizationArms(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	validAccount := DirectAccount{ID: "w13g8-acc", ConfigRevision: 1, DispatchRevision: 1, Type: "api_key", Provider: "gpt", ProtocolProfileID: "profile_gpt_openai_v1", EndpointMode: "responses_json", Status: "active", Schedulable: true}
	validSource := func() *DirectSource {
		credentials, err := EncryptV1Envelope("w13g8-secret", []byte(`{"api_key":"sk-w13g8"}`))
		if err != nil {
			t.Fatal(err)
		}
		return &DirectSource{ID: "w13g8-src", ConfigRevision: 1, Provider: "gpt", ProtocolProfileID: "profile_gpt_openai_v1", Type: "api_key", Status: "active", Schedulable: true, CredentialsEncrypted: credentials}
	}
	validAuthorization := func() *DirectAuthorization {
		return &DirectAuthorization{ID: "w13g8-auth", Status: "active", QuotaEligible: true}
	}
	// 授权/来源缺失。
	if err := validateDirectAuthorization(DirectInput{Account: validAccount}, now); err == nil || !strings.Contains(err.Error(), "authorization/source") {
		t.Fatalf("缺 authorization 必须报错: %v", err)
	}
	// 授权不可用（状态/配额/过期）。
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: &DirectAuthorization{ID: "w13g8-auth", Status: "inactive", QuotaEligible: true}, Source: validSource(), Binding: DirectBinding{AuthorizationBindingID: "w13g8-auth"}}, now); err == nil || !strings.Contains(err.Error(), "授权不可用") {
		t.Fatalf("inactive 授权必须报错: %v", err)
	}
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: &DirectAuthorization{ID: "w13g8-auth", Status: "active", QuotaEligible: false}, Source: validSource(), Binding: DirectBinding{AuthorizationBindingID: "w13g8-auth"}}, now); err == nil {
		t.Fatal("配额不可用授权必须报错")
	}
	expired := now.Add(-time.Hour)
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: &DirectAuthorization{ID: "w13g8-auth", Status: "active", QuotaEligible: true, ExpiresAt: &expired}, Source: validSource(), Binding: DirectBinding{AuthorizationBindingID: "w13g8-auth"}}, now); err == nil {
		t.Fatal("过期授权必须报错")
	}
	// binding 不匹配。
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: validAuthorization(), Source: validSource(), Binding: DirectBinding{AuthorizationBindingID: "w13g8-other"}}, now); err == nil || !strings.Contains(err.Error(), "binding 不匹配") {
		t.Fatalf("binding 不匹配必须报错: %v", err)
	}
	// 物理来源不可用（缺凭据）。
	badSource := validSource()
	badSource.CredentialsEncrypted = ""
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: validAuthorization(), Source: badSource, Binding: DirectBinding{AuthorizationBindingID: "w13g8-auth"}}, now); err == nil || !strings.Contains(err.Error(), "物理来源账户不可用") {
		t.Fatalf("缺凭据来源必须报错: %v", err)
	}
	// 物理来源已到期。
	expiredSource := validSource()
	expiredSource.AccountExpiresAt = &expired
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: validAuthorization(), Source: expiredSource, Binding: DirectBinding{AuthorizationBindingID: "w13g8-auth"}}, now); err == nil || !strings.Contains(err.Error(), "已到期") {
		t.Fatalf("到期来源必须报错: %v", err)
	}
	// 物理来源冷却中。
	cooldown := now.Add(time.Hour)
	cooldownSource := validSource()
	cooldownSource.CooldownUntil = &cooldown
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: validAuthorization(), Source: cooldownSource, Binding: DirectBinding{AuthorizationBindingID: "w13g8-auth"}}, now); err == nil || !strings.Contains(err.Error(), "冷却") {
		t.Fatalf("冷却来源必须报错: %v", err)
	}
}

// TestW13G8EncryptV1EnvelopeEmptySecret 驱动 EncryptV1Envelope 的空 secret 臂。
func TestW13G8EncryptV1EnvelopeEmptySecret(t *testing.T) {
	if _, err := EncryptV1Envelope("  ", []byte("w13g8")); err == nil || !strings.Contains(err.Error(), "secret 不能为空") {
		t.Fatalf("空 secret 必须报错: %v", err)
	}
}

// TestW13G8MustJSONContract 固定 mustJSON 的契约（string 序列化不会 panic，
// panic 分支为防御性不可达）。
func TestW13G8MustJSONContract(t *testing.T) {
	if got := mustJSON("w13g8"); got != `"w13g8"` {
		t.Fatalf("mustJSON 必须输出 JSON 字符串: %s", got)
	}
}
