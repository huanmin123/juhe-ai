package taskruns

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/idgen"
)

// newID 生成带前缀的随机 ID（等价 Node newId(prefix) 的形态：
// `<prefix>_<时间基数36>_<随机段>`，仅要求全局唯一与可读，不要求跨语言逐字节一致）。
// 随机段收敛到 shared/platform/idgen（格式不变；极端 crypto/rand 失败由
// idgen 以时间熵回退，保持长度与格式，替代原 panic 分支）。
func newID(prefix string) string {
	return fmt.Sprintf("%s_%s_%s", prefix, formatTimeBase36(time.Now()), idgen.RandomHex(8))
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
