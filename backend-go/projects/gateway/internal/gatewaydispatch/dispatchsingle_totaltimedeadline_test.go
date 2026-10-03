package gatewaydispatch

// 普通路由速度优先总时间兜底截止（设计 6.3）：档位选档纯函数的分支覆盖。
// 压缩识别优先（恒压缩档）；非压缩请求按估算输入 token 与
// routestrategies.SpeedFirstLargeInputTokenThreshold 分界选档；preauth 配置
// 两阈值为 nil 时回落内联默认（与 chain 侧 chainSpeedFirstRuntimeConfigOf
// 一致）。该函数同时被 attempt 装配与 chain 层完成观测消费。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

func int64PtrOf(value int64) *int64 { return &value }

func TestResolveNormalRouteTotalTimeDeadline(t *testing.T) {
	normalConfig := &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
		TotalTimeDeadlineMs:           int64PtrOf(60_000),
		CompactionTotalTimeDeadlineMs: int64PtrOf(600_000),
	}
	tests := []struct {
		name                  string
		config                *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig
		compactionTimeoutsOff bool
		estimatedInputTokens  int
		attemptStartedAtMs    int64
		wantOK                bool
		wantThresholdMs       int64
		wantCompactionLane    bool
		wantDeadlineAtMs      int64
	}{
		{
			// preauth 配置缺席 = 不装配总时间维度。
			name:   "配置缺席不装配",
			config: nil,
			wantOK: false,
		},
		{
			// 压缩识别优先：compactionTimeoutsDisabled=true 恒压缩档。
			name:                  "压缩门恒压缩档",
			config:                normalConfig,
			compactionTimeoutsOff: true,
			estimatedInputTokens:  0,
			attemptStartedAtMs:    1_000,
			wantOK:                true,
			wantThresholdMs:       600_000,
			wantCompactionLane:    true,
			wantDeadlineAtMs:      601_000,
		},
		{
			// 非压缩 + 估算输入 ≥ 100000 → 大输入自动套压缩档。
			name:                 "大输入估算套压缩档",
			config:               normalConfig,
			estimatedInputTokens: routestrategies.SpeedFirstLargeInputTokenThreshold,
			attemptStartedAtMs:   2_000,
			wantOK:               true,
			wantThresholdMs:      600_000,
			wantCompactionLane:   true,
			wantDeadlineAtMs:     602_000,
		},
		{
			// 普通小请求 → 普通档。
			name:                 "普通小请求普通档",
			config:               normalConfig,
			estimatedInputTokens: routestrategies.SpeedFirstLargeInputTokenThreshold - 1,
			attemptStartedAtMs:   3_000,
			wantOK:               true,
			wantThresholdMs:      60_000,
			wantCompactionLane:   false,
			wantDeadlineAtMs:     63_000,
		},
		{
			// preauth 两阈值为 nil（存量策略缺省提交）→ 内联默认 120s/300s。
			name:                 "阈值缺省回落默认",
			config:               &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{},
			estimatedInputTokens: 1,
			attemptStartedAtMs:   5_000,
			wantOK:               true,
			wantThresholdMs:      120_000,
			wantCompactionLane:   false,
			wantDeadlineAtMs:     125_000,
		},
		{
			name:                  "压缩阈值缺省回落默认",
			config:                &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{},
			compactionTimeoutsOff: true,
			attemptStartedAtMs:    5_000,
			wantOK:                true,
			wantThresholdMs:       300_000,
			wantCompactionLane:    true,
			wantDeadlineAtMs:      305_000,
		},
		{
			// 运行态解码零值（显式非法）同样回落默认，不产生非正阈值。
			name:                 "阈值非法回落默认",
			config:               &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{TotalTimeDeadlineMs: int64PtrOf(0)},
			estimatedInputTokens: 1,
			wantOK:               true,
			wantThresholdMs:      120_000,
			wantCompactionLane:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deadline, ok := ResolveNormalRouteTotalTimeDeadline(NormalRouteTotalTimeDeadlineInput{
				Config:                     tt.config,
				CompactionTimeoutsDisabled: tt.compactionTimeoutsOff,
				EstimatedInputTokens:       tt.estimatedInputTokens,
				AttemptStartedAtMs:         tt.attemptStartedAtMs,
			})
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if deadline.ThresholdMs != tt.wantThresholdMs {
				t.Fatalf("thresholdMs = %d, want %d", deadline.ThresholdMs, tt.wantThresholdMs)
			}
			if deadline.CompactionLane != tt.wantCompactionLane {
				t.Fatalf("compactionLane = %v, want %v", deadline.CompactionLane, tt.wantCompactionLane)
			}
			if tt.wantDeadlineAtMs != 0 && deadline.DeadlineAtMs != tt.wantDeadlineAtMs {
				t.Fatalf("deadlineAtMs = %d, want %d", deadline.DeadlineAtMs, tt.wantDeadlineAtMs)
			}
		})
	}
}
