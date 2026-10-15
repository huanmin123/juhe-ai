// probe_test_helpers_test.go 是测试助手留守副本：testInput/testEnvelope 原实现
// 位于下沉包 shared/platform/accounttest/exactkeyprobe 的 probe_test.go（随
// 探针执行器闭包迁移，成对关系见该包 probe.go 注释），跨包不可见，故按需复制
// 给本包留守测试使用；int64Pointer/boolPointer/mustJSON 同理（原实现随
// direct_input.go 下沉）。两侧语义必须一致，修改任一侧须评估另一侧。
package accounthealth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

// probeChallengeAnswerPattern 匹配 J1 探针算术挑战（exactkeyprobe 的
// probeChallengePromptFormat，2026-10-15 起取代固定令牌）。
var probeChallengeAnswerPattern = regexp.MustCompile(`请计算 (\d+)\+(\d+)`)

// probeChallengeAnswer 从探针请求体解析算术挑战并返回两数之和的十进制文本，
// 供假上游回显；请求不含算术挑战时返回空串。
func probeChallengeAnswer(request *http.Request) string {
	body, _ := io.ReadAll(request.Body)
	match := probeChallengeAnswerPattern.FindSubmatch(body)
	if match == nil {
		return ""
	}
	left, _ := strconv.Atoi(string(match[1]))
	right, _ := strconv.Atoi(string(match[2]))
	return strconv.Itoa(left + right)
}

func testInput(baseURL, mode string) exactkeyprobe.Input {
	now := time.Now().UTC()
	return exactkeyprobe.Input{
		AccountID:            "account-1",
		InputVersion:         1,
		ConfigRevision:       1,
		DispatchRevision:     1,
		Provider:             "openai",
		Type:                 "api_key",
		EndpointMode:         mode,
		HealthModel:          "gpt-test",
		BaseURL:              baseURL,
		IssuedAt:             now,
		ExpiresAt:            now.Add(time.Hour),
		TLSPolicyVersion:     "test",
		AllowInsecureBaseURL: strings.HasPrefix(baseURL, "http://"),
	}
}

func testEnvelope(t *testing.T, secret, plaintext string) string {
	t.Helper()
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	encoded := gcm.Seal(nil, iv, []byte(plaintext), nil)
	ciphertext := encoded[:len(encoded)-gcm.Overhead()]
	tag := encoded[len(encoded)-gcm.Overhead():]
	return "v1:" + base64.RawURLEncoding.EncodeToString(iv) + ":" + base64.RawURLEncoding.EncodeToString(tag) + ":" + base64.RawURLEncoding.EncodeToString(ciphertext)
}

// int64Pointer/boolPointer/mustJSON 与 exactkeyprobe 包内同名生产助手成对。
func int64Pointer(value int64) *int64 { return &value }

func boolPointer(value bool) *bool { return &value }

func mustJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
