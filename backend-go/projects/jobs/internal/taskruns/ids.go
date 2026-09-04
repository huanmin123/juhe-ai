package taskruns

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// newID 生成带前缀的随机 ID（等价 Node newId(prefix) 的形态：
// `<prefix>_<时间基数36>_<随机段>`，仅要求全局唯一与可读，不要求跨语言逐字节一致）。
func newID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("taskruns newID 随机源不可用: %v", err))
	}
	return fmt.Sprintf("%s_%s_%s", prefix, formatTimeBase36(time.Now()), hex.EncodeToString(buf))
}

func formatTimeBase36(t time.Time) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	ms := uint64(t.UnixMilli())
	if ms == 0 {
		return "0"
	}
	out := make([]byte, 0, 12)
	for ms > 0 {
		out = append([]byte{digits[ms%36]}, out...)
		ms /= 36
	}
	return string(out)
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
