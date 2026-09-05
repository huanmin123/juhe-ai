package circuitstore

import (
	"crypto/rand"
	"encoding/hex"
)

// newRandomUUID 生成 RFC4122 v4 形状的随机 UUID（等价 Node randomUUID 的
// outbox claim token / evidence id 用途；不引入额外依赖）。
func newRandomUUID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return ""
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	text := hex.EncodeToString(bytes[:])
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:32]
}
