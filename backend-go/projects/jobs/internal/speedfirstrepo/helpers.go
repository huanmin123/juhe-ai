package speedfirstrepo

import (
	mathrand "math/rand"
	"time"
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
func passiveJitterWindowMS(intervalMS int64) int64 {
	if intervalMS < 1 {
		intervalMS = 1
	}
	var windowMS int64
	switch {
	case intervalMS < 60_000:
		// intervalMS < 60_000 时 half = intervalMS/2 < 30_000 恒成立，
		// 原 else 分支（windowMS = 30_000）不可达，按死守卫删除（w12f）。
		return intervalMS / 2
	case intervalMS < 60*60_000:
		windowMS = 30_000
	case intervalMS < 24*60*60_000:
		windowMS = 30 * 60_000
	case intervalMS < 7*24*60*60_000:
		windowMS = 60 * 60_000
	default:
		windowMS = 8 * 60 * 60_000
	}
	// 各 case 给出的 windowMS 恒 <= intervalMS/2 且恒非负：
	// 原 `if windowMS > half` 与 `if windowMS < 0` 钳制不可达，按死守卫删除（w12f）。
	return windowMS
}
