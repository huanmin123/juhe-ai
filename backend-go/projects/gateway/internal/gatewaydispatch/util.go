package gatewaydispatch

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// REFACTOR-0006 阶段 A：decodeJSONObject / trimString / jsonCloneValue 随
// 传输族迁入 gatewayupstream（helpers.go），本包经 gatewayupstream_bridge.go
// 的私有转发保持消费点零改动；阶段 B ports 下沉时随 util.go 归位。

// isPlainObjectValue mirrors the Node isPlainObject guard.
func isPlainObjectValue(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

// uuid4String mirrors Node randomUUID() (v4, lowercase, dashed).
func uuid4String() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

// sha256HexBytes mirrors Node createHash('sha256')...digest('hex').
func sha256HexBytes(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
