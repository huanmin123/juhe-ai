package gatewaycircuit

import (
	crand "crypto/rand"
	"encoding/hex"
	mrand "math/rand"
	"time"
)

func defaultNowMs() int64 { return time.Now().UnixMilli() }

func defaultRandom() float64 { return mrand.Float64() }

// defaultCreateID mirrors the Node randomUUID fallback.
func defaultCreateID() string {
	bytes := make([]byte, 16)
	// crypto/rand.Read 自 Go 1.24 起保证永不返回错误，无需错误臂。
	_, _ = crand.Read(bytes)
	bytes[6] = (bytes[6] & 0x0f) | 0x40 // version 4
	bytes[8] = (bytes[8] & 0x3f) | 0x80 // RFC 4122 variant
	encoded := hex.EncodeToString(bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
