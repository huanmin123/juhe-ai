// 本文件由 jobs internal/accounthealth/w13g5_health_arms_test.go 拆分而来（成对关系见 probe.go 包注释）：
// 承载随探针执行器闭包下沉的被移函数测试；同源文件的留守测试仍在 jobs 包内，
// 两侧不重复、不丢失。
package exactkeyprobe

import (
	"encoding/json"
	"testing"
	"time"
)

func TestW13g5HealthProbeCodexMetadata(t *testing.T) {
	if got := codexMetadataJSON(nil); got != "null" {
		t.Fatalf("nil 序列化: %s", got)
	}
	if got := codexMetadataJSON(map[string]any{"a": 1}); got == "" {
		t.Fatal("合法值必须序列化")
	}
	if err := verifyImagesJSON(nil); err == nil {
		t.Fatal("nil images 必须报错")
	}
	if err := verifyImagesJSON(map[string]any{}); err == nil {
		t.Fatal("空 images 必须报错")
	}
}

func TestW13g5HealthProbeTransportArms(t *testing.T) {
	// 无代理直通。
	if _, err := probeTransport(Input{}, ProbeOptions{}); err != nil {
		t.Fatal(err)
	}
	// 代理凭据解密失败（坏 envelope 文本）。
	badCipher := CredentialEnvelope{Kind: "v1", Ciphertext: "not-an-envelope"}
	if _, _, err := probeTransportConfig(Input{Proxy: &badCipher}, ProbeOptions{Secret: "w13g5-secret"}); err == nil {
		t.Fatal("代理凭据不可用必须报错")
	}
	// 代理协议不支持（合法 v1 envelope 包裹 ftp URL）。
	plainProxy, err := EncryptV1Envelope("w13g5-secret", []byte("ftp://w13g5-proxy"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := probeTransportConfig(Input{Proxy: &CredentialEnvelope{Kind: "v1", Ciphertext: plainProxy}}, ProbeOptions{Secret: "w13g5-secret"}); err == nil {
		t.Fatal("未支持的代理协议必须报错")
	}
	// 合法代理 URL → 成功。
	httpProxy, err := EncryptV1Envelope("w13g5-secret", []byte("http://127.0.0.1:9"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := probeTransportConfig(Input{Proxy: &CredentialEnvelope{Kind: "v1", Ciphertext: httpProxy}}, ProbeOptions{Secret: "w13g5-secret"}); err != nil {
		t.Fatal(err)
	}
}

func TestW13g5HealthDirectProbeTargetArms(t *testing.T) {
	// directProbeTarget 的错误臂依赖 accountprobe 视图上下文（候选源缺失），
	// 单元级不可构造，登记于文件头。
	if err := validateDirectAccount(DirectAccount{ID: ""}, time.Now()); err == nil {
		t.Fatal("空 ID 必须报错")
	}
	if err := validateDirectAccount(DirectAccount{ID: "w13g5", ConfigRevision: 1, DispatchRevision: 1, Type: "w13g5-type"}, time.Now()); err == nil {
		t.Fatal("未知 type 必须报错")
	}
}

func TestW13g5HealthDirectTimeArms(t *testing.T) {
	if _, ok := directTime(nil, "w13g5-missing"); ok {
		t.Fatal("缺键必须失败")
	}
}

// TestW13g5HealthDirectHelpers 是 jobs accounthealth 同名测试的成对拆分：
// 承载随 direct_input.go 下沉的 openAIProfileMode/mustJSON/directTime 臂；
// directInputScanCap/directSettingInt 臂留守 jobs 包内，两侧不重复、不丢失。
func TestW13g5HealthDirectHelpers(t *testing.T) {
	// openAIProfileMode。
	if !openAIProfileMode("api_key", "images_json", true) {
		t.Fatal("api_key images 模式必须允许")
	}
	if openAIProfileMode("w13g5-type", "chat_json", true) {
		t.Fatal("未知账户类型必须拒绝")
	}
	if !openAIProfileMode("oauth", "responses_sse", false) {
		t.Fatal("oauth responses 模式必须允许")
	}
	// mustJSON。
	if got := mustJSON("w13g5"); got != `"w13g5"` {
		t.Fatalf("文本必须 JSON 编码: %s", got)
	}
	if got := mustJSON(`{"a":1}`); got == "" {
		t.Fatal("合法 JSON 必须压缩输出")
	}
	// directTime。
	values := map[string]json.RawMessage{"w13g5-at": json.RawMessage(`"2026-09-18T08:00:00Z"`)}
	if parsed, ok := directTime(values, "w13g5-at"); !ok || parsed.IsZero() {
		t.Fatalf("合法时间必须解析: %v %v", parsed, ok)
	}
	if _, ok := directTime(map[string]json.RawMessage{"w13g5-at": json.RawMessage(`"not-time"`)}, "w13g5-at"); ok {
		t.Fatal("非法时间必须失败")
	}
}
