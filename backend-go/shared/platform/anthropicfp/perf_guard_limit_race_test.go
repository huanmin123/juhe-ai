//go:build race

package anthropicfp

import "time"

// datelinePerfGuardLimit 是大 body 性能护栏（TestNormalizeDatelineLargeBodyPerformance）
// 的耗时上限。race 检测器对线性扫描有常数级放大（实测本护栏用例 -race 下
// ≈2.0s、非 race ≈0.10s，约 20×），race 模式上限按同量级放宽以避免护栏
// 抖动；若清洗退化回 O(命中×body)（修复前基线 12.3s 非 race），耗时按
// 同倍数放大后仍远超本上限，护栏不会失效。
const datelinePerfGuardLimit = 8 * time.Second
