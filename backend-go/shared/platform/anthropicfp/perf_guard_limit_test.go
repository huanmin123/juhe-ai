//go:build !race

package anthropicfp

import "time"

// 见 perf_guard_limit_race_test.go 的说明；非 race 模式护栏 = 2s
// （契约口径：修复后目标 <1s，实测 ≈0.10s，2s 防机器波动；修复前
// sjson 基线 12.3s @ 8.39MB/3961 命中）。
const datelinePerfGuardLimit = 2 * time.Second
