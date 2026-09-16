package accountcrypto

// w12h 补充 arms：DecryptJSON 的 tag/ciphertext 段 base64 解码拒绝分支。
// 不可达语句登记（w12h 实测无注入点）：
//   - crypto.go EncryptJSON 中 crypto/rand.Read 失败分支：包内直接使用全局
//     crypto/rand，无注入点，rand.Read 对 12 字节缓冲实际不失败。

import (
	"strings"
	"testing"
)

func TestW12HDecryptJSONRejectsBadSegmentEncoding(t *testing.T) {
	envelope, err := EncryptJSON("w12h-secret", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(envelope, ":")
	if len(parts) != 4 {
		t.Fatalf("信封段数异常: %q", envelope)
	}
	var target map[string]any

	// tag 段不是合法 base64url。
	badTag := append([]string(nil), parts...)
	badTag[2] = "!!!"
	if err := DecryptJSON("w12h-secret", strings.Join(badTag, ":"), &target); err == nil || !strings.Contains(err.Error(), "格式不受支持") {
		t.Fatalf("tag 段非法 base64 必须拒绝: %v", err)
	}

	// ciphertext 段不是合法 base64url。
	badCipher := append([]string(nil), parts...)
	badCipher[3] = "***"
	if err := DecryptJSON("w12h-secret", strings.Join(badCipher, ":"), &target); err == nil || !strings.Contains(err.Error(), "格式不受支持") {
		t.Fatalf("ciphertext 段非法 base64 必须拒绝: %v", err)
	}
}
