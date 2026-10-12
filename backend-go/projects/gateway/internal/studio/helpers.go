package studio

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"strconv"
	"strings"
)

// 局部纯工具（hex/解析/边界），studio 包内自持，不外溢到其他域。

// randomHex32 生成 16 字节随机数的 32 位十六进制文本（各 id 前缀族共用）。
func randomHex32() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("studio: 生成随机 id 失败: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func parseMediaType(value string) (string, map[string]string, error) {
	return mime.ParseMediaType(value)
}

func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}

func isJSONObject(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var parsed map[string]any
	return json.Unmarshal([]byte(trimmed), &parsed) == nil
}

func parseInt(text string) (int, bool) {
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0, false
	}
	return value, true
}
