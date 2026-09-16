package accountcrypto

import (
	"strings"
	"testing"
)

// TestW9AEncryptJSONErrorBranches 覆盖不可序列化值的错误传播。
func TestW9AEncryptJSONErrorBranches(t *testing.T) {
	if _, err := EncryptJSON("secret", make(chan int)); err == nil {
		t.Fatal("不可序列化值必须报错")
	}
}

// TestW9ADecryptJSONErrorBranches 覆盖 v1 信封的全部拒绝分支。
func TestW9ADecryptJSONErrorBranches(t *testing.T) {
	var target map[string]any
	if err := DecryptJSON("secret", "not-v1-envelope", &target); err == nil || !strings.Contains(err.Error(), "格式不受支持") {
		t.Fatalf("非 v1 格式必须拒绝: %v", err)
	}
	if err := DecryptJSON("secret", "v1:!!!:!!!:!!!", &target); err == nil || !strings.Contains(err.Error(), "格式不受支持") {
		t.Fatalf("非法 base64 必须拒绝: %v", err)
	}
	// 合法 base64 但 IV 长度不足。
	if err := DecryptJSON("secret", "v1:AAAA:AAAA:AAAA", &target); err == nil || !strings.Contains(err.Error(), "格式不受支持") {
		t.Fatalf("IV/tag 长度错误必须拒绝: %v", err)
	}
	// 结构正确但 GCM 认证失败。
	envelope, err := EncryptJSON("secret", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	if err := DecryptJSON("other-secret", envelope, &target); err == nil || !strings.Contains(err.Error(), "格式不受支持") {
		t.Fatalf("密钥不匹配必须拒绝: %v", err)
	}
	// 密文被篡改。
	parts := strings.Split(envelope, ":")
	parts[3] = strings.Repeat("A", len(parts[3]))
	if err := DecryptJSON("secret", strings.Join(parts, ":"), &target); err == nil {
		t.Fatal("篡改密文必须拒绝")
	}
	// 认证通过但明文不是目标类型。
	boolEnvelope, err := EncryptJSON("secret", true)
	if err != nil {
		t.Fatal(err)
	}
	var structured map[string]any
	if err := DecryptJSON("secret", boolEnvelope, &structured); err == nil {
		t.Fatal("类型不匹配必须报错")
	}
}

// TestW9ASealJSONWithIVLengthCheck 覆盖 IV 长度校验分支。
func TestW9ASealJSONWithIVLengthCheck(t *testing.T) {
	if _, err := sealJSONWithIV("secret", []byte(`{}`), []byte("short")); err == nil || !strings.Contains(err.Error(), "IV 长度无效") {
		t.Fatalf("IV 长度无效必须报错: %v", err)
	}
	sealed, err := sealJSONWithIV("secret", []byte(`{"k":"v"}`), make([]byte, 12))
	if err != nil || !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("确定性封签失败: %q err %v", sealed, err)
	}
	again, err := sealJSONWithIV("secret", []byte(`{"k":"v"}`), make([]byte, 12))
	if err != nil || again != sealed {
		t.Fatal("同 IV 必须产出相同信封")
	}
}
