package speedfirstrepo

import (
	mathrand "math/rand"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/schedulejitter"
)

// 本文件承载原 proberepo 包内被 speedfirst.go 依赖的共享小工具
//（包拆分后随迁，实现不变）。

// randomToken 生成小写字母数字随机 token（Redis claim/锁 token）。
func randomToken(length int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buffer := make([]byte, length)
	for index := range buffer {
		buffer[index] = alphabet[mathrand.Intn(len(alphabet))]
	}
	return string(buffer)
}

func formatMillis(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(rfc3339Milli)
}

// passiveJitterWindowMS 等价 passiveScheduleJitterWindowMs。
// 实现收敛到 shared/platform/schedulejitter（全输入域等价，含 interval<1
// 钳制与 ms 整除语义）。
func passiveJitterWindowMS(intervalMS int64) int64 {
	return int64(schedulejitter.Window(time.Duration(intervalMS) * time.Millisecond) / time.Millisecond)
}
